# asqs-core

**asqs-core** is an open-source CLI that automatically generates **unit tests, end-to-end tests,
per-symbol documentation, and a whole-repo overview document** for **Java, C#, and
JavaScript/TypeScript** repositories, using a
code-graph + retrieval-augmented-generation (RAG) + LLM pipeline. Point it at a local folder or a
remote git URL and it indexes the code, finds under-tested symbols, generates tests/docs, and
**validates them by actually compiling and running them** in a sandbox — repairing failures with an
LLM fixer loop.

It is the open foundation of a larger quality system. The enterprise layer (project-intelligence,
governance/policy, multi-tenant control plane, audit reports, web UI, CI webhook triggers, per-step
LLM providers) is intentionally **not** part of this core.

> **Status.** The full engine (indexing, retrieval, generation, evaluation/repair), the three
> language indexers, and the `asqs-core run` CLI build as a standalone Go module — `go build ./...`
> is green and `asqs-core run` is wired end to end. Executing a real run requires a Postgres +
> pgvector database, an LLM key, and (for the Docker sandbox) Docker; follow the steps below.

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
- An **LLM API key** (OpenAI / Anthropic / Azure OpenAI), or a local **Ollama** endpoint
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
  typed edges, and source chunks; chunks are embedded into pgvector.
- **Planning** uses the symbol graph + RAG to pick under-tested symbols and build a focused
  retrieval context per gap (target + dependencies + similar tests, MMR-diversified).
- **Generation** uses a provider-agnostic LLM with embedded per-language skill-packs and contracts.
- **Evaluation** generates every gap's test first, then compiles + runs the **whole project once**
  in a local or Docker sandbox (not per gap — one compile per fix iteration, not N). The LLM fixer
  repairs failures over a bounded loop (`runner.start_max_iteration`); tests that repeatedly fail are
  discarded so the rest stay green. Only artifacts that compile and pass survive. A **quality gate**
  rejects any fixer edit that would degrade the test (e.g. gutting assertions into an empty/tautological
  shell), so repairs never trade correctness for a green compile.
- **Documentation** (`--docs`) produces per-symbol doc comments **and**, in parallel, a whole-repo
  overview document built from the index (batched LLM passes over the source files plus
  file-dependency/visual sections), written to `docs/documentation.md`.

## Limitations / non-goals

No web UI, no REST API, no multi-tenant control plane, no project-intelligence (repo skill-file
reading), no pre-generation seams, no audit reports, no governance/policy engine, no
coverage/mutation gates, and no per-step LLM provider selection. Those live in the commercial layer.

## License

[Apache-2.0](./LICENSE).
