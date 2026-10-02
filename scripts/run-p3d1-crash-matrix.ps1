param(
  [string]$RunID = ""
)
$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath (Split-Path -Parent $PSScriptRoot)

if ($RunID -ne '') {
  $env:P3D1_RUN_ID = $RunID
}

if (Get-Command wsl -ErrorAction SilentlyContinue) {
  wsl -e sh -lc 'cd /mnt/c/Users/rafael/Desktop/DR && chmod +x scripts/check-p3d1-lab.sh scripts/run-p3d1-crash-matrix.sh && scripts/run-p3d1-crash-matrix.sh'
  exit $LASTEXITCODE
}

throw 'WSL or a Linux shell is required to run the P3D.1 KVM/libvirt crash matrix harness.'
