$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$secrets = Get-Content .local\lab-secrets.json -Raw | ConvertFrom-Json
$db = [string]$secrets.DATABASE_URL
$db = $db -replace 'postgres:5432','127.0.0.1:15432'
$nats = [string]$secrets.NATS_URL
$nats = $nats -replace 'nats:4222','127.0.0.1:14222'
$env:DATABASE_URL = $db
$env:NATS_URL = $nats
$env:S3_ENDPOINT = ([string]$secrets.S3_ENDPOINT) -replace 'host.docker.internal','127.0.0.1'
$env:S3_ACCESS_KEY = $secrets.S3_ACCESS_KEY
$env:S3_SECRET_KEY = $secrets.S3_SECRET_KEY
$env:S3_BUCKET = $secrets.S3_BUCKET
$env:S3_TLS = 'false'
$env:MASTER_KEY = $secrets.MASTER_KEY
$env:SIGNING_SEED = $secrets.SIGNING_SEED
$auth = $secrets.AUTH_PRINCIPALS
if ($auth -is [string]) {
  $env:AUTH_PRINCIPALS = $auth
} else {
  $env:AUTH_PRINCIPALS = $auth | ConvertTo-Json -Compress
}
$env:LISTEN_ADDR = '127.0.0.1:18080'
& .local\bin\draas-api.exe
