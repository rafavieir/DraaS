#!/usr/bin/env bash
set -euo pipefail
out_dir="${1:-artifacts/P3D.1/libvirt-smoke-$(date -u +%Y%m%d-%H%M%S)}"
mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd -P)"
name="draas-smoke-$(date -u +%Y%m%d%H%M%S)-$$"
disk="$out_dir/smoke.qcow2"
xml="$out_dir/smoke.xml"
serial="$out_dir/serial.log"
result="$out_dir/libvirt-smoke.json"
cleanup() { virsh destroy "$name" >/dev/null 2>&1 || true; virsh undefine "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
: > "$serial"
chmod 0666 "$serial" || true
qemu-img create -f qcow2 "$disk" 64M > "$out_dir/qemu-img.log" 2>&1
chmod 0666 "$disk" || true
cat > "$xml" <<XML
<domain type='kvm'>
  <name>$name</name>
  <memory unit='MiB'>128</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os>
  <features><acpi/><apic/></features>
  <cpu mode='host-passthrough'/>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>destroy</on_reboot>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='$disk'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <serial type='file'><source path='$serial'/><target port='0'/></serial>
    <console type='file'><source path='$serial'/><target type='serial' port='0'/></console>
    <memballoon model='none'/>
  </devices>
</domain>
XML
virsh define "$xml" > "$out_dir/define.log" 2>&1
virsh start "$name" > "$out_dir/start.log" 2>&1
sleep 2
state="$(virsh domstate "$name" 2>&1 || true)"
if [[ "$state" != *running* ]]; then echo "domain did not reach running: $state" > "$out_dir/error.log"; exit 1; fi
virsh destroy "$name" > "$out_dir/destroy.log" 2>&1 || true
virsh undefine "$name" > "$out_dir/undefine.log" 2>&1
if virsh list --all --name | grep -Fx "$name" >/dev/null 2>&1; then echo "domain still exists after cleanup" > "$out_dir/error.log"; exit 1; fi
cat > "$result" <<JSON
{"result":"PASS","domain":"$name","state_observed":"$state","cleanup_verified":true,"checked_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
JSON
cat "$result"