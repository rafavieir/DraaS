package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/draas-platform/draas/internal/blockstorage"
	"github.com/draas-platform/draas/internal/catalog"
	"github.com/draas-platform/draas/internal/failpoint"
	velerorp "github.com/draas-platform/draas/internal/velero"
	"github.com/draas-platform/draas/pkg/contracts"
	libvirtlab "github.com/draas-platform/draas/providers/libvirtlab"
)

const recoveryLeaseTTL = 30 * time.Second

func configuredProtectionBackend() string {
	if v := os.Getenv("DRAAS_PROTECTION_BACKEND"); v != "" {
		return v
	}
	return os.Getenv("DRAAS_STORAGE_BACKEND")
}

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
	case catalog.RecoveryStageNetworkPrepare:
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageResourceProvision, "RUNNING")
	case catalog.RecoveryStageResourceProvision:
		return a.handleResourceProvision(ctx, lease, owner)
	case catalog.RecoveryStageDiskMaterialize:
		return a.handleDiskMaterialize(ctx, lease, owner)
	case catalog.RecoveryStageDiskAttach:
		return a.handleDiskAttach(ctx, lease, owner)
	case catalog.RecoveryStagePowerOn:
		return a.handlePowerOn(ctx, lease, owner)
	case catalog.RecoveryStageWaitGuest:
		return a.handleWaitGuest(ctx, lease, owner)
	case catalog.RecoveryStageValidateOS:
		return a.handleValidateOS(ctx, lease, owner)
	case catalog.RecoveryStageValidateApplication:
		return a.handleValidateApplication(ctx, lease, owner)
	case catalog.RecoveryStageReadyForActivation, catalog.RecoveryStageCompleted:
		return nil
	default:
		_ = a.DB.AppendRecoveryTimeline(ctx, tenant, recoveryJobID, "RECOVERY_RECONCILE_DEFERRED_TO_LEGACY_RUNNER", lease.Stage, "", map[string]any{"owner": owner, "generation": lease.Generation})
		return nil
	}
}

func (a *App) vmHandleForRecovery(ctx context.Context, lease catalog.RecoveryLease) (contracts.ResourceHandle, error) {
	r, err := a.DB.ProviderResourceByJobType(ctx, lease.Tenant, lease.RecoveryJobID, "VM")
	if err != nil {
		return contracts.ResourceHandle{}, err
	}
	var h contracts.ResourceHandle
	if err = json.Unmarshal(r.Handle, &h); err != nil {
		return contracts.ResourceHandle{}, err
	}
	return h, nil
}

func (a *App) handleZFSDiskMaterialize(ctx context.Context, lease catalog.RecoveryLease, owner string) (bool, error) {
	if configuredProtectionBackend() != "zfs" {
		return false, nil
	}
	op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageDiskMaterialize, "CLONE_ZVOL", "ZFS", "ZVOL", "recovery", "CLONED")
	if err != nil {
		return true, err
	}
	job, err := a.DB.RecoveryJobByJob(ctx, lease.Tenant, lease.RecoveryJobID)
	if err != nil {
		return true, err
	}
	raw, _, status, err := a.DB.RecoveryPointManifest(ctx, lease.Tenant, job.RecoveryPointID)
	if err != nil {
		return true, err
	}
	if status != "VERIFIED" {
		return true, fmt.Errorf("recovery point %s is not VERIFIED", job.RecoveryPointID)
	}
	rp, ok := blockstorage.DecodeRecoveryPoint(raw)
	if !ok {
		return true, fmt.Errorf("recovery point %s is not a zfs block recovery point", job.RecoveryPointID)
	}
	pool := os.Getenv("DRAAS_ZFS_POOL")
	if pool == "" {
		return true, fmt.Errorf("DRAAS_ZFS_POOL is required for zfs recovery")
	}
	target, err := blockstorage.TargetDataset(pool, lease.Tenant, lease.RecoveryJobID)
	if err != nil {
		return true, err
	}
	if ok, err := a.DB.RenewRecoveryLease(ctx, lease.Tenant, lease.RecoveryJobID, owner, lease.Generation, recoveryLeaseTTL); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("lost recovery lease before zfs clone")
		}
		return true, err
	}
	if op.Status != catalog.OperationConfirmed {
		if err = a.DB.MarkRecoveryOperationStarted(ctx, lease.Tenant, op.OperationID); err != nil {
			return true, err
		}
	}
	mgr := blockstorage.ZFSManager{Pool: pool}
	clone, err := mgr.CloneFromSnapshot(ctx, blockstorage.CloneSpec{Tenant: lease.Tenant, RecoveryJobID: lease.RecoveryJobID, OperationID: op.OperationID, Snapshot: rp.ZFSSnapshot, TargetName: target})
	if err != nil {
		_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationUnknown, err.Error())
		return true, err
	}
	if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, clone.Dataset, ""); err != nil {
		return true, err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "ZFS_CLONE_CONFIRMED", catalog.RecoveryStageDiskMaterialize, op.OperationID, map[string]any{"snapshot": rp.ZFSSnapshot, "dataset": clone.Dataset, "device": clone.Device})
	if err = failpoint.Trigger(ctx, "after_disk_materialize"); err != nil {
		return true, err
	}
	return true, a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskAttach, "RUNNING")
}

