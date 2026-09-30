$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath (Split-Path -Parent $PSScriptRoot)
Write-Output 'UI: http://127.0.0.1:8080. Ctrl+C stops only the port-forward.'
kubectl --kubeconfig .local\kubeconfig -n draas-system port-forward svc/draas-api 8080:8080 --address 127.0.0.1
