# Recovery and acceptance

## Simulator acceptance
`scripts/test-lab.ps1` drives the live Kubernetes API: creates a 16 MiB workload, generates full, changes the first chunk, generates a second point, checks deduplication, removes the synthetic source, requests test and restore, validates signed evidence and tests tenant/RBAC boundaries. Reports carry `simulation=true`, `boot_verified=false` and `SIMULATION_PASS`.

STORED means persisted, not recoverable. The audit worker must authenticate/decrypt/decompress every chunk, compare SHA-256 and verify the manifest signature/Merkle root before VERIFIED. The recovery path verifies again, materializes a temporary disk and validates the simulator format. Corrupt or missing data prevents success. Cleanup removes the temporary file. No real VM is created by this provider.

## Catalog rebuild
`draasctl catalog-rebuild` runs administratively with backend configuration and `REBUILD_TENANT`. It lists that tenant's immutable-creation manifests, verifies them against configured trust, reads/verifies all chunks, recreates absent synthetic workload catalog entries as inactive and records verified points. This command does not regenerate keys, recreate job history or recover deleted objects. Store and keys must both survive. Original workload names/policies are not guaranteed because the initial manifest is simulator-scoped.

## DR of the platform
Export Helm values, CRDs, safe configuration and encrypted secret backups to a separate security domain. Take PostgreSQL base/WAL backups and regularly restore them. NATS persists job events in its PVC, and outbox records close publication gaps. A brand-new NATS stream with a surviving catalog still needs explicit re-enqueue of previously published unfinished jobs; do not claim this failure is automatically repaired yet. Lost catalog recovery can rebuild data points from S3, not all platform history.

## Real boot acceptance (pending)
ZSvirt credentials, lab cluster, disk import API and isolated network topology are required. Implement official REST operations for VM/disks/network, cleanup/finalizers, progress and timeouts. Then protect a bootable Linux workload; alter data; remove only the lab original; restore; boot; validate file/HTTP service; measure preparation, transfer, provisioning, boot and readiness. Repeat Windows and multi-machine waves later. Do not extrapolate simulator timings to a customer SLA.
