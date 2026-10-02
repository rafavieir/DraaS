package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/draas-platform/draas/internal/catalog"
	velerorp "github.com/draas-platform/draas/internal/velero"
)

type statePoint struct {
	ID              string                  `json:"id"`
	TenantID        string                  `json:"tenant_id"`
	AssetID         string                  `json:"asset_id"`
	Cluster         string                  `json:"cluster"`
	Status          string                  `json:"status"`
	CreatedAt       string                  `json:"created_at"`
	CompletedAt     string                  `json:"completed_at"`
	BackupName      string                  `json:"backup_name"`
	BackupUID       string                  `json:"backup_uid"`
	BSL             string                  `json:"bsl"`
	ProtectionLevel string                  `json:"protection_level"`
	Point           velerorp.RecoveryPoint  `json:"recovery_point"`
	LastRestore     *velerorp.RestoreResult `json:"last_restore,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return usage()
	}
	switch os.Args[1] {
	case "status":
		return status(os.Args[2:])
	case "k8s":
		if len(os.Args) >= 4 && os.Args[2] == "backup" && os.Args[3] == "create" {
			return backupCreate(os.Args[4:])
		}
	case "recovery-points":
		if len(os.Args) >= 3 && os.Args[2] == "list" {
			return rpList(os.Args[3:])
		}
	case "restore":
		if len(os.Args) >= 3 && os.Args[2] == "create" {
			return restoreCreate(os.Args[3:])
		}
	}
	return usage()
}

func usage() error {
	return fmt.Errorf("usage: draas-k8s-dr status | k8s backup create --namespace NS [--tenant TENANT --asset ID] | recovery-points list | restore create --recovery-point RP")
}

func commonFlags(name string, args []string) (*flag.FlagSet, *string, *string, *time.Duration) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	stateDir := fs.String("state-dir", env("DRAAS_STATE_DIR", ".local/draas-state"), "DraaS local state directory")
	veleroNS := fs.String("velero-namespace", env("DRAAS_VELERO_NAMESPACE", "velero"), "Velero namespace")
	timeout := fs.Duration("timeout", durationEnv("DRAAS_OPERATION_TIMEOUT", 15*time.Minute), "operation timeout")
	return fs, stateDir, veleroNS, timeout
}

func status(args []string) error {
	fs, _, _, _ := commonFlags("status", args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	out := env("DRAAS_VELERO_PREFLIGHT_JSON", "")
	if out == "" {
		out = filepath.Join("artifacts", "P3D.1", "latest-velero-ready.json")
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return fmt.Errorf("preflight artifact not found; run scripts/check-velero.sh first: %w", err)
	}
	fmt.Println(string(b))
	return nil
}

func backupCreate(args []string) error {
	fs, stateDir, veleroNS, timeout := commonFlags("k8s backup create", args)
	tenant := fs.String("tenant", env("DRAAS_TENANT", "lab-tenant"), "tenant id")
	asset := fs.String("asset", "", "asset/workload id")
	cluster := fs.String("cluster", env("DRAAS_CLUSTER", "k8s-lab"), "cluster id")
	namespace := fs.String("namespace", "", "Kubernetes namespace to protect")
	storageLocation := fs.String("storage-location", "default", "Velero BackupStorageLocation")
	snapshots := fs.Bool("snapshot-volumes", false, "ask Velero to snapshot volumes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*namespace) == "" {
		return errors.New("--namespace is required")
	}
	if *asset == "" {
		*asset = *namespace
	}
	pointID := "rp-" + catalog.ID()
	backupName := sanitize("draas-" + *tenant + "-" + pointID)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	mgr, err := velerorp.NewManager(*veleroNS)
	if err != nil {
		return err
	}
	info, err := mgr.CreateBackup(ctx, velerorp.BackupSpec{Tenant: *tenant, WorkloadID: *asset, RecoveryPointID: pointID, Name: backupName, IncludedNamespaces: []string{*namespace}, StorageLocation: *storageLocation, SnapshotVolumes: *snapshots, TTL: 24 * time.Hour}, *timeout)
	if err != nil {
		return err
	}
	rp := velerorp.BuildRecoveryPoint(*tenant, *asset, pointID, info, []string{*namespace}, nil, false)
	sp := statePoint{ID: pointID, TenantID: *tenant, AssetID: *asset, Cluster: *cluster, Status: "VERIFIED", CreatedAt: info.StartTimestamp, CompletedAt: info.CompletionTimestamp, BackupName: info.Name, BackupUID: info.UID, BSL: info.BackupStorageLocation, ProtectionLevel: rp.ProtectionLevel, Point: rp}
	if err = savePoint(*stateDir, sp); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "SUCCEEDED", "recovery_point": sp})
}

func rpList(args []string) error {
	fs, stateDir, _, _ := commonFlags("recovery-points list", args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	points, err := loadPoints(*stateDir)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"items": points})
}

func restoreCreate(args []string) error {
	fs, stateDir, veleroNS, timeout := commonFlags("restore create", args)
	rpID := fs.String("recovery-point", "", "DraaS recovery point id")
	tenant := fs.String("tenant", env("DRAAS_TENANT", "lab-tenant"), "tenant id")
	jobID := fs.String("job", "", "restore job id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *rpID == "" {
		return errors.New("--recovery-point is required")
	}
	if *jobID == "" {
		*jobID = "restore-" + catalog.ID()
	}
	sp, err := loadPoint(*stateDir, *rpID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	mgr, err := velerorp.NewManager(*veleroNS)
	if err != nil {
		return err
	}
	result, err := mgr.CreateRestore(ctx, velerorp.RestoreSpec{Tenant: *tenant, RecoveryJobID: *jobID, OperationID: *jobID, Point: sp.Point})
	if err != nil {
		sp.Status = string(result.Status)
		_ = savePoint(*stateDir, sp)
		return err
	}
	sp.Status = "RESTORE_SUCCEEDED"
	sp.LastRestore = &result
	if err = savePoint(*stateDir, sp); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "SUCCEEDED", "restore": result, "recovery_point": sp.ID})
}

func savePoint(dir string, sp statePoint) error {
	if err := os.MkdirAll(filepath.Join(dir, "recovery-points"), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "recovery-points", sp.ID+".json"), b, 0o644)
}

func loadPoint(dir, id string) (statePoint, error) {
	var sp statePoint
	b, err := os.ReadFile(filepath.Join(dir, "recovery-points", id+".json"))
	if err != nil {
		return sp, err
	}
	return sp, json.Unmarshal(b, &sp)
}

func loadPoints(dir string) ([]statePoint, error) {
	root := filepath.Join(dir, "recovery-points")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]statePoint, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sp, err := loadPoint(dir, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CompletedAt > out[j].CompletedAt })
	return out, nil
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = out[:63]
	}
	return strings.Trim(out, "-")
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func durationEnv(k string, fallback time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
