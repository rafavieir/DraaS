# DraaS

Plataforma de **Disaster Recovery as a Service (DRaaS)** desenvolvida em Go, com control plane em **Kubernetes**, catálogo em PostgreSQL, processamento assíncrono com **NATS JetStream** e armazenamento S3 externo ao cluster.

> **Status atual:** vertical slice funcional em ambiente de laboratório, utilizando simuladores.
>
> O projeto ainda **não está homologado para produção**. O fluxo atual valida backup, deduplicação, integridade e recuperação de dados sintéticos, mas ainda não representa o ciclo completo de recuperação de uma VM real nem comprova RTO de produção.

---

## Arquitetura

Principais componentes:

| Componente | Implementação |
|---|---|
| API e painel | Go HTTP, API `/api/v1`, assets embutidos e JavaScript nativo |
| Jobs | PostgreSQL + outbox transacional + NATS JetStream |
| Backup | Chunks de 1 MiB, SHA-256, Zstandard e AES-256-GCM |
| Auditoria | Merkle Root, Ed25519 e verificação independente |
| Recovery | Provider desacoplado com simulador explícito |
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
