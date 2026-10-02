package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/blockstorage"
	"github.com/draas-platform/draas/internal/catalog"
)

func main() {
	var tenant string
	var workload string
	var point string
	var snapshot string
	var sizeBytes int64
	var status string
	flag.StringVar(&tenant, "tenant", os.Getenv("DRAAS_TENANT"), "tenant id")
	flag.StringVar(&workload, "workload", os.Getenv("DRAAS_WORKLOAD_ID"), "workload id")
	flag.StringVar(&point, "point", os.Getenv("DRAAS_RECOVERY_POINT_ID"), "recovery point id")
	flag.StringVar(&snapshot, "snapshot", os.Getenv("DRAAS_ZFS_SNAPSHOT"), "source ZFS snapshot, e.g. pool/draas/tenants/t/workloads/w/source@rp")
	flag.Int64Var(&sizeBytes, "size-bytes", 0, "logical size in bytes")
	flag.StringVar(&status, "status", "VERIFIED", "catalog status")
	flag.Parse()

	if !backup.ValidID(tenant) || !backup.ValidID(workload) || !backup.ValidID(point) {
		fatal(errors.New("tenant, workload and point must be valid DraaS identifiers"))
	}
	if snapshot == "" || sizeBytes < 1 {
		fatal(errors.New("snapshot and positive size-bytes are required"))
	}
	if err := exec.Command("zfs", "list", "-H", "-t", "snapshot", snapshot).Run(); err != nil {
		fatal(fmt.Errorf("ZFS snapshot not found or zfs unavailable: %w", err))
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fatal(errors.New("DATABASE_URL is required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := catalog.Open(ctx, dbURL)
	if err != nil {
		fatal(err)
	}
	defer db.Pool.Close()
	if err = db.Migrate(ctx); err != nil {
		fatal(err)
	}
	manifest := blockstorage.RecoveryPoint{
		StorageBackend: "zfs",
		Tenant:         tenant,
		WorkloadID:     workload,
		RecoveryPoint:  point,
		ZFSSnapshot:    snapshot,
		SizeBytes:      sizeBytes,
		Metadata: map[string]string{
			"consistency": "ZFS_SNAPSHOT",
			"disk_format": "zvol",
		},
	}
	stats := map[string]any{"registered_by": "draas-zfs-register-rp", "snapshot": snapshot, "size_bytes": sizeBytes}
	if err = db.SaveBlockRecoveryPoint(ctx, tenant, point, workload, sizeBytes, manifest, stats, status); err != nil {
		fatal(err)
	}
	fmt.Printf("registered zfs recovery point tenant=%s workload=%s point=%s snapshot=%s status=%s\n", tenant, workload, point, snapshot, status)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
