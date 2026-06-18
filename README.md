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

It is the open foundation of a larger quality system. The enterprise layer (project-intelligence,
governance/policy, multi-tenant control plane, audit reports, web UI, CI webhook triggers, per-step
LLM providers) is intentionally **not** part of this core.

> **Status.** The full engine (indexing, retrieval, generation, evaluation/repair), the three
> language indexers, and the `asqs-core run` CLI build as a standalone Go module — `go build ./...`
> is green and `asqs-core run` is wired end to end. Executing a real run requires a Postgres +
> pgvector database, an LLM API key or a local Ollama endpoint running an open-source model (like Llama, Codestral, or Qwen),
> and (for the Docker sandbox) Docker; follow the steps below.

## What it does

One command, one pipeline:

```
bootstrap → index → plan → generate (every gap) → evaluate the whole project once
            (compile + test [+ e2e] + LLM fixer loop → discard repeatedly-failing tests)
            └─ with --docs: a whole-repo overview document is generated IN PARALLEL
```

- **bootstrap** — detect (and optionally install) the repo's test framework.
- **index** — parse the code into a symbol graph (symbols, typed edges, embedded chunks in pgvector).
- **plan** — rank under-tested symbols into "gaps" and assemble per-gap retrieval context.
- **generate** — an LLM writes the unit/E2E test or doc, grounded in similar code from your repo. With
  `--docs`, per-symbol docs **and** a whole-repo overview document (`docs/documentation.md` — workflows,
  dependencies, file-graph visuals from the index) are produced; the overview is generated **in parallel**
  with the per-symbol test/doc generation.
- **evaluate** — compile and test the **whole project once** (not per gap) in a sandbox; an LLM
  fixer repairs failures over a bounded loop (`runner.start_max_iteration`). Tests that can't be
  made to pass are discarded so the rest stay green.

Optionally **ship** the result: after a _stable_ run, commit + push a branch and open/update a PR/MR
on GitHub, GitLab, Bitbucket, or Azure DevOps.

## Prerequisites

- **Go 1.24+**
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

## 2. Build the language indexers

```bash
make build-indexers
# Java:  tools/java-indexer/target/java-indexer-0.1.0.jar     (mvn package)
# JS/TS: tools/js-ts-indexer/dist/index.js                    (npm ci && npm run build)
# C#:    tools/csharp-indexer/publish/CSharpIndexer.dll        (dotnet publish -c Release)
```

Point `indexer.*_path` in your config at the produced artifacts (defaults shown in
`config.example.yaml`).

## 3. Docker sandbox images (only when `runner.type: docker`)

Pulled on first use; override any of them in config:

| Language          | Default image                         |
| ----------------- | ------------------------------------- |
| Java (Maven)      | `maven:3.9-eclipse-temurin-21`        |
| Java (Gradle)     | `gradle:8.11-jdk21`                   |
| Node / TypeScript | `node:20-bookworm`                    |
| C# / .NET         | `mcr.microsoft.com/dotnet/sdk:10.0`   |
| Playwright (Java) | `mcr.microsoft.com/playwright/java`   |
| Playwright (.NET) | `mcr.microsoft.com/playwright/dotnet` |

With `runner.type: local`, the toolchains run on the host instead.

## 4. Configure

```bash
cp config.example.yaml config.yaml
# edit: database URL, llm provider + key/model, indexer artifact paths, runner type
```

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

`--docs` produces both per-symbol documentation **and** a whole-repo overview document
(`docs/documentation.md`), the latter generated in parallel with test/doc generation. Tune the overview
via `indexer.overview_doc_path`, `indexer.overview_max_files_per_slice`,
`indexer.overview_max_index_runes_per_slice`, and `indexer.overview_max_completion_tokens`.

## How it works

