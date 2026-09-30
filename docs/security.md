# Security review — lab slice

## Implemented
- Random bearer credentials mapped server-side to fixed tenant and role; a tenant header cannot override identity.
- Read-only role rejects mutations. Sensitive mutations produce catalog audit rows transactionally.
- Body/header/time limits, fixed token-bucket-like per-principal minute quota, pagination and allowlisted actions.
- Parameterized queries and tenant filters; composite foreign keys prevent cross-tenant catalog association.
- Tenant/version-specific HKDF key derivation, independent signing seed, random AES-GCM nonces and authenticated context.
- SHA-256 stream/chunk hashes, signed complete manifests and domain-separated Merkle tree. Verification uses configured trusted keys.
- Conditional object creation and bounded reads; forged manifests/path traversal rejected.
- Loopback lab access, CSP, no browser token persistence, no arbitrary URL/script checks or shell execution in recovery.
- Non-root app containers, restricted filesystem and namespaced operator RBAC.

## Remaining limitations
This is **not production-ready**. Bootstrap API credentials are not OIDC/MFA. The API process currently holds lab encryption/signing material and broad S3/catalog credentials. PostgreSQL lab user can modify audit rows; SQL audit is not WORM. There is no cryptographic key rotation workflow, HSM/KMS, PostgreSQL RLS, tenant-specific runtime deployment, retention lock enforcement, GC, signed external anchor, or independent immutable audit export. Secret Kubernetes storage depends on cluster configuration; lab etcd encryption is not configured. TLS is absent on private lab service connections. Namespaces alone do not enforce network isolation. Simulated tests perform no network I/O and cannot establish hypervisor network safety.

Do not enable custom application-check endpoints until SSRF allowlists, egress restriction and sandboxed execution exist. Never treat supplied public keys inside a manifest as trust roots. Back up original master/signing secrets outside object storage. Maintain application keys independently from object-store credentials.

Automated checks cover unauthorized calls, read-only mutation denial, cross-tenant access, manifest tampering, wrong keys, chunk corruption, missing data and unsafe identifiers. Integration evidence and dependency scan output are separate from an external security audit.
