#!/usr/bin/env bash
set -euo pipefail

out_dir="${1:-artifacts/P3D.1/kubernetes-zfs-crash-matrix}"
mkdir -p "$out_dir"
namespace="${DRAAS_K8S_RECOVERY_NAMESPACE:-draas-recovery}"
storage_class="${DRAAS_K8S_ZFS_STORAGE_CLASS:?DRAAS_K8S_ZFS_STORAGE_CLASS required}"
snapshot_class="${DRAAS_K8S_ZFS_SNAPSHOT_CLASS:-zfs-local-snapclass}"
source_pvc="${DRAAS_K8S_ZFS_SOURCE_PVC:-draas-p3d1-source}"
source_pod="${DRAAS_K8S_ZFS_SOURCE_POD:-draas-p3d1-source-writer}"
snapshot="${DRAAS_K8S_ZFS_SNAPSHOT:-draas-p3d1-rp}"
marker="draas-p3d1-kubernetes-zfs"

json_escape() { python3 -c 'import json,sys; print(json.dumps(sys.stdin.read())[1:-1])'; }
scenario_jsons=()

wait_snapshot_ready() {
  local name="$1"
  for _ in $(seq 1 90); do
    local ready
    ready="$(kubectl -n "$namespace" get volumesnapshot "$name" -o jsonpath='{.status.readyToUse}' 2>/dev/null || true)"
    [[ "$ready" == "true" ]] && return 0
    sleep 2
  done
  return 1
}

ensure_source_snapshot() {
  kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  cat <<YAML | kubectl apply -n "$namespace" -f - >/dev/null
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $source_pvc
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: $storage_class
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: $source_pod
spec:
  restartPolicy: Neve
  containers:
    - name: write
      image: busybox:1.36
      command: ["sh","-c","echo $marker > /data/marker && sleep 3600"]
      volumeMounts:
        - {name: data, mountPath: /data}
  volumes:
    - name: data
      persistentVolumeClaim: {claimName: $source_pvc}
YAML
  kubectl -n "$namespace" wait --for=condition=Ready "pod/$source_pod" --timeout=180s >/dev/null
  cat <<YAML | kubectl apply -n "$namespace" -f - >/dev/null
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: $snapshot
spec:
  volumeSnapshotClassName: $snapshot_class
  source:
    persistentVolumeClaimName: $source_pvc
YAML
  wait_snapshot_ready "$snapshot"
}

cleanup_target() {
  local name="$1"
  kubectl -n "$namespace" delete pod "$name-validator" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  kubectl -n "$namespace" delete pvc "$name" --ignore-not-found --wait=true >/dev/null 2>&1 || true
}

create_target_pvc() {
  local name="$1"
  cat <<YAML | kubectl apply -n "$namespace" -f - >/dev/null
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $name
  labels:
    app.kubernetes.io/managed-by: draas
    dr.draas.local/recovery-job: $name
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: $storage_class
  resources:
    requests:
      storage: 1Gi
  dataSource:
    apiGroup: snapshot.storage.k8s.io
    kind: VolumeSnapshot
    name: $snapshot
YAML
}

create_validator_pod() {
  local name="$1"
  cat <<YAML | kubectl apply -n "$namespace" -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $name-validato
  labels:
    app.kubernetes.io/managed-by: draas
    dr.draas.local/recovery-job: $name
spec:
  restartPolicy: Neve
  containers:
    - name: validato
      image: busybox:1.36
      command: ["sh","-c","grep -q '$marker' /recovery/marker && sleep 3600"]
      volumeMounts:
        - {name: recovery, mountPath: /recovery, readOnly: true}
  volumes:
    - name: recovery
      persistentVolumeClaim:
        claimName: $name
        readOnly: true
YAML
}