func (a *App) handleKubernetesZFSDiskMaterialize(ctx context.Context, lease catalog.RecoveryLease, owner string) (bool, error) {
	if configuredProtectionBackend() != blockstorage.KubernetesZFSBackend {
		return false, nil
	}
	op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageDiskMaterialize, "CREATE_PVC_FROM_SNAPSHOT", "KUBERNETES", "PVC", "recovery", "BOUND")
	if err != nil {
		return true, err
	}
	job, err := a.DB.RecoveryJobByJob(ctx, lease.Tenant, lease.RecoveryJobID)
	if err != nil {
		return true, err
	}
	raw, _, status, err := a.DB.RecoveryPointManifest(ctx, lease.Tenant, job.RecoveryPointID)
	if err != nil {
		return true, err
	}
	if status != "VERIFIED" {
		return true, fmt.Errorf("recovery point %s is not VERIFIED", job.RecoveryPointID)
	}
	rp, ok := blockstorage.DecodeRecoveryPoint(raw)
	if !ok {
		return true, fmt.Errorf("recovery point %s is not a kubernetes zfs block recovery point", job.RecoveryPointID)
	}
	if ok, err := a.DB.RenewRecoveryLease(ctx, lease.Tenant, lease.RecoveryJobID, owner, lease.Generation, recoveryLeaseTTL); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("lost recovery lease before kubernetes zfs pvc clone")
		}
		return true, err
	}
	if op.Status != catalog.OperationConfirmed {
		if err = a.DB.MarkRecoveryOperationStarted(ctx, lease.Tenant, op.OperationID); err != nil {
			return true, err
		}
	}
	namespace := env("DRAAS_K8S_RECOVERY_NAMESPACE", "draas-recovery")
	storageClass := os.Getenv("DRAAS_K8S_ZFS_STORAGE_CLASS")
	mgr, err := blockstorage.NewKubernetesZFSManager(namespace, storageClass)
	if err != nil {
		return true, err
	}
	clone, err := mgr.CloneFromSnapshot(ctx, blockstorage.KubernetesCloneSpec{Tenant: lease.Tenant, RecoveryJobID: lease.RecoveryJobID, OperationID: op.OperationID, RecoveryPoint: rp})
	if err != nil {
		_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationUnknown, err.Error())
		return true, err
	}
	handle := contracts.ResourceHandle{ProviderResourceID: clone.Namespace + "/" + clone.PVC, ProviderCluster: "kubernetes", Metadata: map[string]string{
		"managed_by": "draas", "tenant": lease.Tenant, "recovery_job_id": lease.RecoveryJobID, "operation_id": op.OperationID,
		"namespace": clone.Namespace, "pvc": clone.PVC, "storage_class": clone.StorageClass, "volume_snapshot": clone.VolumeSnapshot, "protection_backend": blockstorage.KubernetesZFSBackend,
	}}
	_, err = a.DB.UpsertProviderResource(ctx, lease.Tenant, lease.RecoveryJobID, "", "KUBERNETES", "PVC", handle.ProviderResourceID, op.OperationID, "BOUND", "RECOVERY_PERSISTENT", handle)
	if err != nil {
		return true, err
	}
	if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, handle.ProviderResourceID, ""); err != nil {
		return true, err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "KUBERNETES_ZFS_PVC_BOUND", catalog.RecoveryStageDiskMaterialize, op.OperationID, map[string]any{"namespace": clone.Namespace, "pvc": clone.PVC, "storage_class": clone.StorageClass, "volume_snapshot": clone.VolumeSnapshot})
	if err = failpoint.Trigger(ctx, "after_disk_materialize"); err != nil {
		return true, err
	}
	return true, a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskAttach, "RUNNING")
}

