# asqs-core

**asqs-core** is an open-source CLI that automatically generates **unit tests, end-to-end tests,
per-symbol documentation, and a whole-repo overview document** for **Java, C#, and
JavaScript/TypeScript** repositories in **small incremental updates**. Using a
code-graph + retrieval-augmented-generation (RAG) + LLM pipeline, it supports **first-class integration
with self-hosted, open-source models (Llama, Codestral, Qwen)** to provide absolute privacy/security
and massive cost reduction. Point it at a local folder or a
remote git URL and it indexes the code, finds under-tested symbols, generates tests/docs, and
**validates them by actually compiling and running them** in a sandbox — repairing failures with an
LLM fixer loop.

It is the open foundation of a larger quality system. The enterprise layer (governance/policy engine,
multi-tenant control plane, REST API + web UI, persisted audit reports, CI webhook triggers, per-step
LLM providers, parallel session orchestration) is intentionally **not** part of this core.

> **Status.** The full engine (indexing, retrieval, generation, evaluation/repair), the three
> language indexers, and the `asqs-core run` CLI build as a standalone Go module — `go build ./...`
> is green and `asqs-core run` is wired end to end. Executing a real run requires a Postgres +
> pgvector database, an LLM API key or a local Ollama endpoint running an open-source model (like Llama, Codestral, or Qwen),
> and (for the Docker sandbox) Docker; follow the steps below.

## What it does

One command, one pipeline:

```
bootstrap → index → plan → project intel → generate (every gap) → evaluate the whole project once
            (compile + test [+ e2e] + LLM fixer loop → discard repeatedly-failing tests)
            └─ with --docs: a whole-repo overview document is generated IN PARALLEL
```

- **bootstrap** — detect (and optionally install) the repo's test framework.
- **index** — parse the code into a symbol graph (symbols, typed edges, embedded chunks in pgvector).
- **plan** — rank under-tested symbols into "gaps" and assemble per-gap retrieval context.
- **project intel** — discover and rank your repo's own markdown docs, `SKILL.md` files, OpenAPI specs,
  and SQL schemas, then inject them as a context block into every generation prompt so your written
  conventions take precedence over generic guidance. On by default; cached under `.asqs/`.
- **generate** — an LLM writes the unit/E2E test or doc, grounded in similar code from your repo. With
  `--docs`, per-symbol docs **and** a whole-repo overview document (`docs/documentation.md` — workflows,
  dependencies, file-graph visuals from the index) are produced; the overview is generated **in parallel**
  with the per-symbol test/doc generation.
- **evaluate** — compile and test the **whole project once** (not per gap) in a sandbox; an LLM
  fixer repairs failures over a bounded loop (`fixer.iterations.start`). Tests that can't be
  made to pass are discarded so the rest stay green.

Optionally **ship** the result: after a _stable_ run, commit + push a branch and open/update a PR/MR
on GitHub, GitLab, Bitbucket, or Azure DevOps.

## Prerequisites

- **Go 1.25+** (see the `go` directive in `go.mod`)
- **Docker** (for PostgreSQL + pgvector, and the optional Docker sandbox)
- A local **Ollama** endpoint running open-source models (e.g., Llama, Codestral, Qwen) for maximum privacy and cost reduction, or an external **LLM API key** (OpenAI / Anthropic / Azure OpenAI)
- To **build the indexers**: JDK 21 + Maven, Node 20+, and .NET SDK 10
- For the **local** (non-Docker) sandbox: the matching toolchain on PATH (Maven/Gradle, Node, .NET)

## 1. Start PostgreSQL + pgvector

```bash
docker compose up -d
```

This starts `pgvector/pgvector:pg16` (db/user/password = `asqs`) on port 5432. The schema (symbols,
edges, files, index_runs, and the `chunks` table with a `vector(1536)` column + HNSW index) is
applied automatically on first run.

**Live tests use a separate scratch database, never `asqs`.** The `*_live_test.go` suites write
fixtures, so they only run when `ASQS_TEST_METADATA_URL` points at a database whose *name*
contains `test` or `scratch` — anything else is refused, so an indexed corpus cannot be written to
by accident:

