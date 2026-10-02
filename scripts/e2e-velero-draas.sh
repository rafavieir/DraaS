#!/usr/bin/env bash
set -euo pipefail

root="${DRAAS_ARTIFACT_ROOT:-artifacts/P3D.1/e2e-latest}"
state_dir="$root/state"
summary="$root/summary.json"
acceptance="$root/P3D.1-acceptance.json"
velero_ns="${DRAAS_VELERO_NAMESPACE:-velero}"
tenant="${DRAAS_TENANT:-lab-tenant}"
cluster="${DRAAS_CLUSTER:-k8s-lab}"
timeout="${DRAAS_OPERATION_TIMEOUT:-10m}"
mkdir -p "$root" "$state_dir" .local/tmp
run_id="${DRAAS_E2E_RUN_ID:-$(date -u +%Y%m%d%H%M%S)}"
app_ns="${DRAAS_E2E_NAMESPACE:-$app_ns-$run_id}"
pvc_ns="${DRAAS_E2E_PVC_NAMESPACE:-$pvc_ns-$run_id}"

json_get() {
  python3 -c 'import json,sys; data=json.load(open(sys.argv[1])); cur=data
for part in sys.argv[2].split("."):
    cur=cur[part]
print(cur)' "$1" "$2"
}

write_summary() {
  local result="$1" reason="${2:-}"
  python3 - <<PY
import json
summary = {
  "preflight": "$preflight_result",
  "backup": "$backup_result",
  "recovery_point": "$rp_result",
  "destructive_test": "$destroy_result",
  "restore": "$restore_result",
  "application_validation": "$app_result",
  "pvc_backup": "$pvc_backup_result",
  "pvc_restore": "$pvc_restore_result",
  "persistent_data_validation": "$pvc_validation_result",
  "result": "$result",
  "reason": "$reason",
  "lab_usable": "$result" == "PASS",
  "production_ready": False,
  "artifacts_root": "$root",
  "namespace": "$app_ns",
  "pvc_namespace": "$pvc_ns"
}
open("$summary", "w").write(json.dumps(summary, indent=2) + "\n")
acceptance = {
  "milestone": "P3D.1",
  "result": "$result",
  "lab_usable": "$result" == "PASS",
  "production_ready": False,
  "reason": "$reason",
  "artifacts_root": "$root",
  "namespace": "$app_ns",
  "pvc_namespace": "$pvc_ns",
  "evidence": summary
}
open("$acceptance", "w").write(json.dumps(acceptance, indent=2) + "\n")
PY
}

preflight_result=FAIL
backup_result=FAIL
rp_result=FAIL
destroy_result=FAIL
restore_result=FAIL
app_result=FAIL
pvc_backup_result=FAIL
pvc_restore_result=FAIL
pvc_validation_result=FAIL
trap 'rc=$?; if [[ $rc -ne 0 ]]; then write_summary FAIL "command failed at line $LINENO"; fi' EXIT

scripts/check-velero.sh "$root/preflight.json"
preflight_state=$(json_get "$root/preflight.json" result)
if [[ "$preflight_state" != "READY" ]]; then
  preflight_result=BLOCKED
  write_summary BLOCKED "Velero preflight is $preflight_state"
  exit 2
fi
preflight_result=PASS

cat > .local/tmp/e2e-workload.yaml <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: $app_ns
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: draas-e2e-config
  namespace: $app_ns
data:
  message: DRAAS-E2E-OK
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: draas-e2e-app
  namespace: $app_ns
spec:
  replicas: 1
  selector:
    matchLabels: {app: draas-e2e-app}
  template:
    metadata:
      labels: {app: draas-e2e-app}
    spec:
      containers:
        - name: app
          image: busybox:1.36
          imagePullPolicy: IfNotPresent
          command: ["sh", "-c"]
          args: ["mkdir -p /www && cp /config/message /www/index.html && httpd -f -p 8080 -h /www"]
          ports: [{containerPort: 8080, name: http}]
          volumeMounts: [{name: config, mountPath: /config}]
      volumes:
        - name: config
          configMap: {name: draas-e2e-config}
---
apiVersion: v1
kind: Service
metadata:
  name: draas-e2e-app
  namespace: $app_ns
spec:
  selector: {app: draas-e2e-app}
  ports: [{port: 80, targetPort: http, name: http}]
YAML

