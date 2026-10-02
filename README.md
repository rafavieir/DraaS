# DraaS

Plataforma de **Disaster Recovery as a Service (DRaaS)** desenvolvida em Go, com control plane em **Kubernetes**, cat√°logo em PostgreSQL, processamento ass√≠ncrono com **NATS JetStream** e armazenamento S3 externo ao cluster.

> **Status atual:** vertical slice funcional em ambiente de laborat√≥rio, utilizando simuladores.
>
> O projeto ainda **n√£o est√° homologado para produ√ß√£o**. O fluxo atual valida backup, deduplica√ß√£o, integridade e recupera√ß√£o de dados sint√©ticos, mas ainda n√£o representa o ciclo completo de recupera√ß√£o de uma VM real nem comprova RTO de produ√ß√£o.

---

## Arquitetura

Principais componentes:

| Componente | Implementa√ß√£o |
|---|---|
| API e painel | Go HTTP, API `/api/v1`, assets embutidos e JavaScript nativo |
| Jobs | PostgreSQL + outbox transacional + NATS JetStream |
| Backup | Chunks de 1 MiB, SHA-256, Zstandard e AES-256-GCM |
| Auditoria | Merkle Root, Ed25519 e verifica√ß√£o independente |
| Recovery | Provider desacoplado com simulador expl√≠cito |
| Kubernetes | Helm, namespaces, PVCs, probes, resources e RBAC |
| Operator | controller-runtime, `ProtectionPolicy` e `ProtectedWorkload` |
| CLI | `draasctl` e `draas-bench` |

---

## Executando no Windows

### Requisitos

- Docker Desktop em modo Linux
- Go 1.26+
- PowerShell
- kubectl

O bootstrap instala `kind` e `Helm` apenas em:

```text
.local/tools

---

## Kubernetes Quickstart ó Velero + DraaS

Este È o fluxo mÌnimo validado para laboratÛrio Kubernetes:

1. Validar Velero, CSI, BSL e RBAC:

```bash
export KUBECONFIG=$PWD/.local/p3d1-k8s-zfs-vm/kubeconfig
export DRAAS_PROTECTION_BACKEND=velero
export DRAAS_VELERO_NAMESPACE=velero
scripts/check-velero.sh artifacts/P3D.1/latest-velero-ready.json
```

2. Criar um backup Kubernetes pelo DraaS:

```bash
go run ./cmd/draas-k8s-dr k8s backup create \
  --namespace draas-e2e-test \
  --tenant lab-tenant \
  --asset draas-e2e-test \
  --state-dir artifacts/P3D.1/e2e-latest/state
```

3. Listar RecoveryPoints locais gerados pelo DraaS:

```bash
go run ./cmd/draas-k8s-dr recovery-points list \
  --state-dir artifacts/P3D.1/e2e-latest/state
```

4. Restaurar pelo DraaS, sem executar `velero restore create` manualmente:

```bash
go run ./cmd/draas-k8s-dr restore create \
  --recovery-point RP_ID \
  --tenant lab-tenant \
  --state-dir artifacts/P3D.1/e2e-latest/state
```

5. Executar o E2E automatizado:

```bash
DRAAS_ARTIFACT_ROOT=artifacts/P3D.1/e2e-latest \
  scripts/e2e-velero-draas.sh
```

O E2E prova:

- Velero `READY`;
- BackupStorageLocation `Available`;
- backup real de namespace Kubernetes;
- RecoveryPoint DraaS autom·tico;
- destruiÁ„o do namespace;
- Restore criado pelo DraaS via Kubernetes API;
- workload restaurado;
- HTTP `DRAAS-E2E-OK` validado;
- segundo teste com PVC/ZFS e conte˙do `DRAAS-PERSISTENT-DATA-OK` validado.

Artefatos principais:

```text
artifacts/P3D.1/e2e-latest/summary.json
artifacts/P3D.1/e2e-latest/P3D.1-acceptance.json
artifacts/P3D.1/latest-velero-ready.json
```

### Troubleshooting r·pido

- `Velero DEGRADED`: rode `scripts/check-velero.sh` e verifique o gate que ficou `WARN` ou `FAIL`.
- `BSL unavailable`: confirme `kubectl -n velero get backupstoragelocations default -o yaml` e o endpoint S3 configurado.
- `ImagePullBackOff` no MinIO: use um endpoint S3 compatÌvel externo/lab e configure o BSL; n„o altere a arquitetura DraaS.
- `backup Failed` ou `PartiallyFailed`: consulte `kubectl -n velero describe backup BACKUP`.
- `restore PartiallyFailed`: consulte warnings/errors em `kubectl -n velero describe restore RESTORE`.
- `PVC Pending`: valide `StorageClass`, `VolumeSnapshotClass` e OpenEBS/ZFS.
- Snapshot CSI n„o detectado: a `VolumeSnapshotClass` deve ter `velero.io/csi-volumesnapshot-class=true`.
