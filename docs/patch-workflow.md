# Patch Workflow — Politica No-Fork

## Visao Geral

Este documento descreve o workflow para contribuicao de patches ao repositorio
oficial do Go CDK (`google/go-cloud`) quando funcionalidades necessarias nao sao
suportadas nativamente. O processo garante que implementacoes customizadas possam
ser usadas imediatamente enquanto trabalhamos para aceitacao upstream.

**Politica fundamental: Nao fazemos forks permanentes.** Todo patch nasce com a
intencao de ser merged upstream.

---

## Quando Criar um Patch

Crie um patch para o Go CDK quando:

1. **Go CDK nao suporta um provedor de nuvem necessario**
   - Exemplo: Huawei Cloud OBS com funcionalidades avancadas
   - Exemplo: Alibaba Cloud TableStore com operacoes especificas

2. **Provedor existente tem bugs que afetam o caso de uso**
   - Funcionalidade critica quebrada na release oficial
   - Workaround requer alteracoes de codigo na implementacao do provedor

3. **Funcionalidades avancadas nao disponiveis no Go CDK core**
   - Versionamento de bucket para storage
   - Gerenciamento de lifecycle policies
   - Configuracoes avancadas de encryption
   - Retry logic customizado ou connection pooling

4. **Otimizacao de performance especifica para o workload**
   - Padroes de reuso de conexao
   - Otimizacoes de operacoes em batch
   - Melhorias de uso de memoria

**Arvore de Decisao**:
```
Funcionalidade esta no roadmap do Go CDK?
├─ Sim → Aguardar se timeline aceitavel (< 2 semanas)
└─ Nao → Podemos implementar como adapter customizado?
    ├─ Sim → Implementar adapter customizado (patch nao necessario)
    └─ Nao → Criar patch e submeter upstream
```

---

## Ciclo de Vida do Patch

Os patches seguem um ciclo de vida bem definido, organizado em tres diretorios:

```
patches/
├── pending/       # Patch implementado, PR ainda nao submetido
├── submitted/     # PR submetido upstream, aguardando review/merge
└── merged/        # PR aceito e merged, aguardando release oficial
```

### Transicoes

```
pending/ → submitted/ → merged/ → (removido do go.mod)
```

1. **pending**: Patch implementado e testado localmente. Ainda nao submetido
   como PR upstream.
2. **submitted**: PR criado contra `google/go-cloud`. O `go.mod` contem
   `replace` apontando para o fork temporario.
3. **merged**: PR aceito upstream. Aguardando proxima release do Go CDK.
   O `replace` no `go.mod` ainda aponta para o fork.
4. **removido**: Release oficial contendo o patch e publicada. Remove-se o
   `replace` do `go.mod` e atualiza para a versao oficial.

---

## Processo de Fork

### Passo 1: Fork do Repositorio Oficial

1. Navegue para https://github.com/google/go-cloud
2. Clique em "Fork"
3. Selecione sua conta GitHub
4. Aguarde o fork completar (~30 segundos)

### Passo 2: Clone do Fork

```bash
git clone https://github.com/SEU_USERNAME/go-cloud.git
cd go-cloud

# Adicione upstream remote
git remote add upstream https://github.com/google/go-cloud.git

# Verifique remotes
git remote -v
```

### Passo 3: Crie Branch de Feature

```bash
# Para novas funcionalidades
git checkout -b feat/add-huawei-obs-versioning

# Para bug fixes
git checkout -b fix/aws-s3-retry-logic

# Para novos provedores
git checkout -b feat/add-alibaba-tablestore-provider
```

**Convencao de branches**:
- Prefixo: `feat/`, `fix/`, ou `docs/`
- Nomes descritivos com hifens
- Uma funcionalidade por branch (mudancas atomicas)
- Branch a partir de `main`

---

## Diretrizes de Implementacao

### Siga os Padroes do Go CDK

Estude implementacoes de provedores existentes antes de codificar. Siga os
padroes de error handling, logging estruturado e documentacao godoc.

### Qualidade de Codigo

1. **Error Handling**: Wrap errors com contexto usando `%w`
2. **Logging**: Use logging estruturado (`log.Printf`), inclua request IDs
3. **Documentacao**: godoc comments para todas as funcoes exportadas
4. **Testes**: Unit tests para todas as novas funcoes, cobertura >80%

### Testes Obrigatorios

```bash
# Unit tests
go test -v -coverprofile=coverage.out

# Ver cobertura
go tool cover -html=coverage.out

# Suite completa antes de submeter PR
go test ./... -race -count=1
```

---

## Version Pinning

### Atualize go.mod com Replace Directive

Enquanto aguarda merge upstream, aponte para a versao forked:

```go
module meuprojeto/app

go 1.23

require (
    gocloud.dev v0.49.0
)

// Replace com fork enquanto aguarda merge upstream
// Patch: feat/add-huawei-obs-versioning
// PR: https://github.com/google/go-cloud/pull/XXXX
replace gocloud.dev => github.com/SEU_USERNAME/go-cloud v0.49.1-huawei-versioning
```

### Estrategia de Pinning

