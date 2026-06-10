# JS/TS indexer

Node/TypeScript sidecar for ASQS that indexes JavaScript and TypeScript repositories using the TypeScript Compiler API (via **ts-morph**). Output is JSONL to stdout, one line per file, in **LangIndexerJSON** shape so the Go pipeline can use the same storage and retrieval as Java.

See **DESIGN-JS-TS.md** (repo root) for full architecture, phases, and symbol/edge model.

## Build

```bash
npm install
npm run build
```

**Docker indexing (QualityBot):** With **`indexer.execution: docker`**, Go runs `node dist/index.js` inside **`node:20-bookworm`** (or **`indexer.docker_node_image`**). The whole **`tools/js-ts-indexer`** tree (including **`node_modules`**) is mounted read-only at **`/indexer`**; the repo at **`/workspace`**; JSONL is written to a host temp file mounted at **`/out/asqs-jst-index.jsonl`**. Heap size: **`indexer.docker_node_heap_mb`** (default 4096). Shared flags: **`docker_cli`**, **`docker_memory`**, **`docker_cpus`**, **`docker_network`**.

Integration smoke:

```bash
go test -tags=integration ./tools/js-ts-indexer/... -count=1 -run TestRunIndexerDocker_smoke
```

## Tests (Vitest)

```bash
npm test
```

Covers Nest `API_ROUTE`, React / Angular / Vue / Solid `PAGE_ROUTE`, E2E spec heuristics, and HTTP client symbols. See **`docs/E2E-INDEXING.md`** in the repo root.

**Filesystem scan:** `src/file-list.ts` **`SKIP_DIRS`** must **not** skip **`e2e`**, **`e2e-tests`**, **`__tests__`**, or **`cypress`** — those trees hold Playwright/Cypress/Jest specs that become **`E2E_SPEC`**; skipping them used to empty **`ListGapsE2E`** for JS/TS (aligned with Go **`ScanRepoForFiles`**).

## Usage

```bash
node dist/index.js --repo /path/to/repo [--jsonl-out /path/to/index.jsonl] [--output /path] [--frameworks auto|none|comma-list]
```

- **--repo** (required): repository root.
- **--jsonl-out** (optional): write JSONL **only** to this file (directories created); stdout stays free for anything else. Same records as default stdout mode. QualityBot can set **`indexer.jst_jsonl_out: temp`** so Go uses a temp file instead of a pipe (recommended if you hit parse errors on huge single-file payloads).
- **--output** (optional): directory for **Phase 1 artifacts** after indexing:
  - **`packages.jsonl`** — one JSON object per workspace package (paths, `moduleKind`, `packageRole`, `sourceRoots` / `testRoots`, split `dependencies` / `devDependencies`, scripts).
  - **`index-summary.json`** — repo metadata, all `tsconfig` hints (`extends`, **`extendsChain`** base→leaf, **`mergedCompilerOptions`** shallow merge, `references`), Angular projects from `angular.json`, per-package **source file lists** (filesystem), and `indexedFileCount` from the AST pass.
- **--frameworks**: `auto` (default) uses **`package.json` discovery** for React/Nest/Angular/Vue/Solid/AngularJS and always enables **Node** builtins/entry/CLI + **HTTP client** + **E2E** enrichers on matching files. `none` = core TS/JS graph only (no framework/router/E2E/HTTP/node extras). Otherwise a **comma- or pipe-separated** list: `node`, `nest`, `react`, `angular`, `angularjs`, `vue`, `solid`, `http`, `e2e`, **`tanstack`** (`@tanstack/react-router` / `createFileRoute`) — each framework token still requires the matching dependency signal from discovery (except `node`, `http`, `e2e`; **`tanstack`** can be forced explicitly). See **`docs/PLAN.md`** §2.

**Workspaces:** npm/yarn `workspaces` plus `pnpm-workspace.yaml` are expanded for `packages/*` and single-folder entries. Patterns containing `**` are not expanded.

Stdout: one JSON object per line (path, lang, module, is_test, symbols, edges). Go invokes this CLI and parses the stream into `map[string]*indexer.ParsedFile`.

**IMPORTS edges:** `callee_fq_name` is the **resolved source file’s module id** (same as that file’s `MODULE` symbol, e.g. `src.utils`), not the raw specifier (`./foo`, `@/bar`). Unresolved imports (packages outside the project) emit no `IMPORTS` edge so the Go layer can join edges to symbols for the overview file graph.

**Large lines / pipe safety:** The indexer writes each line with **`fs.writeSync` in a loop** (full buffer; no partial writes) and uses **FD 1** when `process.stdout.fd` is unset (common when stdout is a pipe). Falling back to `process.stdout.write` without draining can **interleave** two JSONL records and break Go’s JSON parser (`invalid character 'p' after object key:value pair`). **Duplicate edges** `(caller, callee, edge_type)` are **deduped** before emit to shrink huge files (e.g. monolithic classes with many repeated `CALLS`).

