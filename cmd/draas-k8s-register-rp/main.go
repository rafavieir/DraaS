package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/blockstorage"
	"github.com/draas-platform/draas/internal/catalog"
)

func main() {
	var tenant string
	var workload string
	var point string
	var namespace string
	var snapshot string
	var storageClass string
	var sizeBytes int64
	var status string
	flag.StringVar(&tenant, "tenant", os.Getenv("DRAAS_TENANT"), "tenant id")
	flag.StringVar(&workload, "workload", os.Getenv("DRAAS_WORKLOAD_ID"), "workload id")
	flag.StringVar(&point, "point", os.Getenv("DRAAS_RECOVERY_POINT_ID"), "recovery point id")
	flag.StringVar(&namespace, "namespace", env("DRAAS_K8S_RECOVERY_NAMESPACE", "draas-recovery"), "Kubernetes namespace containing the VolumeSnapshot")
	flag.StringVar(&snapshot, "snapshot", os.Getenv("DRAAS_K8S_VOLUME_SNAPSHOT"), "Kubernetes VolumeSnapshot name")
	flag.StringVar(&storageClass, "storage-class", os.Getenv("DRAAS_K8S_ZFS_STORAGE_CLASS"), "ZFS-backed StorageClass used for recovery PVCs")
	flag.Int64Var(&sizeBytes, "size-bytes", 0, "logical size in bytes")
	flag.StringVar(&status, "status", "VERIFIED", "catalog status")
	flag.Parse()

	if !backup.ValidID(tenant) || !backup.ValidID(workload) || !backup.ValidID(point) {
		fatal(errors.New("tenant, workload and point must be valid DraaS identifiers"))
	}
	if snapshot == "" || namespace == "" || storageClass == "" || sizeBytes < 1 {
		fatal(errors.New("namespace, snapshot, storage-class and positive size-bytes are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mgr, err := blockstorage.NewKubernetesZFSManager(namespace, storageClass)
	if err != nil {
		fatal(err)
	}
	if err = mgr.SnapshotExists(ctx, snapshot); err != nil {
		fatal(fmt.Errorf("VolumeSnapshot %s/%s not found or unavailable: %w", namespace, snapshot, err))
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
	manifest := blockstorage.RecoveryPoint{
		StorageBackend: blockstorage.KubernetesZFSBackend,
		Tenant:         tenant,
		WorkloadID:     workload,
		RecoveryPoint:  point,
		SizeBytes:      sizeBytes,
		Metadata: map[string]string{
			"kubernetes_namespace":       namespace,
			"kubernetes_volume_snapshot": snapshot,
			"kubernetes_storage_class":   storageClass,
			"consistency":                "CSI_VOLUME_SNAPSHOT",
			"disk_format":                "block",
		},
	}
	stats := map[string]any{"registered_by": "draas-k8s-register-rp", "namespace": namespace, "volume_snapshot": snapshot, "storage_class": storageClass, "size_bytes": sizeBytes}
	if err = db.SaveBlockRecoveryPoint(ctx, tenant, point, workload, sizeBytes, manifest, stats, status); err != nil {
		fatal(err)
	}
	fmt.Printf("registered kubernetes-zfs recovery point tenant=%s workload=%s point=%s snapshot=%s/%s status=%s\n", tenant, workload, point, namespace, snapshot, status)
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
