#!/usr/bin/env bash
set -uo pipefail

out="${1:-/dev/stdout}"
namespace="${DRAAS_VELERO_NAMESPACE:-velero}"
draas_namespace="${DRAAS_NAMESPACE:-draas-system}"
worker_subject="system:serviceaccount:${draas_namespace}:draas-worker"

json_escape() {
  local raw="${1:-}"
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$raw"
  else
    printf '"%s"' "${raw//"/\\"}"
  fi
}

gate_status() { printf '%s' "$1"; }

result="READY"
reason="Velero protection backend is ready"

kubectl_available="PASS"
namespace_gate="PASS"
crds="PASS"
controller="PASS"
bsl="PASS"
csi="PASS"
node_agent="PASS"
credentials="PASS"
object_storage="PASS"
rbac_backup="UNKNOWN"
rbac_restore="UNKNOWN"
rbac_bsl="UNKNOWN"
controller_ready="0/0"
bsl_phase=""
bsl_message=""
snapshot_classes=""
node_agent_ready="0/0"

if ! command -v kubectl >/dev/null 2>&1; then
  kubectl_available="FAIL"
  result="NOT_READY"
  reason="kubectl not found"
else
  if ! kubectl get namespace "$namespace" >/dev/null 2>&1; then
    namespace_gate="FAIL"
    result="NOT_READY"
    reason="namespace $namespace not found"
  fi

  if ! kubectl api-resources --api-group=velero.io 2>/dev/null | grep -q '^backups'; then
    crds="FAIL"
    result="NOT_READY"
    reason="Velero CRDs velero.io/v1 are not installed"
  fi

  if [[ "$namespace_gate" == "PASS" ]]; then
    if ! kubectl -n "$namespace" get deploy velero >/dev/null 2>&1; then
      controller="FAIL"
      result="NOT_READY"
      reason="Velero controller deployment is not installed"
    else
      desired="$(kubectl -n "$namespace" get deploy velero -o jsonpath='{.status.replicas}' 2>/dev/null || printf 0)"
      ready="$(kubectl -n "$namespace" get deploy velero -o jsonpath='{.status.readyReplicas}' 2>/dev/null || printf 0)"
      desired="${desired:-0}"
      ready="${ready:-0}"
      controller_ready="${ready}/${desired}"
      if [[ "$desired" == "0" || "$ready" != "$desired" ]]; then
        controller="FAIL"
        result="NOT_READY"
        reason="Velero controller is not ready"
      fi
    fi
  fi

  if [[ "$crds" == "PASS" && "$namespace_gate" == "PASS" ]]; then
    if ! kubectl -n "$namespace" get backupstoragelocations.velero.io default >/dev/null 2>&1; then
      bsl="WARN"
      credentials="WARN"
      object_storage="WARN"
      if [[ "$result" == "READY" ]]; then result="DEGRADED"; reason="Velero core is installed, but default BackupStorageLocation is missing"; fi
    else
      bsl_phase="$(kubectl -n "$namespace" get backupstoragelocations.velero.io default -o jsonpath='{.status.phase}' 2>/dev/null || true)"
      bsl_message="$(kubectl -n "$namespace" get backupstoragelocations.velero.io default -o jsonpath='{.status.message}' 2>/dev/null || true)"
      if [[ "$bsl_phase" != "Available" ]]; then
        bsl="WARN"
        object_storage="WARN"
        if [[ "$result" == "READY" ]]; then result="DEGRADED"; reason="Velero BackupStorageLocation default is not Available: ${bsl_phase:-Unknown}"; fi
      fi
      cred_name="$(kubectl -n "$namespace" get backupstoragelocations.velero.io default -o jsonpath='{.spec.credential.name}' 2>/dev/null || true)"
      cred_key="$(kubectl -n "$namespace" get backupstoragelocations.velero.io default -o jsonpath='{.spec.credential.key}' 2>/dev/null || true)"
      if [[ -n "$cred_name" ]]; then
        if ! kubectl -n "$namespace" get secret "$cred_name" >/dev/null 2>&1; then
          credentials="WARN"
          if [[ "$result" == "READY" ]]; then result="DEGRADED"; reason="Velero credential secret $cred_name is missing"; fi
        elif [[ -n "$cred_key" ]] && ! kubectl -n "$namespace" get secret "$cred_name" -o jsonpath="{.data.${cred_key}}" >/dev/null 2>&1; then
          credentials="WARN"
          if [[ "$result" == "READY" ]]; then result="DEGRADED"; reason="Velero credential secret $cred_name does not contain key $cred_key"; fi
        fi
      else
        credentials="WARN"
        if [[ "$result" == "READY" ]]; then result="DEGRADED"; reason="Velero BackupStorageLocation has no credential reference"; fi
      fi
    fi
  fi

  if kubectl api-resources --api-group=snapshot.storage.k8s.io 2>/dev/null | grep -q '^volumesnapshotclasses'; then
    snapshot_classes="$(kubectl get volumesnapshotclasses.snapshot.storage.k8s.io -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}' 2>/dev/null || true)"
    if [[ -z "${snapshot_classes// /}" ]]; then
      csi="WARN"
      if [[ "$result" == "READY" ]]; then result="DEGRADED"; reason="CSI snapshot API is installed, but no VolumeSnapshotClass exists"; fi
    fi
  else
    csi="WARN"
    if [[ "$result" == "READY" ]]; then result="DEGRADED"; reason="CSI snapshot API is not installed"; fi
  fi

  if [[ "$namespace_gate" == "PASS" ]]; then
    if kubectl -n "$namespace" get daemonset node-agent >/dev/null 2>&1; then
      desired="$(kubectl -n "$namespace" get daemonset node-agent -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || printf 0)"
      ready="$(kubectl -n "$namespace" get daemonset node-agent -o jsonpath='{.status.numberReady}' 2>/dev/null || printf 0)"
      desired="${desired:-0}"
      ready="${ready:-0}"
      node_agent_ready="${ready}/${desired}"
      if [[ "$desired" == "0" || "$ready" != "$desired" ]]; then
        node_agent="WARN"
        if [[ "$result" == "READY" ]]; then result="DEGRADED"; reason="Velero node-agent/DataMover is installed but not fully ready"; fi
      fi
    else
      node_agent="WARN"
      if [[ "$result" == "READY" ]]; then result="DEGRADED"; reason="Velero node-agent/DataMover is not installed"; fi
    fi
  fi

  if kubectl auth can-i get backups.velero.io --namespace "$namespace" --as "$worker_subject" >/dev/null 2>&1; then rbac_backup="true"; else rbac_backup="false"; fi
  if kubectl auth can-i create restores.velero.io --namespace "$namespace" --as "$worker_subject" >/dev/null 2>&1; then rbac_restore="true"; else rbac_restore="false"; fi
  if kubectl auth can-i get backupstoragelocations.velero.io --namespace "$namespace" --as "$worker_subject" >/dev/null 2>&1; then rbac_bsl="true"; else rbac_bsl="false"; fi
