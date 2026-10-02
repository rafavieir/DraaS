package velero

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	Backend                 = "velero"
	MinimumSupportedVersion = "1.12.0"
	LabelManagedBy          = "app.kubernetes.io/managed-by"
	LabelTenant             = "dr.draas.local/tenant"
	LabelRecoveryJob        = "dr.draas.local/recovery-job"
	LabelRecoveryPoint      = "dr.draas.local/recovery-point"
	AnnotationOperationID   = "dr.draas.local/operation-id"
	AnnotationBackupUID     = "dr.draas.local/velero-backup-uid"
)

var (
	backupGVR  = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "backups"}
	restoreGVR = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "restores"}
	bslGVR     = schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "backupstoragelocations"}
)

type RecoveryPoint struct {
	FormatVersion           string            `json:"format_version"`
	Engine                  string            `json:"engine"`
	StorageBackend          string            `json:"storage_backend,omitempty"` // legacy compatibility
	Tenant                  string            `json:"tenant"`
	WorkloadID              string            `json:"workload_id"`
	RecoveryPoint           string            `json:"recovery_point"`
	ExternalReference       ExternalReference `json:"external_reference"`
	VeleroNamespace         string            `json:"velero_namespace,omitempty"` // legacy compatibility
	VeleroBackup            string            `json:"velero_backup,omitempty"`    // legacy compatibility
	IncludedNamespaces      []string          `json:"included_namespaces,omitempty"`
	RestoreNamespaceMapping map[string]string `json:"restore_namespace_mapping,omitempty"`
	ProtectionLevel         string            `json:"protection_level"`
	PersistentData          DataProtection    `json:"persistent_data"`
	OffsiteCopy             bool              `json:"offsite_copy"`
	Consistency             string            `json:"consistency"`
	BackupPhase             string            `json:"backup_phase,omitempty"`
	Warnings                int64             `json:"warnings,omitempty"`
	Errors                  int64             `json:"errors,omitempty"`
	StartedAt               string            `json:"started_at,omitempty"`
	CompletedAt             string            `json:"completed_at,omitempty"`
	Metadata                map[string]string `json:"metadata,omitempty"`
}

type ExternalReference struct {
	Namespace string `json:"namespace"`
	Backup    string `json:"backup_name"`
	UID       string `json:"backup_uid,omitempty"`
}

type DataProtection struct {
	Method  string `json:"method"`
	Offsite bool   `json:"offsite"`
}

type BackupInfo struct {
	Namespace             string
	Name                  string
	UID                   string
	Phase                 string
	BackupStorageLocation string
	StartTimestamp        string
	CompletionTimestamp   string
	Expiration            string
	Warnings              int64
	Errors                int64
	IncludedNamespaces    []string
	UsesCSISnapshots      bool
	UsesDataMover         bool
}

type RestoreStatus string

const (
	RestoreStatusNew               RestoreStatus = "NEW"
	RestoreStatusInProgress        RestoreStatus = "IN_PROGRESS"
	RestoreStatusWaitingForPlugins RestoreStatus = "WAITING_FOR_PLUGIN_OPERATIONS"
	RestoreStatusCompleted         RestoreStatus = "COMPLETED"
	RestoreStatusPartiallyFailed   RestoreStatus = "PARTIALLY_FAILED"
	RestoreStatusFailed            RestoreStatus = "FAILED"
	RestoreStatusFailedValidation  RestoreStatus = "FAILED_VALIDATION"
	RestoreStatusUnknown           RestoreStatus = "UNKNOWN"
)

type Manager struct {
	Namespace string
	Client    dynamic.Interface
}

type RestoreSpec struct {
	Tenant        string
	RecoveryJobID string
	OperationID   string
	Point         RecoveryPoint
	Name          string
}

type BackupSpec struct {
	Tenant             string
	WorkloadID         string
	RecoveryPointID    string
	Name               string
	IncludedNamespaces []string
	StorageLocation    string
	SnapshotVolumes    bool
	TTL                time.Duration
}