func (a *App) handleDiskMaterialize(ctx context.Context, lease catalog.RecoveryLease, owner string) error {
	if handled, err := a.handleVeleroRestore(ctx, lease, owner); handled {
		return err
	}
	if handled, err := a.handleKubernetesZFSDiskMaterialize(ctx, lease, owner); handled {
		return err
	}
	if handled, err := a.handleZFSDiskMaterialize(ctx, lease, owner); handled {
		return err
	}

	op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageDiskMaterialize, "MATERIALIZE_DISK", "OBJECT_STORE", "DISK", "recovery", "VERIFIED")
	if err != nil {
		return err
	}
	job, err := a.DB.RecoveryJobByJob(ctx, lease.Tenant, lease.RecoveryJobID)
	if err != nil {
		return err
	}
	manifest, err := a.Engine.Load(ctx, lease.Tenant, job.RecoveryPointID)
	if err != nil {
		return err
	}
	target, err := recoveryDiskPath(lease.RecoveryJobID)
	if err != nil {
		return err
	}
	partial := target + ".partial"
	if hash, statErr := hashExistingDisk(target, manifest.StreamHash); statErr == nil {
		if op.Status != catalog.OperationConfirmed {
			if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, target, ""); err != nil {
				return err
			}
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "DISK_MATERIALIZE_NOOP_CONFIRMED", catalog.RecoveryStageDiskMaterialize, op.OperationID, map[string]any{"disk": target, "sha256": hash})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskAttach, "RUNNING")
	} else if !os.IsNotExist(statErr) {
		_ = os.Remove(target)
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "INVALID_FINAL_DISK_REMOVED", catalog.RecoveryStageDiskMaterialize, op.OperationID, map[string]any{"disk": target, "error": statErr.Error()})
	}
	if _, err = os.Stat(partial); err == nil {
		if removeErr := os.Remove(partial); removeErr != nil {
			return removeErr
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "PARTIAL_DISK_REMOVED", catalog.RecoveryStageDiskMaterialize, op.OperationID, map[string]any{"partial": partial})
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if ok, err := a.DB.RenewRecoveryLease(ctx, lease.Tenant, lease.RecoveryJobID, owner, lease.Generation, recoveryLeaseTTL); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("lost recovery lease before disk materialize")
		}
		return err
	}
	if err = a.DB.MarkRecoveryOperationStarted(ctx, lease.Tenant, op.OperationID); err != nil {
		return err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "DISK_MATERIALIZE_STARTED", catalog.RecoveryStageDiskMaterialize, op.OperationID, map[string]any{"recovery_point_id": manifest.ID, "target": target})
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	stats, restoreErr := a.Engine.Restore(ctx, manifest, lease.Tenant, manifest.ID, f, nil)
	if e := failpoint.Trigger(ctx, "during_disk_materialize"); e != nil && restoreErr == nil {
		restoreErr = e
	}
	if restoreErr == nil {
		restoreErr = f.Sync()
	}
	closeErr := f.Close()
	if restoreErr != nil {
		_ = os.Remove(partial)
		_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationUnknown, restoreErr.Error())
		return restoreErr
	}
	if closeErr != nil {
		_ = os.Remove(partial)
		_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationUnknown, closeErr.Error())
		return closeErr
	}
	hash, err := hashExistingDisk(partial, manifest.StreamHash)
	if err != nil {
		_ = os.Remove(partial)
		_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationFailed, err.Error())
		return err
	}
	if err = os.Rename(partial, target); err != nil {
		_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationUnknown, err.Error())
		return err
	}
	if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, target, ""); err != nil {
		return err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "DISK_MATERIALIZE_CONFIRMED", catalog.RecoveryStageDiskMaterialize, op.OperationID, map[string]any{"disk": target, "sha256": hash, "stats": stats})
	if err = failpoint.Trigger(ctx, "after_disk_materialize"); err != nil {
		return err
	}
	return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskAttach, "RUNNING")
}

func (a *App) handleVeleroRestore(ctx context.Context, lease catalog.RecoveryLease, owner string) (bool, error) {
	if configuredProtectionBackend() != velerorp.Backend {
		return false, nil
	}
	op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageDiskMaterialize, "CREATE_VELERO_RESTORE", "VELERO", "RESTORE", "recovery", "COMPLETED")
	if err != nil {
		return true, err
	}
	job, err := a.DB.RecoveryJobByJob(ctx, lease.Tenant, lease.RecoveryJobID)
	if err != nil {
		return true, err
	}
	raw, _, status, err := a.DB.RecoveryPointManifest(ctx, lease.Tenant, job.RecoveryPointID)
	if err != nil {
		return true, err
	}
	if status != "VERIFIED" {
		return true, fmt.Errorf("recovery point %s is not VERIFIED", job.RecoveryPointID)
	}
	rp, ok := velerorp.DecodeRecoveryPoint(raw)
	if !ok {
		return true, fmt.Errorf("recovery point %s is not a velero recovery point", job.RecoveryPointID)
	}
	namespace := env("DRAAS_VELERO_NAMESPACE", "velero")
	mgr, err := velerorp.NewManager(namespace)
	if err != nil {
		return true, err
	}
	if ok, err := a.DB.RenewRecoveryLease(ctx, lease.Tenant, lease.RecoveryJobID, owner, lease.Generation, recoveryLeaseTTL); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("lost recovery lease before velero restore")
		}
		return true, err
	}
	if op.Status != catalog.OperationConfirmed {
		if err = a.DB.MarkRecoveryOperationStarted(ctx, lease.Tenant, op.OperationID); err != nil {
			return true, err
		}
	}
	restore, err := mgr.CreateRestore(ctx, velerorp.RestoreSpec{Tenant: lease.Tenant, RecoveryJobID: lease.RecoveryJobID, OperationID: op.OperationID, Point: rp})
	if err != nil {
		_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationUnknown, err.Error())
		return true, err
	}
	handle := contracts.ResourceHandle{ProviderResourceID: restore.Namespace + "/" + restore.Name, ProviderCluster: "kubernetes", Metadata: map[string]string{
		"managed_by": "draas", "tenant": lease.Tenant, "recovery_job_id": lease.RecoveryJobID, "operation_id": op.OperationID,
		"namespace": restore.Namespace, "restore": restore.Name, "backup": restore.Backup, "phase": restore.Phase, "protection_backend": velerorp.Backend,
	}}
	_, err = a.DB.UpsertProviderResource(ctx, lease.Tenant, lease.RecoveryJobID, "", "VELERO", "RESTORE", handle.ProviderResourceID, op.OperationID, "COMPLETED", "RECOVERY_PERSISTENT", handle)
	if err != nil {
		return true, err
	}
	if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, handle.ProviderResourceID, ""); err != nil {
		return true, err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "VELERO_RESTORE_COMPLETED", catalog.RecoveryStageDiskMaterialize, op.OperationID, map[string]any{"namespace": restore.Namespace, "restore": restore.Name, "backup": restore.Backup, "phase": restore.Phase})
	if err = failpoint.Trigger(ctx, "after_disk_materialize"); err != nil {
		return true, err
	}
	return true, a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskAttach, "RUNNING")
}

