# DraaS

Plataforma DRaaS em Go com control plane **Kubernetes**, catÃ¡logo PostgreSQL, jobs NATS JetStream e storage S3 **externo ao cluster**.

**Estado:** primeiro vertical slice com simuladores. NÃ£o Ã© uma plataforma homologada para produÃ§Ã£o. O simulador restaura e verifica dados reais de uma imagem sintÃ©tica; nÃ£o inicia uma VM e nÃ£o comprova RTO de produÃ§Ã£o.

## Iniciar no Windows

Requisitos: Docker Desktop em modo Linux, Go 1.26+, PowerShell e kubectl. O bootstrap instala kind/Helm apenas em `.local/tools` e cria o cluster isolado `draas-lab`. NÃ£o modifica outros projetos.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/lab-up.ps1
# Em um terminal que ficarÃ¡ aberto:
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/lab-access.ps1
# Copiar a credencial sem exibi-la:
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/lab-token.ps1 -Clipboard
```

Acesse **http://127.0.0.1:8080** e cole a credencial. Credenciais, kubeconfig e chaves sÃ£o gerados em `.local`, fora do versionamento. NÃ£o apague esse diretÃ³rio se quiser recuperar os backups existentes.

## Fluxo executÃ¡vel

1. Adicionar um workload simulado em **Workloads**.
2. Executar **Backup** e aguardar a verificaÃ§Ã£o em **Recovery points**.
3. **Alterar dados**, executar outro backup e observar chunks novos/reutilizados.
4. **Remover origem** sintÃ©tica, com confirmaÃ§Ã£o.
5. Executar **Testar recovery** ou **Restaurar no simulador**.
6. Abrir **Testes de DR â†’ Detalhes e evidÃªncia**: hashes, Merkle root, tempo medido, verificaÃ§Ãµes e assinatura Ed25519.

O segundo backup usa deduplicaÃ§Ã£o com manifest completo; **nÃ£o Ã© CBT**. Restore do simulador materializa um arquivo temporÃ¡rio, verifica o conteÃºdo, valida a aplicaÃ§Ã£o sintÃ©tica e limpa os recursos. NÃ£o mantÃ©m VM ou disco de produÃ§Ã£o.

## VerificaÃ§Ã£o

```powershell
go test ./...
docker build --target test -t draas-platform:test .
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-lab.ps1
npm ci
npx playwright install chromium
npm run test:ui
```

O target `test` executa `go vet`, testes com race detector e benchmarks Linux. A integraÃ§Ã£o exige o laboratÃ³rio e port-forward. EvidÃªncias ficam em `docs/evidence`.

**ExecuÃ§Ã£o validada nesta entrega:** veja o [relatÃ³rio de aceite](docs/evidence/acceptance.md), o [relatÃ³rio do fluxo completo](docs/evidence/vertical-slice.json) e a [captura do painel](docs/evidence/ui-1440.png). Operator, substituiÃ§Ã£o do worker, cancelamento, isolamento entre tenants e armazenamento S3 foram exercitados no laboratÃ³rio.

## Componentes

| Componente | ImplementaÃ§Ã£o |
|---|---|
| API e painel | Go HTTP, API `/api/v1`, assets embutidos, JavaScript nativo |
| Jobs | PostgreSQL + outbox transacional + NATS JetStream |
| Backup | Chunks de 1 MiB, SHA-256, Zstandard, AES-256-GCM |
| Auditoria | Merkle, Ed25519, job de verificaÃ§Ã£o independente |
| Recovery | Provider desacoplado e simulador explÃ­cito |
| Kubernetes | Helm, namespaces, PVCs, probes, recursos e RBAC |
| Operator | controller-runtime, ProtectionPolicy e ProtectedWorkload |
| CLI | draasctl, draas-bench |

Consulte [arquitetura](docs/architecture.md), [implantaÃ§Ã£o](docs/deployment.md), [seguranÃ§a](docs/security.md), [recuperaÃ§Ã£o](docs/recovery.md), [desenvolvimento](docs/development.md), [performance](docs/performance.md) e [backlog](docs/backlog.md).

## PrÃ³ximo milestone real

Conectar um laboratÃ³rio ZSvirt e uma origem Proxmox/VMware. SÃ³ declarar esse milestone concluÃ­do apÃ³s full, alteraÃ§Ã£o/incremental, remoÃ§Ã£o da VM original de laboratÃ³rio, restauraÃ§Ã£o, **boot**, validaÃ§Ã£o de arquivo/serviÃ§o e RTO medido. Windows/VSS, HA, WORM, KMS, OIDC e orquestraÃ§Ã£o de mÃºltiplas VMs permanecem no backlog.

