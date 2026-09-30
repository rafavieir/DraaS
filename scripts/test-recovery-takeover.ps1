param(
  [string]$Failpoint = 'after_provider_call_before_persist',
  [int]$LeaseWaitSeconds = 35
)
$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath (Split-Path -Parent $PSScriptRoot)

if (!(Test-Path '.local\lab-secrets.json')) {
  throw 'Lab is not initialized. Run scripts/lab-up.ps1 first.'
}
if (!(Get-Command docker -ErrorAction SilentlyContinue)) {
  throw 'Docker is required for the real libvirt takeover harness.'
}
if (!(Get-Command kubectl -ErrorAction SilentlyContinue)) {
  throw 'kubectl is required for the real libvirt takeover harness.'
}

New-Item -ItemType Directory -Force -Path 'artifacts\P3D.1' | Out-Null
$run = Get-Date -Format 'yyyyMMdd-HHmmss'
$artifact = "artifacts\P3D.1\$run"
New-Item -ItemType Directory -Force -Path $artifact | Out-Null

@{
  status = 'BLOCKED'
  reason = 'The persistent state-machine foundation exists, but real recovery is not fully wired into one-transition reconciliation yet.'
  required_next_step = 'Wire LIBVIRT_LAB real recovery into ReconcileRecoveryJob, then run worker kill/takeover against this harness.'
  failpoint = $Failpoint
  lease_wait_seconds = $LeaseWaitSeconds
  created_at = (Get-Date).ToUniversalTime().ToString('o')
} | ConvertTo-Json -Depth 5 | Set-Content "$artifact\crash-matrix.json"

@"
# P3D.1 Worker Takeover Harness

Status: BLOCKED

This harness is intentionally present before PASS. It must not report success until real LIBVIRT_LAB recovery is wired into the persistent reconciler.

Requested failpoint: $Failpoint
Lease wait seconds: $LeaseWaitSeconds

Required PASS evidence:

- Worker A creates or discovers provider resource.
- Worker A dies after failpoint.
- Lease expires.
- Worker B acquires higher lease generation.
- UNKNOWN operation is reconciled through provider discovery.
- Resource is adopted.
- Duplicate VM/network/disk count stays at 1.
- Recovery reaches READY_FOR_ACTIVATION.
- Guest and application validation pass.
"@ | Set-Content "$artifact\acceptance.md" -NoNewline

Write-Output "Created blocked takeover artifact at $artifact"
exit 1

