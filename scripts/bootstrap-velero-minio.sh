#!/usr/bin/env bash
set -euo pipefail

velero_version="${DRAAS_VELERO_VERSION:-v1.17.0}"
aws_plugin="${DRAAS_VELERO_AWS_PLUGIN:-velero/velero-plugin-for-aws:v1.13.0}"
bin_dir="${DRAAS_LOCAL_BIN:-$PWD/.local/bin}"
namespace="${DRAAS_VELERO_NAMESPACE:-velero}"
bucket="${DRAAS_VELERO_BUCKET:-velero}"
minio_image="${DRAAS_MINIO_IMAGE:-quay.io/minio/minio:RELEASE.2024-12-18T13-15-44Z}"
mc_image="${DRAAS_MINIO_MC_IMAGE:-quay.io/minio/mc:RELEASE.2024-11-21T17-21-54Z}"
install_lab_minio="${DRAAS_VELERO_INSTALL_LAB_MINIO:-true}"
mkdir -p "$bin_dir"

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required" >&2
  exit 2
fi

if ! command -v velero >/dev/null 2>&1; then
  os="linux"
  arch="amd64"
  url="https://github.com/vmware-tanzu/velero/releases/download/${velero_version}/velero-${velero_version}-${os}-${arch}.tar.gz"
  tmp="$(mktemp -d)"
  curl -L --fail --retry 3 -o "$tmp/velero.tgz" "$url"
  tar -xzf "$tmp/velero.tgz" -C "$tmp"
  cp "$tmp"/velero-*/velero "$bin_dir/velero"
  chmod +x "$bin_dir/velero"
  export PATH="$bin_dir:$PATH"
elif [[ ":$PATH:" != *":$bin_dir:"* ]]; then
  export PATH="$bin_dir:$PATH"
fi

kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

if ! kubectl -n "$namespace" get deploy velero >/dev/null 2>&1; then
  velero install \
    --namespace "$namespace" \
    --provider aws \
    --plugins "$aws_plugin" \
    --no-default-backup-location \
    --no-secret \
    --features=EnableCSI \
    --use-node-agent \
    --wait
else
  echo "Velero controller already installed in namespace $namespace"
fi

kubectl -n "$namespace" rollout status deploy/velero --timeout=180s

if [[ "$install_lab_minio" != "true" ]]; then
  scripts/check-velero.sh
  exit 0
fi

cat <<YAML | kubectl apply -n "$namespace" -f -
apiVersion: v1
kind: Secret
metadata:
  name: minio-creds
type: Opaque
stringData:
  root-user: minio
  root-password: minio123
---
apiVersion: v1
kind: Secret
metadata:
  name: cloud-credentials
type: Opaque
stringData:
  cloud: |
    [default]
    aws_access_key_id=minio
    aws_secret_access_key=minio123
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: minio-data
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 5Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: minio
spec:
  replicas: 1
  selector: {matchLabels: {app: minio}}
  template:
    metadata: {labels: {app: minio}}
    spec:
      containers:
        - name: minio
          image: ${minio_image}
          args: ["server", "/data"]
          env:
            - name: MINIO_ROOT_USER
              valueFrom: {secretKeyRef: {name: minio-creds, key: root-user}}
            - name: MINIO_ROOT_PASSWORD
              valueFrom: {secretKeyRef: {name: minio-creds, key: root-password}}
          ports: [{containerPort: 9000, name: s3}]
          volumeMounts: [{name: data, mountPath: /data}]
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: minio-data}
---
apiVersion: v1
kind: Service
metadata:
  name: minio
spec:
  selector: {app: minio}
  ports: [{port: 9000, targetPort: s3, name: s3}]
YAML

if ! kubectl -n "$namespace" rollout status deploy/minio --timeout=180s; then
  echo "MinIO lab deployment did not become ready; Velero core remains installed and preflight will report DEGRADED." >&2
  scripts/check-velero.sh
  exit 3
fi

if ! kubectl -n "$namespace" run minio-mc --rm -i --restart=Never --image="$mc_image" --command -- sh -c "mc alias set local http://minio:9000 minio minio123 && mc mb -p local/${bucket} || true"; then
  echo "MinIO client could not create bucket; Velero core remains installed and preflight will report DEGRADED." >&2
  scripts/check-velero.sh
  exit 3
fi

cat <<YAML | kubectl apply -n "$namespace" -f -
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: default
spec:
  provider: aws
  objectStorage:
    bucket: ${bucket}
  config:
    region: minio
    s3ForcePathStyle: "true"
    s3Url: http://minio.${namespace}.svc:9000
  credential:
    name: cloud-credentials
    key: cloud
YAML

kubectl -n "$namespace" get deploy velero
scripts/check-velero.sh
