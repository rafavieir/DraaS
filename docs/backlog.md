# Delivery ledger

## DONE

- [x] Go streaming engine, S3, Zstandard, AES-GCM, Merkle and Ed25519.
- [x] Durable jobs, PostgreSQL, NATS, tenant authorization, cancellation, bounded retries.
- [x] Simulator full -> changed data -> deduplicated backup -> source deletion -> restore -> signed evidence.
- [x] Operational UI, API and CLI; measured results, no invented health.
- [x] Kubernetes lab, declarative deployment, ProtectionPolicy + ProtectedWorkload operator.
- [x] Unit, integration, negative security, corruption, race and benchmark checks.
- [x] Prometheus scrape, provisioned Grafana dashboard, structured OpenTelemetry span export.
- [x] Declarative scheduling and persistence/cancellation across worker replacement tested.
- [x] REAL LINUX RECOVERY CORE: Linux guest boot, app validation, source destruction and signed report in lab.
- [x] P3A API REAL RECOVERY: API/worker driven real recovery.
- [x] P3B RECOVER -> ACTIVATE: explicit billable DR activation session.
- [x] P3C POWER CONTROLS: start, shutdown, reboot, force-stop and final start through API/job/provider.

## IN PROGRESS

- [ ] P3D.1 Recovery Provider Contract v2: provider primitives, capabilities, persistent handles, state-machine persistence, idempotency tests and old/new worker safety.
- [ ] P3D.2 Multi-store backup redundancy: BackupStoreSet, replica states, async/sync policies, failover reads and repair jobs.
- [ ] P3D.3 Control-plane disaster recovery: rebuild catalog from signed durable artifacts after PostgreSQL/NATS loss.
- [ ] P3D.4 Scheduled DR validation: RecoveryTestPolicy, jittered schedules, sandbox recovery, signed reports and cleanup state.
- [ ] P3D.5 Recovery admission control: capacity estimation, priorities, queueing, preemption of tests and RTO queue-delay accounting.
- [ ] P3D.6 Failure/chaos qualification: storage, catalog, worker, API, Kubernetes, network and provider timeout matrix.

## BLOCKED

- [ ] ZSvirt real provider acceptance: requires lab endpoint, supported API version, credentials and failure-safe isolated network lifecycle.
- [ ] Production HA claims: requires multi-node control plane, storage failure-domain proof, TLS/mTLS, KMS/Vault, tested failover and production runbooks.
- [ ] WORM/Object Lock claims: requires qualified object store with retention/delete-denial tests.

## FUTURE

- [ ] P3E ConsoleBroker.
- [ ] P3F Metering / Usage Ledger.
- [ ] P3G Deactivation.
- [ ] P3H Reconciliation.
- [ ] P3I ZSvirt Real.
- [ ] P3J Proxmox Real.
- [ ] P3K Multi-VM Recovery.
- [ ] P3L Linux Consistency.
- [ ] P3M Windows/VSS.
- [ ] P3N Physical P2V.
- [ ] P3O WORM.
- [ ] P3P Merkle External Anchor / Blockchain.
- [ ] P3Q Large Scale.
- [ ] P3R Production HA.
- [ ] P3S Production Qualification.

Unavailable integrations must fail explicitly. Never translate simulator or same-host lab success into production readiness. `production_ready` stays false until the formal gate says otherwise.