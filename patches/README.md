# Patches — Estratégia Patch-over-Fork

Este diretório implementa a **política NO-FORK** da diretiva RISEGen OpenCloud:
nenhum projeto da holding mantém fork permanente do Go CDK (ou de qualquer
biblioteca upstream). Toda customização nasce como patch com intenção de ser
merged no upstream `google/go-cloud`.

## Ciclo de vida

```
patches/
├── pending/       # Patch implementado, PR ainda NÃO submetido upstream
├── submitted/     # PR submetido ao google/go-cloud, aguardando review/merge
└── merged/        # PR aceito upstream — remover a dependência do patch local
```

## Regras

1. **Todo patch começa em `pending/`** com um `README.md` próprio descrevendo
   o gap, o provider afetado, e o teste pareado.
2. **Antes de usar um patch em qualquer projeto**, submeta o PR upstream via
   `scripts/submit-patch.sh` e mova para `submitted/` com o link do PR.
3. **Quando merged upstream**, mova para `merged/` e atualize o `go.mod` do
   projeto consumidor para a versão oficial do Go CDK que contém a mudança.
4. **Nunca** commite um fork como dependência permanente. Se o upstream rejeitar
   o patch, reavaliar por consenso (docs/patch-workflow.md, árvore de decisão).