- **Indexing** runs language-native parsers (Java AST, C# Roslyn, TypeScript) that emit symbols,
  typed edges, and source chunks; chunks are embedded into pgvector. On subsequent runs, it performs
  **small incremental updates**, only re-indexing changed or added files to keep execution times fast.
- **Planning** uses the symbol graph + RAG to pick under-tested symbols and build a focused
  retrieval context per gap (target + dependencies + similar tests, MMR-diversified). It generates
  tests in **small, reviewable incremental batches** to avoid massive, unreviewable pull requests.
- **Generation** uses a provider-agnostic LLM with embedded per-language skill-packs and contracts.
  This includes first-level support for self-hosted open-source models (such as Llama, Codestral, and Qwen),
  ensuring no source code leaves your local environment for ultimate data security and cost reduction.
- **Evaluation** generates every gap's test first, then compiles + runs the **whole project once**
  in a local or Docker sandbox (not per gap — one compile per fix iteration, not N). The LLM fixer
  repairs failures over a bounded loop (`runner.start_max_iteration`); tests that repeatedly fail are
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

- **Local sandbox needs the toolchain on PATH.** With `runner.type: local`, asqs-core shells out to
  the repo's real build tools — so **Java** (JDK + Maven/Gradle), **.NET SDK**, and/or **Node** must be
  installed and on `PATH` for the language you're running. Missing tools surface as "command not found"
  or compile/test steps that never start. If you don't want to install them, use `runner.type: docker`
  (the SDK lives in the image instead).
- **Build the indexers first.** The advanced indexers are separate artifacts: run `make build-indexers`
  and point `indexer.advanced_jar_path` (Java JAR), `indexer.jst_indexer_path` (JS/TS `dist/index.js`),
  and `indexer.csharp_indexer_dll_path` (C# DLL) at them. Symptom: `indexer.jst_indexer_path is not set …`
  or `… is not set (dotnet publish tools/csharp-indexer)`. Building the indexers needs JDK 21 + Maven,
  Node 20+, and .NET SDK 10 respectively.
- **PostgreSQL + pgvector must be running with the vector extension.** `docker compose up -d` starts
  `pgvector/pgvector:pg16` (the `vector` extension is what stores embeddings). A plain Postgres without
  pgvector fails on the `vector(…)` column / index. Point `database.metadata_url` at it. Also keep
  `database.embeddings_dimension` (default `1536`) in sync with your embedding model — a mismatch causes
  insert/query errors against the `vector(1536)` column.
- **An LLM key/provider is required.** Set `llm.provider` + `llm.model` and either `llm.api_key` or
  `llm.api_key_from_env` (Ollama needs no key, just `llm.base_url`). With no key the generation/fixer/doc
  steps fail or no-op. Embeddings can use a different provider via `llm.embedding_provider`.
- **Docker must be available for the Docker paths.** When `runner.type: docker` (or `indexer.execution:
docker`, or the test/E2E bootstrap runs in Docker), the Docker daemon must be running and `docker` on
  `PATH`. Symptom: "Cannot connect to the Docker daemon".

### Docker sandbox & offline runs

- **Offline-by-default: cache your dependencies.** The Docker sandbox runs compile/test **offline**
  (`job_network_test: none`) for reproducibility, so dependencies must already be cached. If a build
  can't fetch deps (e.g. Maven `Temporary failure in name resolution`, NuGet `NETSDK1064 … was not
found`), do **one** of: (a) mount your host package cache — `cache_maven_host`, `cache_gradle_host`,
  `cache_npm_host`, `cache_nuget_host`; (b) set `runner.docker_disable_offline_test: true` to download
  live (needs working Docker DNS); or (c) use `runner.type: local`. For a fully offline machine, mount a
  **pre-populated** host cache (run a build once with network, then point the `cache_*_host` key at it).
- **Custom / version-pinned images must exist locally.** If you override an image to a specific build
  (e.g. `image_playwright_dotnet: asqs-playwright-dotnet:net10`), that image must be present in the local
  Docker (built or pulled) before the run, or the container fails to start with an image-not-found error.
  Build it first (`docker build -t asqs-playwright-dotnet:net10 …`).

### C# specifics (common)

- **Match the SDK image to the project's target framework.** A `net8.0` project built in a `sdk:10.0`
  image fails with `NETSDK1127: The targeting pack Microsoft.NETCore.App is not installed`. Leave
  `runner.image_dotnet: ""` so asqs-core infers `sdk:{major}` from your `.csproj` TFMs, or set it
  explicitly (e.g. `mcr.microsoft.com/dotnet/sdk:8.0`).
- **`CS0246: 'Xunit' could not be found` → enable the test-framework bootstrap.** Generated C# tests
  must live in a project that references xUnit. Set `runner.test_framework_bootstrap.enabled: true`
  (mode `xunit`/`auto`); asqs-core then creates a dedicated `tests/<Repo>.Tests.csproj` and routes tests
  there instead of into a production project. (Generated tests now default to a `tests/` tree even
  without it, so production still compiles — but they only _run_ when a test project exists.)
- **Style gates and `dotnet format`.** With C#, asqs-core runs `dotnet format` on generated tests by
  default (override via `runner.format_command`). For the **local** sandbox the `dotnet` CLI must be on
  PATH; for **Docker** the SDK image provides it. If you don't want formatting, set `format_command` to a
  no-op or use a repo without a `dotnet format --verify-no-changes` gate.

### Generation quality, output & shipping

- **Result quality depends heavily on the LLM.** The strength of the generation/fixer model is the
  single biggest factor in test quality and how often the fixer succeeds. Prefer a strong, current model
  for `llm.model`; weak models produce low-value tests (which the quality gate rejects) and fewer
  successful repairs. Larger `runner.start_max_iteration` gives the fixer more attempts at the cost of
  more LLM calls.
- **"could not detect a supported language" / "no source files found".** Language is auto-detected from
  the file scan; an empty or wrong detection usually means everything was filtered by
  `indexer.skip_path_prefixes`, or the repo path is wrong. Pass `--lang` to force it.
- **`--docs` writes into your source tree.** Per-symbol docs are inserted above declarations in source
  files, and the overview is written to `indexer.overview_doc_path` (default `docs/documentation.md`).
  Point asqs-core at a clean working tree (or a branch) so you can review the diff.
- **Shipping (`--ship`) requirements.** Ship only runs on a **stable** result (`run not stable — not
shipping` otherwise), and needs a VCS token (`vcs.github.token`) and a recognizable origin (HTTPS or
  SSH — asqs-core rewrites SSH→HTTPS for the push). If the PR step can't resolve owner/repo from the
  origin URL, set `vcs.<provider>.default_owner` / `default_repo`.
- **Exit code 1 on a green-looking run.** The CLI exits non-zero when generated tests didn't end up in a
  passing whole-project build (`!Stable()`). Check the summary line and the per-symbol `discarded` /
  `unstable` statuses; the `discard` mechanism drops repeatedly-failing tests so the rest stay green.

## Limitations

No web UI, no REST API, no multi-tenant control plane, no project-intelligence (repo skill-file
reading), no pre-generation seams, no audit reports, no governance/policy engine, no
coverage/mutation gates, and no per-step LLM provider selection. Those live in the commercial layer.

## License

[Apache-2.0](./LICENSE).