validate_http() {
  local ns="$1" pod="$2" url="$3" expected="$4"
  kubectl -n "$ns" delete pod "$pod" --ignore-not-found=true --wait=true >/dev/null
  kubectl -n "$ns" run "$pod" --restart=Never --image=busybox:1.36 --command -- sh -c "wget -qO- $url" >/dev/null
  for _ in $(seq 1 60); do
    phase=$(kubectl -n "$ns" get pod "$pod" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]] && break
    sleep 1
  done
  out=$(kubectl -n "$ns" logs "$pod")
  kubectl -n "$ns" delete pod "$pod" --ignore-not-found=true --wait=false >/dev/null
  test "$out" = "$expected"
}

kubectl delete namespace $app_ns --ignore-not-found=true --wait=true --timeout=180s >/dev/null
kubectl apply -f .local/tmp/e2e-workload.yaml >/dev/null
kubectl -n $app_ns rollout status deploy/draas-e2e-app --timeout=180s
validate_http $app_ns validate http://draas-e2e-app.$app_ns.svc.cluster.local DRAAS-E2E-OK

go run ./cmd/draas-k8s-dr k8s backup create --namespace $app_ns --tenant "$tenant" --asset $app_ns --cluster "$cluster" --timeout "$timeout" --state-dir "$state_dir" > "$root/backup.json"
backup_result=PASS
rp_id=$(json_get "$root/backup.json" recovery_point.id)
rp_result=PASS
kubectl delete namespace $app_ns --wait=true --timeout=180s >/dev/null
if kubectl get namespace $app_ns >/dev/null 2>&1; then exit 1; fi
destroy_result=PASS
go run ./cmd/draas-k8s-dr restore create --recovery-point "$rp_id" --tenant "$tenant" --timeout "$timeout" --state-dir "$state_dir" > "$root/restore.json"
restore_result=PASS
kubectl -n $app_ns rollout status deploy/draas-e2e-app --timeout=180s
validate_http $app_ns validate http://draas-e2e-app.$app_ns.svc.cluster.local DRAAS-E2E-OK
app_result=PASS

kubectl label volumesnapshotclass zfs-local-snapclass velero.io/csi-volumesnapshot-class=true --overwrite >/dev/null
cat > .local/tmp/e2e-pvc-workload.yaml <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: $pvc_ns
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: draas-e2e-data
  namespace: $pvc_ns
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: zfs-local
  resources:
    requests:
      storage: 1Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $pvc_ns-app
  namespace: $pvc_ns
spec:
  replicas: 1
  selector:
    matchLabels: {app: $pvc_ns-app}
  template:
    metadata:
      labels: {app: $pvc_ns-app}
    spec:
      containers:
        - name: app
          image: busybox:1.36
          imagePullPolicy: IfNotPresent
          command: ["sh", "-c"]
          args: ["test -f /data/draas-test.txt || echo DRAAS-PERSISTENT-DATA-OK > /data/draas-test.txt; httpd -f -p 8080 -h /data"]
          ports: [{containerPort: 8080, name: http}]
          volumeMounts: [{name: data, mountPath: /data}]
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: draas-e2e-data}
---
apiVersion: v1
kind: Service
metadata:
  name: $pvc_ns-app
  namespace: $pvc_ns
spec:
  selector: {app: $pvc_ns-app}
  ports: [{port: 80, targetPort: http, name: http}]
YAML
kubectl delete namespace $pvc_ns --ignore-not-found=true --wait=true --timeout=180s >/dev/null
kubectl apply -f .local/tmp/e2e-pvc-workload.yaml >/dev/null
kubectl -n $pvc_ns rollout status deploy/$pvc_ns-app --timeout=180s
validate_http $pvc_ns validate-pvc http://$pvc_ns-app.$pvc_ns.svc.cluster.local/draas-test.txt DRAAS-PERSISTENT-DATA-OK
go run ./cmd/draas-k8s-dr k8s backup create --namespace $pvc_ns --tenant "$tenant" --asset $pvc_ns --cluster "$cluster" --snapshot-volumes --timeout "$timeout" --state-dir "$state_dir" > "$root/pvc-backup.json"
pvc_backup_result=PASS
pvc_rp_id=$(json_get "$root/pvc-backup.json" recovery_point.id)
kubectl delete namespace $pvc_ns --wait=true --timeout=180s >/dev/null
go run ./cmd/draas-k8s-dr restore create --recovery-point "$pvc_rp_id" --tenant "$tenant" --timeout "$timeout" --state-dir "$state_dir" > "$root/pvc-restore.json"
pvc_restore_result=PASS
kubectl -n $pvc_ns rollout status deploy/$pvc_ns-app --timeout=180s
validate_http $pvc_ns validate-pvc http://$pvc_ns-app.$pvc_ns.svc.cluster.local/draas-test.txt DRAAS-PERSISTENT-DATA-OK
pvc_validation_result=PASS
write_summary PASS ""
trap - EXIT
cat "$summary"
