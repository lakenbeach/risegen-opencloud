# Matriz de Provedores

Status de cada servico por provedor de nuvem suportado pelo OpenCloud.

## Legenda

| Status          | Significado                                        |
|-----------------|----------------------------------------------------|
| **GA**          | Pronto para producao, testado e documentado        |
| **beta**        | Funcional, em validacao final, pode ter mudancas   |
| **experimental**| Em desenvolvimento, nao usar em producao           |
| **—**           | Nao suportado / nao planejado                      |

---

## Storage (Blob)

| Provedor  | Status         | Implementacao   | URL Scheme       | Notas                           |
|-----------|----------------|-----------------|------------------|---------------------------------|
| AWS S3    | **GA**         | Nativo Go CDK   | `s3://`          | Suporte completo                |
| Azure     | **beta**       | Nativo Go CDK   | `azblob://`      | Blob storage                    |
| GCP       | **beta**       | Nativo Go CDK   | `gs://`          | Google Cloud Storage            |
| Alibaba   | **beta**       | Custom adapter  | `alioss://`      | Object Storage Service          |
| Huawei    | **experimental**| Custom adapter | `huaweios://`    | Object Storage Service          |
| Local     | **GA**         | Nativo Go CDK   | `s3://` (MinIO)  | MinIO (S3-compatible)           |

---

## Database (Docstore)

| Provedor  | Status         | Implementacao   | URL Scheme              | Notas                    |
|-----------|----------------|-----------------|-------------------------|--------------------------|
| AWS       | **GA**         | Nativo Go CDK   | `dynamodb://`           | DynamoDB                 |
| Azure     | **beta**       | Nativo Go CDK   | `azurecosmosdb://`      | CosmosDB                 |
| GCP       | **beta**       | Nativo Go CDK   | `firestore://`          | Firestore                |
| Alibaba   | **beta**       | Custom adapter  | `tablestore://`         | TableStore               |
| Huawei    | **experimental**| Custom adapter | `gaussdb://`            | GaussDB                  |
| Local     | **GA**         | Nativo Go CDK   | `dynamodb://` (Alternator) | ScyllaDB Alternator (DynamoDB-compatible) |

### Schema de Chave

O docstore requer campo chave. O adapter usa chave composta `(pk, sk)`:
- `pk` (partition key): identifica a entidade/particao
- `sk` (sort key): identifica o item dentro da particao

Override via `provider_config`: `partition_key` e `sort_key`.

---

## Queue (SQS-like)

| Provedor  | Status         | Implementacao   | URL Scheme       | Notas                           |
|-----------|----------------|-----------------|------------------|---------------------------------|
| AWS SQS   | **GA**         | Nativo Go CDK   | `sqs://`         | Simple Queue Service            |
| Azure     | **beta**       | Nativo Go CDK   | `azsbsb://`      | Azure Service Bus               |
| GCP       | **beta**       | Nativo Go CDK   | `gcppubsub://`   | Cloud Pub/Sub (pull mode)      |
| Alibaba   | **beta**       | Custom adapter  | `alimns://`      | Message Notification Service    |
| Huawei    | **experimental**| Custom adapter | `huaweismn://`   | Simple Message Notification     |
| Local     | **GA**         | Nativo Go CDK   | `nats://`        | NATS                            |

---

## PubSub

| Provedor  | Status         | Implementacao   | URL Scheme       | Notas                           |
|-----------|----------------|-----------------|------------------|---------------------------------|
| AWS SNS   | **GA**         | Nativo Go CDK   | `awssns://`      | Simple Notification Service     |
| Azure     | **beta**       | Nativo Go CDK   | `azsbsb://`      | Azure Service Bus Topics        |
| GCP       | **beta**       | Nativo Go CDK   | `gcppubsub://`   | Cloud Pub/Sub                   |
| Alibaba   | **beta**       | Custom adapter  | `alimns://`      | Message Notification Service    |
| Huawei    | **experimental**| Custom adapter | `huaweismn://`   | Simple Message Notification     |
| Local     | **GA**         | Nativo Go CDK   | `nats://`        | NATS (publish/subscribe)        |

---

## Secrets

| Provedor  | Status         | Implementacao   | URL Scheme       | Notas                           |
|-----------|----------------|-----------------|------------------|---------------------------------|
| AWS       | **GA**         | Nativo Go CDK   | `awssecretsmanager://` | Secrets Manager           |
| Azure     | **beta**       | Nativo Go CDK   | `azurekeyvault://`     | Key Vault              |
| GCP       | **GA**         | Nativo Go CDK   | `gcpsecretmanager://`  | Secret Manager         |
| Alibaba   | **experimental**| Custom adapter | —                | KMS Secrets Manager           |
| Huawei    | **experimental**| Custom adapter | —                | DEW (Data Encryption Workshop) |
| Local     | **GA**         | Variaveis de ambiente / Redis | `redis://` ou `env://` | Para desenvolvimento local |

---

## Matriz Resumo

| Servico               | AWS    | Azure            | GCP              | Alibaba          | Huawei              | Local              |
|-----------------------|--------|------------------|------------------|------------------|---------------------|--------------------|
| Storage (Blob)        | GA     | beta             | beta             | beta             | experimental        | GA (MinIO)         |
| Database (Docstore)   | GA     | beta (CosmosDB)  | beta (Firestore) | beta (TableStore)| experimental (GaussDB)| GA (ScyllaDB)    |
| Queue (SQS-like)      | GA     | beta             | beta             | beta             | experimental        | GA (NATS)          |
| PubSub                | GA     | beta             | beta             | beta             | experimental        | GA (NATS)          |
| Secrets               | GA     | beta             | GA               | experimental     | experimental        | GA (env vars)      |

---

## Notas de Compatibilidade

### Local Stack

O stack local de desenvolvimento (`docker/docker-compose.yml`) usa:

- **MinIO** para storage (S3-compatible, porta 19000)
- **ScyllaDB Alternator** para database (DynamoDB-compatible, porta 18000)
- **NATS** para pub/sub e filas (porta 4222)
- **Redis** para secrets (porta 16379)

Isso garante que desenvolvimento e testes locais usem as mesmas interfaces
que producao, apenas com provedores diferentes.

### Promocao de Status

Um servico e promovido de `experimental` para `beta` quando:
- Testes de integracao passam consistentemente
- Documentacao esta completa
- Pelo menos um projeto da holding usa em staging

Um servico e promovido de `beta` para `GA` quando:
- Roda em producao por pelo menos 30 dias sem incidentes criticos
- Performance benchmarks estao dentro do budget
- Runbook de operacao esta documentado
