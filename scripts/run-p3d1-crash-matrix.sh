#!/usr/bin/env bash
set -uo pipefail

master_run="${P3D1_RUN_ID:-$(date -u +%Y%m%d-%H%M%S)}"
root="${DRAAS_ARTIFACT_ROOT:-artifacts/P3D.1/$master_run}"
env_dir="$root/environment"
summary_dir="$root/summary"
mkdir -p "$env_dir" "$summary_dir"

env_json="$env_dir/lab-capabilities.json"
set +e
scripts/check-p3d1-lab.sh "$env_json" > "$env_dir/check-p3d1-lab.stdout" 2> "$env_dir/check-p3d1-lab.stderr"
check_rc=$?
set -e

result="UNKNOWN"
if command -v jq >/dev/null 2>&1; then
  result="$(jq -r '.result // "UNKNOWN"' "$env_json" 2>/dev/null || printf UNKNOWN)"
elif command -v python3 >/dev/null 2>&1; then
  result="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("result","UNKNOWN"))' "$env_json" 2>/dev/null || printf UNKNOWN)"
fi

write_blocked() {
  local reason="$1"
  cat > "$summary_dir/P3D.1-acceptance.json" <<JSON
{
  "milestone": "P3D.1",
  "result": "BLOCKED",
  "production_ready": false,
  "p3d_status": "IN_PROGRESS",
  "reason": "$reason",
  "environment_result": "$result",
  "artifacts_root": "$root",
  "mandatory_execution_skipped": true,
  "scenarios": []
}
JSON
  cat > "$summary_dir/crash-matrix.json" <<JSON
{
  "milestone": "P3D.1",
  "result": "BLOCKED",
  "reason": "$reason",
  "scenarios": [
    {"name":"baseline","result":"SKIPPED"},
    {"name":"after_provider_call_before_persist","result":"SKIPPED"},
    {"name":"after_vm_create","result":"SKIPPED"},
    {"name":"during_disk_materialize","result":"SKIPPED"},
    {"name":"after_disk_materialize","result":"SKIPPED"},
    {"name":"after_disk_attach","result":"SKIPPED"},
    {"name":"after_power_on","result":"SKIPPED"},
    {"name":"before_app_validation","result":"SKIPPED"},
    {"name":"after_app_validation","result":"SKIPPED"},
    {"name":"duplicate_delivery","result":"SKIPPED"},
    {"name":"two_worker_race","result":"SKIPPED"},
    {"name":"provider_timeout","result":"SKIPPED"}
  ]
}
JSON
  cat > "$summary_dir/P3D.1-acceptance.md" <<MD
# P3D.1 Acceptance

Status: **BLOCKED**

P3D: **IN PROGRESS**  
production_ready: **false**

Reason: $reason

Environment result: **$result**

Artifacts root: $root

| Scenario | Kill occurred | Takeover | Adoption | VM count | Partial disks | App | Final | Result |
|---|---:|---:|---:|---:|---:|---:|---|---|
| baseline | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| after_provider_call_before_persist | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| after_vm_create | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| during_disk_materialize | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| after_disk_materialize | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| after_disk_attach | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| after_power_on | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| before_app_validation | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| after_app_validation | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| duplicate_delivery | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| two_worker_race | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |
| provider_timeout | no | no | no | n/a | n/a | n/a | skipped | SKIPPED |

No PASS was emitted because mandatory real KVM/libvirt execution did not run.
MD
}

protection_backend="${DRAAS_PROTECTION_BACKEND:-${DRAAS_STORAGE_BACKEND:-zfs}}"
if [[ "$protection_backend" != "kubernetes-zfs" && "$protection_backend" != "velero" && ( $check_rc -ne 0 || "$result" != "SUPPORTED" ) ]]; then
  write_blocked "Linux/KVM lab environment is not supported; mandatory crash matrix was not executed."
  cp "$env_json" "$summary_dir/environment.json" 2>/dev/null || true
  echo "P3D.1 BLOCKED: environment result=$result artifacts=$root" >&2
  exit 2
fi

