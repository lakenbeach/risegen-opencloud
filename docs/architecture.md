# OpenCloud Architecture

## Visao Geral

Este documento descreve a arquitetura do **OpenCloud** — sistema cloud-agnostic
baseado no [Go CDK (Cloud Development Kit)](https://gocloud.dev/) que fornece
interfaces unificadas para storage, pub/sub, banco de dados e gerenciamento
de secrets atraves de multiplos provedores de nuvem.

### Principios de Design

1. **Zero imports de SDK de nuvem na aplicacao**: codigo de aplicacao nunca
   importa boto3, azure-storage, google-cloud-storage ou qualquer SDK de
   provedor diretamente
2. **Troca de provedor por configuracao**: mude de provedor alterando YAML
   ou variaveis de ambiente — sem alteracao de codigo ou recompilacao
3. **Local-first**: desenvolvimento completo local com MinIO, NATS, Redis e
   ScyllaDB Alternator via Docker Compose
4. **Migracao gradual**: endpoints de aplicacao podem coexistir com handlers
   Go nativos durante migracao
5. **Extensibilidade por patch**: funcionalidades nao suportadas podem ser
   implementadas como patches e submetidas upstream

---

## Camadas de Arquitetura

```
┌─────────────────────────────────────────────────────────────┐
│                    Application Layer                         │
│  Flask / FastAPI / Go HTTP / Qualquer framework             │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                 Python Wrapper Layer                         │
│  StorageBucket | PubSubTopic | DatabaseTable | SecretStore   │
│  (opencloud/*.py — ctypes interface)                │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                   CGO Bridge Layer                           │
│  libcloudadapter.so (C shared library exported from Go)     │
│  cmd/main.go (//export functions)                           │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                Go CDK Adapter Layer                          │
│  Storage | PubSub | Database | Secrets                      │
│  (pkg/adapter/*.go)                                         │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│              Provider Implementation Layer                   │
│  AWS S3/DynamoDB/SNS | Azure Blob/CosmosDB | GCS           │
│  Alibaba OSS/TableStore | Huawei OBS/GaussDB               │
│  Local: MinIO | NATS | Redis | ScyllaDB Alternator          │
└─────────────────────────────────────────────────────────────┘
```

---

## Detalhes dos Componentes

### 1. Go CDK Adapters (`pkg/adapter/`)

A selecao de provedor e **dirigida por URL**: o Go CDK abre um recurso a
partir de uma URL, entao o provedor e escolhido inteiramente por configuracao.
O lado Python nunca sabe qual provedor esta em uso.

#### Storage Adapter (`storage.go`)
- **Interface**: `gocloud.dev/blob.Bucket`
- **Provedores**:
  - AWS S3 / MinIO (`s3://bucket?endpoint=http://localhost:19000&s3ForcePathStyle=true`)
  - Google Cloud Storage (`gs://bucket`)
  - Azure Blob (`azblob://container`)
  - Alibaba OSS (`alioss://bucket`) — pacote de provedor customizado
  - Huawei OBS (`huaweios://bucket`) — pacote de provedor customizado
- **Operacoes**: `InitBucket`, `Upload`, `Download`, `ListKeys`, `DeleteObject`, `GetSignedURL`
- **Erros**: `classifyStorageError` mapeia erros do provedor para codigos estaveis
  (`NOT_FOUND`, `PERMISSION_DENIED`, `TIMEOUT`, `INVALID_CONFIG`, `UPLOAD_FAILED`)
- **Retry**: `RetryWithBackoff` retenta falhas transientes com backoff exponencial;
  nunca retenta `PERMISSION_DENIED` / `INVALID_CONFIG`

#### Pub/Sub Adapter (`pubsub.go`)
- **Interface**: `gocloud.dev/pubsub.Topic` / `pubsub.Subscription`
- **Provedores**:
  - AWS SNS/SQS, Google Pub/Sub, Azure Service Bus, RabbitMQ (URL schemes nativos do Go CDK)
  - NATS — selecionado por URL `nats://` (env `NATS_SERVER_URL`, default `nats://127.0.0.1:4222`)
- **Operacoes**: `InitTopic`, `Publish`, `PullMessages`, `Acknowledge`
- **Formato de mensagem**: `{body, metadata}`; body em base64 para payloads binarios

#### Database Adapter (`database.go`)
- **Interface**: `gocloud.dev/docstore.Collection`
- **Provedores**:
  - AWS DynamoDB / ScyllaDB Alternator (`dynamodb://table?endpoint=http://localhost:18000`)
  - Azure CosmosDB (`azurecosmosdb://collection`)
  - Alibaba TableStore (`tablestore://table`)
  - Huawei GaussDB (adapter customizado)
- **Schema**: docstore requer campo chave. O adapter usa chave composta `(pk, sk)` —
  `pk` identifica a entidade/particao, `sk` o item
- **Operacoes**: `InitTable`, `PutItem`, `GetItem`, `Query`, `Scan`, `DeleteItem`, `UpdateItem`

#### Secrets Adapter (`secrets.go`)
- **Interface**: Key-value secret store backed by `runtimevar`/URL
- **Provedores**: AWS Secrets Manager, Azure Key Vault, Google Secret Manager, Redis
- **Operacoes**: `InitSecretStore`, `GetSecret`, `SetSecret`, `DeleteSecret`,
  `ListSecrets`, `GetSecretMetadata`
- **Seguranca**: credenciais nunca sao logadas; URLs sanitizadas por `sanitizeURL`

---

### 2. CGO Bridge Layer (`cmd/`)

#### Proposito

Permite que codigo Python chame funcoes Go sem precisar entender internos do Go.
A bridge exporta assinaturas compativeis com C que o modulo `ctypes` do Python
pode carregar.

#### Protocolo de Comunicacao

**Toda troca e uma unica string JSON como argumento**, e toda funcao retorna uma
string JSON que o chamador deve liberar com `CloudAdapter_FreeString`:

```c
char* CloudAdapter_GetVersion(void);
void  CloudAdapter_FreeString(char* s);

char* CloudAdapter_Close(char* argsJSON);

char* CloudAdapter_InitBucket(char* argsJSON);
char* CloudAdapter_Upload(char* argsJSON);
char* CloudAdapter_Download(char* argsJSON);
char* CloudAdapter_ListKeys(char* argsJSON);
char* CloudAdapter_DeleteObject(char* argsJSON);
char* CloudAdapter_GetSignedURL(char* argsJSON);

char* CloudAdapter_InitTopic(char* argsJSON);
char* CloudAdapter_Publish(char* argsJSON);
char* CloudAdapter_PullMessages(char* argsJSON);
char* CloudAdapter_Acknowledge(char* argsJSON);

char* CloudAdapter_InitTable(char* argsJSON);
char* CloudAdapter_PutItem(char* argsJSON);
char* CloudAdapter_GetItem(char* argsJSON);
char* CloudAdapter_Query(char* argsJSON);
char* CloudAdapter_Scan(char* argsJSON);
char* CloudAdapter_DeleteItem(char* argsJSON);
char* CloudAdapter_UpdateItem(char* argsJSON);

char* CloudAdapter_InitSecretStore(char* argsJSON);
char* CloudAdapter_GetSecret(char* argsJSON);
char* CloudAdapter_SetSecret(char* argsJSON);
char* CloudAdapter_DeleteSecret(char* argsJSON);
char* CloudAdapter_ListSecrets(char* argsJSON);
char* CloudAdapter_GetSecretMetadata(char* argsJSON);
```

#### Registro de Handles

Funcoes `Init*` abrem um recurso e retornam um **handle** opaco; toda operacao
subsequente recebe esse handle em vez de reabrir o provedor:

```json
{"bucket_id": "1"}
```

- Handles vivem em um registro global ao processo, guardado por mutex
- Handle desconhecido/fechado e rejeitado com `not_initialized`
- `Close` libera todos os handles abertos

#### Envelope de Resposta

Toda chamada retorna um objeto JSON. Campos de sucesso sao flat ao lado de `status`:

```json
{"status": "ok", "key": "docs/a.txt", "size": 12}
{"status": "error", "code": "NOT_FOUND", "error": "object not found"}
```

#### Gerenciamento de Memoria

- Strings de entrada sao copiadas pelo cgo; o chamador mantem ownership
- Strings de saida sao alocadas pelo Go e **devem** ser liberadas com `CloudAdapter_FreeString`
- O wrapper Python le o ponteiro como `c_void_p`, decodifica uma copia, e libera

---

### 3. Python Wrapper Layer (`opencloud/`)

#### Base Adapter (`adapter.py`)
- Carrega `libcloudadapter.so` via `ctypes.CDLL` (caminho de `CLOUD_ADAPTER_SO_PATH`)
- Serializa argumentos para JSON, chama o simbolo exportado, libera o ponteiro retornado
- Converte envelopes de erro em excecoes Python (`StorageError`, `PubSubError`,
  `DatabaseError`, `SecretsError`) usando o `code` estavel
- Redacta credenciais antes de logar

#### Interfaces Python

```python
# Storage
class StorageBucket:
    def upload(self, key: str, data: bytes, content_type: str | None = None) -> dict
    def download(self, key: str) -> bytes
    def list_keys(self, prefix: str = "") -> list[str]
    def delete(self, key: str) -> None
    def get_signed_url(self, key: str, expiry_seconds: int = 3600) -> str

# Pub/Sub
class PubSubTopic:
    def publish(self, message: Any, attributes: dict | None = None) -> dict
    def pull_messages(self, subscription_name: str, max_messages: int = 10) -> list
    def acknowledge(self, subscription_name: str, message_ids: list[str]) -> None

# Database
class DatabaseTable:
    def put_item(self, item: dict) -> dict
    def get_item(self, keys: dict) -> dict | None
    def query(self, filter_expr: str, expression_values: dict | None = None,
              limit: int | None = None) -> list[dict]
    def scan(self, limit: int | None = None) -> list[dict]
    def delete_item(self, keys: dict) -> None
    def update_item(self, keys: dict, updates: dict) -> dict

# Secrets
class SecretStore:
    def get_secret(self, name: str, version: str | None = None) -> str
    def set_secret(self, name: str, value: str) -> dict
    def delete_secret(self, name: str, force: bool = False) -> None
    def list_secrets(self, prefix: str = "") -> list
```

---

### 4. Gerenciamento de Configuracao

#### Configuracao Python (provider_config)

O dict passado a `Init*` e traduzido na URL correspondente pelo adapters:

```python
# Storage — MinIO / S3
{"provider": "minio", "endpoint": "http://localhost:19000", "region": "us-east-1",
 "access_key_id": "minioadmin", "secret_access_key": "minioadmin"}

# Pub/Sub — NATS
{"provider": "nats", "server": "nats://localhost:4222"}

# Database — DynamoDB / ScyllaDB Alternator
{"provider": "scylladb", "endpoint": "http://localhost:18000",
 "region": "us-east-1", "access_key_id": "dummy", "secret_access_key": "dummy",
 "partition_key": "pk", "sort_key": "sk"}

# Secrets — Redis
{"provider": "redis", "endpoint": "redis://localhost:16379"}
```

#### Configuracao Go (URL-based)

```yaml
storage:
  url: "s3://my-bucket?region=us-east-1"
  # local MinIO:
  # url: "s3://my-bucket?endpoint=http://localhost:19000&s3ForcePathStyle=true&disableSSL=true"

pubsub:
  topic_url: "nats://events"

database:
  collection_url: "dynamodb://my-table?endpoint=http://localhost:18000"

secrets:
  secret_url: "redis://localhost:16379"
```

`Config` implementa `MarshalJSON` que **redacta** `SecretAccessKey` / senhas,
entao dumps de configuracao sao sempre seguros para log.

#### Hot-Reload

O config loader Go usa `fsnotify` para monitorar alteracoes em arquivos YAML:
1. Detecta evento de modificacao
2. Parseia nova configuracao
3. Valida conexoes do novo provedor
4. Fecha conexoes antigas graciosamente
5. Estabelece novas conexoes
6. Atualiza instancias globais dos adapters

**Tempo de troca**: < 30 segundos

---

## Performance

### Overhead CGO

Medido com benchmark de 500 iteracoes de `CloudAdapter_GetVersion`:

| Metrica | Valor  | Budget  |
|---------|--------|---------|
| mean    | 0.0028 ms | < 2 ms |
| p95     | 0.0049 ms | < 2 ms |
| max     | 0.1414 ms | < 2 ms |

O custo principal e serializacao/deserializacao JSON; I/O de rede domina chamadas reais.

### Uso de Memoria
- Shared library Go: ~20MB carregada em memoria
- Alocacao por request: minima (Go GC gerencia limpeza)
- Uploads grandes: streamed em chunks (nao carregados inteiros em memoria)

### Concorrencia
- Adapters Go lidam com requests concorrentes via goroutines
- Wrapper Python e thread-safe (protecao GIL do ctypes)
- Connection pooling gerenciado pelo Go CDK internamente

---

## Opcoes de Deploy

### Opcao 1: Docker Compose (Local)
```bash
docker compose -f docker/docker-compose.yml up -d
```
Servicos: MinIO, NATS, Redis, ScyllaDB

### Opcao 2: Kubernetes (Producao)
Deploy do servidor Go HTTP como sidecar ao lado da aplicacao:
```yaml
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - name: app
          image: myproject/app:latest
        - name: opencloud-adapter
          image: risegen/opencloud-adapter:latest
          ports:
            - containerPort: 8081
```

### Opcao 3: Serverless (Lambda/Cloud Functions)
Empacote o adapter Go como Lambda layer:
```bash
./scripts/build-lib.sh
zip layer.zip go/build/libcloudadapter.so
aws lambda publish-layer-version --layer-name opencloud --zip-file fileb://layer.zip
```

---

## Troubleshooting

### Biblioteca compartilhada nao encontrada
```
OSError: libcloudadapter.so: cannot open shared object file
```
**Solucao**: Defina `CLOUD_ADAPTER_SO_PATH` para o caminho correto da `.so`.

### Conexao com provedor falhou
```
{"status": "error", "code": "INVALID_CONFIG", "error": "connection refused"}
```
**Solucao**: Verifique se o servico local esta rodando (`docker compose ps`)
e se a URL de configuracao esta correta.

### Credenciais invalidas
```
{"status": "error", "code": "PERMISSION_DENIED", "error": "access denied"}
```
**Solucao**: Verifique as credenciais do provedor. Para stack local, use
as credenciais default (minioadmin/minioadmin para MinIO).

### Timeout
```
{"status": "error", "code": "TIMEOUT", "error": "context deadline exceeded"}
```
**Solucao**: Aumente o timeout na configuracao ou verifique a conectividade
de rede com o provedor.

### Memory leak
Se observar crescimento de memoria, verifique se esta chamando
`CloudAdapter_FreeString` para toda string retornada pela bridge.
O wrapper Python faz isso automaticamente.

---

## Referencias

- [Go CDK Documentation](https://gocloud.dev/)
- [CGO Documentation](https://pkg.go.dev/cmd/cgo)
- [Python ctypes Documentation](https://docs.python.org/3/library/ctypes.html)
- [Patch Workflow](patch-workflow.md)
- [Provider Matrix](provider-matrix.md)
