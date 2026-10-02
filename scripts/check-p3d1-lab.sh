#!/usr/bin/env bash
set -uo pipefail

out="${1:-}"
if [[ -z "$out" ]]; then
  root="${DRAAS_ARTIFACT_ROOT:-artifacts/P3D.1/$(date -u +%Y%m%d-%H%M%S)/environment}"
  mkdir -p "$root"
  out="$root/lab-capabilities.json"
else
  mkdir -p "$(dirname "$out")"
fi

json_escape() {
  python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))' 2>/dev/null || jq -Rs . 2>/dev/null || sed 's/"/\\"/g; s/^/"/; s/$/"/'
}
cmd_text() {
  if command -v "$1" >/dev/null 2>&1; then
    "$@" 2>&1 | head -c 20000
  else
    printf 'NOT_FOUND'
  fi
}
cmd_status() {
  if command -v "$1" >/dev/null 2>&1; then
    "$@" >/dev/null 2>&1
    printf '%s' "$?"
  else
    printf '127'
  fi
}
bool() { if [[ "$1" == "0" || "$1" == "true" ]]; then printf true; else printf false; fi; }

uname_s="$(uname -a 2>&1 || true)"
os_release="$(cat /etc/os-release 2>&1 || true)"
go_version="$(cmd_text go version)"
virsh_version="$(cmd_text virsh --version)"
qemu_version="$(cmd_text qemu-system-x86_64 --version)"
zfs_version="$(cmd_text zfs version)"
zpool_list="$(cmd_text zpool list -H -o name)"
virt_validate="$(cmd_text virt-host-validate qemu)"
id_text="$(id 2>&1 || true)"
groups_text="$(groups 2>&1 || true)"
kvm_ls="$(ls -l /dev/kvm 2>&1 || true)"
cpu_virt_count="$(egrep -c '(vmx|svm)' /proc/cpuinfo 2>/dev/null || printf 0)"
free_h="$(free -h 2>&1 || true)"
df_h="$(df -h 2>&1 || true)"
ip_addr="$(cmd_text ip addr)"
ip_route="$(cmd_text ip route)"
libvirt_status="$(systemctl is-active libvirtd 2>/dev/null || systemctl is-active virtqemud 2>/dev/null || service libvirtd status 2>&1 || service virtqemud status 2>&1 || true)"

linux=false
[[ "$(uname -s 2>/dev/null || true)" == "Linux" ]] && linux=true
command -v go >/dev/null 2>&1; go_ok=$?
command -v virsh >/dev/null 2>&1; virsh_ok=$?
command -v qemu-system-x86_64 >/dev/null 2>&1; qemu_ok=$?
command -v zfs >/dev/null 2>&1; zfs_ok=$?
command -v zpool >/dev/null 2>&1; zpool_ok=$?
[[ -e /dev/kvm ]]; kvm_exists=$?
[[ -r /dev/kvm ]]; kvm_read=$?
[[ -w /dev/kvm ]]; kvm_write=$?
virsh list --all >/dev/null 2>&1; virsh_list_ok=$?

kvm_usable=false
if [[ $kvm_exists -eq 0 && $kvm_read -eq 0 && $kvm_write -eq 0 && "$cpu_virt_count" -gt 0 ]]; then
  kvm_usable=true
fi
libvirt_ok=false
if [[ $virsh_ok -eq 0 && $virsh_list_ok -eq 0 ]]; then
  libvirt_ok=true
fi
qemu_ok_bool=false
[[ $qemu_ok -eq 0 ]] && qemu_ok_bool=true
zfs_ok_bool=false
[[ $zfs_ok -eq 0 && $zpool_ok -eq 0 ]] && zfs_ok_bool=true
go_ok_bool=false
[[ $go_ok -eq 0 ]] && go_ok_bool=true
nested=false
[[ "$cpu_virt_count" -gt 0 ]] && nested=true

result="UNSUPPORTED"
if [[ "$linux" == true && "$go_ok_bool" == true && "$kvm_usable" == true && "$libvirt_ok" == true && "$qemu_ok_bool" == true ]]; then
  result="SUPPORTED"
elif [[ "$linux" == true && ( "$kvm_usable" == true || "$libvirt_ok" == true || "$go_ok_bool" == true ) ]]; then
  result="PARTIAL"
fi

cat > "$out" <<JSON
{
  "checked_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "linux": $linux,
  "go": {"available": $go_ok_bool, "version": $(printf '%s' "$go_version" | json_escape)},
  "kvm_device": {"exists": $(bool $kvm_exists), "readable": $(bool $kvm_read), "writable": $(bool $kvm_write), "listing": $(printf '%s' "$kvm_ls" | json_escape)},
  "kvm_usable": $kvm_usable,
  "libvirt": {"available": $libvirt_ok, "virsh_version": $(printf '%s' "$virsh_version" | json_escape), "daemon_status": $(printf '%s' "$libvirt_status" | json_escape), "virsh_list_all_exit": $virsh_list_ok},
  "qemu": {"available": $qemu_ok_bool, "version": $(printf '%s' "$qemu_version" | json_escape)},
  "zfs": {"available": $zfs_ok_bool, "version": $(printf '%s' "$zfs_version" | json_escape), "pools": $(printf '%s' "$zpool_list" | json_escape)},
  "nested_virtualization": $nested,
  "cpu_virtualization_flags": ${cpu_virt_count:-0},
  "identity": {"id": $(printf '%s' "$id_text" | json_escape), "groups": $(printf '%s' "$groups_text" | json_escape)},
  "memory": $(printf '%s' "$free_h" | json_escape),
  "disk": $(printf '%s' "$df_h" | json_escape),
  "network": {"ip_addr": $(printf '%s' "$ip_addr" | json_escape), "ip_route": $(printf '%s' "$ip_route" | json_escape)},
  "virt_host_validate": $(printf '%s' "$virt_validate" | json_escape),
  "result": "$result"
}
JSON

cat "$out"
if [[ "$result" == "SUPPORTED" ]]; then
  exit 0
fi
exit 2