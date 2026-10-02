#!/usr/bin/env bash
set -uo pipefail

out="${1:-/dev/stdout}"
namespace="${DRAAS_K8S_RECOVERY_NAMESPACE:-draas-recovery}"
storage_class="${DRAAS_K8S_ZFS_STORAGE_CLASS:-}"
result="PASS"
reason=""

if ! command -v kubectl >/dev/null 2>&1; then
  result="BLOCKED"
  reason="kubectl not found"
elif [[ -z "$storage_class" ]]; then
  result="BLOCKED"
  reason="DRAAS_K8S_ZFS_STORAGE_CLASS is not configured"
elif ! kubectl version --client >/dev/null 2>&1; then
  result="BLOCKED"
  reason="kubectl client is not functional"
elif ! kubectl get namespace "$namespace" >/dev/null 2>&1; then
  result="BLOCKED"
  reason="namespace $namespace not found"
elif ! kubectl get storageclass "$storage_class" >/dev/null 2>&1; then
  result="BLOCKED"
  reason="StorageClass $storage_class not found"
elif ! kubectl api-resources --api-group=snapshot.storage.k8s.io 2>/dev/null | grep -q '^volumesnapshots'; then
  result="BLOCKED"
  reason="VolumeSnapshot API snapshot.storage.k8s.io is not installed"
fi

worker_can_create_pvc="UNKNOWN"
worker_can_create_pod="UNKNOWN"
worker_can_get_snapshot="UNKNOWN"
if command -v kubectl >/dev/null 2>&1 && kubectl get namespace "$namespace" >/dev/null 2>&1; then
  worker_subject="system:serviceaccount:${DRAAS_NAMESPACE:-draas-system}:draas-worker"
  if kubectl auth can-i create persistentvolumeclaims --namespace "$namespace" --as "$worker_subject" >/dev/null 2>&1; then worker_can_create_pvc="true"; else worker_can_create_pvc="false"; fi
  if kubectl auth can-i create pods --namespace "$namespace" --as "$worker_subject" >/dev/null 2>&1; then worker_can_create_pod="true"; else worker_can_create_pod="false"; fi
  if kubectl auth can-i get volumesnapshots.snapshot.storage.k8s.io --namespace "$namespace" --as "$worker_subject" >/dev/null 2>&1; then worker_can_get_snapshot="true"; else worker_can_get_snapshot="false"; fi
fi

cat > "$out" <<JSON
{
  "backend": "kubernetes-zfs",
  "result": "$result",
  "namespace": "$namespace",
  "storage_class": "$storage_class",
  "reason": "$reason",
  "rbac": {
    "worker_can_create_pvc": "$worker_can_create_pvc",
    "worker_can_create_pod": "$worker_can_create_pod",
    "worker_can_get_snapshot": "$worker_can_get_snapshot"
  }
}
JSON

if [[ "$result" == "PASS" ]]; then
  exit 0
fi
exit 2
