# DraaS

## Scope and truthfulness
The first delivery is an executable simulator vertical slice. It proves encrypted backup, tenant-scoped deduplication, independent manifests, signature/integrity verification, restore and signed evidence. Simulator validation is never OS boot validation. Production readiness and the first real Linux milestone require an actual hypervisor, isolated networks and a bootable guest.

## Planes
- Control: Kubernetes, Go API, bounded workers, operator, PostgreSQL catalog, NATS JetStream.
- Data: S3-compatible object storage **outside Kubernetes**. No disk data in PostgreSQL.
- Recovery: provider interface; isolated simulator initially, ZSvirt REST adapter after a real lab is available.

API -> PostgreSQL job/outbox transaction -> JetStream -> worker -> source -> fixed chunks -> SHA-256 -> Zstandard -> AES-256-GCM -> tenant/key-version scoped S3 -> signed manifest -> separate verification job -> VERIFIED recovery point -> restore -> provider validation -> signed report.

Workers process one durable message each. PostgreSQL advisory locks fence concurrent execution of a job. Job IDs are recovery-point IDs, effects are repeatable, and manifests use conditional creation. A persistent outbox closes the database/message publication gap. Failed jobs have bounded retries; cancellation is persisted. There is no in-memory critical job queue.

## Integrity and recovery
Full manifests describe all chunks in order, including for deduplicated follow-up backups. Each manifest records the whole-stream hash, chunk hashes, offsets, sizes, source metadata, tenant, key version and Merkle root. Trust comes from configured signing keys, never an embedded untrusted public key. A restored stream is verified before provider validation. The simulator checks a workload image format and its service payload; it cannot boot a VM.

## Security
Lab authentication uses random bearer credentials bound server-side to tenant/role. Tenant identifiers are not accepted from a request header. Encryption keys are derived with HKDF using tenant and version; AES-GCM authenticates tenant/version/hash. Signing and encryption secrets are distinct. Production needs OIDC, key rotation/KMS, TLS throughout, WORM retention and independently secured audit storage.

## Lab
An isolated kind cluster hosts API, worker, operator, PostgreSQL and NATS. S3 runs as a separate Docker container with its own named volume. All lab credentials are generated into ignored .local files. No existing Docker containers, volumes or Kubernetes contexts are deleted. The lab is not HA and does not claim production isolation.

## Recovery of the control plane
S3 manifests and chunks plus the original signing trust/key material are sufficient for stream reconstruction. Catalog rebuild is an explicit admin CLI operation. Keep encrypted, separate backups of keys, PostgreSQL and deployment configuration. Loss of keys is loss of recoverability.

