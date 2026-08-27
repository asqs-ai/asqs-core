# asqs-core behaviour reference

What the pipeline actually does, and why it does it that way. This is the document the source cites
when a comment says "see docs/DOCUMENTATION.md"; it explains decisions that are not obvious from the
code, not the API surface.

Two companions: [CONFIG-REFERENCE.md](./CONFIG-REFERENCE.md) is the generated key list, and
[TEST-FRAMEWORK-BOOTSTRAP.md](./TEST-FRAMEWORK-BOOTSTRAP.md) covers the optional pre-index step that
installs a test framework.

- [A run, end to end](#a-run-end-to-end)
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

## A run, end to end

One `asqs-core run` is a single pass: index the repository, plan a set of test gaps, generate a test
for each, then evaluate the whole project **once** and repair what fails.

The evaluation being whole-project rather than per-gap is the shape everything else follows from. A
per-gap loop would compile the project once per generated file; a project of any size makes that
unaffordable. So generation writes every artifact first, one compile-and-test pass covers them all,
and the fix loop works on the set. The cost is that one badly generated file can hold the whole run
unstable — which is what [discard](#discard-and-stability) exists to resolve.

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