func recoveryDiskPath(runID string) (string, error) {
	if os.Getenv("LIBVIRT_LAB_ROOT") == "" {
		return "", fmt.Errorf("LIBVIRT_LAB_ROOT is not configured")
	}
	p, err := libvirtlab.New(os.Getenv("LIBVIRT_LAB_ROOT"), os.Getenv("LIBVIRT_FIXTURE_BINARY"))
	if err != nil {
		return "", err
	}
	dir, err := p.Dir(runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "recovery.qcow2"), nil
}

func hashExistingDisk(path, expected string) (string, error) {
	hash, err := libvirtlab.HashFile(path)
	if err != nil {
		return "", err
	}
	if hash != expected {
		return hash, fmt.Errorf("disk hash mismatch for %s", path)
	}
	return hash, nil
}

func (a *App) handleKubernetesZFSDiskAttach(ctx context.Context, lease catalog.RecoveryLease, owner string) (bool, error) {
	if configuredProtectionBackend() != blockstorage.KubernetesZFSBackend {
		return false, nil
	}
	op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageDiskAttach, "ATTACH_PVC_TO_VALIDATOR", "KUBERNETES", "POD", "recovery-validator", "RUNNING")
	if err != nil {
		return true, err
	}
	pvcResource, err := a.DB.ProviderResourceByJobType(ctx, lease.Tenant, lease.RecoveryJobID, "PVC")
	if err != nil {
		return true, err
	}
	var pvcHandle contracts.ResourceHandle
	if err = json.Unmarshal(pvcResource.Handle, &pvcHandle); err != nil {
		return true, err
	}
	namespace := pvcHandle.Metadata["namespace"]
	pvc := pvcHandle.Metadata["pvc"]
	if namespace == "" || pvc == "" {
		return true, fmt.Errorf("kubernetes PVC handle missing namespace or pvc metadata")
	}
	mgr, err := blockstorage.NewKubernetesZFSManager(namespace, pvcHandle.Metadata["storage_class"])
	if err != nil {
		return true, err
	}
	pod, phase, err := mgr.CreateValidationPod(ctx, lease.Tenant, lease.RecoveryJobID, op.OperationID, pvc)
	if err != nil {
		_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationUnknown, err.Error())
		return true, err
	}
	handle := contracts.ResourceHandle{ProviderResourceID: namespace + "/" + pod, ProviderCluster: "kubernetes", Metadata: map[string]string{
		"managed_by": "draas", "tenant": lease.Tenant, "recovery_job_id": lease.RecoveryJobID, "operation_id": op.OperationID,
		"namespace": namespace, "pod": pod, "pvc": pvc, "phase": phase, "protection_backend": blockstorage.KubernetesZFSBackend,
	}}
	_, err = a.DB.UpsertProviderResource(ctx, lease.Tenant, lease.RecoveryJobID, "", "KUBERNETES", "POD", handle.ProviderResourceID, op.OperationID, "RUNNING", "RECOVERY_PERSISTENT", handle)
	if err != nil {
		return true, err
	}
	if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, handle.ProviderResourceID, ""); err != nil {
		return true, err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "KUBERNETES_ZFS_PVC_ATTACHED", catalog.RecoveryStageDiskAttach, op.OperationID, map[string]any{"namespace": namespace, "pod": pod, "pvc": pvc, "phase": phase})
	if err = failpoint.Trigger(ctx, "after_disk_attach"); err != nil {
		return true, err
	}
	return true, a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStagePowerOn, "RUNNING")
}

