package velero

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDecodeRecoveryPointSupportsNewAndLegacyModels(t *testing.T) {
	rp, ok := DecodeRecoveryPoint([]byte(`{"engine":"velero","tenant":"t","workload_id":"w","recovery_point":"p","external_reference":{"namespace":"velero","backup_name":"backup-1","backup_uid":"uid-1"}}`))
	if !ok || rp.ExternalReference.Backup != "backup-1" || rp.ExternalReference.UID != "uid-1" || rp.Engine != Backend {
		t.Fatalf("expected new velero recovery point, got ok=%v rp=%+v", ok, rp)
	}

	legacy, ok := DecodeRecoveryPoint([]byte(`{"storage_backend":"velero","tenant":"t","workload_id":"w","recovery_point":"p","velero_namespace":"velero","velero_backup":"backup-legacy"}`))
	if !ok || legacy.Engine != Backend || legacy.ExternalReference.Backup != "backup-legacy" || legacy.VeleroBackup != "backup-legacy" {
		t.Fatalf("expected legacy velero recovery point compatibility, got ok=%v rp=%+v", ok, legacy)
	}

	if _, ok := DecodeRecoveryPoint([]byte(`{"storage_backend":"kubernetes-zfs"}`)); ok {
		t.Fatal("non-velero point must be rejected")
	}
}

func TestRestoreObjectHasDeterministicOwnership(t *testing.T) {
	rp := RecoveryPoint{RecoveryPoint: "point-1", VeleroBackup: "backup-1", IncludedNamespaces: []string{"app"}, RestoreNamespaceMapping: map[string]string{"app": "app-dr"}}
	o := restoreObject("velero", "restore-1", rp, "tenant", "job", "op", "backup-uid")
	if o.GetName() != "restore-1" || o.GetNamespace() != "velero" {
		t.Fatalf("unexpected identity %s/%s", o.GetNamespace(), o.GetName())
	}
	if got := o.GetLabels()[LabelManagedBy]; got != "draas" {
		t.Fatalf("expected managed-by label, got %q", got)
	}
	if got := o.GetAnnotations()[AnnotationBackupUID]; got != "backup-uid" {
		t.Fatalf("expected backup uid annotation, got %q", got)
	}
	spec := o.Object["spec"].(map[string]any)
	if spec["backupName"] != "backup-1" || spec["restorePVs"] != true {
		t.Fatalf("unexpected spec %#v", spec)
	}
}

func TestValidateRestoreOwnershipRejectsMismatchedExistingRestore(t *testing.T) {
	o := restoreObject("velero", "restore-1", RecoveryPoint{RecoveryPoint: "point-1", VeleroBackup: "backup-1"}, "tenant", "job", "op", "backup-uid")
	if err := validateRestoreOwnership(o, "tenant", "job", "point-1", "op", "backup-uid"); err != nil {
		t.Fatalf("expected ownership to validate: %v", err)
	}
	o.SetAnnotations(map[string]string{AnnotationOperationID: "other", AnnotationBackupUID: "backup-uid"})
	if err := validateRestoreOwnership(o, "tenant", "job", "point-1", "op", "backup-uid"); err == nil || !strings.Contains(err.Error(), "refusing to adopt") {
		t.Fatalf("expected adoption rejection, got %v", err)
	}
}

func TestRestoreResultAndPhaseMapping(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "restore-1", "annotations": map[string]any{AnnotationBackupUID: "uid-1"}},
		"spec":     map[string]any{"backupName": "backup-1"},
		"status":   map[string]any{"phase": "PartiallyFailed", "warnings": int64(2), "errors": int64(1)},
	}}
	result := restoreResultFromObject("velero", obj)
	if result.Status != RestoreStatusPartiallyFailed || result.BackupUID != "uid-1" || result.Warnings != 2 || result.Errors != 1 {
		t.Fatalf("unexpected restore result: %+v", result)
	}
	if MapRestorePhase("FailedValidation") != RestoreStatusFailedValidation || MapRestorePhase("mystery") != RestoreStatusUnknown {
		t.Fatalf("unexpected phase mapping")
	}
}

func TestProtectionLevel(t *testing.T) {
	level, persistent, offsite := ProtectionLevel(BackupInfo{})
	if level != "CONFIG_ONLY" || persistent.Method != "none" || offsite {
		t.Fatalf("unexpected config-only level: %s %+v %v", level, persistent, offsite)
	}
	level, persistent, offsite = ProtectionLevel(BackupInfo{UsesCSISnapshots: true})
	if level != "LOCAL_RECOVERY" || persistent.Method != "csi_snapshot" || offsite {
		t.Fatalf("unexpected local level: %s %+v %v", level, persistent, offsite)
	}
	level, persistent, offsite = ProtectionLevel(BackupInfo{UsesCSISnapshots: true, UsesDataMover: true})
	if level != "OFFSITE_RECOVERABLE" || persistent.Method != "velero_data_mover" || !offsite {
		t.Fatalf("unexpected offsite level: %s %+v %v", level, persistent, offsite)
	}
}