```bash
docker compose exec postgres createdb -U asqs asqs_scratch   # once
ASQS_TEST_METADATA_URL='postgres://asqs:asqs@localhost:5432/asqs_scratch?sslmode=disable' make test-live
```

With the variable unset, `make test-live` skips every live test and exits 0.

## 2. Build the language indexers

```bash
make build-indexers
# Java:  tools/java-indexer/target/java-indexer-0.1.0.jar     (mvn package)
# JS/TS: tools/js-ts-indexer/dist/index.js                    (npm ci && npm run build)
# C#:    tools/csharp-indexer/publish/CSharpIndexer.dll        (dotnet publish -c Release)
```

Point `indexer.*_path` in your config at the produced artifacts (defaults shown in
`config.example.yaml`).

> **Rebuild the C# indexer after upgrading.** It now compiles per `.csproj` group — each project
> together with the sources of the projects it transitively references — instead of one file at a
> time. That is what lets a call from one project into another resolve to a fully-qualified callee
> at all; a stale `publish/CSharpIndexer.dll` silently keeps the old per-file behaviour and simply
> emits fewer `CALLS` edges. Each run now also reports how many invocations it resolved and how many
> it could not, so the gap is visible rather than a silent `continue`.

## 3. Docker sandbox images (only when `general.sandbox.type: docker`)

Pulled on first use; override any of them in config:

| Language          | Default image                         |
| ----------------- | ------------------------------------- |
| Java (Maven)      | `maven:3.9-eclipse-temurin-21`        |
| Java (Gradle)     | `gradle:8.11-jdk21`                   |
| Node / TypeScript | `node:20-bookworm`                    |
| C# / .NET         | `mcr.microsoft.com/dotnet/sdk:10.0`   |
| Playwright (Java) | `mcr.microsoft.com/playwright/java`   |
| Playwright (.NET) | `mcr.microsoft.com/playwright/dotnet` |

With `general.sandbox.type: local`, the toolchains run on the host instead.

## 4. Configure

```bash
cp config.example.yaml config.yaml
# edit: database URL, llm provider + key/model, indexer artifact paths, runner type
```

Project intel is on by default and needs no configuration. The `generation.policy.project_intel` block keeps
`enabled`, `extra_doc_globs` / `extra_skill_globs` and `use_embeddings_rank`; the scan's shape — the
rune budget, the file counts, the relevance floor, the cache location and the fingerprint mode — is
fixed in code, because an operator has no basis on which to prefer 11 doc files to 12 and this
project's discipline is that a default earns its change through measurement. The remaining keys are
settable via `RETRIEVAL_PROJECT_INTEL_*` environment variables. `use_embeddings_rank` embeds the selected docs but does not change what is
injected — asqs-core builds one shared context block per run rather than re-ranking it per gap.

## 5. Run

```bash
# Local folder
asqs-core run --config config.yaml --repo ./path/to/project [--lang java] [--max-gaps 5] [--docs]

# Remote git URL (cloned to a temp dir)
asqs-core run --config config.yaml --repo https://github.com/org/repo.git --lang ts --ship --docs

# Ship to a VCS repo after a stable run (needs a VCS token in config)
asqs-core run --config config.yaml --repo ./project --ship --ship-branch asqs-core --base-branch main

# OR with go run command
go run ./cmd/asqs-core run --config config.yaml --repo ./path/to/project
```

Flags: `--lang` (auto-detected if omitted), `--max-gaps`, `--max-gaps-e2e`, `--docs`,
`--sandbox local|docker`, `--ship`, `--ship-branch`, `--base-branch`, `--dry-run`.

### How many gaps a run generates

`--max-gaps` and `--max-gaps-e2e` resolve in this order — **flag → config → built-in default**:

| Source                                              | `max-gaps` | `max-gaps-e2e` |
| --------------------------------------------------- | ---------- | -------------- |
| `--max-gaps` / `--max-gaps-e2e` on the command line | wins       | wins           |
| `indexer.policy.max_gaps` / `indexer.max_gaps_e2e`         | then this  | then this      |
| built-in default                                    | `10`       | `0` (skip E2E) |

The config keys also accept the `ASQS_INDEXER_MAX_GAPS` and `ASQS_INDEXER_MAX_GAPS_E2E` environment
variables. Every run prints which source won, so a surprising gap count is one line away:

```
asqs-core: max-gaps=20 (config) max-gaps-e2e=5 (config)
```

Two details worth knowing:

- `--max-gaps-e2e 0` is **explicit** and turns E2E off for that run even when the config enables it.
  For `--max-gaps`, `0` is treated as "unset" and falls through to the config (a run capped at zero
  unit gaps would do nothing, and the planner clamps it anyway).
- A negative value — from a flag or the config file — is rejected with an error rather than being
  silently clamped.

`--docs` produces both per-symbol documentation **and** a whole-repo overview document
(`docs/documentation.md`), the latter generated in parallel with test/doc generation. Tune the overview
via `generation.docs.overview.path`, `generation.docs.overview.max_files_per_slice` and
`generation.docs.overview.max_index_runes_per_slice`; the completion-token cap is a constant.

## How it works

