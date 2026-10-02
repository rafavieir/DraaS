package blockstorage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const KubernetesZFSBackend = "kubernetes-zfs"

var (
	pvcGVR            = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}
	podGVR            = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	storageClassGVR   = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
	volumeSnapshotGVR = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}
)

type KubernetesZFSManager struct {
	Namespace    string
	StorageClass string
	Client       dynamic.Interface
}

type KubernetesCloneSpec struct {
	Tenant           string
	RecoveryJobID    string
	OperationID      string
	RecoveryPoint    RecoveryPoint
	TargetPVC        string
	VolumeSnapshot   string
	VolumeSnapshotNS string
	SizeBytes        int64
}

type KubernetesCloneResult struct {
	Namespace      string            `json:"namespace"`
	PVC            string            `json:"pvc"`
	StorageClass   string            `json:"storage_class"`
	VolumeSnapshot string            `json:"volume_snapshot"`
	Phase          string            `json:"phase"`
	Props          map[string]string `json:"props,omitempty"`
}

func NewKubernetesZFSManager(namespace, storageClass string) (KubernetesZFSManager, error) {
	cfg, err := kubernetesConfig()
	if err != nil {
		return KubernetesZFSManager{}, err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return KubernetesZFSManager{}, err
	}
	if namespace == "" {
		namespace = "draas-recovery"
	}
	if storageClass == "" {
		return KubernetesZFSManager{}, errors.New("DRAAS_K8S_ZFS_STORAGE_CLASS is required for kubernetes-zfs")
	}
	return KubernetesZFSManager{Namespace: namespace, StorageClass: storageClass, Client: client}, nil
}