func (a *App) handleDiskAttach(ctx context.Context, lease catalog.RecoveryLease, owner string) error {
	if configuredProtectionBackend() == velerorp.Backend {
		op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageDiskAttach, "OBSERVE_VELERO_RESTORE", "VELERO", "RESTORE", "recovery", "RESTORED")
		if err != nil {
			return err
		}
		restore, err := a.DB.ProviderResourceByJobType(ctx, lease.Tenant, lease.RecoveryJobID, "RESTORE")
		if err != nil {
			return err
		}
		if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, restore.ProviderResourceID, ""); err != nil {
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "VELERO_RESTORE_OBSERVED", catalog.RecoveryStageDiskAttach, op.OperationID, map[string]any{"provider_resource_id": restore.ProviderResourceID})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStagePowerOn, "RUNNING")
	}
	if handled, err := a.handleKubernetesZFSDiskAttach(ctx, lease, owner); handled {
		return err
	}
	op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageDiskAttach, "ATTACH_DISK", "LIBVIRT_LAB", "DISK", "recovery", "ATTACHED")
	if err != nil {
		return err
	}
	h, err := a.vmHandleForRecovery(ctx, lease)
	if err != nil {
		return err
	}
	if h.Metadata["disk"] == "" {
		return fmt.Errorf("provider VM handle has no disk metadata")
	}
	if _, err = os.Stat(h.Metadata["disk"] + ".partial"); err == nil {
		return fmt.Errorf("refusing to attach partial disk %s", h.Metadata["disk"]+".partial")
	}
	if _, err = os.Stat(h.Metadata["disk"]); err != nil {
		return err
	}
	if op.Status != catalog.OperationConfirmed {
		if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, h.Metadata["disk"], ""); err != nil {
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "OPERATION_CONFIRMED", catalog.RecoveryStageDiskAttach, op.OperationID, map[string]any{"mode": "disk already defined in VM XML"})
	}
	if err = failpoint.Trigger(ctx, "after_disk_attach"); err != nil {
		return err
	}
	return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStagePowerOn, "RUNNING")
}

func (a *App) handlePowerOn(ctx context.Context, lease catalog.RecoveryLease, owner string) error {
	if configuredProtectionBackend() == velerorp.Backend {
		op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStagePowerOn, "OBSERVE_RESTORED_WORKLOAD", "VELERO", "RESTORE", "recovery", "AVAILABLE")
		if err != nil {
			return err
		}
		restore, err := a.DB.ProviderResourceByJobType(ctx, lease.Tenant, lease.RecoveryJobID, "RESTORE")
		if err != nil {
			return err
		}
		if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, restore.ProviderResourceID, ""); err != nil {
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "VELERO_WORKLOAD_RESTORED", catalog.RecoveryStagePowerOn, op.OperationID, map[string]any{"provider_resource_id": restore.ProviderResourceID})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageWaitGuest, "RUNNING")
	}
	if configuredProtectionBackend() == blockstorage.KubernetesZFSBackend {
		op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStagePowerOn, "OBSERVE_VALIDATOR_POD", "KUBERNETES", "POD", "recovery-validator", "RUNNING")
		if err != nil {
			return err
		}
		pod, err := a.DB.ProviderResourceByJobType(ctx, lease.Tenant, lease.RecoveryJobID, "POD")
		if err != nil {
			return err
		}
		if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, pod.ProviderResourceID, ""); err != nil {
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "KUBERNETES_VALIDATOR_RUNNING", catalog.RecoveryStagePowerOn, op.OperationID, map[string]any{"provider_resource_id": pod.ProviderResourceID})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageWaitGuest, "RUNNING")
	}
	op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStagePowerOn, "START_VM", "LIBVIRT_LAB", "VM", "recovery", "RUNNING")
	if err != nil {
		return err
	}
	p, h, err := a.libvirtProviderAndHandle(ctx, lease)
	if err != nil {
		return err
	}
	state, err := p.GetPowerState(ctx, h)
	if err != nil {
		return err
	}
	if state.State != "running" {
		if err = a.DB.MarkRecoveryOperationStarted(ctx, lease.Tenant, op.OperationID); err != nil {
			return err
		}
		if ok, err := a.DB.RenewRecoveryLease(ctx, lease.Tenant, lease.RecoveryJobID, owner, lease.Generation, recoveryLeaseTTL); err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("lost recovery lease before power on")
			}
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "PROVIDER_CALL_STARTED", catalog.RecoveryStagePowerOn, op.OperationID, map[string]any{"action": "start"})
		if err = p.Start(ctx, op.OperationID, h); err != nil {
			_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationUnknown, err.Error())
			return err
		}
		if err = failpoint.Trigger(ctx, "after_power_on"); err != nil {
			return err
		}
	}
	state, err = p.GetPowerState(ctx, h)
	if err != nil {
		return err
	}
	if state.State != "running" {
		return fmt.Errorf("VM did not reach running state: %s", state.State)
	}
	if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, h.ProviderResourceID, ""); err != nil {
		return err
	}
	return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageWaitGuest, "RUNNING")
}

