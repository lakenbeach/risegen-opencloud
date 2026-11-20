# Diretiva Multicloud RISEGen

| Campo        | Valor                              |
|--------------|-------------------------------------|
| **Data**     | 2026-11-20                         |
| **Status**   | Ratificada                         |
| **Autor**    | Founder (marcvlima)                |
| **Escopo**   | Toda a holding RISEGen             |
| **Repo**     | `risegen-opencloud`                |

---

## 1. Objetivo

Estabelecer a politica multicloud da holding RISEGen, garantindo que nenhum
projeto dependa de um unico fornecedor de nuvem e que toda abstracao de
infraestrutura em nuvem passe pelo repositorio **risegen-opencloud**.

---

## 2. Principios Fundamentais

### 2.1 Independencia de Fornecedor

Nenhum projeto da holding depende de um fornecedor de nuvem especifico.
Todo codigo de aplicacao e agnostico ao provedor: nao importa boto3,
azure-storage, google-cloud-storage ou qualquer SDK de provedor diretamente.
A troca de provedor acontece exclusivamente por configuracao (YAML ou
variaveis de ambiente), sem alteracao de codigo.

### 2.2 Abstracao Centralizada

Toda abstracao de nuvem passa pelo **risegen-opencloud**. Este repositorio
e a unica fonte de adaptadores, bridges e wrappers para servicos de nuvem.
Projetos da holding consomem opencloud como dependencia — nunca reimplementam
integracao com provedor.

### 2.3 Patches Upstream Obrigatorios (Politica No-Fork)

**Nao fazemos forks permanentes do Go CDK (`google/go-cloud`).**

Quando uma funcionalidade necessaria nao e suportada pelo Go CDK:

1. Implementamos a funcionalidade como **patch** no diretorio `patches/`.
2. Submetemos o patch como **Pull Request upstream** para `google/go-cloud`.
3. Enquanto o PR nao e aceito, usamos `replace` no `go.mod` apontando para
   o fork temporario.
4. Assim que o merge upstream acontece, removemos o `replace` e atualizamos
   para a versao oficial.

Workarounds locais permanentes sao **proibidos**. Todo patch nasce com a
intencao de ser merged upstream.

### 2.4 Matriz de Provedores

O status de cada servico por provedor e definido na **Matriz de Provedores**
(`docs/provider-matrix.md`):

| Status          | Significado                                        |
|-----------------|----------------------------------------------------|
| **GA**          | Pronto para producao, testado e documentado        |
| **beta**        | Funcional, em validacao final                      |
| **experimental**| Em desenvolvimento, nao usar em producao           |

### 2.5 Padrao Unico

O `risegen-opencloud` e o padrao para **todos** os projetos da holding que
necessitem de infraestrutura em nuvem. Novos projetos adotam opencloud desde
o dia zero; projetos legados migram gradualmente.

### 2.6 Estrategia Patch-over-Fork

Quando precisamos de customizacoes:

- **Implementamos como patches** que sao submetidos upstream.
- **Nunca** mantemos forks paralelos de longo prazo.
- O diretorio `patches/` segue o ciclo: `pending` → `submitted` → `merged`.
- Patches em `patches/merged` sao mantidos como historico, mas removidos
  do `go.mod` (replace directive removida).

---

## 3. Camadas de Arquitetura

```
Aplicacao (qualquer projeto da holding)
    ↓
Python Wrapper (opencloud-adapter)
    ↓
CGO Bridge (libcloudadapter.so)
    ↓
Go CDK Adapter (google/go-cloud)
    ↓
Provider (AWS | Azure | GCP | Alibaba | Huawei | Local)
```

Detalhes: `docs/architecture.md`

---

## 4. Governanca

- Alteracoes nesta diretiva requerem aprovacao do Founder.
- Novos provedores ou servicos passam pelo processo de onboarding
  (`docs/onboarding.md`) e sao registrados na matriz.
- Violacoes da politica no-fork devem ser corrigidas em ate 1 sprint.

---

## 5. Assinatura

**Ratificada por:** marcvlima (Founder)
**Data:** 2026-11-20
