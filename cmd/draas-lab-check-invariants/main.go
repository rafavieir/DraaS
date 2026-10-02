package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/draas-platform/draas/internal/platform"
	libvirtlab "github.com/draas-platform/draas/providers/libvirtlab"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app, err := platform.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()
	tenant := os.Getenv("DRAAS_CHECK_TENANT")
	recoveryJob := os.Getenv("DRAAS_CHECK_RECOVERY_JOB")
	if tenant == "" || recoveryJob == "" {
		return fmt.Errorf("DRAAS_CHECK_TENANT and DRAAS_CHECK_RECOVERY_JOB are required")
	}
	report := map[string]any{"tenant_id": tenant, "recovery_job_id": recoveryJob, "checked_at": time.Now().UTC()}
	var duplicateVMs, duplicateResourcesByOperation, confirmedWithoutResource, resourcesWithoutOperation, ownershipMismatches, activeLeases, readyIncomplete int
	if err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM (
 SELECT provider_resource_id FROM provider_resources WHERE tenant_id=$1 AND recovery_job_id=$2 AND resource_type='VM'
 GROUP BY provider_resource_id HAVING count(*) > 1
) d`, tenant, recoveryJob).Scan(&duplicateVMs); err != nil {
		return err
	}
	if err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM (
 SELECT operation_id FROM provider_resources WHERE tenant_id=$1 AND recovery_job_id=$2 AND operation_id <> ''
 GROUP BY operation_id HAVING count(*) > 1
) d`, tenant, recoveryJob).Scan(&duplicateResourcesByOperation); err != nil {
		return err
	}
	if err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recovery_operations o
WHERE o.tenant_id=$1 AND o.recovery_job_id=$2 AND o.status='CONFIRMED'
 AND o.resource_type <> ''
 AND NOT EXISTS (SELECT 1 FROM provider_resources r WHERE r.tenant_id=o.tenant_id AND r.operation_id=o.operation_id)`, tenant, recoveryJob).Scan(&confirmedWithoutResource); err != nil {
		return err
	}
	if err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM provider_resources r
WHERE r.tenant_id=$1 AND r.recovery_job_id=$2 AND r.operation_id <> ''
 AND NOT EXISTS (SELECT 1 FROM recovery_operations o WHERE o.tenant_id=r.tenant_id AND o.operation_id=r.operation_id)`, tenant, recoveryJob).Scan(&resourcesWithoutOperation); err != nil {
		return err
	}
	if err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM provider_resources r
WHERE r.tenant_id=$1 AND r.recovery_job_id=$2
 AND (r.handle->'provider_metadata'->>'managed_by' <> 'draas'
  OR r.handle->'provider_metadata'->>'tenant' <> $1
  OR NULLIF(r.handle->'provider_metadata'->>'recovery_job_id','') IS DISTINCT FROM $2
  OR NULLIF(r.operation_id,'') IS DISTINCT FROM NULLIF(r.handle->'provider_metadata'->>'operation_id',''))`, tenant, recoveryJob).Scan(&ownershipMismatches); err != nil {
		return err
	}
	if err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recovery_jobs
WHERE tenant_id=$1 AND job_id=$2 AND lease_owner <> '' AND lease_expires_at > now()`, tenant, recoveryJob).Scan(&activeLeases); err != nil {
		return err
	}
	if err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recovery_jobs j
WHERE j.tenant_id=$1 AND j.job_id=$2 AND j.stage='READY_FOR_ACTIVATION'
 AND (
  NOT EXISTS (SELECT 1 FROM provider_resources r WHERE r.tenant_id=j.tenant_id AND r.recovery_job_id=j.job_id AND r.resource_type='VM')
  OR NOT EXISTS (SELECT 1 FROM recovery_operations o WHERE o.tenant_id=j.tenant_id AND o.recovery_job_id=j.job_id AND o.stage='DISK_MATERIALIZE' AND o.status='CONFIRMED')
  OR NOT EXISTS (SELECT 1 FROM recovery_timeline t WHERE t.tenant_id=j.tenant_id AND t.recovery_job_id=j.job_id AND t.event_type='GUEST_READY')
  OR NOT EXISTS (SELECT 1 FROM recovery_timeline t WHERE t.tenant_id=j.tenant_id AND t.recovery_job_id=j.job_id AND t.event_type='OS_VALIDATED')
  OR NOT EXISTS (SELECT 1 FROM recovery_timeline t WHERE t.tenant_id=j.tenant_id AND t.recovery_job_id=j.job_id AND t.event_type='APPLICATION_VALIDATED')
 )`, tenant, recoveryJob).Scan(&readyIncomplete); err != nil {
		return err
	}
	partialDisks := 0
	if runDir := os.Getenv("DRAAS_CHECK_RUN_DIR"); runDir != "" {
		matches, err := filepath.Glob(filepath.Join(runDir, "*.partial"))
		if err != nil {
			return err
		}
		partialDisks = len(matches)
	}
	invalidFinalDisk := 0
	if disk := os.Getenv("DRAAS_CHECK_FINAL_DISK"); disk != "" {
		expected := os.Getenv("DRAAS_CHECK_DISK_SHA256")
		actual, hashErr := libvirtlab.HashFile(disk)
		if hashErr != nil || (expected != "" && actual != expected) {
			invalidFinalDisk = 1
			report["final_disk_error"] = fmt.Sprint(hashErr)
		}
		report["final_disk_sha256"] = actual
		report["expected_disk_sha256"] = expected
	}
	report["duplicate_vms"] = duplicateVMs
	report["duplicate_resources_by_operation"] = duplicateResourcesByOperation
	report["confirmed_operations_without_resource"] = confirmedWithoutResource
	report["resources_without_operation"] = resourcesWithoutOperation
	report["ownership_mismatches"] = ownershipMismatches
	report["active_leases"] = activeLeases
	report["ready_for_activation_incomplete"] = readyIncomplete
	report["partial_disks"] = partialDisks
	report["invalid_final_disk"] = invalidFinalDisk
	report["pass"] = duplicateVMs == 0 && duplicateResourcesByOperation == 0 && confirmedWithoutResource == 0 && resourcesWithoutOperation == 0 && ownershipMismatches == 0 && activeLeases <= 1 && readyIncomplete == 0 && partialDisks == 0 && invalidFinalDisk == 0
	return json.NewEncoder(os.Stdout).Encode(report)
}
