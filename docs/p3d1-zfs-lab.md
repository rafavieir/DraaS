# P3D.1 Kubernetes DR with Velero

P3D.1 uses Velero as the Kubernetes protection engine. DraaS owns product workflow, policy, billing state, recovery jobs, audit, validation and the RecoveryPoint catalog. Velero owns Kubernetes backup/restore orchestration and CSI volume snapshot integration.

For Kubernetes workloads, do not make DraaS recreate Velero primitives by hand. Use the new protection backend variable:

```bash
export DRAAS_PROTECTION_BACKEND=velero
export DRAAS_VELERO_NAMESPACE=velero
```

`DRAAS_STORAGE_BACKEND=velero` is still accepted for legacy compatibility, but new deployments should use `DRAAS_PROTECTION_BACKEND=velero`. ZFS/CSI remains the block layer underneath Kubernetes; Velero remains the restore orchestrator.

Required cluster components:

- Kubernetes reachable by in-cluster config or `KUBECONFIG`.
- Velero core/CRDs/controller installed with `EnableCSI` when PVC snapshots are required.
- A configured Velero `BackupStorageLocation` for off-cluster object metadata and Velero backup data.
- CSI snapshots installed and working when persistent volumes must be restored.
- `node-agent`/DataMover when the protection policy requires offsite persistent data movement.
- For local block storage, a ZFS-backed CSI driver can provide the underlying `StorageClass`.

Velero recovery point manifest shape:

```json
{
  "format_version": "draas.recovery-point/v1",
  "engine": "velero",
  "tenant": "lab-tenant",
  "workload_id": "app-stack",
  "recovery_point": "rp-id",
  "external_reference": {
    "namespace": "velero",
    "backup_name": "app-stack-20261001-120000",
    "backup_uid": "..."
  },
  "included_namespaces": ["app"],
  "restore_namespace_mapping": {
    "app": "app-dr-test"
  },
  "protection_level": "LOCAL_RECOVERY",
  "persistent_data": {
    "method": "csi_snapshot",
    "offsite": false
  },
  "offsite_copy": false,
  "consistency": "VELERO_BACKUP",
  "backup_phase": "Completed",
  "metadata": {
    "restore_engine": "velero",
    "backup_storage_location": "default"
  }
}
```

Protection levels emitted by the DraaS catalog:

- `CONFIG_ONLY`: Velero backup has Kubernetes objects, but no detected CSI/DataMover persistent data signal.
- `LOCAL_RECOVERY`: CSI snapshots are present and can restore from local snapshot-capable storage.
- `OFFSITE_RECOVERABLE`: Velero DataMover/offsite movement is detected.
- `DR_READY`: reserved for the higher-level acceptance gate after a full recovery drill passes.

Registering a Velero backup as a DraaS recovery point:

```bash
export DATABASE_URL='postgres://...'
export KUBECONFIG=/path/to/kubeconfig
export DRAAS_VELERO_NAMESPACE=velero

go run ./cmd/draas-velero-register-rp \
  -tenant lab-tenant \
  -workload app-stack \
  -point rp-id \
  -velero-namespace velero \
  -backup app-stack-20261001-120000 \
  -included-namespaces app \
  -namespace-mapping app=app-dr-test
```

The register command validates that the referenced Velero `Backup` exists and is `Completed`. It refuses `Failed`, `PartiallyFailed`, missing or unreadable backups, and records UID, timestamps, BSL, CSI/DataMover hints, warnings and errors in the DraaS RecoveryPoint metadata.

Preflight and harness:

```bash
scripts/check-velero.sh
DRAAS_PROTECTION_BACKEND=velero scripts/run-p3d1-crash-matrix.sh
```

`check-velero.sh` returns `READY`, `DEGRADED` or `NOT_READY` with detailed gates for CRDs, controller, BSL, CSI, node-agent/DataMover, credentials, object storage and worker RBAC. `DEGRADED` means Velero core is present, but a required production gate is not fully ready.

Lab bootstrap:

```bash
scripts/bootstrap-velero-minio.sh
```

The bootstrap installs Velero core first with `--no-default-backup-location`, then attempts the optional MinIO lab BSL. If MinIO image pull or bucket creation fails, Velero remains installed and preflight reports `DEGRADED` instead of leaving the cluster without Velero CRDs/controller.
