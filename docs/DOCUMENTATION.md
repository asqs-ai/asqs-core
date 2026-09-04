# asqs-core behaviour reference

What the pipeline actually does, and why it does it that way. This is the document the source cites
when a comment says "see docs/DOCUMENTATION.md"; it explains decisions that are not obvious from the
code, not the API surface.

This is the only document in `docs/`. The configuration keys themselves are documented in
`internal/config/schema_v2.go`, where each field carries its own doc comment — a test fails on an
undocumented key, so the structs are the reference rather than a copy of them that could drift.

- [A run, end to end](#a-run-end-to-end)
- [Test framework bootstrap](#test-framework-bootstrap)
  - [Detection asks which packages are missing, not whether a word appears](#detection-asks-which-packages-are-missing-not-whether-a-word-appears)
  - [Verification runs a test, because a config parses either way](#verification-runs-a-test-because-a-config-parses-either-way)
  - ["Already set up" is verified, not assumed](#already-set-up-is-verified-not-assumed)
  - [E2E bootstrap (Playwright)](#e2e-bootstrap-playwright)
  - [Where it runs](#where-it-runs)
  - [Audit trail and limitations](#audit-trail-and-limitations)
- [Indexing](#indexing)
  - [Symbol identity](#symbol-identity)
  - [Symbol line/column spans](#symbol-linecolumn-spans)
  - [Structured signature_json & chunk_metadata](#structured-signature_json--chunk_metadata)
  - [TESTS_SOURCE edges](#tests_source-edges)
- [Retrieval](#retrieval)
  - [Profiles and budgets](#profiles-and-budgets)
  - [Lost in the Middle / RAG grounding](#lost-in-the-middle--rag-grounding)
  - [Retrieval sufficiency (abstention)](#retrieval-sufficiency-abstention)
  - [Within-run retrieve cache](#within-run-retrieve-cache)
- [Generation](#generation)
  - [Two-phase test generation](#two-phase-test-generation)
  - [The tool loop](#the-tool-loop)
  - [Knowledge sources](#knowledge-sources)
  - [The bootstrap contract](#the-bootstrap-contract)
- [Evaluation and the fix loop](#evaluation-and-the-fix-loop)
  - [The runner plan](#the-runner-plan)
  - [Circuit breakers](#circuit-breakers)
  - [Discard and stability](#discard-and-stability)
- [Measurement](#measurement)
  - [First-wave quality metrics](#first-wave-quality-metrics)
- [Configuration](#configuration)
  - [Upgrading from the pre-v2 schema](#upgrading-from-the-pre-v2-schema)
- [Deployment](#deployment)

## A run, end to end

One `asqs-core run` is a single pass: index the repository, plan a set of test gaps, generate a test
for each, then evaluate the whole project **once** and repair what fails.

The evaluation being whole-project rather than per-gap is the shape everything else follows from. A
per-gap loop would compile the project once per generated file; a project of any size makes that
unaffordable. So generation writes every artifact first, one compile-and-test pass covers them all,
and the fix loop works on the set. The cost is that one badly generated file can hold the whole run
unstable — which is what [discard](#discard-and-stability) exists to resolve.

## Test framework bootstrap

Off by default. When `bootstrap.test_framework.enabled` is true, the run detects whether the target
repository has a test runner the dominant stack can actually use; if not, it patches the build,
installs the stack, and **proves it by running a test**. This happens after checkout and **before**
the language indexer, so metadata and the runner commands see the new setup.

```yaml
bootstrap:
  test_framework:
    enabled: false            # master switch
    mode: auto                # auto | jest | junit | xunit | off
    pin_versions: true        # exact versions in package.json (JS/TS only)
    allow_lockfile_change: true
    execution: auto           # auto | local | docker
  require_docker: false       # fail fast rather than installing on the host
```

`allow_lockfile_change: false` makes installs frozen (`npm ci`, `pnpm install --frozen-lockfile`)
**except** for the first install after bootstrap patched `package.json` — that one must refresh the
lockfile or the new devDependencies cannot be written, even under `CI=true`.

### Detection asks which packages are missing, not whether a word appears

The rule that "the word junit appears in the POM" is enough is how a Spring Boot module carrying only
`junit-jupiter` skips bootstrap and then fails to compile every generated test that touches
`@SpringBootTest`, Mockito or AssertJ — against a manifest the fix loop is not allowed to write.
Detection therefore resolves a framework profile and checks its required coordinates individually; a
**partially** equipped module is bootstrapped.

**JavaScript / TypeScript.** A hand-written runner config (`jest.config.*`, `vitest.config.*`,
`karma.conf.*`, `.mocharc.*`, `jasmine.json`) still short-circuits detection. Otherwise **Vite decides
the runner**: anything that builds with Vite gets Vitest, because Vitest reads the repository's own
`vite.config` — JSX transform, path aliases, CSS handling and `import.meta.env` all behave as they do
in the app build, where reproducing that under Jest means a parallel transform stack that drifts on
every config change. Two frameworks override it: Angular ships its own builder and
`jest-preset-angular`, and NestJS is a CommonJS server framework whose own convention is Jest.
An ESM package (`"type": "module"`) gets Vitest too — Jest runs ESM only behind
`--experimental-vm-modules`, and the CommonJS workaround verifies a stack that then cannot run the
ESM test files generation produces.

| Detected | Runner | Adds |
|---|---|---|
| Angular (`@angular/core`) | Jest | `jest-preset-angular` (peer-matched), `@types/jest`, `setup-jest.ts`, `tsconfig.spec.json` |
| NestJS (`@nestjs/core`) | Jest | `ts-jest`, `@types/jest`, `@nestjs/testing` at the matching major |
| React + Vite | Vitest | `vitest` (Vite-major matched), `jsdom`, `@testing-library/react` (React-major matched) |
| Vue / Svelte | Vitest | `vitest`, `jsdom`, `@vue/test-utils` at the Vue major / `@testing-library/svelte` |
| Any other Vite package | Vitest | `vitest`, plus `jsdom` when it renders to a DOM |
| React without Vite | Jest | `jest-environment-jsdom`, Testing Library, `ts-jest` on TS |
| plain | Jest | `jest` (+ `ts-jest`, `@types/*` on TS) |

Three version couplings are resolved from the repository rather than pinned globally, because each
one has an unsatisfiable peer range on the wrong side: `@testing-library/react` tracks the React
major, `vitest` tracks the Vite major, `@vue/test-utils` is hard-split by the Vue major. A fourth is
deliberately **not** the newest release — `@angular-devkit/build-angular` declares
`peerOptional jest ^29.5.0` through Angular 19, so the newer `jest-preset-angular` lines (which need
Jest 30) make `npm install` fail with `ERESOLVE`.

Bootstrap writes its runner into `scripts["test:asqs"]` and sets `scripts.test` only when the package
has none. Assigning `scripts.test = "jest"` unconditionally is how `ng test` disappears from an
Angular repository.

**Java.** The framework is read from `pom.xml` / `build.gradle(.kts)`: Spring Boot gets
`spring-boot-starter-test`, Quarkus gets `quarkus-junit5` + `quarkus-junit5-mockito` + AssertJ,
Micronaut gets `micronaut-test-junit5` + Mockito + AssertJ, plain gets JUnit 5 + Mockito + AssertJ +
Surefire. **Android is declined** — it needs `testOptions { unitTests.all { useJUnitPlatform() } }`,
and adding dependencies without it yields a suite that silently runs zero tests and reports success.
Framework-coupled artifacts are emitted **without a version** whenever a parent POM or BOM manages
them; pinning them is how a Boot 3.2 app compiles against a mismatched Spring Test major.

**C#.** ASP.NET Core (`Sdk="Microsoft.NET.Sdk.Web"`) adds `Microsoft.AspNetCore.Mvc.Testing` at the
project's .NET major — 10.x cannot be referenced from a `net8.0` project — and an EF Core reference
additively pulls `EntityFrameworkCore.InMemory` at the EF major already present. MAUI/iOS/Android and
Windows desktop are **declined**: they need `dotnet workload install`, which stock SDK containers
lack. The **test runner is whatever the repository already established** (xUnit / NUnit / MSTest);
xUnit is the default only when nothing is, so bootstrap never drags a second runner into a solution.
Central Package Management is honoured — with a `Directory.Packages.props` on the path, `PackageVersion`
entries are merged there and `PackageReference` carries no version (NU1008).

> **FluentAssertions is pinned to the 7.x line on purpose.** 7.2.2 is Apache-2.0; **8.0.0 switched to
> a commercial licence.** ASQS writes this `PackageReference` into customer repositories, so bumping
> the pin would impose a licensing obligation on every client the bootstrap touches. A test guards it.

### Verification runs a test, because a config parses either way

`npx jest --showConfig` proves that a config file parses. A config parses perfectly while the
transform cannot handle TSX, the environment has no `document`, or the framework preset is missing —
all of which appear only when a test actually runs. So bootstrap writes and executes one:

1. **`asqs-bootstrap-smoke` (mandatory)**, with the extension that exercises the real transform (`.ts`
   on a TypeScript package, not `.cjs`). On a `jsdom` profile it also touches `document`, so a runner
   left on the default `node` environment — the most common reason a generated component test fails —
   is caught by the gate rather than by the first generated test. Failure fails bootstrap.
2. **`asqs-framework-smoke` (advisory)**: React renders through Testing Library, Angular creates a
   standalone component through `TestBed`, Nest resolves a provider. Vue and Svelte additionally get a
   companion `.vue` / `.svelte` component that the test imports — such a file compiles only through
   that framework's Vite plugin, so importing a real one is what proves the plugin actually reaches the
   test run through the merged `vite.config`. Failure logs `test_bootstrap.framework_smoke_failed`,
   deletes the test and its companion, and continues on the verified runner stack.

**Formatters and linters never abort a run.** Most bind to Maven's `validate` phase, which runs before
compile, so a style violation kills the build before the question is even asked — a real run on
spring-petclinic aborted at minute one because `spring-javaformat` disliked the smoke test's javadoc
wrapping. Verification now passes `-Dspring-javaformat.skip=true`, `-Dcheckstyle.skip=true`,
`-Dspotless.check.skip=true` and friends (unknown `-D` properties are ignored by Maven, so listing
plugins a project does not use costs nothing), and when a build failure carries a formatter rejection
**and no compiler diagnostic**, bootstrap logs `test_bootstrap.verify_style_violation`, marks the
contract unverified and continues. A compiler diagnostic in the same output always wins.

**Smoke tests are instruments, not deliverables.** Each is removed once it has answered its question,
on success as well as failure; what it proved is recorded in the audit and in the contract's
`framework_smoke` status. This is not tidiness — a leftover foreign file that the project's own style
rules reject would fail every subsequent compile in the run, in a file the fix loop has no mandate to
repair. (On the bootstrap path the mandatory smoke test stays: it is part of the stack just established.)

### "Already set up" is verified, not assumed

Detection reads manifests, and "every dependency is declared" is a different claim from "a test runs
here". A Gradle build with JUnit 5 and no `useJUnitPlatform()` runs zero tests and reports success; a
Jest config can parse and still fail to transform the repository's own syntax; a .NET solution can
restore cleanly with no matching shared runtime installed. So a complete stack is still proven: one
throwaway smoke test, run, removed again, outcome recorded as `verified: true` / `false`.

Two rules separate this from the bootstrap path. **Nothing is configured** — the repository's own
runner is the one under test, so a Vite repository already using Jest is verified with Jest, not with
the Vitest bootstrap would have chosen, and repositories on Karma, Jasmine, Mocha or AVA are left
alone entirely (`test_bootstrap.verify_existing_skipped`). And **the smoke test is removed either
way**, since bootstrap changed nothing else here.

If it cannot run, the run **stops**: generated tests would fail identically, and the repair loop
cannot fix a runner or a build manifest. There is no switch for this — bootstrap is opt-in, an
operator who turned it on asked for a working test stack, and repositories bootstrap cannot drive are
skipped automatically rather than configured away.

### E2E bootstrap (Playwright)

When `indexer.policy.max_gaps_e2e` is above zero and `bootstrap.e2e_framework.enabled` is true, the
same shape applies to the E2E stack: detect Playwright/Cypress from config files, dependencies and
scripts; if nothing is found and `mode` is `auto` or `playwright`, add `@playwright/test`, a
`test:e2e` script, `playwright.config.ts` and an `e2e/smoke.spec.ts` (so `playwright test --list`
finds a test), install, then verify with `--list`. `mode: cypress` does the Cypress equivalent.

Java adds Playwright Java in test scope plus an install task and
`src/test/java/com/asqs/e2e/AsqsPlaywrightSmokeE2E.java`; C# adds `Microsoft.Playwright` and
`E2E/AsqsPlaywrightSmokeE2E.cs`. The smoke test launches headless Chromium against a `data:` URL —
enough to prove the driver loads and the browser is installed, without the application or a server.
`mode: cypress` is not applicable on the JVM or .NET and falls back to Playwright
(`e2e_bootstrap.mode_fallback`).

### Where it runs

`bootstrap.test_framework.execution` and `bootstrap.e2e_framework.execution` take `auto` (the
default — Docker when `general.sandbox.type` is `docker`, host otherwise), `local`, or `docker`.
`bootstrap.require_docker` makes a bootstrap that *would* install on the host fail fast after
detection shows work is needed.

Image selection is where E2E differs from unit. Unit bootstrap uses the same images as evaluation
(`general.sandbox.images.node` chosen against `general.build.build_tool`,
`general.sandbox.images.dotnet` for C#). E2E bootstrap uses the Playwright images —
`general.sandbox.images.playwright`, `general.sandbox.images.playwright_java`,
`general.sandbox.images.playwright_dotnet` — whose default tags are aligned in code with the
Playwright pins, so browser bundles match the package version rather than being installed on top of a
plain SDK image. Playwright containers also run with `--ipc=host`, per Playwright's own
recommendation. The `playwright_dotnet` default carries .NET SDK 8 only; a repository targeting
`net9.0` or later needs a custom image or bootstrap reports NETSDK1045.

For pnpm and yarn in Docker, install runs `npm install -g corepack@latest` then `corepack enable`
first, so the shims work and Corepack's signing keys match the registry. pnpm installs also set
`--store-dir` outside the repository, because a project `.npmrc` with `store-dir=.pnpm-store` would
otherwise create thousands of files under the working tree.

Subprocess timeouts come from `general.sandbox.timeout`, capped internally for this phase. Network is
needed for first-time dependency resolution.

### Audit trail and limitations

Bootstrap emits `test_bootstrap.*` (and `e2e_bootstrap.*`) throughout: `start`, `skip`,
`skip_wrong_mode`, `skip_detected`, `skip_lang`, `skip_framework_unsupported`, the per-language
profile events `js_profile` / `java_profile` / `csharp_profile` carrying the detected framework and
the full required dependency list, `apply_start`, `patched`, `install`, `verify_style_violation`,
`verify_existing_start` / `_ok` / `_failed` / `_skipped`, `framework_smoke_skipped` / `_unsupported` /
`_failed`, `contract_written`, and the `*_failed` error events. What generation did with the result is
a separate pair of events — see [The bootstrap contract](#the-bootstrap-contract).

Known limits: only the repository root is modified, so a monorepo's other workspaces are untouched;
ESM-only JS repositories may still need manual tweaks; unusual multi-module Java layouts may need
manual fixes; C# supports SDK-style `.csproj` only, and a non-SDK-style project is rejected with a
clear error rather than patched.

Integration tests: `go test -tags=integration ./internal/testbootstrap/... -count=1` (needs network,
Node, and `dotnet` on PATH where applicable).

## Indexing

Language-native parsers (Java AST, C# Roslyn, TypeScript) emit symbols and edges; Go chunks each
symbol, sanitizes it, embeds it, and stores symbols and edges in Postgres with chunks in pgvector.

### Symbol identity

A symbol's id is durable across reindexes. The natural key is
`(repo_id, file, fq_name, kind, dup_ordinal)` and inserts upsert on it, so an unchanged file keeps
its ids — which is what makes `chunks.symbol_id` meaningful and per-symbol history possible at all.

`dup_ordinal` is the 1-based order of appearance among same-key symbols in one file. It exists
because **not every indexer can distinguish overloads in the FQName**: the advanced Java indexer
emits `Type#method` for every overload. Without the ordinal those overloads collide on the natural
key and silently merge onto one row. Identity degrades only if overloads are *reordered* within a
file, which is rare and self-heals on the next reindex.

The consequence to know about: reindexing a file no longer deletes its symbols, so the indexer prunes
what the file no longer declares and clears the file's outbound edges explicitly. The old
delete-symbols cascade used to do the second as a side effect.

Runs against a git checkout also record one `symbol_versions` row per (symbol, commit), hashing the
symbol's **source line span** — not its chunks, since chunking has its own budgets and a chunking
change would otherwise register as churn on untouched code. Churn (`count(DISTINCT body_hash)` over
90 days) is available to gap ranking but **ships at weight 0**: the term is absent until a measured
comparison justifies a value.

### Symbol line/column spans

`start_line` / `end_line` are always present. `start_column` / `end_column` are optional and NULL when
the indexer is line-based or the parser did not report them — a line-only indexer is a supported
configuration, not a degraded one, so consumers must handle the absence rather than assume zero.

### Structured signature_json & chunk_metadata

Language indexers put a structured signature in `symbols.signature_json`, using cross-language keys
where the concept is shared: `visibility`, `exported`, `framework`, `http_method`, `path_pattern`.
An allow-listed subset is copied into each chunk's `chunk_metadata` at chunk time, so retrieval can
filter on those facts without joining back to `symbols`.

C# additionally stores `bare_fq_name` — the parameterless, generic-free form of a method's name. C#
FQNames carry parameter lists (`OrderService#GetOrder(string)`) so overloads are distinct, but a model
that read `OrderService#GetOrder` in prose asks with the bare form, and the `get_symbol` tool falls
back to that key. Only C# symbols carry it, so no other language can false-hit.

### TESTS_SOURCE edges

After indexing, `TESTS_SOURCE` edges are rebuilt from test file to production symbol: derived from
calls and imports across the `files.is_test` boundary, plus JUnit-style naming
(`FooTest` / `FooTests` / `FooIT` → `Foo`).

Gap listing uses them to **deprioritize** rather than exclude — a symbol with a test that exercises
one path is still a candidate for the paths it misses — and to explain itself in the gap's reason.

## Retrieval

For each gap, retrieval assembles the context the generator sees: the target symbol, its dependency
neighbourhood, similar existing tests to copy conventions from, and relevant fixtures and config.

### Profiles and budgets

A **profile** (`java_unit`, `http_api`, `react_feature`, `nest_module`, `full_stack`,
`e2e_playwright`) decides which chunk types are searched and how the sections are weighted, because
what counts as useful context differs sharply between a service method and a page component.

Section caps are constants — 5 similar tests, 15 dependency chunks, 5 fixtures — with per-profile
overrides in `retrieval.profile_budgets`. The global caps were frozen deliberately: per-profile is the
only level at which those numbers mean anything, so a single global knob invited tuning that could not
be right for every profile at once.

### Lost in the Middle / RAG grounding

Section order is not arbitrary. Models attend most reliably to the beginning and end of a long
context and least reliably to its middle, so the target symbol's own source goes first and the
instruction goes last, with supporting material in between.

The same finding drives failure localization: when a previous run's compiler or test output is
available, retrieval re-weights toward the code that output implicates, and the target's own chunks
are never truncated to make room.

### Retrieval sufficiency (abstention)

Retrieval may decline a gap it has too little context for, rather than generating a test that will be
discarded later.

Two independent criteria, either of which can be disabled by setting it to zero:

- **`min_similar_tests_for_generation`** requires at least this many similar-test anchors. It
  **defaults to 0**, so a greenfield repository with no indexed tests is not blocked from ever
  starting. Set it to 1 or more to require that the model has an example of the repository's
  conventions before it writes anything.
- **`min_similarity_cosine`** (default 0.5) requires that some retrieved similar chunk reaches that
  cosine similarity to the target. It applies **only when similar chunks were actually found** and
  the target has an embedding — otherwise a greenfield repository would fail a test it cannot pass.

`retrieval.policy.abstention.enabled: false` turns the whole policy off.

### Within-run retrieve cache

One plan build memoizes successful retrievals by symbol, profile, budgets and hint flags. Concurrent
requests for the same key share a single retrieval through singleflight; later lookups hit an
in-memory map.

The cache is per plan build, deliberately. Retrieval reads an index that the same run has just
written, so a longer-lived cache would serve context from before the current index pass.

## Generation

### Two-phase test generation

For unit gaps, generation can run as two model calls instead of one: the first writes a **compilable
skeleton** — imports, the test container, mock setup, named test methods with placeholder bodies —
and the second fills in the assertions, conditioned on that skeleton.

The split exists because a single call has to decide structure and behaviour at once, and structure
errors (a missing import, the wrong test container for the framework) fail the compile before any
assertion is ever evaluated. Separating them lets the second call work against something that already
compiles.

It is skipped for E2E gaps, for extend-existing work, and when no test path can be suggested.

### The tool loop

With `generation.policy.tools.enabled`, the generator can query the index while writing instead of
working only from the context assembled up front: look up a symbol, read a file range, search code,
expand the call graph, read dependency documentation.

The reason is measured rather than aesthetic. Retrieval assembles context once and the model gets a
single turn; against a labelled suite that delivers roughly half the relevant chunks, and without
tools the model has no way to ask for the rest. The loop is what turns a retrieval miss into a
lookup.

It is **off by default** and bounded on four axes — turns, calls per turn, calls per run, and result
size — because an unbounded loop starves the shared LLM limiter. On Ollama it additionally requires
`general.llm.ollama_num_ctx`, since the provider silently truncates tool definitions out of a prompt
whose context window it does not know.

Tools are read-only. Nothing in the registry writes or executes.

### Knowledge sources

Two optional sources supplement the repository's own code:

- **Project intel** scans the repository's markdown and agent `SKILL.md` files and injects the
  relevant parts, so a repository's own conventions reach the model. On by default; the scan's shape
  is fixed in code, and what a deployment varies is whether it runs and which extra paths count.
- **Dependency documentation** ingests docs for direct dependencies — Maven sources jars, NuGet XML,
  `node_modules` type declarations — so the model can read a third-party API instead of guessing it.
  Local only: nothing here reaches the network. Off by default, and its chunks carry a distinct type
  so they can be excluded from ordinary retrieval rather than diluting it.

**Web search** is the one component that sends data out of the process, and every setting on it is a
brake: off by default, an offline mode that serves only from cache, a host allow-list that fails
closed when empty, and deny tokens derived from the repository's own identity so its private names
cannot leave inside a query. The replay cache lives in the repository under test, which means **a
shipped run can commit search queries into the pull request** — worth knowing before enabling it on a
private repository.

### The bootstrap contract

When the optional bootstrap runs, it writes `.asqs/test-stack.json`: an authoritative list of test
libraries that are actually on the classpath, plus exact imports for framework test types resolved
against *this* project's compile classpath.

Generation reads it as a hard allow-list. Without it, the prompt shipped a raw `pom.xml` and left the
model to infer availability — one run had twenty candidates rejected for importing `org.mockito` and
`org.assertj` into a module carrying neither.

The contract is **not** committed. It describes one bootstrap of one ephemeral environment, so a later
run reading a stale copy would treat it as authoritative when it no longer matches. The caches beside
it in `.asqs/` *are* committed, deliberately: that is the only way a cache written inside a per-run
workspace reaches the next run.

## Evaluation and the fix loop

### The runner plan

Both sandbox targets — `docker` and `local` — run the **same argv**, resolved from the repository's
build tooling. A repository's own wrapper scripts (`./mvnw`, `./gradlew`) are deliberately not
invoked; the resolved binary is. Wrappers download a second toolchain at build time, which makes a run
depend on network access at exactly the point the sandbox is trying to isolate.

Compile and test run **offline by default** so a run is reproducible, with dependency restore as the
one networked phase. That is why a docker run against an unwarmed cache fails to fetch dependencies:
either mount the host cache, allow network during test, or use the local target.

An unrecognised `general.sandbox.type` **fails at startup**. It used to fall through silently, and a
typo then green-lit a run that compiled nothing, tested nothing, and shipped.

### Circuit breakers

The fix loop stops early when it is no longer converging, on four independent signals: the same
diagnostic repeating, a previously fixed failure recurring, rounds that change nothing measurable, and
one artifact whose failure fingerprint keeps coming back.

Their thresholds are configurable, and **non-positive means "use the built-in", not "disabled"** — a
threshold of zero would end every loop on its first round.

One subtlety worth knowing: when the fixer's own writes break the compile, the repeated-failure streak
**pauses** rather than resetting. Resetting made discard unreachable, because a fixer that alternated
between two broken states never accumulated a streak.

### Discard and stability

A run is stable when evaluation passes outright, or when it still passes after discarding artifacts
that repeatedly failed. Discarding is bounded: it only happens when at least one generated test still
passes, so a run never reports success by throwing away everything it produced.

Discard is also the answer to whole-project evaluation's weakness. One bad artifact would otherwise
hold every good one hostage.

## Measurement

### First-wave quality metrics

Each run records one `first_wave_metrics` row: how many gaps were planned and generated, how many
survived evaluation, how many were discarded, iterations spent, and prompt/completion token usage.

The row is written **once per run** and is `NULL` while running, when evaluation was skipped, or when
it errored — `NULL` rather than zeroes, so "we did not measure" is distinguishable from "we measured
zero".

`asqs-core ab-report` compares those rows across config revisions. This is the instrument for
deciding whether a change to retrieval, generation or the fix loop actually helped: the CLI records
its config file as a revision automatically, so two runs with different settings are joinable without
any extra bookkeeping.

It is a deliberately outcome-shaped instrument. There is no offline IR harness in the open core — no
labelled retrieval suite, no nDCG — so a retrieval change is judged by whether the tests it produced
compiled and passed. Coarser and slower than a golden set, but it needs no fixture corpus to maintain
and it answers the question the change was made to answer.

Two features currently ship **off by default and unmeasured**, awaiting exactly this comparison: the
generation tool loop and the fixer's tool access. Their defaults will not change without one.

## Configuration

The YAML surface is **schema v2**, organised by pipeline step. Every field in
`internal/config/schema_v2.go` carries a doc comment and a test fails on an undocumented key, so the
structs are the reference — there is no second copy to drift.

Three properties are worth knowing before editing a config:

- **Unknown keys fail the load** and name their own path. A typo is an error, not a silent no-op.
- **Every key has an environment variable**, derived rather than tagged: `ASQS_` plus the dotted path
  upper-cased, with a leading `general.` dropped. `general.llm.model` is `ASQS_LLM_MODEL`. An
  undecodable value is an error, so a variable cannot silently do nothing.
- **Every toggle is positive** — `enabled`, never `disable_*`. The runtime still carries some negative
  fields; `TranslateV2ToRuntime` is the single place each inversion happens.

Settings with no plausible per-deployment value are **constants at their consumers, not keys** — chunk
sizing, section budgets, the project-intel scan shape, cache locations. A default earns a change
through measurement, which an operator has no basis to do from a config file. A frozen key left in a
config will fail the strict load; delete it.

Two Ollama-only keys under `general.llm` trade VRAM for latency and reach every step, since
generation, documentation and the fixer all build the same client. `general.llm.ollama_keep_alive`
is sent as `keep_alive` on every chat and embed request: `-1` keeps the model loaded between calls
instead of reloading it after the server's five-minute idle default; an integer is seconds, a
duration such as `30m` also works, and anything else fails the load. `general.llm.ollama_think:
false` is sent as `think` and stops a reasoning model from spending its output budget on a chain of
thought before the answer; unset leaves the server default, and `true` is rejected by Ollama for
models without the thinking capability.

### Upgrading from the pre-v2 schema

**There is no automatic migration.** Start from `config.example.yaml` and move values across.

| pre-v2 | v2 |
|---|---|
| `vcs.<provider>.*` × 4 provider blocks | `general.git.*` — one block for the active provider |
| `runner.type`, `runner.timeout`, images, caches | `general.sandbox.*` |
| `runner.build_tool`, `runner.eval_profile`, commands | `general.build.*` |
| `runner.test_framework_bootstrap`, `runner.e2e_framework_bootstrap` | `bootstrap.test_framework`, `bootstrap.e2e_framework` |
| `runner.max_iteration`, `runner.start_max_iteration` | `fixer.iterations.*` |
| `indexer.max_gaps`, `max_gaps_e2e`, path prefixes | `indexer.policy.*` |
| `database.embeddings_dimension` | `general.llm.embeddings.dimension` |
| `indexer.mono_repo_workspace` | `general.build.workspace.path` |

**Three things to do before the first run on this version:**

1. **Rewrite the config** as above. A pre-v2 file is recognised and told which sections moved.
2. **Run `asqs-core migrate`.** Several schema migrations are deliberately not applied on startup
   because some rewrite tables.
3. **Rebuild the C# indexer** and repoint `indexer.csharp.indexer_dll_path`. C# method FQNames now
   carry parameter lists, so overloads are distinct end to end. A stale DLL emits the old format; the
   indexer detects that and forces a full reindex, so a missed rebuild costs a slow run rather than a
   wrong index — but it keeps costing one until you rebuild.

Behaviour changes worth knowing: an unrecognised `general.sandbox.type` now **fails at startup**
rather than silently green-lighting a run that compiled nothing; `TestData` directories now produce
gaps; Docker Gradle builds invoke the resolved binary rather than `./gradlew`, which can break a build
pinned to a wrapper-specific version; and symbol ids now survive a reindex.

## Deployment

The CLI is one-shot: it runs the pipeline once and exits. There is no service, scheduler or webhook
listener — drive it from cron or CI.

On Kubernetes and OpenShift that means a **Job or CronJob, never a Deployment**: the process exits
when the run finishes, so a Deployment would restart it forever.

**The evaluation sandbox cannot use Docker inside a restricted pod.** Mounting the Docker socket
fails on CRI-O, which is what OpenShift runs — there is no Docker daemon to talk to — and true
Docker-in-Docker needs a privileged sidecar that most clusters forbid. The supported configuration is
`general.sandbox.type: local` with the language toolchains baked into the image, so compile and test
run as ordinary processes in the pod.

Give the pod a PVC for the dependency caches (`general.sandbox.caches.*`). Compile and test run
offline by default for reproducibility, so an unwarmed cache is the usual cause of a first-run
failure to resolve dependencies.
