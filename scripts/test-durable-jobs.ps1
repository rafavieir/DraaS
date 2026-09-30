$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath (Split-Path -Parent $PSScriptRoot)
$config = Get-Content -Raw .local\lab-secrets.json | ConvertFrom-Json
$auth = $config.AUTH_PRINCIPALS | ConvertFrom-Json
$headers = @{Authorization="Bearer $($auth[0].token)"}
$base = 'http://127.0.0.1:8080/api/v1'
$workload = Invoke-RestMethod -Uri "$base/workloads" -Method Post -Headers $headers -ContentType application/json -Body (@{name='Durable queue acceptance';size_mib=2} | ConvertTo-Json)
kubectl --kubeconfig .local\kubeconfig -n draas-system scale deployment/draas-worker --replicas=0
if ($LASTEXITCODE -ne 0) { throw 'Failed to pause lab worker' }
try {
  kubectl --kubeconfig .local\kubeconfig -n draas-system wait --for=delete pod -l app=draas-worker --timeout=60s
  if ($LASTEXITCODE -ne 0) { throw 'Worker did not stop' }
  $headers['Idempotency-Key'] = [guid]::NewGuid().ToString()
  $cancelled = Invoke-RestMethod -Uri "$base/workloads/$($workload.id)/backup" -Method Post -Headers $headers -ContentType application/json -Body '{}'
  Invoke-RestMethod -Uri "$base/jobs/$($cancelled.id)/cancel" -Method Post -Headers $headers -ContentType application/json -Body '{}' | Out-Null
  $headers['Idempotency-Key'] = [guid]::NewGuid().ToString()
  $queued = Invoke-RestMethod -Uri "$base/workloads/$($workload.id)/backup" -Method Post -Headers $headers -ContentType application/json -Body '{}'
  if ($queued.status -ne 'QUEUED') { throw 'Expected durable QUEUED state' }
} finally {
  kubectl --kubeconfig .local\kubeconfig -n draas-system scale deployment/draas-worker --replicas=1
}
kubectl --kubeconfig .local\kubeconfig -n draas-system rollout status deployment/draas-worker --timeout=60s
$deadline = [DateTime]::UtcNow.AddSeconds(60)
do {
  $job = Invoke-RestMethod -Uri "$base/jobs/$($queued.id)" -Headers $headers
  if ($job.status -in @('FAILED','CANCELLED')) { throw "Unexpected job status $($job.status)" }
  if ($job.status -ne 'COMPLETED') { Start-Sleep -Seconds 2 }
} while ($job.status -ne 'COMPLETED' -and [DateTime]::UtcNow -lt $deadline)
if ($job.status -ne 'COMPLETED') { throw 'Queued backup did not complete after worker restart' }
$cancelState = Invoke-RestMethod -Uri "$base/jobs/$($cancelled.id)" -Headers $headers
if ($cancelState.status -ne 'CANCELLED' -or $cancelState.progress_bytes -ne 0) { throw 'Cancellation did not prevent execution' }
@{result='PASS';queued_job=$queued.id;completed_status=$job.status;cancelled_job=$cancelled.id;cancelled_status=$cancelState.status;scope='Queue persisted while no workers existed; new worker resumed publication/execution';timestamp=[DateTime]::UtcNow.ToString('o')} | ConvertTo-Json -Depth 8 | Set-Content -Encoding UTF8 docs\evidence\durable-jobs.json
Write-Output 'PASS: queued work survived worker replacement; cancelled queued job did not execute.'