func kubernetesConfig() (*rest.Config, error) {
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

func (m KubernetesZFSManager) Check(ctx context.Context) Capabilities {
	cap := Capabilities{Pool: m.StorageClass, Details: map[string]string{"provider": KubernetesZFSBackend, "namespace": m.Namespace, "storage_class": m.StorageClass}}
	if m.Client == nil {
		mgr, err := NewKubernetesZFSManager(m.Namespace, m.StorageClass)
		if err != nil {
			cap.Error = err.Error()
			return cap
		}
		m.Client = mgr.Client
	}
	if _, err := m.Client.Resource(storageClassGVR).Get(ctx, m.StorageClass, metav1.GetOptions{}); err != nil {
		cap.Error = fmt.Sprintf("zfs StorageClass %q unavailable: %v", m.StorageClass, err)
		return cap
	}
	cap.Available = true
	cap.Version = KubernetesZFSBackend
	cap.Pools = []string{m.StorageClass}
	return cap
}

func (m KubernetesZFSManager) SnapshotExists(ctx context.Context, name string) error {
	if m.Client == nil {
		return errors.New("kubernetes dynamic client is required")
	}
	if name == "" {
		return errors.New("VolumeSnapshot name is required")
	}
	_, err := m.Client.Resource(volumeSnapshotGVR).Namespace(m.Namespace).Get(ctx, name, metav1.GetOptions{})
	return err
}

func (m KubernetesZFSManager) CloneFromSnapshot(ctx context.Context, spec KubernetesCloneSpec) (KubernetesCloneResult, error) {
	if m.Client == nil {
		return KubernetesCloneResult{}, errors.New("kubernetes dynamic client is required")
	}
	if m.Namespace == "" || m.StorageClass == "" {
		return KubernetesCloneResult{}, errors.New("namespace and storage class are required")
	}
	if spec.TargetPVC == "" {
		spec.TargetPVC = TargetPVCName(spec.Tenant, spec.RecoveryJobID)
	}
	if spec.VolumeSnapshot == "" {
		spec.VolumeSnapshot = spec.RecoveryPoint.Metadata["kubernetes_volume_snapshot"]
	}
	if spec.VolumeSnapshotNS == "" {
		spec.VolumeSnapshotNS = spec.RecoveryPoint.Metadata["kubernetes_namespace"]
	}
	if spec.VolumeSnapshotNS == "" {
		spec.VolumeSnapshotNS = m.Namespace
	}
	if spec.VolumeSnapshotNS != m.Namespace {
		return KubernetesCloneResult{}, errors.New("VolumeSnapshot dataSource must be in the same namespace as the target PVC")
	}
	if spec.VolumeSnapshot == "" {
		return KubernetesCloneResult{}, errors.New("kubernetes_volume_snapshot is required in the recovery point metadata")
	}
	if spec.SizeBytes < 1 {
		spec.SizeBytes = spec.RecoveryPoint.SizeBytes
	}
	if spec.SizeBytes < 1 {
		return KubernetesCloneResult{}, errors.New("positive recovery point size is required")
	}
	if err := m.SnapshotExists(ctx, spec.VolumeSnapshot); err != nil {
		return KubernetesCloneResult{}, fmt.Errorf("VolumeSnapshot %s/%s unavailable: %w", m.Namespace, spec.VolumeSnapshot, err)
	}
	pvc := pvcFromSnapshot(m.Namespace, spec.TargetPVC, m.StorageClass, spec.VolumeSnapshot, spec.SizeBytes, spec.Tenant, spec.RecoveryJobID, spec.OperationID)
	if _, err := m.Client.Resource(pvcGVR).Namespace(m.Namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		if _, getErr := m.Client.Resource(pvcGVR).Namespace(m.Namespace).Get(ctx, spec.TargetPVC, metav1.GetOptions{}); getErr != nil {
			return KubernetesCloneResult{}, err
		}
	}
	phase, err := m.waitPVCBound(ctx, spec.TargetPVC, 2*time.Minute)
	if err != nil {
		return KubernetesCloneResult{}, err
	}
	return KubernetesCloneResult{Namespace: m.Namespace, PVC: spec.TargetPVC, StorageClass: m.StorageClass, VolumeSnapshot: spec.VolumeSnapshot, Phase: phase, Props: map[string]string{"backend": KubernetesZFSBackend}}, nil
}

func (m KubernetesZFSManager) waitPVCBound(ctx context.Context, name string, timeout time.Duration) (string, error) {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		pvc, err := m.Client.Resource(pvcGVR).Namespace(m.Namespace).Get(deadline, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		phase, _, _ := unstructured.NestedString(pvc.Object, "status", "phase")
		if phase == "Bound" {
			return phase, nil
		}
		select {
		case <-deadline.Done():
			if phase == "" {
				phase = "Pending"
			}
			return phase, fmt.Errorf("PVC %s/%s did not become Bound before timeout; current phase=%s", m.Namespace, name, phase)
		case <-tick.C:
		}
	}
}

func (m KubernetesZFSManager) CreateValidationPod(ctx context.Context, tenant, recoveryJobID, operationID, pvc string) (string, string, error) {
	if m.Client == nil {
		return "", "", errors.New("kubernetes dynamic client is required")
	}
	name := TargetPVCName(tenant, recoveryJobID) + "-validator"
	if len(name) > 63 {
		name = name[:63]
	}
	pod := validationPod(m.Namespace, name, pvc, tenant, recoveryJobID, operationID)
	if _, err := m.Client.Resource(podGVR).Namespace(m.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		if _, getErr := m.Client.Resource(podGVR).Namespace(m.Namespace).Get(ctx, name, metav1.GetOptions{}); getErr != nil {
			return "", "", err
		}
	}
	phase, err := m.waitPodRunning(ctx, name, 2*time.Minute)
	return name, phase, err
}

func (m KubernetesZFSManager) waitPodRunning(ctx context.Context, name string, timeout time.Duration) (string, error) {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		pod, err := m.Client.Resource(podGVR).Namespace(m.Namespace).Get(deadline, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		phase, _, _ := unstructured.NestedString(pod.Object, "status", "phase")
		if phase == "Running" || phase == "Succeeded" {
			return phase, nil
		}
		select {
		case <-deadline.Done():
			if phase == "" {
				phase = "Pending"
			}
			return phase, fmt.Errorf("validation pod %s/%s did not become Running before timeout; current phase=%s", m.Namespace, name, phase)
		case <-tick.C:
		}
	}
}

func pvcFromSnapshot(namespace, name, storageClass, snapshot string, sizeBytes int64, tenant, recoveryJobID, operationID string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "draas",
				"dr.draas.local/tenant":        tenant,
				"dr.draas.local/recovery-job":  recoveryJobID,
			},
			"annotations": map[string]any{
				"dr.draas.local/operation-id": operationID,
			},
		},
		"spec": map[string]any{
			"accessModes":      []any{"ReadWriteOnce"},
			"storageClassName": storageClass,
			"resources":        map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%d", sizeBytes)}},
			"dataSource": map[string]any{
				"apiGroup": "snapshot.storage.k8s.io",
				"kind":     "VolumeSnapshot",
				"name":     snapshot,
			},
		},
	}}
}

func validationPod(namespace, name, pvc, tenant, recoveryJobID, operationID string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "draas",
				"dr.draas.local/tenant":        tenant,
				"dr.draas.local/recovery-job":  recoveryJobID,
				"dr.draas.local/role":          "recovery-validator",
			},
			"annotations": map[string]any{
				"dr.draas.local/operation-id": operationID,
			},
		},
		"spec": map[string]any{
			"restartPolicy": "Never",
			"containers": []any{map[string]any{
				"name":            "validator",
				"image":           envDefault("DRAAS_K8S_VALIDATOR_IMAGE", "busybox:1.36"),
				"imagePullPolicy": "IfNotPresent",
				"command":         []any{"sh", "-c", "test -e /recovery && sleep 3600"},
				"volumeMounts":    []any{map[string]any{"name": "recovery", "mountPath": "/recovery"}},
				"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true},
			}},
			"volumes": []any{map[string]any{
				"name": "recovery",
				"persistentVolumeClaim": map[string]any{
					"claimName": pvc,
					"readOnly":  true,
				},
			}},
		},
	}}
}

func TargetPVCName(tenant, recoveryJobID string) string {
	name := "draas-" + tenant + "-" + recoveryJobID
	if len(name) <= 63 {
		return name
	}
	return name[:63]
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
