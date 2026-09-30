# Deployment

## Local topology
`draas-lab` kind cluster with API, worker, operator, PostgreSQL 18 and NATS 2.12. Backup objects are held by a distinct RustFS Docker container `draas-lab-s3` and volume `draas-lab-backups`, not by a Kubernetes PVC. PostgreSQL/NATS PVCs are lab control metadata only.

Helm chart: `deploy/helm/draas`. Six namespaces are created for plane boundaries; the initial core services run in `draas-system`. Empty namespaces are not claimed as deployed services. All app containers run non-root, with read-only root filesystem, no privilege escalation, dropped capabilities and CPU/memory limits. The operator alone mounts a service-account token and its Role is namespaced. Postgres initialization uses the upstream container's standard initialization behavior, which is a lab exception to the app security context.

`scripts/lab-up.ps1` uses a project-local kubeconfig. Upstream kind/Helm executables are downloaded from official distribution URLs. Their checksums/signature verification and complete immutable image pinning are production supply-chain backlog items. The external S3 image is pinned to its tested digest. The lab uses generated secrets, private cluster networking and loopback-only published ports; HTTP/PostgreSQL/NATS TLS are not enabled here. Do not expose the lab on the internet.

## Operator usage
Create a workload through API/UI, then apply:

```yaml
apiVersion: dr.draas.local/v1alpha1
kind: ProtectedWorkload
metadata:
  name: linux-lab
  namespace: draas-system
spec:
  tenantID: lab-tenant
  workloadID: REPLACE_WITH_CATALOG_ID
  policyRef: lab-standard
  suspended: false
```

The operator binds its namespace to `OPERATOR_TENANT`, reconciles policy RPO/RTO and schedules deduplicated jobs using a stable interval key. `suspended: true` stops future scheduling, not already queued jobs. Source deletion blocks new scheduling. Status reports observed generation, phase and job. Other proposed CRDs are not yet implemented; catalog rows are not replaced with CRDs.

## Operations
```powershell
kubectl --kubeconfig .local/kubeconfig -n draas-system get pods,pvc,svc
kubectl --kubeconfig .local/kubeconfig -n draas-system logs deploy/draas-worker
kubectl --kubeconfig .local/kubeconfig -n draas-system get drworkload,drpolicy
```

After rebuilding the image, load it with kind and restart only the project deployments. `helm upgrade` with an unchanged image tag does not restart running pods automatically.

## Observability
Prometheus and Grafana are provisioned by the chart. Prometheus scrapes the tenant-scoped API with a viewer token, evaluates two initial alerts and keeps ephemeral lab metrics. The Grafana dashboard source is `observability/grafana/platform.json`, packaged at `deploy/helm/draas/files/platform.json`. The application exports bounded OpenTelemetry spans as structured container logs, including trace parent propagation through NATS; a production OTLP collector is future work. The dashboard and alerts are a starting set, not the complete production suite.

```powershell
kubectl --kubeconfig .local/kubeconfig -n draas-system port-forward svc/draas-grafana 13000:3000
kubectl --kubeconfig .local/kubeconfig -n draas-system port-forward svc/draas-prometheus 19090:9090
```

Grafana login: `lab-admin`, password in `.local/lab-secrets.json` field `GRAFANA_PASSWORD`. Both forwards must bind only to loopback. Metrics storage is ephemeral in this lab; production retention/HA/alert routing need dedicated configuration.

## Production gate
There is deliberately no misleading `production-ready` values file. Before production: three or more Kubernetes control nodes, tested CNI network isolation, HA PostgreSQL with PITR and least-privilege roles, three-replica NATS quorum, redundant qualified object storage with WORM, TLS/mTLS, OIDC, KMS, separate security domains, offsite catalog/secrets backup, failure-domain testing, measured real VM recovery, retention/GC and complete monitoring. Hardware and network topology must be supplied to automate physical installation safely.