type RestoreResult struct {
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Backup      string            `json:"backup"`
	BackupUID   string            `json:"backup_uid,omitempty"`
	Phase       string            `json:"phase"`
	Status      RestoreStatus     `json:"status"`
	StartedAt   string            `json:"started_at,omitempty"`
	CompletedAt string            `json:"completed_at,omitempty"`
	Warnings    int64             `json:"warnings,omitempty"`
	Errors      int64             `json:"errors,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func DecodeRecoveryPoint(b []byte) (RecoveryPoint, bool) {
	var rp RecoveryPoint
	if len(b) == 0 {
		return rp, false
	}
	if err := json.Unmarshal(b, &rp); err != nil {
		return rp, false
	}
	rp.normalize()
	return rp, rp.Engine == Backend && rp.ExternalReference.Backup != ""
}

func (rp *RecoveryPoint) normalize() {
	if rp.Engine == "" && rp.StorageBackend == Backend {
		rp.Engine = Backend
	}
	if rp.ExternalReference.Namespace == "" {
		rp.ExternalReference.Namespace = rp.VeleroNamespace
	}
	if rp.ExternalReference.Backup == "" {
		rp.ExternalReference.Backup = rp.VeleroBackup
	}
	if rp.VeleroNamespace == "" {
		rp.VeleroNamespace = rp.ExternalReference.Namespace
	}
	if rp.VeleroBackup == "" {
		rp.VeleroBackup = rp.ExternalReference.Backup
	}
	if rp.FormatVersion == "" {
		rp.FormatVersion = "draas.recovery-point/v1"
	}
	if rp.Engine == "" {
		rp.Engine = Backend
	}
	if rp.Consistency == "" {
		rp.Consistency = "VELERO_BACKUP"
	}
	if rp.Metadata == nil {
		rp.Metadata = map[string]string{}
	}
}

func NewManager(namespace string) (Manager, error) {
	cfg, err := kubeConfig()
	if err != nil {
		return Manager{}, err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return Manager{}, err
	}
	if namespace == "" {
		namespace = "velero"
	}
	return Manager{Namespace: namespace, Client: client}, nil
}

func kubeConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		if home, err := os.UserHomeDir(); err == nil {
			kubeconfig = home + "/.kube/config"
		}
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func (m Manager) Check(ctx context.Context) error {
	if m.Client == nil {
		return errors.New("kubernetes dynamic client is required")
	}
	if _, err := m.Client.Resource(backupGVR).Namespace(m.Namespace).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("velero Backup API unavailable in namespace %s: %w", m.Namespace, err)
	}
	if _, err := m.Client.Resource(restoreGVR).Namespace(m.Namespace).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("velero Restore API unavailable in namespace %s: %w", m.Namespace, err)
	}
	if _, err := m.Client.Resource(bslGVR).Namespace(m.Namespace).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("velero BackupStorageLocation API unavailable in namespace %s: %w", m.Namespace, err)
	}
	return nil
}

func (m Manager) BackupInfo(ctx context.Context, name string) (BackupInfo, error) {
	if m.Client == nil {
		return BackupInfo{}, errors.New("kubernetes dynamic client is required")
	}
	if name == "" {
		return BackupInfo{}, errors.New("velero backup name is required")
	}
	obj, err := m.Client.Resource(backupGVR).Namespace(m.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return BackupInfo{}, err
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	bsl, _, _ := unstructured.NestedString(obj.Object, "spec", "storageLocation")
	start, _, _ := unstructured.NestedString(obj.Object, "status", "startTimestamp")
	complete, _, _ := unstructured.NestedString(obj.Object, "status", "completionTimestamp")
	expiration, _, _ := unstructured.NestedString(obj.Object, "status", "expiration")
	warnings, _, _ := unstructured.NestedInt64(obj.Object, "status", "warnings")
	errorsCount, _, _ := unstructured.NestedInt64(obj.Object, "status", "errors")
	included := nestedStringSlice(obj.Object, "spec", "includedNamespaces")
	usesCSI := hasNested(obj.Object, "status", "csiVolumeSnapshots") || hasNested(obj.Object, "status", "volumeSnapshots") || nestedIntPositive(obj.Object, "status", "csiVolumeSnapshotsAttempted") || nestedIntPositive(obj.Object, "status", "csiVolumeSnapshotsCompleted")
	usesDataMover := hasNested(obj.Object, "status", "dataMover") || hasNested(obj.Object, "status", "snapshotBackup") || stringMapContains(obj.GetAnnotations(), "datamover")
	return BackupInfo{Namespace: m.Namespace, Name: name, UID: string(obj.GetUID()), Phase: phase, BackupStorageLocation: bsl, StartTimestamp: start, CompletionTimestamp: complete, Expiration: expiration, Warnings: warnings, Errors: errorsCount, IncludedNamespaces: included, UsesCSISnapshots: usesCSI, UsesDataMover: usesDataMover}, nil
}

func (m Manager) BackupExists(ctx context.Context, name string) error {
	_, err := m.BackupInfo(ctx, name)
	return err
}

func (m Manager) CreateBackup(ctx context.Context, spec BackupSpec, timeout time.Duration) (BackupInfo, error) {
	if m.Client == nil {
		return BackupInfo{}, errors.New("kubernetes dynamic client is required")
	}
	if spec.Name == "" {
		spec.Name = RestoreName(spec.Tenant, spec.RecoveryPointID)
	}
	if spec.StorageLocation == "" {
		spec.StorageLocation = "default"
	}
	if spec.TTL == 0 {
		spec.TTL = 24 * time.Hour
	}
	obj := backupObject(m.Namespace, spec)
	if _, err := m.Client.Resource(backupGVR).Namespace(m.Namespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return BackupInfo{}, err
		}
		existing, getErr := m.Client.Resource(backupGVR).Namespace(m.Namespace).Get(ctx, spec.Name, metav1.GetOptions{})
		if getErr != nil {
			return BackupInfo{}, getErr
		}
		labels := existing.GetLabels()
		if labels[LabelManagedBy] != "draas" || labels[LabelTenant] != spec.Tenant || labels[LabelRecoveryPoint] != spec.RecoveryPointID {
			return BackupInfo{}, fmt.Errorf("refusing to adopt Velero Backup %s/%s: ownership mismatch", m.Namespace, spec.Name)
		}
	}
	return m.waitBackup(ctx, spec.Name, timeout)
}

func (m Manager) waitBackup(ctx context.Context, name string, timeout time.Duration) (BackupInfo, error) {
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		info, err := m.BackupInfo(deadline, name)
		if err != nil {
			return BackupInfo{}, err
		}
		switch info.Phase {
		case "Completed":
			return info, nil
		case "Failed", "PartiallyFailed", "FailedValidation":
			return info, fmt.Errorf("velero backup %s/%s finished with phase %s warnings=%d errors=%d", m.Namespace, name, info.Phase, info.Warnings, info.Errors)
		}
		select {
		case <-deadline.Done():
			return info, fmt.Errorf("velero backup %s/%s did not complete before timeout; current phase=%s", m.Namespace, name, info.Phase)
		case <-tick.C:
		}
	}
}

func backupObject(namespace string, spec BackupSpec) *unstructured.Unstructured {
	included := make([]any, 0, len(spec.IncludedNamespaces))
	for _, ns := range spec.IncludedNamespaces {
		if strings.TrimSpace(ns) != "" {
			included = append(included, strings.TrimSpace(ns))
		}
	}
	backupSpec := map[string]any{
		"storageLocation": spec.StorageLocation,
		"snapshotVolumes": spec.SnapshotVolumes,
		"ttl":             spec.TTL.String(),
	}
	if len(included) > 0 {
		backupSpec["includedNamespaces"] = included
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "velero.io/v1",
		"kind":       "Backup",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      spec.Name,
			"labels": map[string]any{
				LabelManagedBy:     "draas",
				LabelTenant:        spec.Tenant,
				LabelRecoveryPoint: spec.RecoveryPointID,
			},
		},
		"spec": backupSpec,
	}}
}

func (m Manager) CreateRestore(ctx context.Context, spec RestoreSpec) (RestoreResult, error) {
	if m.Client == nil {
		return RestoreResult{}, errors.New("kubernetes dynamic client is required")
	}
	spec.Point.normalize()
	if spec.Point.ExternalReference.Namespace != "" && spec.Point.ExternalReference.Namespace != m.Namespace {
		return RestoreResult{}, fmt.Errorf("recovery point belongs to Velero namespace %s, manager is %s", spec.Point.ExternalReference.Namespace, m.Namespace)
	}
	if spec.Name == "" {
		spec.Name = RestoreName(spec.Tenant, spec.RecoveryJobID)
	}
	backup, err := m.BackupInfo(ctx, spec.Point.ExternalReference.Backup)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("velero backup %s/%s unavailable: %w", m.Namespace, spec.Point.ExternalReference.Backup, err)
	}
	if backup.Phase != "Completed" {
		return RestoreResult{}, fmt.Errorf("velero backup %s/%s is not Completed; phase=%s", m.Namespace, backup.Name, backup.Phase)
	}
	restore := restoreObject(m.Namespace, spec.Name, spec.Point, spec.Tenant, spec.RecoveryJobID, spec.OperationID, backup.UID)
	if _, err = m.Client.Resource(restoreGVR).Namespace(m.Namespace).Create(ctx, restore, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return RestoreResult{}, err
		}
		existing, getErr := m.Client.Resource(restoreGVR).Namespace(m.Namespace).Get(ctx, spec.Name, metav1.GetOptions{})
		if getErr != nil {
			return RestoreResult{}, getErr
		}
		if err = validateRestoreOwnership(existing, spec.Tenant, spec.RecoveryJobID, spec.Point.RecoveryPoint, spec.OperationID, backup.UID); err != nil {
			return RestoreResult{}, err
		}
	}
	return m.waitRestore(ctx, spec.Name, 15*time.Minute)
}

func (m Manager) waitRestore(ctx context.Context, name string, timeout time.Duration) (RestoreResult, error) {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		restore, err := m.Client.Resource(restoreGVR).Namespace(m.Namespace).Get(deadline, name, metav1.GetOptions{})
		if err != nil {
			return RestoreResult{}, err
		}
		result := restoreResultFromObject(m.Namespace, restore)
		switch result.Status {
		case RestoreStatusCompleted:
			return result, nil
		case RestoreStatusFailed, RestoreStatusFailedValidation, RestoreStatusPartiallyFailed:
			return result, fmt.Errorf("velero restore %s/%s finished with phase %s warnings=%d errors=%d", m.Namespace, name, result.Phase, result.Warnings, result.Errors)
		}
		select {
		case <-deadline.Done():
			if result.Phase == "" {
				result.Phase = "New"
				result.Status = RestoreStatusNew
			}
			return result, fmt.Errorf("velero restore %s/%s did not complete before timeout; current phase=%s", m.Namespace, name, result.Phase)
		case <-tick.C:
		}
	}
}

func restoreResultFromObject(namespace string, obj *unstructured.Unstructured) RestoreResult {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	start, _, _ := unstructured.NestedString(obj.Object, "status", "startTimestamp")
	complete, _, _ := unstructured.NestedString(obj.Object, "status", "completionTimestamp")
	warnings, _, _ := unstructured.NestedInt64(obj.Object, "status", "warnings")
	errorsCount, _, _ := unstructured.NestedInt64(obj.Object, "status", "errors")
	backup, _, _ := unstructured.NestedString(obj.Object, "spec", "backupName")
	return RestoreResult{Namespace: namespace, Name: obj.GetName(), Backup: backup, BackupUID: obj.GetAnnotations()[AnnotationBackupUID], Phase: phase, Status: MapRestorePhase(phase), StartedAt: start, CompletedAt: complete, Warnings: warnings, Errors: errorsCount, Metadata: map[string]string{"backend": Backend}}
}

func MapRestorePhase(phase string) RestoreStatus {
	switch phase {
	case "", "New":
		return RestoreStatusNew
	case "InProgress":
		return RestoreStatusInProgress
	case "WaitingForPluginOperations", "WaitingForPluginOperationsPartiallyFailed":
		return RestoreStatusWaitingForPlugins
	case "Completed":
		return RestoreStatusCompleted
	case "PartiallyFailed":
		return RestoreStatusPartiallyFailed
	case "Failed":
		return RestoreStatusFailed
	case "FailedValidation":
		return RestoreStatusFailedValidation
	default:
		return RestoreStatusUnknown
	}
}

func RestoreName(tenant, recoveryJobID string) string {
	name := "draas-" + tenant + "-" + recoveryJobID
	if len(name) <= 63 {
		return name
	}
	return name[:63]
}

func ProtectionLevel(info BackupInfo) (level string, persistent DataProtection, offsite bool) {
	if !info.UsesCSISnapshots && !info.UsesDataMover {
		return "CONFIG_ONLY", DataProtection{Method: "none", Offsite: false}, false
	}
	if info.UsesDataMover {
		return "OFFSITE_RECOVERABLE", DataProtection{Method: "velero_data_mover", Offsite: true}, true
	}
	return "LOCAL_RECOVERY", DataProtection{Method: "csi_snapshot", Offsite: false}, false
}

func BuildRecoveryPoint(tenant, workload, point string, info BackupInfo, included []string, mapping map[string]string, imported bool) RecoveryPoint {
	level, persistent, offsite := ProtectionLevel(info)
	metadata := map[string]string{"restore_engine": Backend, "backup_storage_location": info.BackupStorageLocation, "minimum_supported_velero": MinimumSupportedVersion}
	if imported {
		metadata["ownership"] = "imported"
	} else {
		metadata["ownership"] = "managed"
	}
	return RecoveryPoint{FormatVersion: "draas.recovery-point/v1", Engine: Backend, StorageBackend: Backend, Tenant: tenant, WorkloadID: workload, RecoveryPoint: point, ExternalReference: ExternalReference{Namespace: info.Namespace, Backup: info.Name, UID: info.UID}, VeleroNamespace: info.Namespace, VeleroBackup: info.Name, IncludedNamespaces: included, RestoreNamespaceMapping: mapping, ProtectionLevel: level, PersistentData: persistent, OffsiteCopy: offsite, Consistency: "VELERO_BACKUP", BackupPhase: info.Phase, Warnings: info.Warnings, Errors: info.Errors, StartedAt: info.StartTimestamp, CompletedAt: info.CompletionTimestamp, Metadata: metadata}
}

func restoreObject(namespace, name string, rp RecoveryPoint, tenant, recoveryJobID, operationID, backupUID string) *unstructured.Unstructured {
	rp.normalize()
	spec := map[string]any{"backupName": rp.ExternalReference.Backup, "restorePVs": true}
	if len(rp.IncludedNamespaces) > 0 {
		items := make([]any, 0, len(rp.IncludedNamespaces))
		for _, ns := range rp.IncludedNamespaces {
			items = append(items, ns)
		}
		spec["includedNamespaces"] = items
	}
	if len(rp.RestoreNamespaceMapping) > 0 {
		m := map[string]any{}
		for k, v := range rp.RestoreNamespaceMapping {
			m[k] = v
		}
		spec["namespaceMapping"] = m
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "velero.io/v1",
		"kind":       "Restore",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
			"labels": map[string]any{
				LabelManagedBy:     "draas",
				LabelTenant:        tenant,
				LabelRecoveryJob:   recoveryJobID,
				LabelRecoveryPoint: rp.RecoveryPoint,
			},
			"annotations": map[string]any{AnnotationOperationID: operationID, AnnotationBackupUID: backupUID},
		},
		"spec": spec,
	}}
}

func validateRestoreOwnership(obj *unstructured.Unstructured, tenant, recoveryJobID, recoveryPoint, operationID, backupUID string) error {
	labels := obj.GetLabels()
	annotations := obj.GetAnnotations()
	checks := map[string][2]string{
		LabelManagedBy:        {labels[LabelManagedBy], "draas"},
		LabelTenant:           {labels[LabelTenant], tenant},
		LabelRecoveryJob:      {labels[LabelRecoveryJob], recoveryJobID},
		LabelRecoveryPoint:    {labels[LabelRecoveryPoint], recoveryPoint},
		AnnotationOperationID: {annotations[AnnotationOperationID], operationID},
		AnnotationBackupUID:   {annotations[AnnotationBackupUID], backupUID},
	}
	for key, pair := range checks {
		if pair[0] != pair[1] {
			return fmt.Errorf("refusing to adopt Velero Restore %s/%s: ownership %s=%q expected %q", obj.GetNamespace(), obj.GetName(), key, pair[0], pair[1])
		}
	}
	return nil
}

func nestedStringSlice(obj map[string]any, fields ...string) []string {
	items, ok, _ := unstructured.NestedStringSlice(obj, fields...)
	if ok {
		return items
	}
	anyItems, ok, _ := unstructured.NestedSlice(obj, fields...)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(anyItems))
	for _, item := range anyItems {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func hasNested(obj map[string]any, fields ...string) bool {
	_, ok, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	return ok
}

func stringMapContains(m map[string]string, needle string) bool {
	for k, v := range m {
		if strings.Contains(strings.ToLower(k), needle) || strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}

func nestedIntPositive(obj map[string]any, fields ...string) bool {
	v, ok, _ := unstructured.NestedInt64(obj, fields...)
	return ok && v > 0
}
