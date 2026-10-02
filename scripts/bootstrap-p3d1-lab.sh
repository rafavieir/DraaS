#!/usr/bin/env bash
set -euo pipefail

artifact_root="${DRAAS_ARTIFACT_ROOT:-artifacts/P3D.1/bootstrap-$(date -u +%Y%m%d-%H%M%S)}"
mkdir -p "$artifact_root"
log="$artifact_root/bootstrap.log"
json="$artifact_root/bootstrap-result.json"
exec > >(tee -a "$log") 2>&1

status="BLOCKED"
reason=""
changed=false

is_root=false
if [[ "$(id -u)" -eq 0 ]]; then
  is_root=true
fi
sudo_cmd=()
if [[ "$is_root" == false ]]; then
  if sudo -n true >/dev/null 2>&1; then
    sudo_cmd=(sudo -n)
  else
    reason="sudo is required to install Go/QEMU/libvirt and grant KVM permissions, but non-interactive sudo is unavailable"
    cat > "$json" <<JSON
{
  "status": "$status",
  "reason": "$reason",
  "requires_manual_command": "cd /mnt/c/Users/rafael/Desktop/DR && sudo DRAAS_ARTIFACT_ROOT=$artifact_root scripts/bootstrap-p3d1-lab.sh",
  "changed": false
}
JSON
    cat "$json"
    exit 2
  fi
fi

pkg_manager=""
if command -v apt-get >/dev/null 2>&1; then
  pkg_manager="apt"
elif command -v dnf >/dev/null 2>&1; then
  pkg_manager="dnf"
elif command -v yum >/dev/null 2>&1; then
  pkg_manager="yum"
else
  reason="unsupported Linux distribution: no apt-get, dnf, or yum found"
  cat > "$json" <<JSON
{"status":"BLOCKED","reason":"$reason","changed":false}
JSON
  cat "$json"
  exit 2
fi

install_apt() {
  export DEBIAN_FRONTEND=noninteractive
  "${sudo_cmd[@]}" apt-get update
  "${sudo_cmd[@]}" apt-get install -y --no-install-recommends \
    ca-certificates curl git jq openssh-client build-essential \
    golang-go qemu-kvm qemu-system-x86 qemu-utils cloud-image-utils \
    libvirt-daemon-system libvirt-clients virtinst libguestfs-tools sqlite3
}
install_dnf_yum() {
  local pm="$1"
  "${sudo_cmd[@]}" "$pm" install -y \
    ca-certificates curl git jq openssh-clients gcc gcc-c++ make golang \
    qemu-kvm qemu-img libvirt libvirt-client virt-install libguestfs-tools sqlite
}

case "$pkg_manager" in
  apt) install_apt ;;
  dnf) install_dnf_yum dnf ;;
  yum) install_dnf_yum yum ;;
esac
changed=true

current_user="${DRAAS_LAB_USER:-${SUDO_USER:-}}"
if [[ -z "$current_user" || "$current_user" == "root" ]]; then
  current_user="$(awk -F: '$3 == 1000 {print $1; exit}' /etc/passwd)"
fi
if [[ -z "$current_user" ]]; then current_user="$(id -un)"; fi
for grp in kvm libvirt libvirt-qemu; do
  if getent group "$grp" >/dev/null 2>&1; then
    "${sudo_cmd[@]}" usermod -aG "$grp" "$current_user" || true
  fi
done

if command -v systemctl >/dev/null 2>&1; then
  "${sudo_cmd[@]}" systemctl enable --now libvirtd 2>/dev/null || true
  "${sudo_cmd[@]}" systemctl enable --now virtqemud 2>/dev/null || true
fi
"${sudo_cmd[@]}" service libvirtd start 2>/dev/null || true
"${sudo_cmd[@]}" service virtqemud start 2>/dev/null || true

mkdir -p /var/lib/draas-lab 2>/dev/null || "${sudo_cmd[@]}" mkdir -p /var/lib/draas-lab
"${sudo_cmd[@]}" chown "$current_user":"$current_user" /var/lib/draas-lab 2>/dev/null || true
"${sudo_cmd[@]}" chmod 700 /var/lib/draas-lab 2>/dev/null || true

set +e
scripts/check-p3d1-lab.sh "$artifact_root/lab-capabilities.json"
check_rc=$?
set -e
cap_result="UNKNOWN"
if command -v jq >/dev/null 2>&1; then
  cap_result="$(jq -r '.result // "UNKNOWN"' "$artifact_root/lab-capabilities.json" 2>/dev/null || printf UNKNOWN)"
fi
if [[ "$cap_result" == "SUPPORTED" ]]; then
  status="SUPPORTED"
  reason="Linux/KVM/libvirt lab is ready"
else
  status="PARTIAL"
  reason="bootstrap completed but capability check is $cap_result; user may need to log out/in for group membership or enable nested virtualization"
fi
cat > "$json" <<JSON
{
  "status": "$status",
  "reason": "$reason",
  "changed": $changed,
  "capability_result": "$cap_result",
  "capability_check_exit_code": $check_rc,
  "artifacts_root": "$artifact_root"
}
JSON
cat "$json"
if [[ "$status" == "SUPPORTED" ]]; then
  exit 0
fi
exit 2