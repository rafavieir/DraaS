param([switch]$SkipBuild)
$ErrorActionPreference = 'Stop'
$project = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $project
New-Item -ItemType Directory -Force -Path '.local\tools' | Out-Null
function Assert-LastExit([string]$operation) { if ($LASTEXITCODE -ne 0) { throw "$operation failed (exit $LASTEXITCODE)" } }
function New-HexSecret {
  $bytes = New-Object byte[] 32
  $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
  try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
  return ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant()
}
$kind = Join-Path $project '.local\tools\kind.exe'
if (!(Test-Path -LiteralPath $kind)) { Invoke-WebRequest 'https://kind.sigs.k8s.io/dl/v0.33.0/kind-windows-amd64' -OutFile $kind }
$helm = Join-Path $project '.local\tools\helm\windows-amd64\helm.exe'
if (!(Test-Path -LiteralPath $helm)) {
  Invoke-WebRequest 'https://get.helm.sh/helm-v3.19.0-windows-amd64.zip' -OutFile '.local\tools\helm.zip'
  Expand-Archive -LiteralPath '.local\tools\helm.zip' -DestinationPath '.local\tools\helm' -Force
}
$clusters = & $kind get clusters
Assert-LastExit 'kind inventory'
if ($clusters -notcontains 'draas-lab') { & $kind create cluster --name draas-lab --kubeconfig .local\kubeconfig --wait 120s; Assert-LastExit 'kind create' }
if (!(Test-Path '.local\kubeconfig')) { & $kind get kubeconfig --name draas-lab | Set-Content -Encoding UTF8 '.local\kubeconfig' }
$configPath = Join-Path $project '.local\lab-secrets.json'
if (!(Test-Path -LiteralPath $configPath)) {
  $auth = @(
    @{tenant='lab-tenant'; actor='lab-admin'; role='admin'; token=(New-HexSecret)},
    @{tenant='other-tenant'; actor='other-admin'; role='admin'; token=(New-HexSecret)},
    @{tenant='lab-tenant'; actor='lab-viewer'; role='viewer'; token=(New-HexSecret)}
  )
  $pgPassword = New-HexSecret
  $natsToken = New-HexSecret
  $secret = @{
    DATABASE_URL="postgres://draas:${pgPassword}@postgres:5432/draas?sslmode=disable"
    POSTGRES_PASSWORD=$pgPassword
    NATS_URL="nats://${natsToken}@nats:4222"
    NATS_TOKEN=$natsToken
    S3_ENDPOINT='host.docker.internal:19000'
    S3_ACCESS_KEY='draas-lab'
    S3_SECRET_KEY=(New-HexSecret)
    S3_BUCKET='draas-backups'
    S3_TLS='false'
    MASTER_KEY=(New-HexSecret)
    SIGNING_SEED=(New-HexSecret)
    AUTH_PRINCIPALS=($auth | ConvertTo-Json -Compress)
  }
  $secret | ConvertTo-Json | Set-Content -Encoding UTF8 -LiteralPath $configPath
}
$secret = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
$configuredPrincipals = $secret.AUTH_PRINCIPALS | ConvertFrom-Json
$metricsPrincipal = $configuredPrincipals[2]
$secret | Add-Member -NotePropertyName METRICS_TOKEN -NotePropertyValue $metricsPrincipal.token -Force
if (!$secret.PSObject.Properties['GRAFANA_PASSWORD']) { $secret | Add-Member -NotePropertyName GRAFANA_PASSWORD -NotePropertyValue (New-HexSecret) }
$secret | ConvertTo-Json | Set-Content -Encoding UTF8 -LiteralPath $configPath
@("RUSTFS_ACCESS_KEY=$($secret.S3_ACCESS_KEY)", "RUSTFS_SECRET_KEY=$($secret.S3_SECRET_KEY)", 'RUSTFS_CONSOLE_ENABLE=false', 'RUSTFS_OBS_LOGGER_LEVEL=error') | Set-Content -Encoding ASCII '.local\s3.env'
$names = docker ps -a --format '{{.Names}}'
if ($names -notcontains 'draas-lab-s3') {
  docker run -d --name draas-lab-s3 --label draas.lab=true --restart unless-stopped --env-file .local\s3.env -p 127.0.0.1:19000:9000 -v draas-lab-backups:/data rustfs/rustfs@sha256:8cc9801755448b71a786705ce76692c77e14936cccd87cf2fc31842e58f4d1ff /data
  Assert-LastExit 'external S3 start'
} else { docker start draas-lab-s3 | Out-Null; Assert-LastExit 'external S3 restart' }
if (!$SkipBuild) { docker build -t draas-platform:lab .; Assert-LastExit 'container build' }
& $kind load docker-image draas-platform:lab --name draas-lab
Assert-LastExit 'kind image load'
kubectl --kubeconfig .local\kubeconfig create namespace draas-system --dry-run=client -o json | kubectl --kubeconfig .local\kubeconfig apply -f -
Assert-LastExit 'namespace'
kubectl --kubeconfig .local\kubeconfig label namespace draas-system app.kubernetes.io/managed-by=Helm --overwrite
Assert-LastExit 'namespace owner label'
kubectl --kubeconfig .local\kubeconfig annotate namespace draas-system meta.helm.sh/release-name=draas meta.helm.sh/release-namespace=draas-system --overwrite
Assert-LastExit 'namespace owner annotation'
$manifest = @{apiVersion='v1'; kind='Secret'; metadata=@{name='draas-config';namespace='draas-system'};type='Opaque';stringData=$secret}
$manifest | ConvertTo-Json -Depth 8 | kubectl --kubeconfig .local\kubeconfig apply -f -
Assert-LastExit 'secret creation'
& $helm upgrade --install draas deploy\helm\draas --kubeconfig .local\kubeconfig --namespace draas-system --wait --timeout 5m
Assert-LastExit 'Helm deployment'
Write-Output 'Lab ready. Run scripts/lab-access.ps1 for the local UI. Credentials are in .local/lab-secrets.json (ignored by version control).'
