# RISEGen OpenCloud

Abstracao multicloud para a holding RISEGen. Interface unificada para storage,
banco de dados, pub/sub, filas e secrets — sem dependencia de fornecedor
especifico.

Baseado no [Go CDK (Cloud Development Kit)](https://gocloud.dev/) com bridge
CGO para consumo por Python (e qualquer linguagem com FFI para C).

---

## Quick Start

```bash
# 1. Adicione como dependencia Go
go get github.com/lakenbeach/risegen-opencloud/go

# 2. Compile a shared library (ou baixe de releases)
./scripts/build-lib.sh

# 3. Instale o pacote Python
pip install opencloud-adapter

# 4. Suba o stack local de desenvolvimento
docker compose -f docker/docker-compose.yml up -d

# 5. Configure o provedor (YAML ou env vars)
export OPENCLOUD_STORAGE_URL="s3://my-bucket?endpoint=http://localhost:19000&s3ForcePathStyle=true&disableSSL=true"

# 6. Use no seu projeto
python -c "
from opencloud import StorageBucket
bucket = StorageBucket('my-bucket')
bucket.upload('hello.txt', b'Hello from OpenCloud!')
"
```

Guia completo: [docs/onboarding.md](docs/onboarding.md)

---

## Arquitetura

```
┌─────────────────────────────────────────────────────────────┐
│                    Application Layer                         │
│  Qualquer projeto da holding RISEGen                        │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                 Python Wrapper Layer                         │
│  StorageBucket | PubSubTopic | DatabaseTable | SecretStore   │
│  (opencloud-adapter — ctypes interface)                     │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                   CGO Bridge Layer                           │
│  libcloudadapter.so (C shared library exported from Go)     │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                Go CDK Adapter Layer                          │
│  Storage | PubSub | Database | Secrets                      │
│  (google/go-cloud)                                          │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│              Provider Implementation Layer                   │
│  AWS | Azure | GCP | Alibaba | Huawei | Local               │
└─────────────────────────────────────────────────────────────┘
```

Detalhes: [docs/architecture.md](docs/architecture.md)

---

## Matriz de Provedores

| Servico               | AWS    | Azure            | GCP            | Alibaba          | Huawei              | Local              |
|-----------------------|--------|------------------|----------------|------------------|---------------------|--------------------|
| Storage (Blob)        | GA     | beta             | beta           | beta             | experimental        | GA (MinIO)         |
| Database (Docstore)   | GA     | beta (CosmosDB)  | beta (Firestore) | beta (TableStore) | experimental (GaussDB) | GA (ScyllaDB)    |
| Queue (SQS-like)      | GA     | beta             | beta           | beta             | experimental        | GA (NATS)          |
| PubSub                | GA     | beta             | beta           | beta             | experimental        | GA (NATS)          |
| Secrets               | GA     | beta             | GA             | experimental     | experimental        | GA (env vars)      |

Status detalhado: [docs/provider-matrix.md](docs/provider-matrix.md)

---

## Documentacao

| Documento                                       | Descricao                                    |
|-------------------------------------------------|----------------------------------------------|
| [DIRECTIVE.md](DIRECTIVE.md)                    | Diretiva multicloud da holding               |
| [docs/architecture.md](docs/architecture.md)    | Arquitetura em 5 camadas                     |
| [docs/patch-workflow.md](docs/patch-workflow.md)| Workflow de patches upstream (no-fork)       |
| [docs/onboarding.md](docs/onboarding.md)        | Guia para novos projetos                     |
| [docs/provider-matrix.md](docs/provider-matrix.md) | Status de provedores por servico          |

---

## Stack Local de Desenvolvimento

```bash
docker compose -f docker/docker-compose.yml up -d
```

Servicos incluidos:
- **MinIO** (S3-compatible) — porta 19000
- **NATS** (pub/sub + filas) — porta 4222 / 8222
- **Redis** (secrets/cache) — porta 16379
- **ScyllaDB Alternator** (DynamoDB-compatible) — porta 18000

---

## Contribuindo

### Politica No-Fork

Nao fazemos forks permanentes do Go CDK. Quando uma funcionalidade e necessaria:

1. Implemente como patch em `patches/pending/`
2. Submeta PR upstream (`google/go-cloud`)
3. Mova para `patches/submitted/` enquanto aguarda review
4. Quando merged, mova para `patches/merged/` e remova o `replace` do `go.mod`

Detalhes: [docs/patch-workflow.md](docs/patch-workflow.md)

### Submetendo Patches

```bash
# Submeta um patch automaticamente via gh CLI
./scripts/submit-patch.sh --branch feat/minha-feature --title "feat: descricao"
```

### Desenvolvimento Local

```bash
# Build da shared library
./scripts/build-lib.sh

# Testes Go
cd go && go test ./...

# Testes de integracao (com stack local)
./scripts/test-integration.sh
```

---

## Licenca

Apache License 2.0 — veja [LICENSE](LICENSE).

Copyright 2026 RISEGen / Lakenbeach
