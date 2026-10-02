#!/usr/bin/env bash
set -euo pipefail
pool="${DRAAS_ZFS_POOL:-}"
out_dir="${1:-artifacts/P3D.1/zfs-smoke-$(date -u +%Y%m%d-%H%M%S)}"
mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd -P)"
if [[ -z "$pool" ]]; then echo '{"result":"BLOCKED","reason":"DRAAS_ZFS_POOL is required"}' | tee "$out_dir/zfs-smoke.json"; exit 2; fi
if ! command -v zfs >/dev/null 2>&1 || ! command -v zpool >/dev/null 2>&1; then echo '{"result":"BLOCKED","reason":"zfs/zpool commands not found"}' | tee "$out_dir/zfs-smoke.json"; exit 2; fi
if ! zpool list -H "$pool" >/dev/null 2>&1; then echo "{\"result\":\"BLOCKED\",\"reason\":\"zfs pool not imported: $pool\"}" | tee "$out_dir/zfs-smoke.json"; exit 2; fi
base="$pool/draas-smoke-$(date -u +%Y%m%d%H%M%S)-$$"
snap="$base@rp"
clone="$base-clone"
cleanup(){ zfs destroy "$clone" >/dev/null 2>&1 || true; zfs destroy "$snap" >/dev/null 2>&1 || true; zfs destroy "$base" >/dev/null 2>&1 || true; }
trap cleanup EXIT
zfs create -V 64M -o org.draas:managed_by=draas "$base" > "$out_dir/create.log" 2>&1
zfs snapshot "$snap" > "$out_dir/snapshot.log" 2>&1
zfs clone -o org.draas:managed_by=draas -o org.draas:operation_id=smoke "$snap" "$clone" > "$out_dir/clone.log" 2>&1
if [[ ! -e "/dev/zvol/$clone" ]]; then echo "{\"result\":\"FAIL\",\"reason\":\"clone device missing\",\"clone\":\"$clone\"}" | tee "$out_dir/zfs-smoke.json"; exit 1; fi
zfs destroy "$clone" > "$out_dir/destroy-clone.log" 2>&1
zfs destroy "$snap" > "$out_dir/destroy-snapshot.log" 2>&1
zfs destroy "$base" > "$out_dir/destroy-base.log" 2>&1
trap - EXIT
cat > "$out_dir/zfs-smoke.json" <<JSON
{"result":"PASS","pool":"$pool","base":"$base","snapshot":"$snap","clone":"$clone","checked_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
JSON
cat "$out_dir/zfs-smoke.json"