## Excluded files (config / tooling)

To avoid spending tokens on files that are irrelevant for tests and docs, the indexer **skips**:

- **Known config/tooling basenames:** e.g. `gulpfile.js`, `jest.config.ts`, `postcss.config.js`, `webpack.config.js`, `vite.config.ts`, `next.config.js`, `tailwind.config.js`, `babel.config.js`, `playwright.config.ts`, `cypress.config.ts`, `vitest.config.ts`, `eslint.config.js`, `prettier.config.js`, and many similar (see `CONFIG_TOOLING_BASENAMES` in `src/language-indexer.ts`).
- **Any file matching** `*.config.(js|ts|mjs|cjs|jsx|tsx)` or `*.conf.(js|ts|mjs|cjs)` (e.g. `something.config.ts`).

These files are not emitted in the JSONL output, so they are not indexed, chunked, or embedded by the Go pipeline.

## Layers

- **A — discovery:** `package.json`, `tsconfig.json` (extends chain + merged `compilerOptions`), workspaces (`packages/*`, `packages/**`, `**`), framework signals (**Nuxt**: `frameworkSignals.nuxt`, **`nuxtPagePaths`** from `pages/**/*.vue` when `nuxt` / `@nuxt/schema` is a dependency).
- **B — language indexer:** ts-morph project, AST, symbols, edges (core semantics: type aliases, enums, JSDoc on symbols, `EXPORTS` / `RE_EXPORTS`, `REFS_TYPE`, `INTERFACE_METHOD`, `TEST_BLOCK` — see `src/enrichers-semantics.ts`).
- **C — framework enrichers:** **Node** (Phase 3), **Nest** (Phase 4), **React** (Phase 5: `enrichers-react-graph.ts` — components, hooks, context, props hints; router `PAGE_ROUTE`), **Angular** (Phase 6: `enrichers-angular-graph.ts` — decorators, NgModule, templates path), **AngularJS** (`enrichers-angularjs-graph.ts`), **Vue / Solid** router `PAGE_ROUTE`, E2E/HTTP-client extras. **Still open:** template AST (Angular HTML), full `exports` map, Nest DTO/guards, deeper React graphs. See **`docs/JS-TS-PHASE-STATUS.md`**.
- **D — chunk builder:** symbol-centred chunks (future).
- **E — retrieval profiles:** test/doc/architecture (future).

## Contract

Each stdout line = `LangIndexerJSON` (see `internal/intelligence/indexer/lang.go`):

- `path`, `lang` ("javascript" / "typescript"), `module`, `is_test`
- `symbols`: `{ kind, fq_name, start_line, end_line, signature? }` — base kinds: MODULE, CLASS, INTERFACE, **INTERFACE_METHOD**, FUNCTION, METHOD, VARIABLE, **TYPE_ALIAS**, **ENUM**, **ENUM_MEMBER**, **TEST_BLOCK**; Node: **ENTRYPOINT**, **CLI_COMMAND**, **BUILTIN_MODULE_USE**; React: **REACT_COMPONENT**, **REACT_HOOK**, **REACT_CONTEXT**, **REACT_PROVIDER**, **REACT_CUSTOM_HOOK**; Angular: **ANGULAR_COMPONENT**, **ANGULAR_SERVICE**, **ANGULAR_MODULE**, **ANGULAR_DIRECTIVE**, **ANGULAR_PIPE**, **ANGULAR_TEMPLATE**; AngularJS: **ANGULARJS_MODULE**, **ANGULARJS_CONTROLLER**, **ANGULARJS_SERVICE**, …; Nest: **NEST_CONTROLLER**, **NEST_ROUTE_HANDLER**, **NEST_MODULE**, **NEST_PROVIDER**, **API_ROUTE**; E2E: **E2E_SPEC** (heuristic). `signature` may include **`jsdoc`** (first paragraph). For **VARIABLE** / `const` **FUNCTION** arrows, **`jsdoc` is only taken at module or namespace scope** so a `/** … */` before an inner `let`/`const` is not stored as that local’s documentation.
- `edges`: `{ caller_fq_name, callee_fq_name, edge_type }` — IMPORTS, EXTENDS, IMPLEMENTS, CALLS, CONTAINS, RENDERS, **EXPORTS**, **RE_EXPORTS**, **REFS_TYPE**, **ROUTE_TO_HANDLER**, **USES_HOOK**, **USES_CONTEXT**, **ACCEPTS_PROPS_TYPE**, **USES_TEMPLATE**, **STANDALONE_IMPORTS**, **MODULE_DECLARATIONS**, **MODULE_BOOTSTRAP**, **DECLARES**, **PACKAGE_EXPORT** (package name → resolved file path from `exports`), … plus Node/Nest/package edges documented above. **Both endpoint names must match symbol `fq_name` values** so the Go indexer can store edges (see `docs/E2E-INDEXING.md`). (`REFS_TYPE` / `RE_EXPORTS` callees are often unresolved strings — edges may be dropped in metadata.)