fi

emit_json() {
  if [[ "$out" == "/dev/stdout" || "$out" == "-" ]]; then
    cat
  else
    cat > "$out"
  fi
}

emit_json <<JSON
{
  "backend": "velero",
  "protection_backend": "velero",
  "result": "$result",
  "namespace": "$namespace",
  "reason": $(json_escape "$reason"),
  "gates": {
    "kubectl": "$(gate_status "$kubectl_available")",
    "namespace": "$(gate_status "$namespace_gate")",
    "crds": "$(gate_status "$crds")",
    "controller": {"status": "$(gate_status "$controller")", "ready": "$controller_ready"},
    "backup_storage_location": {"status": "$(gate_status "$bsl")", "phase": $(json_escape "$bsl_phase"), "message": $(json_escape "$bsl_message")},
    "csi": {"status": "$(gate_status "$csi")", "volume_snapshot_classes": $(json_escape "$snapshot_classes")},
    "node_agent_data_mover": {"status": "$(gate_status "$node_agent")", "ready": "$node_agent_ready"},
    "credentials": "$(gate_status "$credentials")",
    "object_storage": "$(gate_status "$object_storage")"
  },
  "rbac": {
    "worker_subject": "$worker_subject",
    "worker_can_get_backup": "$rbac_backup",
    "worker_can_create_restore": "$rbac_restore",
    "worker_can_get_backup_storage_location": "$rbac_bsl"
  }
}
JSON
[[ "$result" == "READY" ]]
