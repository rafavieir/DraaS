# ADR 009 - Multi-Store Backup Architecture

Accepted for P3D design. A single S3-compatible backend is a critical failure domain. The storage layer will separate `BackupStore` from `BackupStoreSet` so policies can choose primary-only, dual-async, dual-sync and primary-plus-immutable behavior.

The product will not depend on AWS-specific replication for correctness. Each copy must have independent health and integrity state. Recovery reads must support failover from primary to secondary with hash verification, record the source store in reports and schedule repair without blocking recovery.

The first lab implementation may use two S3-compatible stores in the same physical environment, but it must be labelled as redundancy testing, not geo-redundancy or immutability.