package platform

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/draas-platform/draas/internal/catalog"
	"github.com/draas-platform/draas/internal/failpoint"
)

const recoveryLeaseTTL = 30 * time.Second

func workerID() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	return fmt.Sprintf("worker-%d", os.Getpid())
}

func (a *App) RecoveryReconcilerLoop(ctx context.Context) {
	id := workerID()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if err := a.scanRecoveries(ctx, id); err != nil && ctx.Err() == nil {
			slog.Error("recovery scanner", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *App) scanRecoveries(ctx context.Context, owner string) error {
	jobs, err := a.DB.RunnableRecoveryJobs(ctx, 10)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = a.ReconcileRecoveryJob(ctx, j.Tenant, j.RecoveryJobID, owner); err != nil {
			slog.Error("recovery reconcile", "tenant", j.Tenant, "recovery_job", j.RecoveryJobID, "error", err)
		}
	}
	return nil
}

func (a *App) ReconcileRecoveryJob(ctx context.Context, tenant, recoveryJobID, owner string) error {
	lease, ok, err := a.DB.AcquireRecoveryLease(ctx, tenant, recoveryJobID, owner, recoveryLeaseTTL)
	if err != nil || !ok {
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.DB.ReleaseRecoveryLease(releaseCtx, tenant, recoveryJobID, owner, lease.Generation); err != nil {
			slog.Error("release recovery lease", "tenant", tenant, "recovery_job", recoveryJobID, "error", err)
		}
	}()
	if err = failpoint.Trigger(ctx, "before_recovery_reconcile"); err != nil {
		return err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, tenant, recoveryJobID, "RECOVERY_RECONCILE_STARTED", lease.Stage, "", map[string]any{"owner": owner, "generation": lease.Generation})
	switch lease.Stage {
	case "", "QUEUED", catalog.RecoveryStageRequested:
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStagePreflight, "RUNNING")
	case catalog.RecoveryStagePreflight:
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageAdmission, "RUNNING")
	case catalog.RecoveryStageAdmission:
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageNetworkPrepare, "RUNNING")
	case catalog.RecoveryStageReadyForActivation, catalog.RecoveryStageCompleted:
		return nil
	default:
		_ = a.DB.AppendRecoveryTimeline(ctx, tenant, recoveryJobID, "RECOVERY_RECONCILE_DEFERRED_TO_LEGACY_RUNNER", lease.Stage, "", map[string]any{"owner": owner, "generation": lease.Generation})
		return nil
	}
}

func (a *App) advanceInternalStage(ctx context.Context, lease catalog.RecoveryLease, owner, nextStage, status string) error {
	if err := failpoint.Trigger(ctx, "before_stage_advance"); err != nil {
		return err
	}
	ok, err := a.DB.RenewRecoveryLease(ctx, lease.Tenant, lease.RecoveryJobID, owner, lease.Generation, recoveryLeaseTTL)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("lost recovery lease before stage advance")
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "LEASE_LOST", lease.Stage, "", map[string]any{"owner": owner, "generation": lease.Generation})
		return err
	}
	ok, err = a.DB.AdvanceRecoveryStage(ctx, lease.Tenant, lease.RecoveryJobID, owner, lease.Generation, lease.Version, nextStage, status)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("optimistic recovery stage advance lost")
		}
		return err
	}
	return failpoint.Trigger(ctx, "after_stage_advance")
}
