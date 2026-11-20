# Guia de Onboarding — Adotando OpenCloud

Este guia orienta novos projetos da holding RISEGen na adocao do OpenCloud
como camada de abstracao multicloud.

---

## Pre-requisitos

| Requisito    | Versao Minima | Verificacao       |
|--------------|---------------|-------------------|
| Go           | 1.23+         | `go version`      |
| Python       | 3.11+         | `python --version`|
| Docker       | 24+           | `docker --version`|
| Docker Compose | 2.20+       | `docker compose version` |
| gh CLI       | 2.0+          | `gh --version`    |

---

## Passo 1: Adicione como Dependencia Go

No seu projeto, adicione o modulo Go do OpenCloud:

```bash
go get github.com/lakenbeach/risegen-opencloud/go
```

No `go.mod` do seu projeto:

```go
require (
    github.com/lakenbeach/risegen-opencloud/go v0.1.0
)
```

---

## Passo 2: Compile a Shared Library (.so) ou Baixe de Releases

### Opcao A: Compilar localmente

```bash
# Clone o repo e compile
git clone https://github.com/lakenbeach/risegen-opencloud.git
cd risegen-opencloud
./scripts/build-lib.sh

# A .so sera gerada em go/build/libcloudadapter.so
```

### Opcao B: Baixar de releases

```bash
# Baixe a versao mais recente
wget https://github.com/lakenbeach/risegen-opencloud/releases/latest/download/libcloudadapter-linux-amd64.so

# Copie para um local acessivel
cp libcloudadapter-linux-amd64.so /usr/local/lib/libcloudadapter.so
```

---

## Passo 3: Instale o Pacote Python

```bash
pip install opencloud-adapter
```

Ou via `requirements.txt`:

```
opencloud-adapter>=0.1.0
```

### Configurar caminho da shared library

```bash
# Via variavel de ambiente
export OPENCLD_SO_PATH=/usr/local/lib/libcloudadapter.so

# Ou no seu codigo Python
from opencloud import CloudAdapterBase
adapter = CloudAdapterBase(so_path="/usr/local/lib/libcloudadapter.so")
```

---

## Passo 4: Configure o Provedor

### Via YAML (`config/provider.yaml`)

```yaml
storage:
  url: "s3://my-bucket?region=us-east-1"
  # Para MinIO local:
  # url: "s3://my-bucket?endpoint=http://localhost:19000&s3ForcePathStyle=true&disableSSL=true"

pubsub:
  topic_url: "nats://events"
  # NATS_SERVER_URL seleciona o servidor

database:
  collection_url: "dynamodb://my-table?endpoint=http://localhost:18000"

secrets:
  secret_url: "redis://localhost:16379"
```

### Via variaveis de ambiente

```bash
export OPENCLOUD_STORAGE_URL="s3://my-bucket?region=us-east-1"
export OPENCLOUD_PUBSUB_TOPIC_URL="nats://events"
export OPENCLOUD_DATABASE_URL="dynamodb://my-table?endpoint=http://localhost:18000"
export OPENCLOUD_SECRETS_URL="redis://localhost:16379"
```

### Via provider_config (Python)

```python
from opencloud import StorageBucket

bucket = StorageBucket("my-bucket", provider_config={
    "provider": "minio",
    "endpoint": "http://localhost:19000",
    "region": "us-east-1",
    "access_key_id": "minioadmin",
    "secret_access_key": "minioadmin",
})
```

---

## Passo 5: Stack Local de Desenvolvimento

Suba os servicos locais via Docker Compose:

```bash
# No diretorio do seu projeto, use o compose do OpenCloud
docker compose -f /path/to/risegen-opencloud/docker/docker-compose.yml up -d

# Ou copie o compose para o seu projeto
cp /path/to/risegen-opencloud/docker/docker-compose.yml docker/
docker compose -f docker/docker-compose.yml up -d
```

Servicos disponiveis:

