# Release notes

## Unreleased — the parity port

This release brings asqs-core to feature parity with the internal engine across indexing, retrieval,
generation, evaluation and configuration. Most of it is additive. The parts that are not are listed
first, because they need something from you.

### Operator actions — do these before your first run on this version

**1. Rewrite your config file. There is no automatic migration.**

Configuration is now **schema v2**, organised by pipeline step rather than by accretion, and the
loader is **strict**: an unknown key fails the load and names its own path. A v1 file is recognised
as such and told which sections moved, rather than being partially understood.

Start from `config.example.yaml` and move your values across.
[docs/CONFIG-REFERENCE.md](./CONFIG-REFERENCE.md) lists every key with its type, default and
environment variable. The headline changes:

| v1 | v2 |
|---|---|
| `vcs.<provider>.*` × 4 provider blocks | `general.git.*` — one block for the active provider |
| `runner.type`, `runner.timeout`, images, caches | `general.sandbox.*` |
| `runner.build_tool`, `runner.eval_profile`, commands | `general.build.*` |
| `runner.test_framework_bootstrap`, `runner.e2e_framework_bootstrap` | `bootstrap.test_framework`, `bootstrap.e2e_framework` |
| `runner.max_iteration`, `runner.start_max_iteration`, fix-loop keys | `fixer.*` |
| `indexer.max_gaps`, `max_gaps_e2e`, path prefixes | `indexer.policy.*` |
| `database.embeddings_dimension` | `general.llm.embeddings.dimension` |
| `indexer.mono_repo_workspace` | `general.build.workspace.path` |
| `disable_*` toggles | positive `enabled` toggles — eight keys inverted |

Environment variables are now **derived** from the key path — `ASQS_` plus the dotted path
upper-cased, with a leading `general.` dropped — so `general.llm.model` is `ASQS_LLM_MODEL`. The
database and LLM variables keep their v1 spellings; most others change. Two side effects worth
knowing: float and list variables now actually apply (the old tag-driven loader silently ignored
them), and an undecodable value is now an **error** rather than a silent no-op.

**Config files are v2-only and are not shared with the commercial product.** The two schemas have
diverged; a template from one will not load in the other, and each says so rather than partially
accepting the file.

**2. Run `asqs-core migrate`.**

Several bundles add schema migrations — trigram and generated-column lookup aids, repo scoping,
degree columns, and the parameter-aware simple name. They are deliberately not applied on startup,
because some rewrite tables.

**3. Rebuild the C# indexer.**

`dotnet publish` it again and repoint `indexer.csharp.indexer_dll_path`. C# method FQNames now carry
parameter lists (`OrderService#GetOrder(string)`), so overloads are distinct end to end. A stale DLL
emits the old parameterless format; the indexer **detects that and forces a full reindex**, so a
missed rebuild costs a slow run rather than a wrong index — but it will keep costing one until you
rebuild.

### Behaviour changes

- **An unrecognised `general.sandbox.type` now fails at startup.** It used to fall through silently,
  and a typo then green-lit a run that compiled nothing, tested nothing, and shipped.
- **`TestData` directories now produce gaps.** The test-path heuristic matched the substring `test`,
  so a `TestData` fixture folder was classified as test code and skipped. It is production code for
  gap purposes, and now counts. Expect more gaps on repositories that use that layout.
- **Docker Gradle builds invoke the resolved binary, not `./gradlew`.** A repository wrapper
  downloads a second toolchain at build time, which makes a run depend on network access exactly
  where the sandbox is isolating it. **This can break a build pinned to a wrapper-specific Gradle
  version** — set `general.build.build_tool` or the Gradle image if it does.
- **Ollama output caps changed.** `num_predict` and the truncation behaviour now follow the model's
  declared window rather than a fixed cap; local-path token accounting was previously always zero and
  now reports real usage.
- **The JS/TS indexer always writes JSONL to a temp file** rather than streaming over stdout. Docker
  execution already worked this way. Symbols and edges are identical; a very large single record no
  longer risks a pipe limit.
- **Symbol ids now survive a reindex.** `chunks.symbol_id` is durable, and runs against a git
  checkout accumulate per-symbol history. The first run after upgrading assigns ordinals once and
  builds a unique index; subsequent runs never renumber.
- **Freezing removed some keys entirely.** Chunk sizing, section budgets, the project-intel scan
  shape, cache locations and container CPU/network limits are constants at their consumers now. A
  frozen key left in a config file will fail the strict load — delete it. Notably,
  `websearch.cache_path` no longer accepts an absolute path.

### New, and off by default

Each of these is opt-in. Nothing changes unless you turn it on.

- **Generation tool loop** (`generation.policy.tools.enabled`) — the generator can query the index
  while writing instead of working only from context assembled up front. Requires a provider with
  tool calling; on Ollama it also needs `general.llm.ollama_num_ctx`.
- **Fixer tool access** (`fixer.policy.tools.enabled`) — the same, for repair.
- **Test-framework bootstrap** (`bootstrap.test_framework.enabled`) — detects a repository with no
  usable test stack, installs one, smoke-verifies it runs, and hands generation an authoritative
  allow-list of importable libraries. It **modifies build files**, which is why it is off.
- **Dependency documentation** (`indexer.dependency_docs.enabled`) — ingests docs for direct
  dependencies. Local only; nothing reaches the network.
- **Web search** (`general.websearch.enabled`) — the one component that sends data out of the
  process. Off by default, host allow-list fails closed when empty, an offline replay mode never
  egresses, and query deny tokens are derived from the repository's own identity. Note that its
  replay cache lives in the repository under test, so **a shipped run can commit search queries into
  the pull request**.

The two tool loops ship **unmeasured**. Their defaults will not change without an `ab-report`
comparison showing they help.

### New documentation

- [docs/DOCUMENTATION.md](./DOCUMENTATION.md) — what each phase does and why.
- [docs/CONFIG-REFERENCE.md](./CONFIG-REFERENCE.md) — generated from the schema; a test fails when it
  goes stale.
- [docs/OPENSHIFT-DEPLOYMENT.md](./OPENSHIFT-DEPLOYMENT.md) — moved into this repository so the
  documentation guards cover it.
- [docs/TEST-FRAMEWORK-BOOTSTRAP.md](./TEST-FRAMEWORK-BOOTSTRAP.md) — the bootstrap step in detail.