- **Indexing** runs language-native parsers (Java AST, C# Roslyn, TypeScript) that emit symbols,
  typed edges, and source chunks; chunks are embedded into pgvector. On subsequent runs, it performs
  **small incremental updates**, only re-indexing changed or added files to keep execution times fast.
- **Planning** uses the symbol graph + RAG to pick under-tested symbols and build a focused
  retrieval context per gap (target + dependencies + similar tests, MMR-diversified). It generates
  tests in **small, reviewable incremental batches** to avoid massive, unreviewable pull requests.
- **Project intel** scans the repo for markdown docs, Cursor-style `SKILL.md` files, OpenAPI specs, and
  SQL schemas, ranks them lexically for relevance, summarizes oversized files with the LLM, and prepends
  the resulting markdown block to every gap's prompt — so generated tests follow _your_ documented
  conventions. The block is built once per run and shared by all gaps (there is no per-gap re-ranking).
  Results are cached in `.asqs/project-intel-cache.json`, keyed by a file/config fingerprint, so repeat
  runs skip the work.
- **Generation** uses a provider-agnostic LLM with embedded per-language skill-packs and contracts.
  This includes first-level support for self-hosted open-source models (such as Llama, Codestral, and Qwen),
  ensuring no source code leaves your local environment for ultimate data security and cost reduction.
- **Evaluation** generates every gap's test first, then compiles + runs the **whole project once**
  in a local or Docker sandbox (not per gap — one compile per fix iteration, not N). The LLM fixer
  repairs failures over a bounded loop (`fixer.iterations.start`); tests that repeatedly fail are
  discarded so the rest stay green. Only artifacts that compile and pass survive. A **quality gate**
  rejects any fixer edit that would degrade the test (e.g. gutting assertions into an empty/tautological
  shell), so repairs never trade correctness for a green compile.
- **Documentation** (`--docs`) produces per-symbol doc comments **and**, in parallel, a whole-repo
  overview document built from the index (batched LLM passes over the source files plus
  file-dependency/visual sections), written to `docs/documentation.md`. Both per-symbol and overview
  docs support **incremental delta updates** so existing files are updated rather than rewritten from scratch.

## Troubleshooting

Most failures are environment/configuration, not bugs. A run touches several external pieces (a
database, an LLM, language toolchains, optionally Docker), so check these first.

### Environment & prerequisites

- **Local sandbox needs the toolchain on PATH.** With `general.sandbox.type: local`, asqs-core shells out to
  the repo's real build tools — so **Java** (JDK + Maven/Gradle), **.NET SDK**, and/or **Node** must be
  installed and on `PATH` for the language you're running. Missing tools surface as "command not found"
  or compile/test steps that never start. If you don't want to install them, use `general.sandbox.type: docker`
  (the SDK lives in the image instead).
- **Build the indexers first.** The advanced indexers are separate artifacts: run `make build-indexers`
  and point `indexer.java.jar_path` (Java JAR), `indexer.jsts.indexer_path` (JS/TS `dist/index.js`),
  and `indexer.csharp.indexer_dll_path` (C# DLL) at them. Symptom: `indexer.jsts.indexer_path is not set …`
  or `… is not set (dotnet publish tools/csharp-indexer)`. Building the indexers needs JDK 21 + Maven,
  Node 20+, and .NET SDK 10 respectively.
- **PostgreSQL + pgvector must be running with the vector extension.** `docker compose up -d` starts
  `pgvector/pgvector:pg16` (the `vector` extension is what stores embeddings). A plain Postgres without
  pgvector fails on the `vector(…)` column / index. Point `general.database.metadata_url` at it. Also keep
  `general.llm.embeddings.dimension` (default `1536`) in sync with your embedding model — a mismatch causes
  insert/query errors against the `vector(1536)` column.
- **An LLM key/provider is required.** Set `general.llm.provider` + `general.llm.model` and either `general.llm.api_key` or
  `general.llm.api_key_from_env` (Ollama needs no key, just `general.llm.base_url`). With no key the generation/fixer/doc
  steps fail or no-op. Embeddings can use a different provider via `general.llm.embeddings.provider`.
- **Docker must be available for the Docker paths.** When `general.sandbox.type: docker` (or `indexer.execution:
docker`, or the test/E2E bootstrap runs in Docker), the Docker daemon must be running and `docker` on
  `PATH`. Symptom: "Cannot connect to the Docker daemon".

### Docker sandbox & offline runs

- **Offline-by-default: cache your dependencies.** The Docker sandbox runs compile/test **offline**
  (`job_network_test: none`) for reproducibility, so dependencies must already be cached. If a build
  can't fetch deps (e.g. Maven `Temporary failure in name resolution`, NuGet `NETSDK1064 … was not
found`), do **one** of: (a) mount your host package cache — `general.sandbox.caches.maven`, `general.sandbox.caches.gradle`,
  `general.sandbox.caches.npm`, `general.sandbox.caches.nuget`; (b) set `general.sandbox.docker.offline_test: false` to download
  live (needs working Docker DNS); or (c) use `general.sandbox.type: local`. For a fully offline machine, mount a
  **pre-populated** host cache (run a build once with network, then point the `cache_*_host` key at it).
- **Custom / version-pinned images must exist locally.** If you override an image to a specific build
  (e.g. `image_playwright_dotnet: asqs-playwright-dotnet:net10`), that image must be present in the local
  Docker (built or pulled) before the run, or the container fails to start with an image-not-found error.
  Build it first (`docker build -t asqs-playwright-dotnet:net10 …`).

### C# specifics (common)

- **Match the SDK image to the project's target framework.** A `net8.0` project built in a `sdk:10.0`
  image fails with `NETSDK1127: The targeting pack Microsoft.NETCore.App is not installed`. Leave
  `general.sandbox.images.dotnet: ""` so asqs-core infers `sdk:{major}` from your `.csproj` TFMs, or set it
  explicitly (e.g. `mcr.microsoft.com/dotnet/sdk:8.0`).
- **`CS0246: 'Xunit' could not be found` → enable the test-framework bootstrap.** Generated C# tests
  must live in a project that references xUnit. Set `bootstrap.test_framework.enabled: true`
  (mode `xunit`/`auto`); asqs-core then creates a dedicated `tests/<Repo>.Tests.csproj` and routes tests
  there instead of into a production project. (Generated tests now default to a `tests/` tree even
  without it, so production still compiles — but they only _run_ when a test project exists.)
- **Style gates and `dotnet format`.** With C#, asqs-core runs `dotnet format` on generated tests by
  default (override via `general.build.format_command`). For the **local** sandbox the `dotnet` CLI must be on
  PATH; for **Docker** the SDK image provides it. If you don't want formatting, set `format_command` to a
  no-op or use a repo without a `dotnet format --verify-no-changes` gate.

### Generation quality, output & shipping

- **Result quality depends heavily on the LLM.** The strength of the generation/fixer model is the
  single biggest factor in test quality and how often the fixer succeeds. Prefer a strong, current model
  for `general.llm.model`; weak models produce low-value tests (which the quality gate rejects) and fewer
  successful repairs. Larger `fixer.iterations.start` gives the fixer more attempts at the cost of
  more LLM calls.
- **"could not detect a supported language" / "no source files found".** Language is auto-detected from
  the file scan; an empty or wrong detection usually means everything was filtered by
  `indexer.policy.skip_path_prefixes`, or the repo path is wrong. Pass `--lang` to force it.
- **`--docs` writes into your source tree.** Per-symbol docs are inserted above declarations in source
  files, and the overview is written to `generation.docs.overview.path` (default `docs/documentation.md`).
  Point asqs-core at a clean working tree (or a branch) so you can review the diff.
- **Shipping (`--ship`) requirements.** Ship only runs on a **stable** result (`run not stable — not
shipping` otherwise), and needs a VCS token (`general.git.token`) and a recognizable origin (HTTPS or
  SSH — asqs-core rewrites SSH→HTTPS for the push). If the PR step can't resolve owner/repo from the
  origin URL, set `vcs.<provider>.default_owner` / `default_repo`.
- **Exit code 1 on a green-looking run.** The CLI exits non-zero when generated tests didn't end up in a
  passing whole-project build (`!Stable()`). Check the summary line and the per-symbol `discarded` /
  `unstable` statuses; the `discard` mechanism drops repeatedly-failing tests so the rest stay green.

## Limitations

asqs-core is a **one-shot CLI**. It runs the pipeline once and exits; there is no long-running
service, no scheduler, and no CI webhook listener — drive it from your own cron or CI job instead.

Not included (these live in the commercial layer):

- **Control plane** — no REST API, no web UI, no multi-tenant tenants/projects.
- **Serve mode** — no cron scheduler, no automatic reruns, no PR-webhook triggers or PR gating.
- **Persisted audit** — steps are logged to stderr only; nothing is written to an audit log table and
  there is no audit reporting/export CLI.
- **Governance/policy engine** — not present, and no longer parses: the `runner.policy:` block went
  with the v1 schema. No coverage gate, no mutation-testing gate.
- **Parallelism** — gaps are generated sequentially. `general.llm.max_concurrent` bounds concurrent LLM calls
  across the run; there is no per-gap worker pool. (The whole-repo overview is the one exception: it
  runs in parallel with generation.)
- **Pre-generation seams** — no controlled source-seam edits or C# project-reference fixes before
  generation. (The evaluator's in-loop C# project-reference autofix _is_ included.)
- **Per-step LLM providers** — one `general.llm.provider` + `general.llm.model` drives generation, docs, fixing, and
  embeddings. Per-step overrides are not wired.
- **Mono-repo scoping** — `general.build.workspace.path` and related keys are ignored; asqs-core indexes
  from the repo root and picks a single primary language by file count.
- **Retrieval tuning** — the `retrieval:` config block is not read. Profiles, per-profile budgets,
  MMR lambda, context compaction, and retrieval **abstention** all run at their built-in defaults
  (abstention is off, so low-confidence gaps are still generated).
- **Post-generate static micro-gate** — a post-generate static gate is ignored; generated files
  go straight to the sandbox after formatting.
- **Private registries** — no Maven `settings.xml` / npm `.npmrc` / NuGet credential injection into the
  Docker sandbox.
- **GitHub Copilot SDK** — the `copilot:` config block parses but no code consumes it.

### Config keys that are CLI-driven here

Shipping is enabled by `--ship`, not by `vcs.<provider>.ship.enabled`. The rest of that block _is_
read: `branch` and `base_branch` supply the defaults for `--ship-branch` / `--base-branch`, and
`draft_pr` opens the PR as a draft (GitHub; ignored for Azure DevOps).

## License

[Apache-2.0](./LICENSE).