| Servico   | Porta  | Uso                            |
|-----------|--------|--------------------------------|
| MinIO     | 19000  | S3-compatible storage          |
| MinIO Console | 19001 | Web UI do MinIO           |
| NATS      | 4222   | Pub/Sub e filas (client)      |
| NATS      | 8222   | NATS monitoring               |
| Redis     | 16379  | Secrets e cache               |
| ScyllaDB  | 18000  | DynamoDB-compatible (Alternator)|

### Verifique se os servicos estao saudaveis

```bash
docker compose -f docker/docker-compose.yml ps
# Todos devem mostrar status "healthy"
```

---

## Passo 6: Rode Testes de Integracao

```bash
# No diretorio do seu projeto
pytest tests/test_integration.py -v

# Ou use o script do OpenCloud
./scripts/test-integration.sh
```

---

## Exemplos de Codigo

### Storage (Blob)

```python
from opencloud import StorageBucket

# Inicializa bucket (provedor definido por configuracao)
bucket = StorageBucket("my-bucket")

# Upload
result = bucket.upload("photos/image.jpg", image_bytes, content_type="image/jpeg")
print(f"Uploaded: {result}")

# Download
data = bucket.download("photos/image.jpg")

# Listar objetos
keys = bucket.list_keys(prefix="photos/")
print(f"Found {len(keys)} objects")

# URL assinada (temporaria)
url = bucket.get_signed_url("photos/image.jpg", expiry_seconds=3600)
print(f"Signed URL: {url}")

# Deletar
bucket.delete("photos/image.jpg")
```

### Banco de Dados (Docstore)

```python
from opencloud import DatabaseTable

# Inicializa tabela (chave composta: pk + sk)
table = DatabaseTable("my-table", provider_config={
    "provider": "scylladb",
    "endpoint": "http://localhost:18000",
    "partition_key": "pk",
    "sort_key": "sk",
})

# Inserir item
table.put_item({
    "pk": "user#123",
    "sk": "profile",
    "name": "Maria Silva",
    "email": "maria@example.com",
})

# Buscar item
item = table.get_item({"pk": "user#123", "sk": "profile"})
print(f"User: {item['name']}")

# Query
results = table.query(
    filter_expr="pk = :pk",
    expression_values={":pk": "user#123"},
    limit=10,
)

# Scan
all_items = table.scan(limit=100)

# Atualizar
table.update_item(
    keys={"pk": "user#123", "sk": "profile"},
    updates={"name": "Maria Santos"},
)

# Deletar
table.delete_item({"pk": "user#123", "sk": "profile"})
```

### Pub/Sub

```python
from opencloud import PubSubTopic

# Inicializa topico
topic = PubSubTopic("events")

# Publicar mensagem
result = topic.publish(
    message={"event": "user.created", "user_id": "123"},
    attributes={"source": "api"},
)
print(f"Published: {result}")

# Puxar mensagens
messages = topic.pull_messages(subscription_name="my-sub", max_messages=10)
for msg in messages:
    print(f"Received: {msg}")

# Confirmar recebimento
topic.acknowledge(
    subscription_name="my-sub",
    message_ids=[msg["id"] for msg in messages],
)
```

### Secrets

```python
from opencloud import SecretStore

# Inicializa store
secrets = SecretStore()

# Criar/atualizar secret
secrets.set_secret("api-key", "sk-abc123", description="API key for service X")

# Recuperar secret
value = secrets.get_secret("api-key")
print(f"Secret: {value}")

# Listar secrets
all_secrets = secrets.list_secrets(prefix="api-")
print(f"Found: {all_secrets}")

# Deletar
secrets.delete_secret("api-key")
```

---

## Proximos Passos

1. **Configure CI/CD**: garanta que testes de integracao rodam no CI
2. **Defina provedores por ambiente**: use variaveis de ambiente para
   selecionar provedor (local em dev, cloud em staging/producao)
3. **Monitore**: adicione metricas de latencia e erro dos adapters
4. **Contribua**: se precisar de funcionalidade nao suportada, siga o
   [Patch Workflow](patch-workflow.md)

---

## Suporte

- Documentacao completa: [docs/architecture.md](architecture.md)
- Matriz de provedores: [docs/provider-matrix.md](provider-matrix.md)
- Issues: https://github.com/lakenbeach/risegen-opencloud/issues
