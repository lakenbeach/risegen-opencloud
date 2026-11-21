# Patch pending: Huawei OBS bucket versioning + lifecycle configuration

> Ciclo: **pending** → submitted → merged (ver `patches/README.md`).

## Gap

O Go CDK (`gocloud.dev/blob`) não suporta nativamente a Huawei OBS. O wrapper
custom (`go/pkg/providers/huawei/obs.go`) implementa um `blob.Bucket`-like via
REST OBS com assinatura compatível com AWS Signature V4 — mas cobria apenas CRUD
de objetos. Operações de **sub-recurso** do bucket (`?versioning` e
`?lifecycle`) não existiam, e a assinatura V4 não incluía a query string
(canonical URI/query) — o que invalida qualquer chamada com sub-recursos.

## Provider afetado

- Provider: `huawei` (package `github.com/lakenbeach/risegen-opencloud/go/pkg/providers/huawei`)
- Arquivo: `go/pkg/providers/huawei/obs.go`
- Contrato: `gocloud.dev/blob` `blob.Bucket` (`As`, `Close`, `NewWriter`, ...)

## Mudança

1. `createSignedRequestWithQuery` — variante de `createSignedRequest` que aceita
   query `url.Values` de sub-recurso, monta a query canônica ordenada
   (`canonicalQuery`), e a inclui tanto na URL quanto no canonical request
   assinado.
2. `generateSignatureV4` — agora recebe a query canônica e a interpola no
   canonical request (Leaf 3 do Signature V4 OBS).
3. Helpers de canonicalização `canonicalQuery`, `canonicalURI`, `rfc3986Encode`
   (com preservação de `/` e de `%XX` existentes).
4. Métodos novos no `OBSBucket`:
   - `EnableBucketVersioning(ctx, status)` / `GetBucketVersioning(ctx)` (`?versioning`)
   - `PutLifecycleConfiguration(ctx, config)` / `GetLifecycleConfiguration(ctx)`
     — `?lifecycle`; GET 404 → `(nil, nil)` (bucket sem configuração).
   - Constantes `VersioningStatusEnabled/Suspended`, `LifecycleRuleStatusEnabled/Disabled`
     e structs XML (`VersioningConfiguration`, `LifecycleConfiguration`, ...).

A assinatura assina o path real enviado (`req.URL.Path`) e o host real
(`req.URL.Host`), cobrindo tanto URL virtual-hosted quanto path-style.

## Teste pareado

- `go/pkg/providers/huawei/obs_test.go`:
  - `TestOBSVersioningRoundTrip` / `TestOBSLifecycleRoundTrip` — `httptest.Server`
    provando que `?versioning=` / `?lifecycle=` vão na query assinada
    (`verifySignedRequest` re-computa a assinatura do lado do servidor).
  - `TestCanonicalQuery`, `TestCanonicalURI`, `TestOBSVersioningValidation`,
    `TestOBSLifecycleValidation`, `TestOBSGetLifecycleNotConfigured`.
- `backend/test_custom_patch.py` — verificação real do patch (fonte Go + compila
  e roda `go test` no pacote huawei), sem mocks.

## Próximo passo

Submeter PR upstream via `scripts/submit-patch.sh` (T069) e, ao merge, mover
para `patches/merged/` e remover dependência local.