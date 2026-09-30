# ADR 010 - Catalog Reconstruction

Accepted for P3D design. PostgreSQL is the control-plane catalog, but object storage manifests must remain sufficient to reconstruct recovery points when PostgreSQL and NATS are lost.

Catalog rebuild must discover manifests, validate schema, verify signatures and hashes, recreate tenant/workload/recovery point references and rebuild replica status. It must be idempotent and safe to run repeatedly. It must not trust any object that fails signature or integrity validation.

Key material is separate from object data. Losing the key plane remains loss of recoverability; this milestone documents the minimum surviving material needed for recovery.