func (a *App) handleWaitGuest(ctx context.Context, lease catalog.RecoveryLease, owner string) error {
	if configuredProtectionBackend() == velerorp.Backend {
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "VELERO_GUEST_READY", catalog.RecoveryStageWaitGuest, "", map[string]any{"validation": "Velero restore completed"})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageValidateOS, "RUNNING")
	}
	if configuredProtectionBackend() == blockstorage.KubernetesZFSBackend {
		pod, err := a.DB.ProviderResourceByJobType(ctx, lease.Tenant, lease.RecoveryJobID, "POD")
		if err != nil {
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "KUBERNETES_GUEST_READY", catalog.RecoveryStageWaitGuest, "", map[string]any{"provider_resource_id": pod.ProviderResourceID})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageValidateOS, "RUNNING")
	}
	p, h, err := a.libvirtProviderAndHandle(ctx, lease)
	if err != nil {
		return err
	}
	guest, err := p.GetGuestState(ctx, h)
	if err != nil || !guest.Healthy {
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "GUEST_NOT_READY", catalog.RecoveryStageWaitGuest, "", map[string]any{"error": fmt.Sprint(err)})
		return nil
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "GUEST_READY", catalog.RecoveryStageWaitGuest, "", guest)
	return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageValidateOS, "RUNNING")
}

func (a *App) handleValidateOS(ctx context.Context, lease catalog.RecoveryLease, owner string) error {
	if configuredProtectionBackend() == velerorp.Backend {
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "VELERO_OS_VALIDATED", catalog.RecoveryStageValidateOS, "", map[string]any{"validation": "Kubernetes objects and persistent volumes restored by Velero"})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageValidateApplication, "RUNNING")
	}
	if configuredProtectionBackend() == blockstorage.KubernetesZFSBackend {
		pod, err := a.DB.ProviderResourceByJobType(ctx, lease.Tenant, lease.RecoveryJobID, "POD")
		if err != nil {
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "KUBERNETES_OS_VALIDATED", catalog.RecoveryStageValidateOS, "", map[string]any{"provider_resource_id": pod.ProviderResourceID, "validation": "validator pod scheduled with read-only PVC mount"})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageValidateApplication, "RUNNING")
	}
	p, h, err := a.libvirtProviderAndHandle(ctx, lease)
	if err != nil {
		return err
	}
	console, ok := p.(interface {
		Command(context.Context, contracts.ResourceHandle, string) (string, error)
	})
	if !ok {
		return fmt.Errorf("provider does not expose lab command validation")
	}
	out, err := console.Command(ctx, h, "uname -s; cat /proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	if !strings.Contains(out, "Linux") {
		return fmt.Errorf("OS validation failed: %s", out)
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "OS_VALIDATED", catalog.RecoveryStageValidateOS, "", map[string]any{"console": out})
	return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageValidateApplication, "RUNNING")
}

func (a *App) handleValidateApplication(ctx context.Context, lease catalog.RecoveryLease, owner string) error {
	if err := failpoint.Trigger(ctx, "before_app_validation"); err != nil {
		return err
	}
	if configuredProtectionBackend() == velerorp.Backend {
		restore, err := a.DB.ProviderResourceByJobType(ctx, lease.Tenant, lease.RecoveryJobID, "RESTORE")
		if err != nil {
			return err
		}
		report := map[string]any{"provider": "VELERO", "protection_backend": velerorp.Backend, "restore": restore.ProviderResourceID, "production_ready": false}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "VELERO_APPLICATION_VALIDATED", catalog.RecoveryStageValidateApplication, "", report)
		if err = failpoint.Trigger(ctx, "after_app_validation"); err != nil {
			return err
		}
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageReadyForActivation, "READY_FOR_ACTIVATION")
	}
	if configuredProtectionBackend() == blockstorage.KubernetesZFSBackend {
		pod, err := a.DB.ProviderResourceByJobType(ctx, lease.Tenant, lease.RecoveryJobID, "POD")
		if err != nil {
			return err
		}
		report := map[string]any{"provider": "KUBERNETES", "protection_backend": blockstorage.KubernetesZFSBackend, "validator_pod": pod.ProviderResourceID, "production_ready": false}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "KUBERNETES_APPLICATION_VALIDATED", catalog.RecoveryStageValidateApplication, "", report)
		if err = failpoint.Trigger(ctx, "after_app_validation"); err != nil {
			return err
		}
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageReadyForActivation, "READY_FOR_ACTIVATION")
	}
	p, h, err := a.libvirtProviderAndHandle(ctx, lease)
	if err != nil {
		return err
	}
	guest, err := p.GetGuestState(ctx, h)
	if err != nil {
		return err
	}
	if !guest.Healthy {
		return fmt.Errorf("guest application is not healthy")
	}
	console, ok := p.(interface {
		Command(context.Context, contracts.ResourceHandle, string) (string, error)
	})
	if !ok {
		return fmt.Errorf("provider does not expose lab command validation")
	}
	out, err := console.Command(ctx, h, "sqlite3 /var/lib/draas-fixture/app.db 'PRAGMA integrity_check; SELECT version FROM app WHERE id=1;'")
	if err != nil {
		return err
	}
	if !strings.Contains(out, "ok") {
		return fmt.Errorf("application validation failed: %s", out)
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "APPLICATION_VALIDATED", catalog.RecoveryStageValidateApplication, "", map[string]any{"guest": guest, "sqlite": out})
	if err = failpoint.Trigger(ctx, "after_app_validation"); err != nil {
		return err
	}
	return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageReadyForActivation, "READY_FOR_ACTIVATION")
}

