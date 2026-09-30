param([switch]$Build)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
$container = 'draas-libvirt-lab'
if ($Build) {
  docker build -f deploy/lab-hypervisor/Dockerfile -t draas-libvirt-lab:0.1 .
  if ($LASTEXITCODE) { throw 'Hypervisor build failed' }
  $env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
  try {
    go build -trimpath -o .local/bin/draas-fixture ./cmd/draas-fixture
    if ($LASTEXITCODE) { throw 'Fixture build failed' }
    go build -trimpath -o .local/bin/draas-lab ./cmd/draas-lab
    if ($LASTEXITCODE) { throw 'Acceptance runner build failed' }
  } finally { Remove-Item Env:GOOS,Env:GOARCH,Env:CGO_ENABLED -ErrorAction SilentlyContinue }
}
$existing = docker ps -a --filter "name=^/$container`$" --format '{{.Names}}'
if (-not $existing) {
  docker run -d --init --name $container --device /dev/kvm --mount type=volume,source=draas-libvirt-lab,target=/var/lib/draas-lab draas-libvirt-lab:0.1 sleep infinity
  if ($LASTEXITCODE) { throw 'Lab container startup failed' }
}
$labSecrets = Get-Content .local/lab-secrets.json | ConvertFrom-Json
$labEnv = @('S3_ENDPOINT=host.docker.internal:19000')
foreach ($labName in @('MASTER_KEY','SIGNING_SEED','S3_ACCESS_KEY','S3_SECRET_KEY','S3_BUCKET')) { $labEnv += "$labName=$($labSecrets.$labName)" }
[IO.File]::WriteAllLines((Join-Path (Get-Location) '.local/hypervisor.env'),$labEnv,[Text.UTF8Encoding]::new($false))
docker cp .local/bin/draas-fixture "${container}:/usr/local/bin/draas-fixture"
if ($LASTEXITCODE) { throw 'Fixture copy failed' }
docker cp .local/bin/draas-lab "${container}:/usr/local/bin/draas-lab"
if ($LASTEXITCODE) { throw 'Runner copy failed' }
$evidence = Join-Path (Get-Location) ('artifacts/' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-real-linux')
New-Item -ItemType Directory -Path $evidence | Out-Null
docker exec --env-file .local/hypervisor.env $container /usr/local/bin/draas-lab 2>&1 | Tee-Object -FilePath "$evidence/run.log"
$result = $LASTEXITCODE
# Copy only reports and manifests: never guest keys, seed ISO, tokens or cloud-init.
$runMatch = Select-String -Path "$evidence/run.log" -Pattern 'RUN_ID=(m2a-[0-9-]+)' | Select-Object -First 1
if ($runMatch) {
  $runID = $runMatch.Matches[0].Groups[1].Value
  foreach ($name in @('report.json','signed-report.json','full-manifest.json','incremental-manifest.json')) {
    docker cp "${container}:/var/lib/draas-lab/$runID/$name" "$evidence/$name" 2>$null
  }
}
if ($result) { throw "Real recovery failed. Evidence: $evidence" }
Write-Host "Real recovery evidence: $evidence"