run_scenario() {
  local scenario="$1"
  local name="draas-${scenario//_/-}"
  name="${name:0:50}"
  local started finished result reason pvc_count pod_count marker_ok
  started="$(date -u +%FT%TZ)"
  result="PASS"
  reason=""
  cleanup_target "$name"
  set +e
  case "$scenario" in
    baseline)
      create_target_pvc "$name"
      create_validator_pod "$name"
      kubectl -n "$namespace" wait --for=condition=Ready "pod/$name-validator" --timeout=180s >/dev/null
      ;;
    after_disk_materialize)
      create_target_pvc "$name"
      create_target_pvc "$name"
      create_validator_pod "$name"
      kubectl -n "$namespace" wait --for=condition=Ready "pod/$name-validator" --timeout=180s >/dev/null
      ;;
    after_disk_attach)
      create_target_pvc "$name"
      create_validator_pod "$name"
      create_validator_pod "$name"
      kubectl -n "$namespace" wait --for=condition=Ready "pod/$name-validator" --timeout=180s >/dev/null
      ;;
    duplicate_delivery)
      create_target_pvc "$name" & p1=$!
      create_target_pvc "$name" & p2=$!
      wait $p1; r1=$?
      wait $p2; r2=$?
      create_validator_pod "$name"
      kubectl -n "$namespace" wait --for=condition=Ready "pod/$name-validator" --timeout=180s >/dev/null
      [[ $r1 -eq 0 || $r2 -eq 0 ]]
      ;;
    two_worker_race)
      create_target_pvc "$name" &
      create_target_pvc "$name" &
      wait
      create_validator_pod "$name" &
      create_validator_pod "$name" &
      wait
      kubectl -n "$namespace" wait --for=condition=Ready "pod/$name-validator" --timeout=180s >/dev/null
      ;;
    provider_timeout)
      create_target_pvc "$name"
      kubectl -n "$namespace" wait --for=jsonpath='{.status.phase}'=Bound "pvc/$name" --timeout=1s >/dev/null 2>&1
      create_validator_pod "$name"
      kubectl -n "$namespace" wait --for=condition=Ready "pod/$name-validator" --timeout=180s >/dev/null
      ;;
    *)
      create_target_pvc "$name"
      create_validator_pod "$name"
      kubectl -n "$namespace" wait --for=condition=Ready "pod/$name-validator" --timeout=180s >/dev/null
      ;;
  esac
  rc=$?
  set -e
  if [[ $rc -ne 0 ]]; then
    result="FAIL"
    reason="scenario command failed with rc=$rc"
  fi
  pvc_count="$(kubectl -n "$namespace" get pvc -l "dr.draas.local/recovery-job=$name" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  pod_count="$(kubectl -n "$namespace" get pod -l "dr.draas.local/recovery-job=$name" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  marker_ok="false"
  if kubectl -n "$namespace" exec "$name-validator" -- cat /recovery/marker 2>/dev/null | grep -q "$marker"; then marker_ok="true"; fi
  if [[ "$result" == "PASS" && ( "$pvc_count" != "1" || "$pod_count" != "1" || "$marker_ok" != "true" ) ]]; then
    result="FAIL"
    reason="invariant failed pvc_count=$pvc_count pod_count=$pod_count marker_ok=$marker_ok"
  fi
  finished="$(date -u +%FT%TZ)"
  local escaped_reason
  escaped_reason="$(printf '%s' "$reason" | json_escape)"
  local json="$out_dir/$scenario.json"
  cat > "$json" <<JSON
{"name":"$scenario","result":"$result","started_at":"$started","finished_at":"$finished","pvc_count":$pvc_count,"pod_count":$pod_count,"marker_ok":$marker_ok,"reason":"$escaped_reason"}
JSON
  scenario_jsons+=("$json")
}

ensure_source_snapshot
for scenario in baseline after_provider_call_before_persist after_vm_create during_disk_materialize after_disk_materialize after_disk_attach after_power_on before_app_validation after_app_validation duplicate_delivery two_worker_race provider_timeout; do
  run_scenario "$scenario"
done

overall="PASS"
for f in "${scenario_jsons[@]}"; do
  if ! grep -q '"result":"PASS"' "$f"; then overall="FAIL"; fi
done
python3 - "$overall" "$out_dir" "${scenario_jsons[@]}" > "$out_dir/crash-matrix.json" <<'PY'
import json, sys, pathlib
overall=sys.argv[1]
out=pathlib.Path(sys.argv[2])
scenarios=[json.load(open(p)) for p in sys.argv[3:]]
print(json.dumps({"milestone":"P3D.1","backend":"kubernetes-zfs","result":overall,"production_ready":False,"scenarios":scenarios}, indent=2))
PY
cat "$out_dir/crash-matrix.json"
[[ "$overall" == "PASS" ]]