if [[ "$protection_backend" == "kubernetes-zfs" ]]; then
  k8s_json="$summary_dir/kubernetes-zfs.json"
  set +e
  scripts/check-kubernetes-zfs.sh "$k8s_json" > "$summary_dir/kubernetes-zfs.stdout" 2> "$summary_dir/kubernetes-zfs.stderr"
  k8s_rc=$?
  set -e
  k8s_result="UNKNOWN"
  k8s_reason="preflight failed"
  if command -v jq >/dev/null 2>&1; then
    k8s_result="$(jq -r '.result // "UNKNOWN"' "$k8s_json" 2>/dev/null || printf UNKNOWN)"
    k8s_reason="$(jq -r '.reason // ""' "$k8s_json" 2>/dev/null || true)"
  elif command -v python3 >/dev/null 2>&1; then
    k8s_result="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("result","UNKNOWN"))' "$k8s_json" 2>/dev/null || printf UNKNOWN)"
    k8s_reason="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("reason",""))' "$k8s_json" 2>/dev/null || true)"
  fi
  if [[ $k8s_rc -ne 0 || "$k8s_result" != "PASS" ]]; then
    write_blocked "Kubernetes ZFS backend is not ready: $k8s_reason."
    cp "$env_json" "$summary_dir/environment.json" 2>/dev/null || true
    echo "P3D.1 BLOCKED: kubernetes zfs backend unavailable artifacts=$root" >&2
    exit 2
  fi
  reason="Kubernetes ZFS backend checks passed, but real baseline/crash-matrix executor is not implemented in this local harness yet."
  cat > "$summary_dir/P3D.1-acceptance.json" <<JSON
{
  "milestone": "P3D.1",
  "result": "BLOCKED",
  "production_ready": false,
  "p3d_status": "IN_PROGRESS",
  "reason": "$reason",
  "environment_result": "$result",
  "protection_backend": "kubernetes-zfs",
  "kubernetes_zfs_result": "$k8s_result",
  "artifacts_root": "$root",
  "mandatory_execution_skipped": true
}
JSON
  cat > "$summary_dir/P3D.1-acceptance.md" <<MD
# P3D.1 Acceptance

Status: **BLOCKED**

P3D: **IN PROGRESS**  
production_ready: **false**

Protection backend: **kubernetes-zfs**  
Kubernetes ZFS check: **$k8s_result**

Reason: $reason

The harness did not emit PASS because the real baseline and worker crash matrix orchestration still must execute.
MD
  cp "$env_json" "$summary_dir/environment.json" 2>/dev/null || true
  echo "P3D.1 BLOCKED: $reason artifacts=$root" >&2
  exit 2
fi
if [[ "$protection_backend" == "velero" ]]; then
  velero_json="$summary_dir/velero.json"
  set +e
  scripts/check-velero.sh "$velero_json" > "$summary_dir/velero.stdout" 2> "$summary_dir/velero.stderr"
  velero_rc=$?
  set -e
  velero_result="UNKNOWN"
  velero_reason="preflight failed"
  if command -v jq >/dev/null 2>&1; then
    velero_result="$(jq -r '.result // "UNKNOWN"' "$velero_json" 2>/dev/null || printf UNKNOWN)"
    velero_reason="$(jq -r '.reason // ""' "$velero_json" 2>/dev/null || true)"
  elif command -v python3 >/dev/null 2>&1; then
    velero_result="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("result","UNKNOWN"))' "$velero_json" 2>/dev/null || printf UNKNOWN)"
    velero_reason="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("reason",""))' "$velero_json" 2>/dev/null || true)"
  fi
  if [[ $velero_rc -ne 0 || "$velero_result" != "READY" ]]; then
    write_blocked "Velero protection backend is not READY: $velero_reason."
    cp "$env_json" "$summary_dir/environment.json" 2>/dev/null || true
    echo "P3D.1 BLOCKED: velero backend unavailable artifacts=$root" >&2
    exit 2
  fi
  reason="Velero protection backend is READY, but real Velero backup/restore crash-matrix executor still must run before PASS."
  cat > "$summary_dir/P3D.1-acceptance.json" <<JSON
{
  "milestone": "P3D.1",
  "result": "BLOCKED",
  "production_ready": false,
  "p3d_status": "IN_PROGRESS",
  "reason": "$reason",
  "environment_result": "$result",
  "protection_backend": "velero",
  "velero_result": "$velero_result",
  "artifacts_root": "$root",
  "mandatory_execution_skipped": true
}
JSON
  cat > "$summary_dir/P3D.1-acceptance.md" <<MD
# P3D.1 Acceptance

Status: **BLOCKED**

P3D: **IN PROGRESS**  
production_ready: **false**

Protection backend: **velero**  
Velero check: **$velero_result**

