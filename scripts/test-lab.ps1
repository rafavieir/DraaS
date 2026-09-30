$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath (Split-Path -Parent $PSScriptRoot)
$config = Get-Content -Raw '.local\lab-secrets.json' | ConvertFrom-Json
$auth = $config.AUTH_PRINCIPALS | ConvertFrom-Json
$env:DRAAS_TEST_URL = 'http://127.0.0.1:8080'
$env:DRAAS_TEST_TOKEN = $auth[0].token
$env:DRAAS_OTHER_TOKEN = $auth[1].token
$env:DRAAS_VIEWER_TOKEN = $auth[2].token
$env:DRAAS_EVIDENCE_DIR = Join-Path (Get-Location) 'docs\evidence'
$env:DRAAS_S3_TEST_ENDPOINT = '127.0.0.1:19000'
$env:DRAAS_S3_TEST_ACCESS = $config.S3_ACCESS_KEY
$env:DRAAS_S3_TEST_SECRET = $config.S3_SECRET_KEY
try { go test ./tests/integration -count=1 -v; if ($LASTEXITCODE -ne 0) { throw 'Integration test failed' } }
finally { Remove-Item Env:DRAAS_TEST_TOKEN,Env:DRAAS_OTHER_TOKEN,Env:DRAAS_VIEWER_TOKEN,Env:DRAAS_S3_TEST_ACCESS,Env:DRAAS_S3_TEST_SECRET -ErrorAction SilentlyContinue }