func (a *App) libvirtProviderAndHandle(ctx context.Context, lease catalog.RecoveryLease) (contracts.RecoveryProviderV2, contracts.ResourceHandle, error) {
	if os.Getenv("LIBVIRT_LAB_ROOT") == "" {
		return nil, contracts.ResourceHandle{}, fmt.Errorf("LIBVIRT_LAB_ROOT is not configured")
	}
	base, err := libvirtlab.New(os.Getenv("LIBVIRT_LAB_ROOT"), os.Getenv("LIBVIRT_FIXTURE_BINARY"))
	if err != nil {
		return nil, contracts.ResourceHandle{}, err
	}
	h, err := a.vmHandleForRecovery(ctx, lease)
	if err != nil {
		return nil, contracts.ResourceHandle{}, err
	}
	return base.ContractV2(), h, nil
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

func (a *App) handleResourceProvision(ctx context.Context, lease catalog.RecoveryLease, owner string) error {
	if configuredProtectionBackend() == velerorp.Backend {
		op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageResourceProvision, "ADMIT_VELERO_RESTORE", "VELERO", "NAMESPACE", "recovery", "ADMITTED")
		if err != nil {
			return err
		}
		namespace := env("DRAAS_VELERO_NAMESPACE", "velero")
		handle := contracts.ResourceHandle{ProviderResourceID: namespace, ProviderCluster: "kubernetes", Metadata: map[string]string{
			"managed_by": "draas", "tenant": lease.Tenant, "recovery_job_id": lease.RecoveryJobID, "operation_id": op.OperationID, "namespace": namespace, "protection_backend": velerorp.Backend,
		}}
		_, err = a.DB.UpsertProviderResource(ctx, lease.Tenant, lease.RecoveryJobID, "", "VELERO", "NAMESPACE", namespace, op.OperationID, "ADMITTED", "RECOVERY_PERSISTENT", handle)
		if err != nil {
			return err
		}
		if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, namespace, ""); err != nil {
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "VELERO_RESTORE_ADMITTED", catalog.RecoveryStageResourceProvision, op.OperationID, map[string]any{"namespace": namespace})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskMaterialize, "RUNNING")
	}
	if configuredProtectionBackend() == blockstorage.KubernetesZFSBackend {
		op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageResourceProvision, "ADMIT_KUBERNETES_RECOVERY", "KUBERNETES", "NAMESPACE", "recovery", "ADMITTED")
		if err != nil {
			return err
		}
		namespace := env("DRAAS_K8S_RECOVERY_NAMESPACE", "draas-recovery")
		handle := contracts.ResourceHandle{ProviderResourceID: namespace, ProviderCluster: "kubernetes", Metadata: map[string]string{
			"managed_by": "draas", "tenant": lease.Tenant, "recovery_job_id": lease.RecoveryJobID, "operation_id": op.OperationID, "namespace": namespace, "protection_backend": blockstorage.KubernetesZFSBackend,
		}}
		_, err = a.DB.UpsertProviderResource(ctx, lease.Tenant, lease.RecoveryJobID, "", "KUBERNETES", "NAMESPACE", namespace, op.OperationID, "ADMITTED", "RECOVERY_PERSISTENT", handle)
		if err != nil {
			return err
		}
		if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, namespace, ""); err != nil {
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "KUBERNETES_RECOVERY_ADMITTED", catalog.RecoveryStageResourceProvision, op.OperationID, map[string]any{"namespace": namespace})
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskMaterialize, "RUNNING")
	}
	op, err := a.DB.EnsureRecoveryOperation(ctx, lease.Tenant, lease.RecoveryJobID, catalog.RecoveryStageResourceProvision, "PROVISION_VM", "LIBVIRT_LAB", "VM", "recovery", "VM_DEFINED")
	if err != nil {
		return err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "STAGE_STARTED", catalog.RecoveryStageResourceProvision, op.OperationID, map[string]any{"owner": owner, "generation": lease.Generation})
	if _, err = a.DB.ProviderResourceByOperation(ctx, lease.Tenant, op.OperationID); err == nil && op.Status == catalog.OperationConfirmed {
		return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskMaterialize, "RUNNING")
	}
	if op.Status == catalog.OperationUnknown || op.Status == catalog.OperationConfirmed {
		adopted, err := a.adoptLibvirtResourceByOperation(ctx, lease, op.OperationID)
		if err != nil {
			return err
		}
		if adopted {
			return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskMaterialize, "RUNNING")
		}
	}
	if op.Status == catalog.OperationNotStarted {
		if ok, err := a.DB.RenewRecoveryLease(ctx, lease.Tenant, lease.RecoveryJobID, owner, lease.Generation, recoveryLeaseTTL); err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("lost recovery lease before provision intent")
			}
			return err
		}
		if err = a.DB.MarkRecoveryOperationStarted(ctx, lease.Tenant, op.OperationID); err != nil {
			return err
		}
		_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "OPERATION_UNKNOWN", catalog.RecoveryStageResourceProvision, op.OperationID, map[string]any{"reason": "intent persisted before provider call"})
	}
	target, err := recoveryDiskPath(lease.RecoveryJobID)
	if err != nil {
		return err
	}
	job, err := a.DB.RecoveryJobByJob(ctx, lease.Tenant, lease.RecoveryJobID)
	if err != nil {
		return err
	}
	if configuredProtectionBackend() == "zfs" {
		pool := os.Getenv("DRAAS_ZFS_POOL")
		if pool == "" {
			return fmt.Errorf("DRAAS_ZFS_POOL is required for zfs recovery")
		}
		dataset, err := blockstorage.TargetDataset(pool, lease.Tenant, lease.RecoveryJobID)
		if err != nil {
			return err
		}
		target = "/dev/zvol/" + dataset
	}
	p, err := libvirtlab.New(os.Getenv("LIBVIRT_LAB_ROOT"), os.Getenv("LIBVIRT_FIXTURE_BINARY"))
	if err != nil {
		return err
	}
	recovered, err := p.CreateWithOperation(ctx, lease.Tenant, lease.RecoveryJobID, lease.RecoveryJobID, "recovery", target, "", true, op.OperationID, job.RecoveryPointID, "TEST")
	if err != nil {
		_ = a.DB.FailRecoveryOperation(ctx, lease.Tenant, op.OperationID, catalog.OperationUnknown, err.Error())
		return err
	}
	h := handleFromLibvirtVM(recovered)
	_, err = a.DB.UpsertProviderResource(ctx, lease.Tenant, lease.RecoveryJobID, "", "LIBVIRT_LAB", "VM", h.ProviderResourceID, op.OperationID, "DEFINED", "RECOVERY_PERSISTENT", h)
	if err != nil {
		return err
	}
	if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, op.OperationID, h.ProviderResourceID, ""); err != nil {
		return err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "PROVIDER_RESOURCE_CREATED", catalog.RecoveryStageResourceProvision, op.OperationID, map[string]any{"provider_resource_id": h.ProviderResourceID})
	return a.advanceInternalStage(ctx, lease, owner, catalog.RecoveryStageDiskMaterialize, "RUNNING")
}

