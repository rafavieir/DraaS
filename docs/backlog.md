# Delivery ledger

## First slice (current implementation target)
- [x] Go streaming engine, S3, Zstandard, AES-GCM, Merkle and Ed25519.
- [x] Durable jobs, PostgreSQL, NATS, tenant authorization, cancellation, bounded retries.
- [x] Simulator full -> changed data -> deduplicated backup -> source deletion -> restore -> signed evidence.
- [x] Operational UI, API and CLI; measured results, no invented health.
- [x] Kubernetes lab, declarative deployment, ProtectionPolicy + ProtectedWorkload operator.
- [x] Unit, integration, negative security, corruption, race and benchmark checks.
- [x] Prometheus scrape, provisioned Grafana dashboard, structured OpenTelemetry span export.
- [x] Declarative scheduling and persistence/cancellation across worker replacement tested.

## Real Linux milestone (not complete until guest boots)
- [ ] Obtain ZSvirt lab endpoint, supported API version and credentials.
- [ ] Implement/test official REST provider including isolated network lifecycle and disk import.
- [ ] Proxmox or VMware source snapshot/export with cleanup finalizers.
- [ ] Full, incremental, remove original **lab-only**, boot, file and HTTP validation, measured RTO.

## Subsequent milestones
- [ ] Windows VSS agent and Windows boot/P2V acceptance.
- [ ] Dependency DAG, recovery waves and complete disaster state/RBAC flow.
- [ ] OIDC, Vault/KMS key rotation, TLS/mTLS, WORM, retention and signed external audit export.
- [ ] Full operator CRDs/reconcilers, HA production profile and failure-domain validation.
- [ ] Checkpoint/resume for multi-TB real sources; CBT, adaptive concurrency, priority scheduling.
- [ ] Capacity discovery, statistically grounded estimates, benchmarks at 10GB–1TB.
- [ ] All source/provider contract suites, chaos suite, PostgreSQL scale tests.
- [ ] Complete NOC, branding, billing boundaries, alert routing and installation automation.

Unavailable integrations must fail explicitly. Never translate a simulator success into production readiness.
