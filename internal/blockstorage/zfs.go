package blockstorage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type ZFSManager struct {
	Pool string
}

type Capabilities struct {
	Available bool              `json:"available"`
	Pool      string            `json:"pool,omitempty"`
	Version   string            `json:"version,omitempty"`
	Error     string            `json:"error,omitempty"`
	Pools     []string          `json:"pools,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

type RecoveryPoint struct {
	StorageBackend string            `json:"storage_backend"`
	Tenant         string            `json:"tenant"`
	WorkloadID     string            `json:"workload_id"`
	RecoveryPoint  string            `json:"recovery_point"`
	ZFSSnapshot    string            `json:"zfs_snapshot"`
	ZFSVolume      string            `json:"zfs_volume,omitempty"`
	SizeBytes      int64             `json:"size_bytes,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type CloneSpec struct {
	Tenant        string
	RecoveryJobID string
	OperationID   string
	Snapshot      string
	TargetName    string
}

type CloneResult struct {
	Dataset string            `json:"dataset"`
	Device  string            `json:"device"`
	Props   map[string]string `json:"props,omitempty"`
}

func (m ZFSManager) Check(ctx context.Context) Capabilities {
	cap := Capabilities{Pool: m.Pool, Details: map[string]string{}}
	version, err := run(ctx, "zfs", "version")
	if err != nil {
		cap.Error = err.Error()
		return cap
	}
	cap.Version = strings.TrimSpace(version)
	pools, err := run(ctx, "zpool", "list", "-H", "-o", "name")
	if err != nil {
		cap.Error = err.Error()
		return cap
	}
	for _, p := range strings.Fields(pools) {
		cap.Pools = append(cap.Pools, p)
		if p == m.Pool {
			cap.Available = true
		}
	}
	if m.Pool == "" && len(cap.Pools) > 0 {
		cap.Available = true
		cap.Pool = cap.Pools[0]
	}
	if !cap.Available {
		cap.Error = fmt.Sprintf("zfs pool %q not found", m.Pool)
	}
	return cap
}

func (m ZFSManager) CloneFromSnapshot(ctx context.Context, spec CloneSpec) (CloneResult, error) {
	if spec.Snapshot == "" || !strings.Contains(spec.Snapshot, "@") {
		return CloneResult{}, errors.New("valid zfs snapshot is required")
	}
	if spec.TargetName == "" {
		return CloneResult{}, errors.New("target dataset is required")
	}
	if !safeDataset(spec.TargetName) || !safeDataset(spec.Snapshot) {
		return CloneResult{}, errors.New("unsafe zfs dataset identity")
	}
	if exists, err := m.Exists(ctx, spec.TargetName); err != nil {
		return CloneResult{}, err
	} else if !exists {
		args := []string{"clone",
			"-o", "org.draas:managed_by=draas",
			"-o", "org.draas:tenant=" + spec.Tenant,
			"-o", "org.draas:recovery_job_id=" + spec.RecoveryJobID,
			"-o", "org.draas:operation_id=" + spec.OperationID,
			spec.Snapshot, spec.TargetName,
		}
		if _, err = run(ctx, "zfs", args...); err != nil {
			return CloneResult{}, err
		}
	}
	device := "/dev/zvol/" + spec.TargetName
	return CloneResult{Dataset: spec.TargetName, Device: device, Props: map[string]string{"snapshot": spec.Snapshot}}, nil
}

func (m ZFSManager) Exists(ctx context.Context, dataset string) (bool, error) {
	if !safeDataset(dataset) {
		return false, errors.New("unsafe zfs dataset identity")
	}
	_, err := run(ctx, "zfs", "list", "-H", dataset)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "dataset does not exist") || strings.Contains(err.Error(), "filesystem does not exist") {
		return false, nil
	}
	return false, err
}

func (m ZFSManager) Destroy(ctx context.Context, dataset string) error {
	if !safeDataset(dataset) {
		return errors.New("unsafe zfs dataset identity")
	}
	exists, err := m.Exists(ctx, dataset)
	if err != nil || !exists {
		return err
	}
	_, err = run(ctx, "zfs", "destroy", dataset)
	return err
}

func TargetDataset(pool, tenant, recoveryJobID string) (string, error) {
	if pool == "" || tenant == "" || recoveryJobID == "" {
		return "", errors.New("pool, tenant and recovery job are required")
	}
	candidate := filepath.ToSlash(filepath.Join(pool, "draas", "tenants", tenant, "recoveries", recoveryJobID))
	if !safeDataset(candidate) {
		return "", errors.New("unsafe zfs target dataset")
	}
	return candidate, nil
}

func DecodeRecoveryPoint(b []byte) (RecoveryPoint, bool) {
	var rp RecoveryPoint
	if len(bytes.TrimSpace(b)) == 0 {
		return rp, false
	}
	if err := json.Unmarshal(b, &rp); err != nil {
		return rp, false
	}
	switch rp.StorageBackend {
	case "zfs":
		return rp, rp.ZFSSnapshot != ""
	case KubernetesZFSBackend:
		return rp, rp.Metadata["kubernetes_volume_snapshot"] != ""
	default:
		return rp, false
	}
}

func safeDataset(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") || strings.Contains(s, "..") || strings.ContainsAny(s, " \t\n\r;&|`$<>\\") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '/' || r == '@' || r == ':' || r == '.') {
			return false
		}
	}
	return true
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}
