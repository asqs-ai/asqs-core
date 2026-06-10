# Provenance

`asqs-core` is an open-source extraction of the fundamental test/doc-generation pipeline from the
proprietary `asqs-go` project. This file records where each package came from so the copied engine
can be re-synced from upstream with clean diffs. Bespoke code (the open-core orchestration) lives in
`internal/pipeline`, `internal/config` adaptations, and `cmd/asqs-core`.

| asqs-core path | origin in asqs-go | notes |
| --- | --- | --- |
| `internal/storage/metadata` | `internal/storage/metadata` | copied; schema to be trimmed to symbols/edges/files/index_runs |
| `internal/storage/embeddings` | `internal/storage/embeddings` | copied verbatim (pgvector chunks + HNSW) |
| `internal/intelligence/model` | `internal/intelligence/model` | copied verbatim (ChatCompleter/Embedder interfaces) |
| `internal/intelligence/indexer` | `internal/intelligence/indexer` | copied verbatim |
| `internal/intelligence/retrieval` | `internal/intelligence/retrieval` | copied; audit/ireval extras omitted |
| `internal/generator/*.go` | `internal/orchestrator/{llm_generator,llm_generator_twophase,doc_generator,generate_schema,skillpacks,doc_content}.go` | `package orchestrator` → `package generator`; `ExtendExistingTestContextPrefix`/`repoRelPathsEqual` re-homed in `extend_helpers.go` |
| `internal/generator/contract` | `internal/generator/contract` | copied verbatim |
| `internal/generator/skillpacks` | `internal/orchestrator/skillpacks` | embedded generation skill-packs |
| `internal/evaluator` | `internal/evaluator` | copied; `llmfix/copilot_fixer.go` omitted |
| `internal/runner` | `internal/runner` | copied (azure/private-registry helpers to be dropped in trim) |
| `internal/llm` | `internal/llm` | copied; `copilot/` omitted |
| `internal/testbootstrap` | `internal/testbootstrap` | copied; `orchestrator.Auditor` → local `testbootstrap.Auditor` |
| `internal/config` | `internal/config` | copied whole to reach a building engine fast; **to be trimmed** to the open-core surface |
| `internal/session/policyspec` | `internal/session/policyspec` | transitive dep of config; removed once config is trimmed |
| `internal/vcs` (+ providers) | `internal/vcs` | copied for ship; webhook receivers (`gates.go`, `handler.go`) omitted |
| `internal/repo` | `internal/repo` | copied (go-git clone/open/add/commit/push + TokenAuth) |
| `internal/{dotnetproj,layout,workspace}` | same | copied leaves |
| `tools/{java-indexer,js-ts-indexer,csharp-indexer}` | `tools/*` | copied (Go wrappers + source; built artifacts excluded) |

**Excluded entirely** (enterprise / out of scope): `internal/{session/*,workflow,api,audit,postgenerate,notification,intelligence/projectintel}`, `orchestrator` (except the generator engine files above), `copilot`, `vcs/{gates,handler}`, `cmd/{apiserver,qualitybot}`.

All copied Go files had imports rewritten `asqs-go/… → github.com/asqs/asqs-core/…`.
