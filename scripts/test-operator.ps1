$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath (Split-Path -Parent $PSScriptRoot)
$config = Get-Content -Raw .local\lab-secrets.json | ConvertFrom-Json
$auth = $config.AUTH_PRINCIPALS | ConvertFrom-Json
$headers = @{Authorization="Bearer $($auth[0].token)"}
$base = 'http://127.0.0.1:8080/api/v1'
$workload = Invoke-RestMethod -Uri "$base/workloads" -Method Post -Headers $headers -ContentType application/json -Body (@{name='Operator scheduled Linux simulator';size_mib=4} | ConvertTo-Json)
$name = 'operator-acceptance-' + [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
$cr = @{apiVersion='dr.draas.local/v1alpha1';kind='ProtectedWorkload';metadata=@{name=$name;namespace='draas-system'};spec=@{tenantID='lab-tenant';workloadID=$workload.id;policyRef='lab-standard';suspended=$false}}
$cr | ConvertTo-Json -Depth 8 | kubectl --kubeconfig .local\kubeconfig apply -f -
if ($LASTEXITCODE -ne 0) { throw 'CR creation failed' }
$deadline = [DateTime]::UtcNow.AddSeconds(75)
$matched = $null
do {
  $page = Invoke-RestMethod -Uri "$base/recovery-points?limit=100" -Headers $headers
  $matched = $page.items | Where-Object { $_.workload_id -eq $workload.id -and $_.status -eq 'VERIFIED' }
  if (!$matched) { Start-Sleep -Seconds 2 }
} while (!$matched -and [DateTime]::UtcNow -lt $deadline)
if (!$matched) { throw 'Operator did not produce a verified point within the deadline' }
$object = kubectl --kubeconfig .local\kubeconfig -n draas-system get protectedworkload $name -o json | ConvertFrom-Json
@{result='PASS';workload_id=$workload.id;recovery_point=$matched.id;operator_status=$object.status;timestamp=[DateTime]::UtcNow.ToString('o')} | ConvertTo-Json -Depth 8 | Set-Content -Encoding UTF8 docs\evidence\operator.json
# Suspend only this acceptance object, keeping all records and point evidence.
$cr.spec.suspended = $true
$cr | ConvertTo-Json -Depth 8 | kubectl --kubeconfig .local\kubeconfig apply -f -
Write-Output 'PASS: declarative ProtectedWorkload -> scheduled backup -> VERIFIED recovery point. Acceptance schedule suspended.'

