package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/catalog"
	velerorp "github.com/draas-platform/draas/internal/velero"
)

func main() {
	var tenant, workload, point, namespace, backupName, included, mapping, status string
	flag.StringVar(&tenant, "tenant", os.Getenv("DRAAS_TENANT"), "tenant id")
	flag.StringVar(&workload, "workload", os.Getenv("DRAAS_WORKLOAD_ID"), "workload id")
	flag.StringVar(&point, "point", os.Getenv("DRAAS_RECOVERY_POINT_ID"), "recovery point id")
	flag.StringVar(&namespace, "velero-namespace", env("DRAAS_VELERO_NAMESPACE", "velero"), "Velero namespace")
	flag.StringVar(&backupName, "backup", os.Getenv("DRAAS_VELERO_BACKUP"), "Velero Backup name")
	flag.StringVar(&included, "included-namespaces", "", "comma-separated namespaces restored from this point")
	flag.StringVar(&mapping, "namespace-mapping", "", "comma-separated source=target namespace mappings")
	flag.StringVar(&status, "status", "VERIFIED", "catalog status")
	flag.Parse()
	if !backup.ValidID(tenant) || !backup.ValidID(workload) || !backup.ValidID(point) {
		fatal(errors.New("tenant, workload and point must be valid DraaS identifiers"))
	}
	if namespace == "" || backupName == "" {
		fatal(errors.New("velero namespace and backup are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mgr, err := velerorp.NewManager(namespace)
	if err != nil {
		fatal(err)
	}
	info, err := mgr.BackupInfo(ctx, backupName)
	if err != nil {
		fatal(fmt.Errorf("Velero backup %s/%s not found: %w", namespace, backupName, err))
	}
	if info.Phase != "Completed" {
		fatal(fmt.Errorf("Velero backup %s/%s is not Completed; phase=%s warnings=%d errors=%d", namespace, backupName, info.Phase, info.Warnings, info.Errors))
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fatal(errors.New("DATABASE_URL is required"))
	}
	db, err := catalog.Open(ctx, dbURL)
	if err != nil {
		fatal(err)
	}
	defer db.Pool.Close()
	if err = db.Migrate(ctx); err != nil {
		fatal(err)
	}
	rp := velerorp.BuildRecoveryPoint(tenant, workload, point, info, splitCSV(included), parseMapping(mapping), true)
	stats := map[string]any{
		"registered_by":           "draas-velero-register-rp",
		"engine":                  velerorp.Backend,
		"external_reference":      rp.ExternalReference,
		"protection_level":        rp.ProtectionLevel,
		"persistent_data":         rp.PersistentData,
		"offsite_copy":            rp.OffsiteCopy,
		"backup_phase":            info.Phase,
		"backup_storage_location": info.BackupStorageLocation,
		"warnings":                info.Warnings,
		"errors":                  info.Errors,
	}
	if err = db.SaveBlockRecoveryPoint(ctx, tenant, point, workload, 1, rp, stats, status); err != nil {
		fatal(err)
	}
	fmt.Printf("registered velero recovery point tenant=%s workload=%s point=%s backup=%s/%s status=%s\n", tenant, workload, point, namespace, backupName, status)
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseMapping(raw string) map[string]string {
	out := map[string]string{}
	for _, item := range splitCSV(raw) {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
