# Indexer

The indexer traverses the repo, detects changes, builds the AST/symbol table and dependency graph, chunks at symbol boundaries (300–800 tokens), sanitizes content, and stores symbols/edges in the metadata DB and chunks/embeddings in the vector DB.

## Scheduling and first run

Use **Scheduler** to run the indexer on a cron schedule and optionally once on first system start:

- **Schedule**: cron expression (e.g. `0 1 * * *` = daily at 01:00). Configure via `indexer.schedule` or `ASQS_INDEXER_SCHEDULE`.
- **Run on first start**: when `run_on_first_start` is true, the scheduler checks whether any previous index run exists for the repo (via `HasPreviousRun(ctx, repoID)`). If none, it runs the indexer once before starting the cron. Configure via `indexer.run_on_first_start` or `ASQS_INDEXER_RUN_ON_FIRST_START`.

Example: create a scheduler with `SchedulerOptions{ Schedule: "0 1 * * *", RunOnFirstStart: true, RepoID: "org/repo", Run: runFunc, HasPreviousRun: func(ctx, repoID) (bool, error) { n, err := metaStore.CountIndexRuns(ctx, repoID); return n > 0, err } }`. Call `Start(ctx)` (e.g. in a goroutine); it runs once immediately if no previous run, then at 01:00 daily. Use `Stop()` to shut down.

## Flow

1. **Change detection** – Compare current file list (path + SHA) with `metadata.files`. Output: added, changed, removed.
2. **Versioning** – Record `index_runs` (run_id, repo_id, commit_sha, started_at, finished_at) for scheduling and auditing.
3. **Incremental updates** – For removed files: delete chunks (embeddings), delete symbols (metadata; edges cascade), delete file row. For added/changed: re-parse, replace symbols/edges for that file, upsert file, chunk and re-embed.
4. **AST / symbol table / graph** – Language helpers (**Java** advanced JAR or minimal Go, **JS/TS** Node indexer, **C#** Roslyn `tools/csharp-indexer` when **`indexer.csharp_indexer_dll_path`** is set) parse each file and return symbols (kind, fq_name, start/end line, optional **`signature` JSON`) and edges (caller_fq_name, callee_fq_name, edge_type). Stored in `metadata.symbols` and `metadata.edges`. **`signature_json`** should use cross-language keys where possible (**`visibility`**, **`exported`**, **`framework`**, **`http_method`**, **`path_pattern`**, …); **`mergeStructuredSignatureIntoChunkMetadata`** copies an allowlist into **`chunk_metadata`** for each chunk (see **`docs/DOCUMENTATION.md`** — Structured signature_json & chunk_metadata).
5. **Sanitization** – Strip or truncate comments/docs to reduce injection risk (see `Sanitize` and `SanitizeOptions`).
6. **Chunking** – One chunk per symbol (method/class/function); split symbols > MaxTokens into sub-ranges; target 300–800 tokens per chunk.
7. **Embeddings** – Optional `Embedder` produces vectors; chunks (with provenance) are written to the vector store.

## Edge resolution and metrics

Caller/callee UUID resolution order: per-file map → run-wide `fq_name` map → **`resolveSymbolIDForFQName`** (uses current file plus paths inferred from **`IMPORTS`** edges when `ListSymbolsByFQName` returns duplicates) → Java qualified-signature normalization → Java **`IMPORTS`** (`resolveJavaImportCalleeID`) → C# **`IMPORTS`** (`resolveCSharpImportCalleeID`, namespace trimming). Unresolved edges increment **`RunResult.EdgesUnresolvedMissingCaller`** / **`EdgesUnresolvedMissingCallee`** (per canonical edge type) and emit audit **`index.edges_unresolved`** when configured.

`metadata.symbols` / `metadata.edges` are not keyed by `repo_id` (chunks are); use separate metadata databases per tenant when strict repo isolation is required.

## Language helper contract (JSON)

Helpers (e.g. `java -jar helper.jar`, `dotnet run` for C#) read source and write to stdout:

```json
{
  "path": "src/main/java/com/example/Foo.java",
  "lang": "java",
  "module": "core",
  "is_test": false,
  "symbols": [
    { "kind": "class", "fq_name": "com.example.Foo", "start_line": 1, "end_line": 50, "signature": null },
    { "kind": "method", "fq_name": "com.example.Foo.bar", "start_line": 10, "end_line": 20, "signature": "{}" }
  ],
  "edges": [
    { "caller_fq_name": "com.example.Foo.bar", "callee_fq_name": "com.example.Util.helper", "edge_type": "calls" }
  ]
}
```

Use `ParsedFileFromJSON(stdout, source)` to convert to `ParsedFile`; then run chunking and storage.

**JS/TS:** Go normally reads JSONL from the Node process **stdout** (`jstindexer.RunIndexer`). Set **`indexer.jst_jsonl_out: temp`** (or a file path) so Node writes the **same** JSONL via **`--jsonl-out`** and Go reads the file after exit — avoids pipe edge cases on very large lines without changing symbols/edges.

## Structural edges (Java vs JS/TS)

- **Java:** class → method **`contains`** edges are **always** synthesized when method FQ names use the `Class#method` form, even if the indexer also emitted other edges (e.g. Spring **`API_ROUTE`**). This keeps retrieval’s `GetEdgesTo` usable.
- **JS/TS:** MODULE → symbol **`CONTAINS`** is added only when the parsed file has **no** native edges, so Nest/React enrichers are not duplicated.

## E2E-oriented symbols (Phase 1)

Nest HTTP routes are indexed as **`API_ROUTE`** symbols with stable `fq_name` so **`ROUTE_TO_HANDLER`** edges persist in Postgres. The **minimal Java indexer** (`tools/java-indexer`) emits the same for **Spring Web** `@RestController` / mapping annotations. The **advanced Java JAR** (`JavaIndexer.java` + `advanced.go`) adds the same E2E-oriented entities as JS/TS where applicable: **`E2E_SPEC`**, Playwright-Java **`TEST_SELECTOR`** (`getByTestId`), and **`API_CLIENT_REQUEST`** + **`CALLS_API`** (WebClient `.uri`, RestTemplate URL strings). The **JS/TS indexer** adds **`PAGE_ROUTE`** for SPA routers, **`E2E_SPEC`**, HTTP client calls, etc. The **C# Roslyn indexer** (`tools/csharp-indexer`, when **`indexer.csharp_indexer_dll_path`** is set) emits **`API_ROUTE`** for ASP.NET attribute routes, **`API_CLIENT_REQUEST`** for **`HttpClient`** usage, and **`E2E_SPEC`** stubs for Playwright .NET / Selenium test files — same post-run **`TARGETS_API_ROUTE`** linking (`apiclient_route_link.go`). Chunk types `route`, `api_contract`, `e2e_pattern`, `page` are assigned in `chunk.go`. See **`docs/E2E-INDEXING.md`** at the repository root.

## `TESTS_SOURCE` (test ↔ production trace)

After **`TARGETS_API_ROUTE`** linking, **`metadata.Store.MaterializeTestsSourceEdges`** rebuilds **`TESTS_SOURCE`** edges (test caller → production callee): derived from **calls/imports** across **`files.is_test`**, plus **JUnit-style** test class names (**`FooTest` / `FooTests` / `FooIT` → `Foo`**). **`ListGaps`** uses them for **deprioritization** and **reason** text. See **[DOCUMENTATION.md — Tests ↔ source trace edges](../../docs/DOCUMENTATION.md#tests-source-edges)**.