func handleFromLibvirtVM(vm libvirtlab.VM) contracts.ResourceHandle {
	return contracts.ResourceHandle{ProviderResourceID: vm.ID, ProviderCluster: "libvirt-qemu-system", Metadata: map[string]string{
		"managed_by": "draas", "tenant": vm.Tenant, "run_id": vm.RunID, "recovery_job_id": vm.RecoveryJobID, "disk": vm.Disk,
		"http_port": fmt.Sprint(vm.HTTPPort), "ssh_port": fmt.Sprint(vm.SSHPort), "isolated": fmt.Sprint(vm.Isolated), "lifetime": vm.Lifetime, "operation_id": vm.Operation,
	}}
}

func (a *App) adoptLibvirtResourceByOperation(ctx context.Context, lease catalog.RecoveryLease, operationID string) (bool, error) {
	if os.Getenv("LIBVIRT_LAB_ROOT") == "" {
		return false, nil
	}
	p, err := libvirtlab.New(os.Getenv("LIBVIRT_LAB_ROOT"), os.Getenv("LIBVIRT_FIXTURE_BINARY"))
	if err != nil {
		return false, err
	}
	finder, ok := p.ContractV2().(interface {
		FindResourceByOperation(context.Context, string) (contracts.ProviderResource, bool, error)
	})
	if !ok {
		return false, nil
	}
	resource, found, err := finder.FindResourceByOperation(ctx, operationID)
	if err != nil || !found {
		return false, err
	}
	if resource.ManagedBy != "draas" || resource.Handle.Metadata["managed_by"] != "draas" {
		return false, fmt.Errorf("provider resource is not owned by draas for operation %s", operationID)
	}
	if resource.Handle.Metadata["tenant"] != lease.Tenant {
		return false, fmt.Errorf("provider resource tenant mismatch for operation %s", operationID)
	}
	if resource.Handle.Metadata["recovery_job_id"] != "" && resource.Handle.Metadata["recovery_job_id"] != lease.RecoveryJobID {
		return false, fmt.Errorf("provider resource recovery job mismatch for operation %s", operationID)
	}
	if resource.Handle.Metadata["operation_id"] != operationID {
		return false, fmt.Errorf("provider resource operation mismatch for operation %s", operationID)
	}
	_, err = a.DB.UpsertProviderResource(ctx, lease.Tenant, lease.RecoveryJobID, "", "LIBVIRT_LAB", "VM", resource.Handle.ProviderResourceID, operationID, resource.State, "RECOVERY_PERSISTENT", resource.Handle)
	if err != nil {
		return false, err
	}
	if err = a.DB.ConfirmRecoveryOperation(ctx, lease.Tenant, operationID, resource.Handle.ProviderResourceID, resource.Handle.ProviderTaskID); err != nil {
		return false, err
	}
	_ = a.DB.AppendRecoveryTimeline(ctx, lease.Tenant, lease.RecoveryJobID, "PROVIDER_RESOURCE_ADOPTED", catalog.RecoveryStageResourceProvision, operationID, map[string]any{"provider_resource_id": resource.Handle.ProviderResourceID})
	return true, nil
}