Reason: $reason
MD
  cp "$env_json" "$summary_dir/environment.json" 2>/dev/null || true
  echo "P3D.1 BLOCKED: $reason artifacts=$root" >&2
  exit 2
fi
if [[ "$protection_backend" == "zfs" ]]; then
  zfs_available="false"
  zfs_pools=""
  if command -v jq >/dev/null 2>&1; then
    zfs_available="$(jq -r '.zfs.available // false' "$env_json" 2>/dev/null || printf false)"
    zfs_pools="$(jq -r '.zfs.pools // ""' "$env_json" 2>/dev/null || true)"
  fi
  if [[ "$zfs_available" != "true" || "$zfs_pools" == "" || "$zfs_pools" == "NOT_FOUND" ]]; then
    write_blocked "ZFS block storage backend requested, but zfs/zpool or an imported pool is not available. S3 fallback is disabled for P3D.1."
    cp "$env_json" "$summary_dir/environment.json" 2>/dev/null || true
    echo "P3D.1 BLOCKED: zfs backend unavailable artifacts=$root" >&2
    exit 2
  fi
  if [[ -z "${DRAAS_ZFS_POOL:-}" ]]; then
    export DRAAS_ZFS_POOL="$(printf '%s
' "$zfs_pools" | head -n1)"
  fi
  zfs_smoke_dir="$root/zfs-smoke"
  set +e
  scripts/smoke-zfs-block.sh "$zfs_smoke_dir" > "$zfs_smoke_dir.stdout" 2> "$zfs_smoke_dir.stderr"
  zfs_smoke_rc=$?
  set -e
  zfs_smoke_result="UNKNOWN"
  if [[ -f "$zfs_smoke_dir/zfs-smoke.json" ]] && command -v jq >/dev/null 2>&1; then
    zfs_smoke_result="$(jq -r '.result // "UNKNOWN"' "$zfs_smoke_dir/zfs-smoke.json")"
  fi
  if [[ $zfs_smoke_rc -ne 0 || "$zfs_smoke_result" != "PASS" ]]; then
    write_blocked "ZFS block storage smoke test did not pass."
    cp "$env_json" "$summary_dir/environment.json" 2>/dev/null || true
    cp "$zfs_smoke_dir/zfs-smoke.json" "$summary_dir/zfs-smoke.json" 2>/dev/null || true
    echo "P3D.1 BLOCKED: zfs smoke failed artifacts=$root" >&2
    exit 2
  fi
fi

smoke_dir="$root/libvirt-smoke"
set +e
scripts/smoke-libvirt-kvm.sh "$smoke_dir" > "$smoke_dir.stdout" 2> "$smoke_dir.stderr"
smoke_rc=$?
set -e
smoke_result="UNKNOWN"
if [[ -f "$smoke_dir/libvirt-smoke.json" ]] && command -v jq >/dev/null 2>&1; then
  smoke_result="$(jq -r '.result // "UNKNOWN"' "$smoke_dir/libvirt-smoke.json")"
fi
if [[ $smoke_rc -ne 0 || "$smoke_result" != "PASS" ]]; then
  reason="Environment is supported, but libvirt smoke test failed."
else
  reason="Environment and libvirt smoke are supported, but real baseline/crash-matrix executor is not implemented in this local harness yet."
fi
cat > "$summary_dir/P3D.1-acceptance.json" <<JSON
{
  "milestone": "P3D.1",
  "result": "BLOCKED",
  "production_ready": false,
  "p3d_status": "IN_PROGRESS",
  "reason": "$reason",
  "environment_result": "$result",
  "libvirt_smoke_result": "$smoke_result",
  "libvirt_smoke_exit_code": $smoke_rc,
  "artifacts_root": "$root",
  "mandatory_execution_skipped": true
}
JSON
cat > "$summary_dir/P3D.1-acceptance.md" <<MD
# P3D.1 Acceptance

Status: **BLOCKED**

P3D: **IN PROGRESS**  
production_ready: **false**

Environment result: **$result**  
Libvirt smoke: **$smoke_result**

Reason: $reason

The harness did not emit PASS because the real baseline and worker crash matrix orchestration still must execute.
MD
cp "$env_json" "$summary_dir/environment.json" 2>/dev/null || true
cp "$smoke_dir/libvirt-smoke.json" "$summary_dir/libvirt-smoke.json" 2>/dev/null || true
echo "P3D.1 BLOCKED: $reason artifacts=$root" >&2
exit 2
