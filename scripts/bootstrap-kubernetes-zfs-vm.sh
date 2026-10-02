#!/usr/bin/env bash
set -euo pipefail

root="${DRAAS_K8S_ZFS_VM_ROOT:-$PWD/.local/p3d1-k8s-zfs-vm}"
vm_name="${DRAAS_K8S_ZFS_VM_NAME:-draas-p3d1-k8s-zfs}"
ubuntu_url="${DRAAS_K8S_ZFS_UBUNTU_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
ssh_port="${DRAAS_K8S_ZFS_SSH_PORT:-2222}"
api_port="${DRAAS_K8S_ZFS_API_PORT:-16443}"
mem="${DRAAS_K8S_ZFS_VM_MEM:-4096}"
cpus="${DRAAS_K8S_ZFS_VM_CPUS:-2}"
mkdir -p "$root"

base="$root/ubuntu-noble-cloudimg-amd64.img"
osdisk="$root/os.qcow2"
zfsdisk="$root/zfs-pool.qcow2"
seed="$root/seed.iso"
ssh_key="$root/id_ed25519"
ssh_pub="$ssh_key.pub"
pidfile="$root/qemu.pid"
logfile="$root/qemu.log"
user_data="$root/user-data"
meta_data="$root/meta-data"

if [[ ! -f "$base" ]]; then
  curl -L --fail --retry 3 -o "$base.tmp" "$ubuntu_url"
  mv "$base.tmp" "$base"
fi
if [[ ! -f "$osdisk" ]]; then
  qemu-img create -f qcow2 -F qcow2 -b "$base" "$osdisk" 30G >/dev/null
fi
if [[ ! -f "$zfsdisk" ]]; then
  qemu-img create -f qcow2 "$zfsdisk" 20G >/dev/null
fi
if [[ ! -f "$ssh_key" ]]; then
  ssh-keygen -t ed25519 -N '' -f "$ssh_key" >/dev/null
fi

cat > "$meta_data" <<META
instance-id: $vm_name
local-hostname: $vm_name
META

cat > "$user_data" <<USERDATA
#cloud-config
users:
  - name: draas
    groups: sudo
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - $(cat "$ssh_pub")
package_update: true
packages:
  - curl
  - ca-certificates
  - gnupg
  - lsb-release
  - jq
  - zfsutils-linux
  - open-iscsi
  - nfs-common
write_files:
  - path: /usr/local/sbin/draas-k8s-zfs-bootstrap.sh
    permissions: '0755'
    content: |
      #!/usr/bin/env bash
      set -euxo pipefail
      systemctl enable --now iscsid || true
      modprobe zfs || true
      if ! zpool list draas-zfs >/dev/null 2>&1; then
        disk=""
        for candidate in /dev/vdb /dev/sdb /dev/disk/by-id/*; do
          if [ -b "\$candidate" ] && ! lsblk -no MOUNTPOINT "\$candidate" | grep -q .; then
            if ! blkid "\$candidate" >/dev/null 2>&1; then disk="\$candidate"; break; fi
          fi
        done
        test -n "\$disk"
        zpool create -f -O compression=lz4 -O atime=off draas-zfs "\$disk"
      fi
      if ! command -v k3s >/dev/null 2>&1; then
        curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC='server --write-kubeconfig-mode 644 --disable traefik --disable servicelb' sh -
      fi
      export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
      until kubectl get nodes; do sleep 3; done
      kubectl create namespace draas-recovery --dry-run=client -o yaml | kubectl apply -f -
      kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-8.2/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
      kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-8.2/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
      kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-8.2/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
      kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-8.2/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml
      kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-8.2/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml
      if ! command -v helm >/dev/null 2>&1; then
        curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
      fi
      helm repo add openebs https://openebs.github.io/openebs >/dev/null 2>&1 || true
      helm repo update
      helm upgrade --install openebs-zfs-localpv openebs/zfs-localpv -n openebs --create-namespace --wait --timeout 10m
      cat <<'SC' | kubectl apply -f -
      apiVersion: storage.k8s.io/v1
      kind: StorageClass
      metadata:
        name: zfs-local
      provisioner: zfs.csi.openebs.io
      allowVolumeExpansion: true
      volumeBindingMode: WaitForFirstConsume
      parameters:
        poolname: draas-zfs
        fstype: zfs
      SC
      cat <<'VSC' | kubectl apply -f -
      apiVersion: snapshot.storage.k8s.io/v1
      kind: VolumeSnapshotClass
      metadata:
        name: zfs-local-snapclass
      driver: zfs.csi.openebs.io
      deletionPolicy: Retain
      VSC
      kubectl -n kube-system wait --for=condition=Ready pod -l k8s-app=kube-dns --timeout=180s || true
      kubectl get storageclass zfs-local
      kubectl api-resources --api-group=snapshot.storage.k8s.io | grep volumesnapshots
      touch /var/lib/draas-k8s-zfs-ready
runcmd:
  - [bash, /usr/local/sbin/draas-k8s-zfs-bootstrap.sh]
USERDATA

cloud-localds "$seed" "$user_data" "$meta_data"

if [[ -f "$pidfile" ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
  echo "VM already running pid=$(cat "$pidfile")"
else
  setsid qemu-system-x86_64 \
    -name "$vm_name" \
    -enable-kvm \
    -machine accel=kvm,type=q35 \
    -cpu host \
    -smp "$cpus" \
    -m "$mem" \
    -drive file="$osdisk",if=virtio,format=qcow2 \
    -drive file="$zfsdisk",if=virtio,format=qcow2 \
    -drive file="$seed",if=virtio,format=raw,readonly=on \
    -netdev user,id=net0,hostfwd=tcp:127.0.0.1:${ssh_port}-:22,hostfwd=tcp:127.0.0.1:${api_port}-:6443 \
    -device virtio-net-pci,netdev=net0 \
    -display none \
    -serial file:"$root/serial.log" \
    -pidfile "$pidfile" \
    >"$logfile" 2>&1 < /dev/null &
fi

echo "VM root: $root"
echo "SSH: ssh -i $ssh_key -p $ssh_port -o StrictHostKeyChecking=no draas@127.0.0.1"
echo "Kubernetes API forwarded to https://127.0.0.1:$api_port"
