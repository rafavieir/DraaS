package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/draas-platform/draas/internal/platform"
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
	var duplicateVMs, confirmedWithoutResource, resourcesWithoutOperation int
	err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM (
 SELECT provider_resource_id FROM provider_resources WHERE tenant_id=$1 AND recovery_job_id=$2 AND resource_type='VM'
 GROUP BY provider_resource_id HAVING count(*) > 1
) d`, tenant, recoveryJob).Scan(&duplicateVMs)
	if err != nil {
		return err
	}
	err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recovery_operations o
WHERE o.tenant_id=$1 AND o.recovery_job_id=$2 AND o.status='CONFIRMED'
 AND o.resource_type <> ''
 AND NOT EXISTS (SELECT 1 FROM provider_resources r WHERE r.tenant_id=o.tenant_id AND r.operation_id=o.operation_id)`, tenant, recoveryJob).Scan(&confirmedWithoutResource)
	if err != nil {
		return err
	}
	err = app.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM provider_resources r
WHERE r.tenant_id=$1 AND r.recovery_job_id=$2 AND r.operation_id <> ''
 AND NOT EXISTS (SELECT 1 FROM recovery_operations o WHERE o.tenant_id=r.tenant_id AND o.operation_id=r.operation_id)`, tenant, recoveryJob).Scan(&resourcesWithoutOperation)
	if err != nil {
		return err
	}
	report["duplicate_vms"] = duplicateVMs
	report["confirmed_operations_without_resource"] = confirmedWithoutResource
	report["resources_without_operation"] = resourcesWithoutOperation
	report["pass"] = duplicateVMs == 0 && confirmedWithoutResource == 0 && resourcesWithoutOperation == 0
	return json.NewEncoder(os.Stdout).Encode(report)
}