1. **Formato de versao**: Semver com sufixo descritivo
   - `v0.49.1-huawei-versioning` (feature)
   - `v0.49.1-fix-s3-retry` (bug fix)

2. **Branch naming**: Combine nome da branch com sufixo da versao
   - Branch: `feat/huawei-obs-versioning`
   - Versao: `v0.49.1-huawei-versioning`

3. **Documentacao**: Comente o replace directive com:
   - Razao do patch
   - Link para PR upstream
   - Timeline esperada de merge

### Rastreamento de Patches Ativos

Mantenha registro em `patches/PATCHES.md`:

```markdown
## Patches Ativos

### Huawei OBS Versioning Support
- **Fork**: https://github.com/SEU_USERNAME/go-cloud/tree/feat/huawei-obs-versioning
- **PR**: https://github.com/google/go-cloud/pull/XXXX
- **Status**: Under review
- **Criado**: 2026-11-20
- **Merge esperado**: 2026-12-01
- **Versao local**: `v0.49.1-huawei-versioning`
```

---

## Submissao de PR

### Preparando o PR

1. **Sync com upstream main**:
```bash
git fetch upstream
git rebase upstream/main
git push origin feat/minha-feature --force-with-lease
```

2. **Squash commits** (se multiplos commits pequenos):
```bash
git rebase -i HEAD~3
```

3. **Push para o fork**:
```bash
git push origin feat/minha-feature
```

### Criando Pull Request

Use o script automatizado ou o `gh` CLI diretamente:

```bash
# Via script (recomendado)
./scripts/submit-patch.sh \
  --branch feat/minha-feature \
  --title "feat: add Huawei OBS versioning support"

# Ou diretamente via gh CLI
gh pr create \
  --repo google/go-cloud \
  --title "feat: descricao da feature" \
  --body "..." \
  --base main \
  --head SEU_USERNAME:feat/minha-feature
```

### Apos Submissao

1. **Monitore o PR**: verifique comentarios de reviewers diariamente
2. **Responda feedback**: dentro de 48 horas
3. **Quando aprovado e merged**:
   - Aguarde proxima release do Go CDK (tipicamente 2-4 semanas)
   - Monitore release notes para sua feature
   - Planeje migracao para versao oficial

---

## Retornando para Versao Oficial

### Passo 1: Aguarde Release Oficial

```bash
# Monitore novas tags
git fetch upstream --tags
git tag -l | grep "^v0\." | tail -5

# Ou verifique: https://github.com/google/go-cloud/releases
```

### Passo 2: Atualize go.mod

Remova o replace directive e atualize a versao:

```go
module meuprojeto/app

go 1.23

require (
    // Atualize para versao oficial contendo o patch
    gocloud.dev v0.50.0  // Era: v0.49.0
)

// REMOVA o replace directive agora que o patch foi merged upstream
```

### Passo 3: Remova Codigo de Patch Local

```bash
# Identifique arquivos com codigo temporario
grep -r "TEMPORARY\|PATCH\|WORKAROUND" pkg/

# Remova adapters temporarios se nao mais necessarios
```

### Passo 4: Rode Suite Completa de Testes

```bash
go mod tidy
go test ./... -v
go test ./... -tags=integration
```

### Passo 5: Mova o Patch para `merged/`

```bash
mv patches/submitted/meu-patch/ patches/merged/meu-patch/
```

### Passo 6: Commit e Deploy

```bash
git add go.mod go.sum patches/
git commit -m "chore: update to go-cdk v0.50.0 with native feature support

Removes replace directive now that feature is merged upstream.
See: https://github.com/google/go-cloud/pull/XXXX"
```

---

## Troubleshooting

### Problemas Comuns

**Replace directive causa erros de resolucao de modulos**
```
Solucao: go mod tidy
```

**PR upstream stale por >2 semanas**
```
Solucao: Rebase no latest main e push novamente
git rebase upstream/main && git push --force-with-lease
```

**Conflito de merge apos mudancas upstream**
```
Solucao: Resolva conflitos manualmente, teste, e push
```

**Maintainers nao respondem**
```
Solucao: Abra issue referenciando o PR, ou pergunte em
https://gocloud.dev/community
```

---

## Referencia Rapida

### Comandos Essenciais

```bash
# Setup do fork
git clone https://github.com/SEU_USERNAME/go-cloud.git
cd go-cloud
git remote add upstream https://github.com/google/go-cloud.git

# Criar branch
git checkout -b feat/minha-feature

# Testar
go test ./...

# Submeter PR
./scripts/submit-patch.sh --branch feat/minha-feature --title "feat: ..."

# Atualizar do upstream
git fetch upstream && git rebase upstream/main

# Pin version
# Edite go.mod: replace gocloud.dev => github.com/SEU_USERNAME/go-cloud vX.Y.Z-patch

# Remover pin apos merge
# Remova replace directive do go.mod
go mod tidy
```

### Links Uteis

- Go CDK Repository: https://github.com/google/go-cloud
- Go CDK Documentation: https://gocloud.dev/
- GitHub CLI: https://cli.github.com/
- Go Modules Reference: https://go.dev/ref/mod
