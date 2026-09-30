param([switch]$Clipboard)
$ErrorActionPreference = 'Stop'
$file = Join-Path (Split-Path -Parent $PSScriptRoot) '.local\lab-secrets.json'
$secret = Get-Content -LiteralPath $file -Raw | ConvertFrom-Json
$principals = $secret.AUTH_PRINCIPALS | ConvertFrom-Json
if ($Clipboard) { $principals[0].token | Set-Clipboard; Write-Output 'Credencial administrativa copiada.' }
else { Write-Output $principals[0].token }
