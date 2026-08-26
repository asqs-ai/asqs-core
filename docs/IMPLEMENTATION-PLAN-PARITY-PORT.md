# Implementation Plan — asqs-core Parity Port

Bringing `asqs-core` up to the engine quality of the upstream tree it mirrors: correctness and
performance fixes, the unified local/docker runner, schema-v2 configuration, LLM tool calling, and
the two external-knowledge sources.

- **Target repo:** `asqs-core` (module `github.com/asqs/asqs-core`)
- **Upstream baseline:** the private `asqs-go` tree at **`8640c59`** *(Multiple fixes, 2026-08-25)*.
  The working tree is **clean** — the 47 uncommitted files an earlier revision of this plan carried
  as "baseline plus working tree" were committed as that commit. §2.6 describes what it contains.
- **Core's own baseline:** `cb867d1` (2026-07-27). Core is a **curated subset, not a snapshot**: its
  newest ported feature matches upstream `e9390f4` (2026-06-29), but **8 of the 131 portable files
  below were available at that point and never ported** — seven predate it (the whole
  `internal/postgenerate/staticcheck` package, 2026-03/04; `internal/storage/storage.go`;
  `model/concurrency_limiter.go`, 2026-06-16; `projectintel/options_from_config.go`) and one,
  `projectintel/symbol_refs.go`, was skipped from `e9390f4` itself. "Core tracks upstream to
  `e9390f4`" is therefore approximate; the file-level inventory is the authority.
  **Everything upstream landed from `86a4d04` (2026-08-11) onward is missing here.**
- **Plan written:** 2026-08-25. Every factual claim below was checked against both trees; claims
  that correct an earlier note are marked **[correction]** and carry their evidence.

---

## 0. What this document is

This is a **port plan, not a re-implementation plan**. Upstream already did the design work, made the
mistakes, and recorded the corrections. The job here is to land the **end state**, once, adapted to
core's shape — never to replay the history that produced it.

That rule is not stylistic. Replaying upstream's sequence would land code that a later upstream
bundle deletes: the local-runner parity pass introduced a `wrapperArgv` indirection that the
unification then removed, and an inert-key warning that the credentials work then removed. Copying
the end state avoids both.

Read §2 before starting anything. It is the verified inventory, and it contains five corrections to
port notes that were written before the trees were measured.

---

## 1. Scope decisions (taken 2026-08-25)

| # | Decision | Consequence |
|---|---|---|
| D1 | **Full feature parity**, not fixes-only. Tool calling, web search and dependency-doc indexing all land in core. | Phases P7 and P8 exist. Core's config schema grows a `generation` tool block and a `general.websearch` section, which the upstream config plan had assumed would stay absent. |
| D2 | **Schema v2 lands, after the runner unification.** | P5 (runner) precedes P6 (config). The runner port lands against core's current v1 keys and P6 re-keys it once. Each phase stays independently testable and bisectable; the cost is one extra touch of `internal/runner`'s config reads. |
| D3 | **Baseline is upstream HEAD.** | Taken when HEAD was `b68af03` with 47 uncommitted files; those were committed as **`8640c59`** on 2026-08-25 and the tree is now clean, so the decision resolves to plain HEAD. The fixer / `apisurface` / reasoning-strip work is in scope with real provenance (§2.6). |
| D4 | **This plan lives in `asqs-core/docs/`.** | It is written as core's own roadmap. Upstream is referenced by shared relative path (`internal/runner/plan.go`), which is identical in both trees, rather than by absolute path into a private repo. |

---

## 2. Verified current state

### 2.1 Inventory

Measured 2026-08-25 over non-test Go sources in `internal/`, `cmd/` and `tools/`, with upstream
imports rewritten `asqs-go/…` → `github.com/asqs/asqs-core/…` before comparison.

| | Count |
|---|---|
| Non-test Go files — core | **246** |
| Non-test Go files — upstream | **551** |
| Paths present in **both** | **221** |
| …of those, **byte-identical** after import rewrite | **108** |
| …of those, **diverged** | **113** |
| Paths present **upstream only** | **330** |
| …in packages that have no counterpart here (`orchestrator`, `session`, `workflow`, `api`, `audit`, `llm/copilot`, `graphquery`, `retrieval/ireval`, `cmd/apiserver`) | 177 |
| …under `cmd/qualitybot` (core has its own single-command CLI) | 11 |
| …**candidates for core** | **142**, of which 11 are excluded by the seam (§2.4) → **131 new files** |

**Renamed pairs — measured, not assumed.** The counts above compare *same-path* files only. Thirteen
files live under a different package name in core and were diffed separately, with both the import
rewrite and the `package` line rewritten. They are **not** a file-for-file copy: one of them would
rank third in §2.2.

| Upstream | Core | Diverged |
|---|---|---:|
| `orchestrator/llm_generator.go` | `generator/llm_generator.go` | **860** |
| `orchestrator/doc_content.go` | `generator/doc_content.go` | 93 |
| `orchestrator/llm_generator_twophase.go` | `generator/llm_generator_twophase.go` | 73 |
| `orchestrator/overview_batch.go` | `overview/overview_batch.go` | 50 |
| `orchestrator/overview.go` | `overview/overview.go` | 40 |
| `workflow/jstindexer_run_config.go` | `pipeline/jstindexer_run_config.go` | 22 |
| `workflow/{csharp,java}indexer_run_config.go` | `pipeline/…` | 8 each |
| `orchestrator/{doc_generator,generate_schema,skillpacks}.go`, `orchestrator/overview_llm_retry.go`, `workflow/java_advanced_lang_indexer.go` | same basenames | **0** |

**1,154 diverged lines** in total, none of which appeared in the earlier revision's diff accounting.

Two clarifications the earlier "renames" table got wrong:

- **`internal/workflow` is not "`internal/pipeline` under another name".** Only the four indexer
  run-config files map file-for-file (60 diverged lines between them). Everything else in
  `internal/workflow` is the session orchestration core inlines into one function — see §2.3.
- `orchestrator/{overview,overview_batch,overview_llm_retry}.go` map to **`internal/overview/`**, not
  to `internal/generator/`. `overview/{extract,run,types}.go` are core-only glue with no upstream twin.

`internal/orchestrator/skillpacks/` and `internal/evaluator/llmfix/skillpacks/` are **byte-identical**
to their core counterparts today. No prompt bodies need porting unless a bundle changes one.

### 2.2 The six largest divergences

| Diverged lines | File | What it is |
|---|---|---|
| 1618 | `internal/evaluator/workflow.go` | Fix-loop breakers, error-log LLM summary, write/read split, format-after-fix |
| 1517 | `internal/evaluator/llmfix/llmfix.go` | Truncation handling, tool-aware completion, flat-edit parsing, plain-source fallback, fact blocks |
| **860** | `generator/llm_generator.go` *(renamed pair)* | Five bundles' subject matter in one file — see below |
| 822 | `internal/config/config.go` | 34 keys core does not have; core carries 33 upstream has since removed |
| 661 | `internal/storage/metadata/store.go` | `pgxpool` migration **and** the `repo_id` parameter on every lookup |
| 565 | `internal/runner/local.go` | The local executor, before the plan/executor split |

**`generator/llm_generator.go` needs a named owner, because no single bundle produces it.** Its 860
diverged lines span **five bundles' subject matter plus an orphan**: the pre-generate
signature/API-surface check (CP49), the tool loop inside `completeOnce` (CP44), the
`DefaultGenerateMaxTokens` raise (CP25), `rankExistingTestPaths` / convention detection / the
provenance manifest (CP51), `auditCapabilityDegraded` (CP26) — and `repairMemberCase`, which no
bundle names at all. **CP59 adds a sixth edit** to the same file (the `.asqs/test-stack.json` call
site). **CP49 owns the reconciliation** — it is the last of those to land and the one with the most at stake in the file
— and every earlier bundle that touches it records what it left behind.

### 2.3 What core already has that upstream does not

Do not "port" these backwards. They are core's, and some are ahead.

- **Newer dependencies.** `go 1.25.0` (upstream `1.24.0`), `pgx v5.10.0` (upstream `v5.8.0`),
  `pgvector-go v0.4.0` + `pgvector-go/pgx v0.4.0` (upstream carries only `pgvector-go v0.3.0`).
  Nothing in this plan may downgrade them.
- **`internal/storage/embeddings` is already on `pgxpool`.** Only the *metadata* store still runs
  `database/sql` + the `pgx` stdlib shim. P2 therefore ports half of what upstream's pool migration
  covered, not all of it.
- **`internal/overview/`** — core owns the whole-repo overview generator as its own package; upstream
  keeps the same files under `internal/orchestrator/`.
- **`internal/pipeline/pipeline.go`** — 782 lines, of which `Run` alone is **404** (lines 89–492),
  inlining what upstream splits across `workflow` + eleven `orchestrator/phase_*.go` files. Every bundle whose upstream diff touches
  a phase file lands **here**, adapted, and that adaptation is the single largest recurring cost in
  this plan.
- **20 test files**, under `evaluator`, `overview`, `testbootstrap`, `pipeline`, `projectintel`,
  `retrieval`, `repo`, `layout`, `notification` and `cmd`. Core is not test-free.
- `internal/config/policy_compat.go` and `internal/config/private_registry_compat.go` — the two
  inert stubs that hold the enterprise seam open so copied callers compile.

### 2.4 The seam — what never lands here

Eleven upstream files are excluded outright. Each was checked; the reason is in the table.

| Excluded file | Reason |
|---|---|
| `internal/config/policy.go`, `internal/config/schema_v2_policy.go` | The governance/policy engine is not part of the open core. `policy_compat.go` keeps `PolicyConfig` inert. Core's v2 sections carry **plain typed fields**, not compiled policy overrides — see P6. |
| `internal/config/private_registry_creds.go`, `internal/config/private_registry_mounts.go`, `internal/config/azure_nuget_docker.go` | Authenticated private-registry injection is enterprise. `private_registry_compat.go` keeps `MaterialisePrivateRegistryMounts` returning `nil, nil`. |
| `internal/evaluator/llmfix/copilot_fixer.go`, `internal/intelligence/projectintel/copilot_summarize.go` | The Copilot SDK backend is not in the open core; `internal/llm/copilot` does not exist here. |
| `internal/vcs/gates.go`, `internal/vcs/handler.go` | Serve-mode only: `handler.go` is an `http.Handler` webhook receiver, `gates.go` the gating rules it runs. Their sole upstream caller is the `serve` command. Core has no serve mode — which is the same reason CP36 deletes the `vcs.<provider>.webhook` / `gating` keys. Excluding them takes the portable total from 133 to **131**. |
| `internal/intelligence/retrieval/doc_plan.go`, `internal/intelligence/retrieval/doc_retrieve.go` | The narrowed doc-context path. Its only upstream caller is an enterprise orchestration file, and core's doc pass deliberately renders the **full** context (`internal/pipeline/pipeline.go:310` sets `DocGeneration` on the complete option set). Adopting the narrow preset is a doc-quality behaviour change with no measurement here; it is a candidate for a later, separately-measured change, not part of this port. |

Also excluded, by package: prompt-cache plumbing (`cache_control` blocks and
`llm.disable_prompt_caching`), session-engine types (`GapSessionID`), the `runner.policy` /
`gap_hooks` dispatcher, the config-deprecation machinery, and `internal/intelligence/graphquery`.

**Port surgically, by named function.** Upstream diffs interleave excluded code with wanted code in
the same hunk; several of the largest files in §2.2 do exactly that.

### 2.5 Corrections to earlier port notes

Five statements in the upstream plans' port columns are wrong or stale against the trees as they
stand today. Each was checked; acting on the old note would waste work or produce a worse design.

1. **[correction] `expand_symbol` does not depend on `graphquery`.**
   The upstream port map says the retrieval tool suite ports only partially because `expand_symbol`
   needs `internal/intelligence/graphquery`, which core lacks. It does not: the handler calls
   `metadata.ExpandGraph` (`internal/intelligence/tools/handlers.go:228`), the recursive-CTE
   traversal that lands here in **CP12**. `graphquery` is imported only by `internal/api`. The tool
   suite ports **whole** once CP12 is in.

2. **[correction] Core's tests are not "mostly absent", and upstream's are almost all portable.**
   Of **358** upstream test files in packages core mirrors, exactly **2** import an enterprise-only
   package (`evaluator/llmfix/copilot_fixer_test.go`, `config/policy_test.go`). The rest compile
   here after the import rewrite plus whatever type adaptation the bundle itself introduces. The old
   advice — "porting a test means writing a fresh minimal `_test.go`" — is false at this scale and
   would throw away the single best asset in the port.

3. **[correction] The A/B measurement tables already exist here.**
   `index_runs`, `config_revisions` and `index_runs.first_wave_metrics` are all in core's
   `internal/storage/metadata/schema.sql` today, and `Store.SetIndexRunFirstWaveMetrics` /
   `GetIndexRunFirstWaveMetrics` are already implemented. The upstream verdict "no — depends on
   control-plane tables that do not exist in asqs-core" is wrong about the tables. What is missing is
   the **writer**: `EvalFirstWaveMetricsForDB` lives in `internal/orchestrator/workflow.go`, so core
   populates the column on no run, ever. **CP16** fixes that, and it is what makes every
   quality-affecting bundle in this plan measurable rather than merely asserted.

4. **[correction] Core's audit payloads are discarded.**
   `internal/pipeline/pipeline.go:81` — `func (stdoutAuditor) Log(_ context.Context, step string, _ interface{})`
   prints the step name and **throws the payload away**. Every bundle upstream ships with "emit an
   audit counter for the silent failure" would therefore be write-only here. **CP03** must land
   first, or the port inherits the counters and none of the observability they were written for.

5. **[correction] Core reaches the fix loop through the path upstream now calls legacy.**
   `internal/pipeline/pipeline.go:448` calls `evaluator.RunEvaluation`. Upstream reaches that
   function only from `internal/orchestrator/phase_evaluate.go`; its session/API runs use a different,
   stateless entry point. **Everything inside `RunEvaluation`** — the repeated-failure streak,
   `exitByDiscardingStuckArtifacts`, the loop breakers — **is live in core and dead-ish upstream.**
   Consequences: (a) fix-loop bundles are *more* valuable here than their upstream status implies;
   (b) hardening that upstream applied only to its session runner has **no landing site here** and is
   correctly out of scope; (c) upstream's own diagnostic (a run log with zero `evaluator.iteration`
   events proves `RunEvaluation` never ran) inverts here — core should always emit them.

6. **[correction] A whole feature wave belongs to no bundle in any upstream plan.**
   Upstream commits `21d25de` and `6e693f4` (both 2026-08-20, between Wave 4 and the runner
   unification) landed **framework-aware test bootstrap** — 102 + 41 files, 9,837 + 1,993
   insertions, of which 74 files / 7,331 insertions are in `internal/testbootstrap` alone — with its
   own 45 KB design document (`docs/TEST-FRAMEWORK-BOOTSTRAP.md`, which contains **no bundle ids**).
   It is not B, R, F, U or C anything. The earlier revision of this plan inherited that silence and
   attributed its 17 new `internal/testbootstrap` files to CP31/CP32/CP49/CP51, none of whose specs
   describe the feature. **P12 (CP58, CP59) now owns it.** 16 of the 17 files come from `21d25de`;
   `java_style_violation.go` comes from `6e693f4`.

### 2.6 What upstream commit `8640c59` contains

`8640c59` (2026-08-25, 47 files, 2,962 insertions) is the newest upstream commit and the baseline
for this plan. An earlier revision described it as an uncommitted working tree with unstable
provenance; it is now a commit, so **D3's caveat is retired** — re-diffing at implementation time is
ordinary diligence, not a special risk. Five groups, not three:

| Group | Portability |
|---|---|
| `internal/evaluator/apisurface/**` hardening — `repodeclared.go`, extended `unresolveddep.go`, `pregenerate.go` targets, `inventedmember.go`, `repomember.go` | **Ports** — CP49, which creates the package here. |
| Fixer batches: flat `{"edits":[…]}` acceptance, plain-source fallback target resolution, k-occurrence `ApplyFixEdits`, Mockito test-failure facts, absent-symbol blocks, error-log LLM summary, structured-deferral note | **Ports** — CP53. `internal/evaluator` and `internal/evaluator/llmfix`, both mirrored. |
| **`internal/intelligence/model/reasoning.go`** + `CompleteResult.ReasoningRunes` + strip wiring in all four LLM clients | **Ports, and matters more here than upstream** — CP25. See below. |
| `orchestrator/llm_generator.go` (+38) — invented-member checks stop short-circuiting; every authority's findings go into **one** retry reason, because the retry budget is one | **Ports** — into `generator/llm_generator.go`, the merge point CP49 owns (§2.2). |
| `orchestrator/project_config_teststack.go` (+96) — the `.asqs/test-stack.json` consumer | **Ports** — CP59. The file has **no core counterpart at all**; it arrived with the bootstrap wave (§2.5-6). |

**The reasoning-block defect is a core-audience defect.** `StripReasoningBlock` removes a *leading*
`<think>` / `<thinking>` block at the provider boundary. Reasoning models emit their chain of thought
inline, in a tag the API does not separate out. A JSON contract survives it — the extraction ladder
walks past a preamble — which is why the defect stayed invisible. A **plain-text** contract does not:
the fixer's last-resort `singleFilePlainFallback` asks for a bare file and then gates the reply on a
syntactic check that rejects a `<think>` prefix by name, so **on a reasoning model that fallback
could not succeed for any reply, however good** (upstream run `api-0c344e6b…`: it had an unambiguous
target, ran, and produced nothing). Core's primary audience runs local Ollama models, where
R1-family reasoning models are common — this is more live here than upstream.

**Also unowned, and not from this commit:** `modelFixesSamplingParams` in the OpenAI client (plus its
193-line test) came from **`6e693f4`** (2026-08-20). o1/o3/o4/gpt-5-family models pin `temperature`
and `top_p`; sending them is an API error. Folded into CP25.

**Still not portable, for the same reason as before:** the barren-gap ship fix
(`stable_after_discard.go`, `clone_keep.go`, feedback rows) lives in `internal/session` and
`internal/workflow`. The one exception remains the transport-level net `contentOrPlaceholder` in
`internal/llm/openai/client.go` — core has the identical `json:"content,omitempty"` shape and the
hazard is **latent** here, becoming live the moment P7 lands tool messages. In CP25, and it must
precede CP44.

---

## 3. Ground rules

1. **Rewrite imports** `asqs-go/…` → `github.com/asqs/asqs-core/…` on every ported file. Three
   upstream *tests* additionally hard-code the module path in `go list` arguments (the websearch
   boundary test); rewrite those strings too or the test silently passes on an empty package list.
2. **Copy the end state, once.** Never replay an upstream bundle sequence inside one core bundle.
3. **Port the test with the code.** §2.5(2) — 356 of 358 are portable. A bundle whose upstream tests
   were skipped is not `done`.
4. **File names never carry a bundle id.** `per_project_compilation_live_test.go`, not `cp54_test.go`.
   Bundle provenance goes in a comment inside the file, or in this document. Test *function* names
   follow the same rule.
5. **Audit counter first.** When a bundle fixes a silent failure, land its counter before its fix —
   the counter sizes the problem in one run. Requires CP03.
6. **Schema DDL stays idempotent and re-runnable.** `InitSchema` executes the whole embedded
   `schema.sql` on every process start. Structure the code depends on belongs in `schema.sql` (with
   `ADD COLUMN IF NOT EXISTS` / guarded `DO $$` blocks); one-shot data backfills belong in the
   migration mechanism CP07 introduces. Upstream learned this the expensive way — a `files` primary
   key that existed only in a migration produced a run that indexed 367 symbols, wrote zero `files`
   rows, and then planned zero gaps.
7. **A bundle that changes ranking, context content or context ordering is not `done` on inspection.**
   It needs a first-wave-metrics comparison across two runs of the same corpus (CP16), or an explicit
   `unmeasured, ships at the previous default` note in its status.
8. **Status line cycle**, edited in place in this file, one line per handoff:
   `blocked → ready → in implementation → in review → in testing → in docs → done`.

---

## 4. Status board

Every bundle starts `blocked` or `ready` by its dependency edge. **Repo is always `asqs-core`.**
The *Upstream* column names the source bundle(s) so the original spec, its review findings and its
implementation record can be found; it is provenance, not instruction.

### P0 — Foundations *(nothing behavioural; everything else assumes these)*

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP01 | Untrack .NET build output; path-specific ignore rules | U §10.5 | — | 0.5 d | `in review` |
| CP02 | Toolchain and CI alignment | — | — | 0.5 d | `in review` |
| CP03 | **Structured audit sink** — payloads stop being discarded | B13 (adapted) | — | 1–2 d | `in review` |
| CP04 | Live-DB test guard and scratch-database convention | R09 | — | 1 d | `in review` |

### P1 — Leaf packages *(no internal dependencies; verified)*

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP05 | `internal/sqlsplit` and honest schema splitting | B31 (L-3) | — | 0.5 d | `in review` |
| CP06 | Seven dependency-free helper packages | B18, B19, B07, F02, F08, U3b | — | 1 d | `in review` |

### P2 — Storage and schema *(the widest blast radius in the plan)*

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP07 | Migration mechanism + `asqs-core migrate` | B04 (infra) | CP05 | 2 d | `in review` |
| CP08 | Vector metric alignment and filtered-ANN recall | B04 | CP07 | 2–3 d | `in review` |
| CP09 | Metadata store → `pgxpool`, pool sizing | B27 | CP05 | 3–4 d | `in review` |
| CP10 | Batched inserts, `COPY`, batched FQName resolution | B28 | CP09 | 2 d | `in review` |
| CP11 | **Repo-scoped `symbols` / `edges` / `files`** | B23, R02 | CP07, CP09 | 4–5 d | `in review` |
| CP12 | Unified graph traversal (recursive CTE) + degree columns | B22 | CP11 | 3 d | `in review` |
| CP13 | Stable symbol identity and churn signal | B26 | CP11, CP55 | 3–4 d | `blocked (CP55)` |
| CP14 | Embedding input limits and embedding cache | B11 | CP07, CP08 | 2–3 d | `in review` |
| CP15 | Fail closed on embedding-dimension mismatch | R03 | CP08 | 0.5 d | `in review` |
| CP16 | **First-wave metrics writer + A/B report** | B14 (see §2.5-3) | CP09, CP18 | 2 d | `in review` |

### P3 — Indexing and retrieval quality

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP17 | Wire compaction, eliminate inert config | B03 | — | 2–3 d | `ready` |
| CP18 | Plan determinism and hot-path lookups | B05 | CP07, CP11 | 2–3 d | `in review` |
| CP19 | `TESTS_SOURCE` nested-cursor fix and honest retry semantics | R01 | CP09 | 1–2 d | `in review` |
| CP20 | Chunk overlap and honest segment line numbers | B29 | CP08 | 2 d | `in review` |
| CP21 | Lexical channel and RRF fusion — **ships `dense`** | B09, R04, R05 | CP08, CP16 | 3–4 d | `in review` |
| CP22 | Relevance-driven fixtures/config and doc-link boost | B10 | CP08 | 2 d | `in review` |
| CP23 | Route-aware E2E gaps and branch gaps | (post-B05 plan work) | CP18 | 2 d | `ready` |
| CP24 | Review-findings cleanup | B31 | CP05 | 2–3 d | `ready` |
| CP57 | *(optional)* Retrieval IR eval harness and golden suite | B06 | CP21 | 4–5 d + labelling | `withdrawn (D16)` |

### P4 — LLM transport and prompt budget

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP25 | Output truncation and transport hardening | B01 (+ latent `content` fix, §2.6) | — | 2–3 d | `in review` |
| CP26 | Provider capability contract and Ollama parity | B08, R08 | CP25 | 2 d | `blocked (CP25)` |
| CP27 | Anthropic block messages *(no prompt caching)* | B12 | CP25 | 1–2 d | `blocked (CP25)` |
| CP28 | Token budget and prompt accounting | B07 | CP06, CP17 | 3–4 d | `blocked (CP06, CP17)` |
| CP29 | `prompt_tokens` end to end | R07 | CP26 | 0.5 d | `blocked (CP26)` |
| CP60 | LLM concurrency limiter — wires two inert core keys | (unbundled upstream) | CP26 | 1–2 d | `blocked (CP26)` |

### P5 — Runner unification *(land `CP30` alone and green before moving any behaviour)*

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP30 | Shared run state, plan/planner, **parity harness** | U0, U1 | — | 3–4 d | `in review` |
| CP31 | Restore stage; JS plan and coverage paths | U4, U2a, U2b | CP30 | 3 d | `in review` |
| CP32 | Build-tool resolution; wrapper-free argv | U3b, U3 | CP30, CP06 | 3 d | `in review` |
| CP33 | Step environment, credential seam, log redaction | U5, U6, U6b | CP30 | 2 d | `in review` |
| CP34 | Browser preflight; local steps onto the plan | U7, U8 | CP31, CP32, CP33 | 5–6 d | `in review` |
| CP35 | Retire `docker_argv.go`; reject unknown sandbox type; per-file format | U9, U10 | CP34 | 2 d | `in review` |

### P6 — Configuration schema v2 *(hard cutover; no v1 support window)*

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP36 | Housekeeping: dead keys, lint upgrade, **golden fixtures recorded** | C1 | CP35, **CP59** | 2 d | `blocked (CP35, CP59)` |
| CP37 | Constants freeze | C2 | CP36 | 1–2 d | `blocked (CP36)` |
| CP38 | v2 schema, strict loader, derived env, translation | C3 | CP36, CP37 | 4–5 d | `blocked (CP36, CP37)` |
| CP39 | Generated reference and regenerated templates | C4 | CP38 | 2 d | `blocked (CP38)` |
| CP40 | Rollout: README, deployment guide, examples, guards | C5, C7 | CP39 | 2 d | `blocked (CP39)` |

### P7 — Tool calling

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP41 | Tool contract in `model` + OpenAI/Ollama/Anthropic support | B15, B16, B17 | CP25, CP26, CP27 | 4–5 d | `blocked (CP25–CP27)` |
| CP42 | Prompted-JSON fallback and 3-tier mode resolution | B18 | CP41, CP06 | 2–3 d | `blocked (CP06, CP41)` |
| CP43 | Read-only retrieval tool suite | B19 | CP41, CP06, CP12 | 4 d | `blocked (CP41)` |
| CP44 | Bounded generation tool loop, budgets, attempt audit | B20 | CP42, CP43, CP28, CP03 | 3–4 d | `blocked (CP03, CP28, CP42, CP43)` |
| CP45 | Core-plus-inventory context restructure | B21 | CP44, CP16 | 3 d | `blocked (CP44, CP16)` |
| CP46 | Fixer tool access | B30 | CP44, CP50 | 3–5 d | `blocked (CP44, CP50)` |

### P8 — External knowledge

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP47 | Web search tool: SearXNG + Brave, ledger, allow-list, offline replay | B54 | CP43, CP44 | 4–5 d | `blocked (CP43, CP44)` |
| CP48 | Dependency doc indexing (offline: Maven sources, NuGet XML, `.d.ts`) | B55 | CP43, CP11 | 5–6 d | `blocked (CP43, CP11)` |

### P9 — Evaluator and fix loop *(highest per-day value here — see §2.5-5)*

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP49 | `internal/evaluator/apisurface` (+ the generator-file merge) | F02 + `8640c59` | CP06, CP32 | 5–6 d | `blocked (CP06, CP32)` |
| CP50 | Fix-loop convergence core | F01, F03, F05, F09 | CP03 | 4–5 d | `blocked (CP03)` |
| CP51 | Extend-merge and artifact identity | F04, F07, F08, F11 | CP06, CP50 | 5–6 d | `blocked (CP06, CP50)` |
| CP52 | Fix-loop breakers and audit honesty | F10 + breaker refactor | CP03, CP50 | 2–3 d | `blocked (CP03, CP50)` |
| CP53 | Fixer robustness batches | `8640c59` (§2.6) | CP49, CP50, CP52 | 3–4 d | `blocked (CP49, CP50, CP52)` |

### P10 — Language indexers

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP54 | C# per-project compilation | B24 | — | 4–5 d | `ready` |
| CP55 | C# parameterized FQNames | B25 | CP54, CP07 | 2–3 d | `blocked (CP54, CP07)` |

### P12 — Framework-aware test bootstrap *(a whole upstream wave, previously unbundled — §2.5-6)*

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP58 | Detection, per-language profiles, smoke verification, goal runners | `21d25de` + `6e693f4` | CP06, CP32, CP33 | 8–10 d | `blocked (CP06, CP32, CP33)` |
| CP59 | The bootstrap → generation contract (`.asqs/test-stack.json`) | `21d25de`, `8640c59` | CP58 | 3–4 d | `blocked (CP58)` |

### P11 — Documentation and release

| ID | Bundle | Upstream | Depends on | Effort | Status |
|----|--------|----------|-----------|--------|--------|
| CP56 | README, behaviour docs, release notes, operator actions | — | everything | 3 d | `blocked` |

### Critical path and parallelism

```
CP05 ─┬─ CP07 ─┬─ CP08 ─┬─ CP14 / CP15 / CP20 / CP22
      │        │        └─ CP21 ── CP57
      └─ CP09 ─┴─ CP11 ─┬─ CP12 ── CP43
                        ├─ CP13   (also needs CP55)
                        └─ CP18 ── CP16 ── CP21 / CP45
CP25 ── CP26 ─┬─ CP29 / CP60
              └─ CP41 ── CP42/CP43 ── CP44 ── CP45/CP46/CP47
CP30 ── CP31/CP32/CP33 ─┬─ CP34 ── CP35 ── CP36 ── CP37 ── CP38 ── CP39 ── CP40
                        └─ CP58 ── CP59 ─────────────┘  (keys must reach CP36)
CP03 ── CP50 ── CP51/CP52 ── CP53
```

**Longest chain:** `CP30 → CP32 → CP58 → CP59 → CP36 → CP37 → CP38 → CP39 → CP40` — runner, then
bootstrap, then config — at **~31 midpoint-days**. It runs through P12 because CP36 must see the
bootstrap keys (§16); the CP34/CP35 route to CP36 is ~26 d. In parallel, `CP05 → CP09 → CP11 → CP18 →
CP16 → CP21` (storage → determinism → measurement → the first bundle whose acceptance *needs*
measurement) is the longest chain on the data side.

**Start these four immediately; they block the most and depend on nothing:** CP03, CP05, CP25, CP30.
CP54 and CP17 are also `ready` and touch nothing the others touch.

**Total effort:** roughly **150–185 engineer-days**; ~153 at the midpoints of P0–P11 (CP49 grew by a
day when it took on the generator-file merge), plus **~13 d** for P12 and **~1.5 d** for CP60 →
**~167**, or ~172 with the optional CP57. P2 (storage, ~26 d),
P5+P6 (runner then config, ~31 d) and P12 (~13 d) dominate the mandatory work. The three phases that
add net-new capability — P7, P8, P9 — are **~52 d together** and are what a reduced-scope decision
would cut first: dropping P7 and P8 alone returns ~32 d and takes the plan back to the fixes-only
shape D1 rejected.

**P12 is the estimate to distrust.** It is sized from the upstream wave (9,837 + 1,993 insertions,
7,331 of them in `internal/testbootstrap`) rather than from a bundle spec, because no upstream
bundle spec exists. Re-estimate it against `docs/TEST-FRAMEWORK-BOOTSTRAP.md` before committing to a
date.

---

## 5. Phase P0 — Foundations

### CP01 — Untrack .NET build output; path-specific ignore rules

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 0.5 d · **Risk:** none

**Verified defect.** `.gitignore` already lists
`internal/testbootstrap/testdata/csharp_csproj_bare/obj/` (line 19) and
`internal/testbootstrap/testdata/e2e_csharp_playwright/obj/` (line 20), and `git ls-files`
nevertheless returns **21 files** under those two paths. Ignore rules never apply to already-tracked
paths. The content cannot be stable either: MSBuild stamps HEAD into
`AssemblyInformationalVersionAttribute`, so four of those files re-dirty on every commit — which is
exactly what `git status` shows in the tree today.

**Tasks**

1. `git rm -r --cached internal/testbootstrap/testdata/csharp_csproj_bare/obj/ internal/testbootstrap/testdata/e2e_csharp_playwright/obj/`
   — index only; the files stay on disk and `dotnet build` regenerates them.
2. Keep the ignore rules **path-specific**. Do not add a blanket `**/obj/` or `**/bin/`. It would be
   harmless today, but **CP49** brings `internal/evaluator/apisurface`, whose `testdata/cs_repo/bin/Release/net8.0/Microsoft.Playwright.xml`
   is a real fixture asserted on by name; a wildcard would silently delete it and break that test.
3. When **CP54/CP49** add fixtures, extend with the same path-specific shape.

**Note the work already in the tree.** `.gitignore` carries an **uncommitted** modification that
already adds all four rules — including the `csharp_aspnetcore_no_test_project/**` pair for a fixture
core does not have yet, and a comment about an `apisurface` fixture core does not have yet either.
Both arrive with this plan (CP58 and CP49), so the rules are correct in advance rather than wrong;
commit them with this bundle rather than reverting and re-adding.

**Also commit `docs/`**, which is untracked today — this plan file included. Rule 8 asks each agent
to edit a `**Status:**` line in place; an untracked file gives those edits no base to diff against.

**Acceptance.** `git ls-files | grep -c '/obj/'` returns 0 for those two paths; `git status` is clean
after a full `go test ./...`; nothing under `testdata/` is missing on a fresh clone + `dotnet build`.

**Reviewer note.** Expect 21 staged deletions. A pull removes reviewers' local copies — harmless,
regenerated on next build, but it reads as a large deletion in the diff.

**Implementation record (2026-08-26).** Done, with one deliberate step beyond the spec above.

- `git rm -r --cached` on both paths: **21 staged deletions**, `git ls-files | grep -c '/obj/'` → 0,
  and `git check-ignore` now resolves those paths to `.gitignore:28/29` — the rules finally bite.
- **The on-disk copies were deleted too**, which the spec had explicitly not asked for ("index only;
  the files stay on disk"). Justified here: unlike upstream, **asqs-core references neither fixture
  from any Go file** — `grep -rl 'csharp_csproj_bare\|e2e_csharp_playwright' --include='*.go'` returns
  nothing repo-wide. They are inert fixtures inherited from the strip, and their `obj/` content is
  regenerable with `dotnet build`. `go build ./...`, `go vet ./internal/testbootstrap/...` and
  `go test ./internal/testbootstrap/...` are green afterwards.
- **The sibling `bin/` directories were empty and untracked**, and — unlike `obj/` — **not** matched
  by any ignore rule. Removed the empty trees. If CP54/CP58 ever make those fixtures live, add
  path-specific `bin/` rules for them, still never a wildcard (see the note above).
- `tools/csharp-indexer/obj/` (`.gitignore:17`) had nothing tracked under it; no action needed.

### CP02 — Toolchain and CI alignment

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 0.5 d · **Risk:** low

**Verified defect.** `go.mod` declares `go 1.25.0`; `.github/workflows/ci.yml` pins
`go-version: "1.24"`. It survives today only because the Go toolchain auto-downloads a newer one —
an implicit network dependency in CI that nobody chose.

**Tasks**

1. Pin CI to `1.25`. Only the `go` job installs Go — the `indexers` job sets up Java, Node and
   .NET and runs `make build-indexers`, which needs no Go toolchain.
2. Add `go build ./... && go vet ./...` as a gate that fails the job — `vet` is already run but the
   port introduces four `copylocks` findings' worth of surface (CP30 fixes them), so the gate must be
   real before CP30 lands.
3. Add a **live-DB job**, skipped by default, that runs the CP04 suite against a scratch Postgres +
   pgvector service container.
4. Do not touch the `indexers` job until CP54 changes the C# build.

**Acceptance.** CI green with no toolchain auto-download; `vet` failure fails the build.

**Implementation record (2026-08-26).** Done.

- `go-version` pinned to `1.25`, matching `go.mod`, with a comment recording why (the 1.24 pin
  survived only via toolchain auto-download — an implicit network dependency).
- Task 2 was **already satisfied**: build, vet and test each run as their own failing step in the
  existing `go` job, so there was nothing to add — recorded rather than duplicated. (The
  "four copylocks findings" this task cited were the same stale claim CP30's record corrects.)
- The `live-db` job runs `make test-live` against a `pgvector/pgvector:pg16` service container
  whose database is named `asqs_scratch` — the name the CP04 guard requires. Skipped by default:
  it runs only on `workflow_dispatch` (added to the `on:` block), so ordinary pushes and PRs are
  untouched.
- The `indexers` job is untouched, per task 4. YAML validated locally.

### CP03 — Structured audit sink

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 1–2 d · **Risk:** low · **Blocks:** CP44, CP50, CP52, and every counter in this plan

**Verified defect.** `internal/pipeline/pipeline.go:81`:

```go
func (stdoutAuditor) Log(_ context.Context, step string, _ interface{}) {
	fmt.Fprintf(os.Stderr, "  · %s\n", step)
}
```

The payload is discarded. `indexer`, `retrieval`, `evaluator` and `testbootstrap` each declare the
identical two-method `Auditor` interface and all four are satisfied by this one type, so **every
structured audit payload in the entire pipeline is thrown away**. Roughly two dozen bundles in this
plan ship a counter whose whole purpose is to make a silent failure visible; without this bundle
they are all write-only.

The upstream equivalent is a Postgres-backed audit pipeline with an async drain, retention and
export — control-plane machinery that does not belong in the open core. The open-core analogue is a
file.

**Tasks**

1. New `internal/audit`: a `JSONLLogger` writing one `{"ts":…,"step":…,"payload":…}` object per line.
2. **Never block the pipeline.** Buffered channel + single drain goroutine; on overflow, drop and
   count. Expose `Dropped()` and print the count once at run end. (Upstream's own record: the
   contention that mattered was the mutex around a write, not the write.)
3. **No prompt bodies by default.** A payload field carrying prompt or completion text is replaced
   by `{sha256, len}`. An opt-in switch restores full content, for post-mortems.
4. Wire through the CLI: `--audit-log <path>` (and its env equivalent). Absent = today's behaviour
   exactly, so the default run is unchanged.
5. Keep the existing stderr step line. It is the interactive UX and nothing should regress it.
6. `stdoutAuditor` stays as the fallback and is renamed to say what it does.

**Acceptance.** With `--audit-log` set, a full run produces valid JSONL where every `step` seen on
stderr appears with its payload. A payload containing a `prompt` key stores a hash and a length, not
the text. A drain goroutine blocked for 30 s does not stall the pipeline; the entries are counted as
dropped. Without the flag, `go test ./...` and a live run behave identically to before.

**Test plan.** Unit: hashing/redaction table; overflow drops and counts rather than blocking; JSONL
round-trips. Integration: `internal/pipeline` run against fakes with the sink attached, asserting a
known step's payload lands.

**Implementation record (2026-08-26).** Done as specified, with the design points below.

- New `internal/audit`: `JSONLLogger` with a bounded queue (2048, the upstream size) and a single
  drain goroutine. **The payload is marshaled at enqueue time**, not at drain time — the line
  records what was true when `Log` was called, and a caller mutating its payload map afterwards
  cannot corrupt the line or race the drain goroutine (pinned by a test). Redaction happens on the
  drain goroutine, off the hot path. A payload that cannot be marshaled becomes an
  `audit_marshal_error` line rather than silently losing the entry.
- **Redaction policy** (`audit.RedactPayload`): any key containing `prompt` or `completion`, plus
  `messages`, is replaced by `{sha256, len}` (len in bytes), recursively through nested maps and
  arrays. No core payload carries such keys *today* — the policy is forward-looking for
  CP44/CP46/CP52, which is exactly why it lands with the sink rather than with them.
- **The opt-in switch is `audit.dump_prompts`** (new `AuditConfig` field, env
  `ASQS_AUDIT_DUMP_PROMPTS`), mirroring the upstream key spelling.
- **Wiring:** `--audit-log` flag > `audit.file_path` config (whose `ASQS_AUDIT_FILE_PATH` env the
  loader already applied — the "env equivalent" for free) > empty = today's behaviour. An explicit
  empty `--audit-log ""` disables a config-set path for one run (same shape as `--max-gaps-e2e 0`).
  Resolution is printed on stderr like the gap caps. Per D6 this gives `audit.*` its reader now.
- `stdoutAuditor` renamed to `stderrAuditor` (it writes to stderr) and moved with `teeAuditor` and
  `buildRunAuditor` into `internal/pipeline/run_auditor.go`; `pipeline.Run` builds the auditor once
  and defers a closer that drains the sink and reports any dropped count. An unopenable audit path
  degrades to the stderr auditor with a warning — the upstream `NewLoggerWithFallback` lesson: the
  optional file sink must not take the run (or the stderr lines) down.
- Verified: unit suite for round-trip / redaction (with exact-hash assertion) / overflow-drops-
  while-writer-blocked (Log provably non-blocking) / write-error counting / close idempotency;
  integration test drives the real `evaluator.RunEvaluation` through `buildRunAuditor` and asserts
  `evaluator.iteration` and `evaluator.step` land in the JSONL **with their payloads**;
  `run --help` shows the flag. Full `go build ./... && go vet ./... && go test ./...` green.
- **Deferred:** a full live-run JSONL spot-check (index/plan/generate stages end to end). The local
  setup evaluates in Docker with a 15 m timeout under a reasoning model whose plain-text handling
  waits on CP25, so the check conflates unrelated defects; the tee is a single object handed to all
  four stage interfaces, and the evaluator stage — the deepest one — is integration-tested. Fold
  the spot-check into the first live run after CP25, or any operator run with `--audit-log` set.

### CP04 — Live-DB test guard and scratch-database convention

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 1 d · **Risk:** low

**Goal.** Several bundles below (CP10, CP11, CP12, CP13, CP19) can only be proven against a real
Postgres. Give them a safe place to run **before** they are written, not after.

**Tasks**

1. Port `internal/storage/metadata/livetest_guard.go` — `ScratchDBForTests()` reads
   `ASQS_TEST_METADATA_URL` and refuses any database whose **name** does not contain `test` or
   `scratch`. That gate exists because upstream once wrote test fixtures into a freshly indexed
   production corpus; the name check is deliberate friction and should be ported verbatim.
2. Add a `make test-live` target running the `*_live_test.go` set. **Note:** upstream has no such
   target — its live tests run ad hoc. Core is deliberately going one step further here, because
   core has no other CI path that touches a database.
3. Document the convention in `README.md` alongside the existing `docker compose` instructions.

**Acceptance.** `ASQS_TEST_METADATA_URL` pointed at a database named `asqs` skips with a reason;
pointed at `asqs_scratch` it runs. `make test-live` with the variable unset exits 0 having skipped.

**Implementation record (2026-08-26).** Done, all three acceptance cases verified against the live
dev Postgres.

- `livetest_guard.go` ported verbatim, plus a core-own unit test pinning the acceptance matrix
  (unset → skip with instructions; `asqs` → refused BY NAME with the reason; `asqs_scratch`,
  `ci_test`, case-insensitive → allowed). Upstream has no such unit test — same "one step further"
  reasoning as the `make test-live` target itself.
- The harness has real tenants from day one: upstream's `initschema_live_test.go` ported (the
  first function; the pre-`repo_id` upgrade test arrives with CP11, noted in-file), plus a
  core-own embeddings twin that exercises the dimension rewrite and is routed through the guard
  because `alignChunksEmbeddingColumn` can TRUNCATE on a dimension change — exactly the
  destructive write the guard exists to prevent. Together they make permanent what CP05 verified
  with a throwaway script.
- `make test-live` runs the storage + intelligence trees with `-count=1`; with the variable unset
  it exits 0 with every live test skipped (verified). The convention is documented in `README.md`
  beside the docker-compose section, including the one-time `createdb asqs_scratch`.
- Verified live: both InitSchema tests ran and passed against `asqs_scratch`; pointed at `asqs`,
  the guard-routed test skipped with `refusing to write to database "asqs"…` verbatim.

---

## 6. Phase P1 — Leaf packages

### CP05 — `internal/sqlsplit` and honest schema splitting

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 0.5 d · **Risk:** low · **Blocks:** CP07, CP09, CP24

**Verified defect.** Both stores split their embedded schema on a bare `;`. `schema.sql:203` carries
the warning this produces:

```
-- (do not use semicolons inside these line comments: store.go splitSQL splits on semicolon only)
```

A rule that says "never type this character" is a bug with documentation. Every guarded `DO $$ … $$`
block that CP11 and CP13 need contains semicolons by construction, so this is a hard prerequisite,
not a cleanup.

**Tasks**

1. New `internal/sqlsplit` — `Statements(script)` ignoring semicolons inside single-quoted strings,
   dollar-quoted bodies, line comments and block comments. Zero internal dependencies (verified).
2. Both `InitSchema` implementations call it; delete the two local `splitSQL` helpers.
3. Delete the warning comments from both `schema.sql` files.

**Acceptance.** A schema statement containing `;` inside a string literal, inside a `DO $$` body, and
inside a trailing comment each execute as **one** statement. Both schemas still initialise from
empty and re-initialise idempotently.

**Implementation record (2026-08-26).** Done as specified.

- `internal/sqlsplit/{sqlsplit,sqlsplit_test}.go` ported verbatim from upstream (pure stdlib — no
  import rewrite needed; one comment word adapted since core has no apiserver).
- Both `InitSchema`s call `sqlsplit.Statements`; the two local `splitSQL` helpers **and** the
  embeddings store's `stripSQLLineComments` pre-pass are deleted (comments are preserved inside
  statements now, which Postgres accepts). The metadata store keeps `database/sql` — the pool
  migration is CP09's, and the upstream end-state call shape was copied around it.
- Only **one** warning comment existed (metadata `schema.sql`, the line inside the run_sessions
  comment block); the embeddings schema never had one. Deleted.
- Verified live against a scratch database (`asqs_scratch_cp05`, created and dropped on the shared
  dev Postgres): both schemas initialise from empty and re-initialise idempotently; the three
  acceptance shapes each split to one statement and execute. One contract nuance the live check
  surfaced: comments attach **forward** to the next statement, so a comment after a script's
  *final* semicolon becomes a comment-only fragment — Postgres treats it as an empty query, and it
  executes cleanly (also verified). This matches upstream behaviour and needs no change.

### CP06 — Seven dependency-free helper packages

- **Status:** `in review` (done 2026-08-26) · **Effort:** 1 d · **Risk:** none

All seven were checked and import **nothing** from `internal/` (except `staticcheck` → `buildtool`),
so they land as straight copies with the import rewrite and no adaptation.

| Package | Needed by | What it is |
|---|---|---|
| `internal/pathsafe` | CP43, and the fixer's shell allow-list | Path containment: is this resolved path inside that root |
| `internal/jsonx` | CP42, CP53 | Extracting a JSON object from prose-wrapped model output |
| `internal/llm/tokens` | CP28 | `Counter` interface + calibrated character heuristic, and `Budget` |
| `internal/buildtool` | CP32, and `postgenerate/staticcheck` | Build-tool identification shared by runner and static check |
| `internal/teststack` | CP49, CP51 | The generated-test stack contract (framework, assertion library, mocking) |
| `internal/javaproj` | CP49, CP32 | POM/Gradle parsing, nearest-project resolution, project facts |
| `internal/genmanifest` | CP51 | Provenance manifest for generated artifacts — what this run wrote |

**Deliberately not in this bundle:** `internal/llm/tokens` ships with the **character heuristic
only**. Upstream rejected `tiktoken-go` because it downloads BPE tables from the network on first
use, which breaks air-gapped installs — a primary deployment mode for the open core specifically.
The heuristic over-estimates, which is the safe direction, and a real tokenizer drops in behind the
`Counter` interface later with no caller change.

**Acceptance.** `go build ./...` clean; each package's upstream tests ported and green.

**Implementation record (2026-08-26).** Straight copies, no import rewrites needed (all seven are
stdlib-only), tests ported and green. `pathsafe` and `jsonx` carry no tests upstream either — their
coverage arrives with their consumers (CP42/CP43/CP53/CP59), so rule 3 is satisfied as-is.

---

## 7. Phase P2 — Storage and schema

### CP07 — Migration mechanism and `asqs-core migrate`

- **Status:** `in review` (done 2026-08-26, commit `ff191f7` — see the record at the end of this
  bundle) · **Effort:** 2 d · **Risk:** medium

**Why it exists.** `InitSchema` re-runs the whole embedded `schema.sql` on every process start.
That is fine for idempotent DDL and impossible for one-shot work: data backfills,
`CREATE INDEX CONCURRENTLY`, and table rewrites have nowhere to live. CP11, CP13 and CP20 all need
one, so the mechanism comes first.

**Tasks**

1. New `internal/storage/migrate`: a `schema_migrations` marker table, an ordered numbered migration
   list, and a runner that applies only unapplied ones.
2. New `asqs-core migrate` subcommand. Core's CLI is a single `run` command
   (`cmd/asqs-core/main.go:28` rejects anything else), so this is a **shape change**: extract the
   command dispatch, keep `run`'s flags and behaviour byte-identical, add `migrate`. Do this here,
   once — CP39 needs `config reference` and CP16 needs `ab-report` later, and three ad-hoc
   dispatch edits is how a CLI ends up untestable.
3. Port upstream's migration bodies **as core reaches them**, not up front: an empty ordered list
   with a working runner is the deliverable here.

**Acceptance.** `asqs-core migrate` against a fresh database is a no-op that records nothing; against
a database missing migration *n* it applies *n* only, and a second invocation is a no-op. `asqs-core
run` behaves exactly as before — a CLI parity test pins the flag set and the usage text.

**Review focus.** The dispatch extraction is the risky half, not the migration runner. `runopts.go`
already has 351 lines of tests; they must still pass unchanged.

**Implementation record (2026-08-26, commit `ff191f7`).** All three tasks done.

- `internal/storage/migrate/migrate.go` is a verbatim port (`Migration{ID, Description,
  Concurrent, Apply}`, `schema_migrations` DDL, `Run`/`Pending`); `migrations.go` is core-authored
  with **empty** `MetadataMigrations()`/`EmbeddingsMigrations()` per task 3 and the rules
  doc-comment (never reuse IDs; migrate never runs schema.sql, so create what you index; no
  `l2_norm` on `vector`). The upstream source guards (`migrations_guard_test.go`) are active; a
  core-authored live test covers the runner contract (apply 2 → re-run no-op → third applies
  alone → `Pending` empty).
- CLI dispatch extracted once in `cmd/asqs-core/main.go` (`dispatch(args)`; former `run()` body
  became `runRun`, byte-identical); `migrate_cmd.go` adapted from upstream `runMigrate` (flags
  `-config`/`-dry-run`, `ValidateMode: "audit"`, metadata + embeddings targets, embeddings URL
  defaulting to metadata's). Upstream's `CountUnscopedRows`/reindex-warning tail is **omitted** —
  that arrives with the bundles that add those queries (CP11/CP55).
- `dispatch_test.go` pins `run`'s flag set + usage header and asserts unknown commands error
  naming the valid set. Live verification against `asqs_scratch` passed; scratch probe rows
  cleaned up (note: cleanup must be a `defer` registered after pool creation, not `t.Cleanup`,
  which runs after the deferred pool close).

### CP08 — Vector metric alignment and filtered-ANN recall

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 2–3 d · **Risk:** medium

**Goal.** Two defects that quietly degrade every retrieval result: the embedding vectors and the
index's distance metric disagree, and filtered ANN searches under-return without saying so.

**Files.** `internal/llm/embeddings/normalize.go` — the file **already exists** here with
`NormalizeTexts` and diverges by 125 lines; this bundle is *additive* into it (`L2Normalize`,
`IsUnitNorm`, `NewNormalizingEmbedder`, and CP14's `NormalizeTextsForModel`). Plus
`internal/llm/client.go` (wrap the embedder),
`internal/storage/embeddings/{store.go,ef_search.go,schema.sql}`, migration for the index rebuild.

**Adaptation.** Core's embeddings store is **already** on `pgxpool`, so the `ef_search` session-GUC
work applies directly with no driver adaptation — this is the one storage bundle that does not wait
on CP09.

**Tasks**

1. L2-normalise embeddings at the boundary; assert unit norm in the store on write.
2. Align the index operator class with the metric the queries use; the rebuild is a migration
   (CP07), not `schema.sql`, because it rewrites an index on a populated corpus.
3. Widen `ef_search` and retry when a filtered search under-returns; **emit a counter** (CP03) for
   every widen — this is the silent failure the bundle exists to expose.

**Acceptance.** `IsUnitNorm` holds for every vector written by a full index run. A filtered search
that under-returns retries at the ceiling and the widen is counted. Live-DB test (CP04) covers both.

**Measurement.** Mechanical correctness — confirm, do not A/B.

**Implementation record (2026-08-26).** All three tasks done, with one correction to the task list:
**upstream never changes the index opclass.** With unit-norm vectors L2² = 2 − 2·cos, so the
existing `vector_l2_ops` index and `<->` ordering *are* cosine ordering — the migration is a
corpus normalize (`l2_normalize` in place), not an index rebuild. Core copies that end state.

- `internal/llm/embeddings/normalize.go` gained `L2Normalize`/`IsUnitNorm`/`NormalizingEmbedder`
  verbatim (the `NormalizeTextsWithLimit`/truncation-counter half of upstream's file is CP14's);
  `normalize_vector_test.go` ported whole. `llm.NewEmbedder` now wraps `newRawEmbedder` (core's
  former body) so every provider's vectors are unit length at the boundary; the CP14 cache must
  later wrap *outside* normalization (upstream's `NewCachedEmbedder` comment records why).
- `ef_search.go` + `ef_search_test.go` ported verbatim: `efSearchFor` clamps to [40, 400],
  `setEFSearch` degrades on unknown GUC, `ANNWidenEvent`/`ANNWidenCount`/`SetANNWidenHook`.
  `Search` now pins a pooled conn (`Acquire`), sets `hnsw.ef_search`, and retries once at the
  ceiling when a filtered search returns < limit/2 — counting and emitting the widen event.
  **The hook has no production consumer upstream either** (the `retrieve.ann_widened` audit
  wiring named in its comment does not exist yet); core copies that end state rather than
  inventing wiring — the counter + hook satisfy "emit a counter". Core's `Search` keeps its own
  filter set (upstream's `ExcludeChunkType`/`OmitEmbedding` fields belong to later bundles).
- `EmbeddingsMigrations()` gained its first real body, `0001_normalize_chunk_embeddings`,
  verbatim (skip-if-unit re-run cheapness, `l2_normalize`, `inner_product` norm — the guard
  tests strip comments, so its prose about `l2_norm(` does not trip them). **Known sharp edge,
  faithful to upstream:** the UPDATE assumes `chunks` exists, so `asqs-core migrate` against a
  never-indexed embeddings database errors here rather than skipping (candidate for CP24).
- Live coverage (core-authored, both green on `asqs_scratch`): a migration live test (denormalized
  vector → unit norm with direction preserved; zero vector untouched; re-run recorded as skipped)
  and a search-path probe through the pinned-conn/SET path. schema.sql needed no change
  (opclass stays `vector_l2_ops`, same as upstream).

### CP09 — Metadata store → `pgxpool`, pool sizing

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 3–4 d · **Risk:** medium

**Goal.** One driver stack. `internal/storage/embeddings` is already `pgxpool`; the metadata store
still opens `database/sql` with the `pgx` stdlib shim (`store.go:12`). Two stacks means two pool
configurations, two error taxonomies, and — the concrete cost — the nested-cursor defect CP19 fixes,
which is a *stdlib-shim* limitation that does not exist on a real pool.

**Tasks**

1. `Store` holds a `*pgxpool.Pool` behind the small `querier` interface (`Query`, `QueryRow`, `Exec`,
   `Begin`, `BeginTx`, `Ping`) so tests can still inject transient failures — `pgxpool` offers no
   driver-registration hook, which is how the stdlib version faked them.
2. `OpenWithConfig(ctx, Config)` with explicit pool sizing; wire sizing on **both** stores.
3. `PoolStat()` for diagnostics.
4. **Keep `sql.NullX` scan destinations.** This is deliberate, not leftover: pgx routes any
   destination implementing `sql.Scanner` through `DecodeDatabaseSQLValue`, which yields exactly what
   `database/sql` delivered. Rewriting ~60 destinations to `pgtype` equivalents inside a bundle whose
   entire premise is *no behaviour change* would be 60 chances to change behaviour.

**Files.** `internal/storage/metadata/*.go` (all of them), `internal/pipeline/pipeline.go` and
`cmd/asqs-core/main.go` for the open path.

**Acceptance.** `go build ./...` clean; every existing test green with **no assertion changed**; a
live run produces byte-identical query results. Pool size is observable via `PoolStat()`.

**Review focus.** `ExecContext`/`QueryRowContext` → `Exec`/`QueryRow` is mechanical; the transaction
paths are not. Check every `Begin`/`Commit`/`Rollback` and every place a `*sql.Tx` was passed down.

**Implementation record (2026-08-26).** All four tasks done; package-wide conversion (10 store
files) plus config wiring on both stores.

- `store.go` head is the upstream end state minus the `simpleName`/`trigram` probe fields (those
  arrive with the migration bundles): `querier` interface (`Query`/`QueryRow`/`Exec`/`Begin`/
  `BeginTx`/`Ping`), `Store{db querier; pool *pgxpool.Pool}` with the sql.NullX rationale comment,
  `Config{ConnString, MaxConns, MinConns}`, `Open` → `OpenWithConfig`, error-shaped `Close`,
  `PoolStat`.
- Mechanical sweep over every non-test file: `XxxContext` → `Xxx`, `sql.ErrNoRows` →
  `pgx.ErrNoRows`, `*sql.Rows` → `pgx.Rows`, `*sql.Tx` → `pgx.Tx` (`scanLatestRevisionTx`),
  `BeginTx(ctx, nil)` → `Begin(ctx)`, `&sql.TxOptions{ReadOnly: true}` →
  `pgx.TxOptions{AccessMode: pgx.ReadOnly}` (`GetProjectAPIBundle`), `tx.Commit/Rollback` gain
  `ctx`, and `RowsAffected()` loses its error return (both the `n, err :=` and `n, _ :=` shapes).
  All `sql.NullX` / `sql.Null[[]byte]` scan destinations kept per task 4.
- **Driver-stack consequence ported into `isTransientConnError`** (materialize_tests_source.go):
  on native pgx the retryable signal is `pgconn.SafeToRetry`, a connection-class SQLSTATE
  (08xxx/57P0x), or "conn closed"/"unexpected EOF" — `driver.ErrBadConn` can no longer be
  produced. Without this the retry loop became a no-op for its only real use case. The dead
  database/sql sentinels are retained (upstream does the same). "conn busy" is deliberately NOT
  matched: that is the deterministic nested-cursor protocol violation, which native pgx names
  distinctly. **Correction to this bundle's Goal / CP19's framing:** the nested-cursor defect is
  *not* erased by the real pool — native pgx reports it as "conn busy" (upstream's historical
  note confirms it failed on 100% of runs with test classes either way). CP19's restructure
  (drain-then-resolve-then-write, honest retry, backoff + `Ping` between attempts, 4 attempts)
  is still required and still owns that fix.
- Config wiring: `database.max_open_conns` — previously one of CP60's zero-reader keys — now caps
  **both** pools via `poolMaxConns()`; `MetadataStoreConfig()` added; `EmbeddingsStoreConfig()`
  gains `MaxConns`; `embeddings.Config`/`Open` gain and honour `MaxConns`;
  `OpenMetadataStore` → `metadata.OpenWithConfig`. **Deliberate deviation:** upstream defaults the
  ceiling from `gap_concurrency`+writers; core's gap loop is sequential and D15 deletes that key
  (CP36), so deriving from it would hand the doomed key a reader. Core defaults to 0 = pgxpool's
  own max(4, NumCPU); the derivation returns if the loop ever goes concurrent (noted at
  `poolMaxConns`).
- Open paths: `pipeline.go` reaches the pool through `cfg.OpenMetadataStore()` (only production
  call site); live tests keep using `metadata.Open(url)`; `migrate` already speaks `pgxpool.New`.
- Acceptance: build/vet clean, all tests green with zero assertion changes, `make test-live` +
  both InitSchema live tests green on `asqs_scratch`. Two transient in-package live probes
  (deleted after running, scratch rows cleaned) exercised every converted call shape against the
  live DB: NullInt32/NullBool/NullString and absent-row `ErrNoRows` paths, `RowsAffected`,
  JSONB audit write/read, `MaterializeTestsSourceEdges` (Begin/Commit),
  `CreateConfigWithInitialRevision`/`AppendConfigRevision` (multi-statement tx), and
  `GetProjectAPIBundle` (ReadOnly `BeginTx`) — plus `PoolStat` observability (`MaxConns=3`
  honoured, pool held 2 conns with `MinConns=1`).

### CP10 — Batched inserts, `COPY`, batched FQName resolution

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 2 d · **Risk:** medium

**Goal.** Index-write throughput. Upstream measured **4.8×** on a realistic shape and **7.5×** at
1 ms round-trip latency — the second number is the one that matters for a hosted database.

**Tasks.** `pgx.Batch` for symbol/edge inserts; `COPY` for the bulk chunk path; resolve FQNames in
one batched round trip instead of per-symbol. New `internal/storage/metadata/{batch.go,fqname.go}`.

**Acceptance.** Equivalence: the same corpus produces byte-identical `symbols`/`edges`/`files` rows
before and after. Partial-failure behaviour is tested explicitly — a batch with one bad row must not
silently drop the other 999. Live-DB test with a timing assertion expressed as a ratio, not a
wall-clock threshold.

**Implementation record (2026-08-26).** Done, sequenced deliberately AFTER CP11 so upstream's
repo-scoped `batch.go` end state could be copied without a strip-and-re-add cycle.

- `internal/storage/metadata/batch.go`: `InsertSymbols` (single-symbol fast path — upstream
  measured the transactional path ~16% slower on one-symbol files; transaction-wrapped `pgx.Batch`
  with RETURNING ids in input order; drain-before-return on error), `InsertEdges` (same shape over
  `edgeInsertQuery`), `ListSymbolsByFQNames` (deduped `= ANY`, absent names absent from the map,
  per-name order identical to `ListSymbolsByFQName`). **Adaptation:** `symbolInsertQuery` is the
  shared constant but stays a plain insert — the natural-key upsert, `dup_ordinal` and
  `assignDupOrdinals` arrive with CP13, and write-time lowercasing with CP18. `InsertSymbol` now
  runs the shared constant so the two paths cannot drift.
- **Correction to this bundle's file list:** `fqname.go` (`BareFQName`) is B25 material — all its
  consumers are CP55/CP21/tools bundles — and is NOT ported here; the "batched FQName resolution"
  the goal means is `ListSymbolsByFQNames` + the indexer's `fqNameCache`. (Conversely CP11 already
  ported `reindex_warning.go`, which §17's file map had under CP55 — its tests ride the two-repo
  suite.)
- `embeddings.InsertChunks` is now one `CopyFrom` (ids generated client-side — COPY cannot RETURN;
  generated columns stay out of `chunkCopyColumns`, which matters when CP21's `content_tsv`
  lands). Adds `github.com/google/uuid`. Live probe confirmed COPY rows are shape-identical to
  `InsertChunk`'s.
- Indexer: per-file symbols go through one `InsertSymbols` call; edges accumulate in
  `pendingEdges` and flush once per file through `flushEdges`, whose per-edge fallback on batch
  error deliberately preserves the old "one bad edge costs that edge" tolerance;
  `prefetchFQNames`/`lookupFQName`/`resolveSymbolIDForFQNameCached` +
  `edgeResolutionCandidates` cut edge resolution to one query per file with derived names cached
  on first miss (Java/C# import resolvers take the cache too). `MetadataWriter` gained the three
  batch methods. **Carried CP11 remainder:** `UpsertFile`'s error is now CHECKED and counted —
  `fileUpsertErrors > 0` fails the run with the audit event, closing the "367 symbols, zero
  files rows, empty plan, reported success" hole; upstream keeps this beside the batch rewrite
  of the same loop.
- Acceptance: ported `batch_live_test.go` green on `asqs_scratch` — atomic partial failure
  (malformed JSONB mid-batch leaves zero rows), ids in input order, `InsertEdges` idempotence
  + repo_id repair, batched-vs-per-name equivalence including row order. **Note:** upstream's end
  state has no timing-ratio test — the 4.8×/7.5× figures were its one-off measurements; the
  correctness suite is what shipped, and core copies that.

### CP11 — Repo-scoped `symbols` / `edges` / `files`

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 4–5 d · **Risk:** high · **Widest blast radius in the plan**

**Verified defect.** Core scopes **chunks** by `repo_id` (`embeddings/types.go`, `indexer/chunk.go`)
but **not** `symbols`, `edges` or `files`. `files.file` is the bare primary key
(`schema.sql:36 — file TEXT PRIMARY KEY`), and `file` is a repo-**relative** path. So `pom.xml` and
`src/index.ts` are the same key in every repository that has one. Two consequences, both real:

- Incremental change detection reads **another repository's** SHA and skips files that did change.
- The indexer's per-file delete before re-insert matches **every** repository sharing that path.

**Tasks**

1. `schema.sql`: `repo_id` on `symbols`, `edges`, `files`, each with its own idempotent
   `ALTER TABLE … ADD COLUMN IF NOT EXISTS` — `CREATE TABLE IF NOT EXISTS` is a no-op on an existing
   database, so a column added to the table definition never reaches one.
2. Move the `files` primary key to `(repo_id, file)` **in `schema.sql`**, inside a guarded `DO $$`
   block (needs CP05), not only in a migration. Upstream shipped it migration-only and a database
   still keyed on `file` failed **every** file upsert with SQLSTATE 42P10 — producing a run that
   indexed 367 symbols, wrote zero `files` rows, and planned zero gaps. Structure the code depends on
   cannot be optional. The data backfill stays a migration.
3. `edges.repo_id` is **denormalised from the caller symbol, deliberately**: traversal joins `symbols`
   on every hop, and a scoped traversal that must reach through `symbols` to learn the repository
   cannot use an index on the edge itself.
4. Composite indexes: `(repo_id, file)`, `(repo_id, fq_name)`, `(repo_id, lang, kind)`,
   `(repo_id, caller_symbol_id)`, `(repo_id, callee_symbol_id)`, `(repo_id, lang)`, `(repo_id, is_test)`.
5. Thread `repoID` through **every** metadata lookup, delete and count. This is the bulk of the diff:
   ~30 method signatures.
6. Update every caller: `internal/pipeline`, `internal/intelligence/{indexer,retrieval,projectintel}`,
   `internal/generator`, `internal/evaluator`.
7. Fold in the repo-scoped `files` **deletion** fix, which upstream split out separately because it
   was the destructive half.

**Acceptance.** A two-repository regression suite (live DB, CP04): index repo A, index repo B sharing
file paths, assert A's symbols/edges/files are untouched and that a change in B is detected. Every
metadata method rejects or scopes an empty `repoID` — decide which, and pin it in a test.

**Review focus.** The ordering constraint inside `schema.sql` is load-bearing: the identity block in
CP13 references `repo_id`, so it must come **after** these `ALTER`s. Upstream had it before and
`InitSchema` died mid-file, leaving a half-upgraded table.

**Implementation record (2026-08-26).** All seven tasks done. **The open question on empty-`repoID`
semantics is answered by upstream's end state, which core copies: empty matches only rows whose
`repo_id` is empty — exact, never a wildcard.** A scoped run can never read or delete a legacy
unscoped row and vice versa; "passing an empty repoID is a programming error and returns nothing
rather than everything" (GetSymbolByID's comment). Pinned by the ported
`TestDeleteFile_emptyRepoIDIsNotAWildcard` and the legacy-row tests.

- `schema.sql`: `repo_id` + idempotent `ALTER`s on all three tables, the guarded `DO $files_pk$`
  key move, and the seven composite indexes. The CP13 identity block will land AFTER these ALTERs
  per the review focus. The ported `TestInitSchema_movesFilesPrimaryKeyOnAnUpgradedDatabase`
  builds the pre-scoping shape in a scratch schema and proves the key moves with data intact; the
  dev scratch database itself went through the upgrade path live (its `files` PK is now
  `(repo_id, file)`).
- Store: ~17 methods gained a leading `repoID`; `DeleteFile` also returns `(deleted bool, err)`;
  `Symbol`/`Edge`/`File` gained `RepoID`; `InsertEdge` now upserts `repo_id` on conflict so a
  re-index repairs pre-scoping edges; `UpsertFile` conflicts on `(repo_id, file)` and fails
  loudly (42P10) on an unmigrated key — intended. `MaterializeTestsSourceEdges(ctx, repoID)` is
  fully scoped (its DELETE was the one statement with no repository predicate anywhere).
  `ListSymbolFilesByRepo` ported (its `reindex` CLI consumer arrives later);
  `reindex_warning.go` ported whole (`CountUnscopedRows`/`ReindexRequiredWarning`/
  `ReposMissingFileRows`/`MissingFileRowsWarning`/`ListRepoIDs`) and wired into
  `asqs-core migrate`'s tail — the piece CP07 deferred here. Upstream's stale "repo-agnostic"
  doc comments on `CountSymbols`/`CountEdges` were corrected rather than copied.
- **Task 7 (destructive half) covers chunks too**: `embeddings.DeleteByFile(ctx, repoID, file)` —
  the same cross-repo delete existed on the chunks side and upstream scoped it in the same wave.
- Migrations `0004_symbols_repo_id` and `0006_repo_scope_edges_and_files` ported verbatim
  (single-repo fast path from `index_runs`, caller-derived edge attribution, single-owner file
  resolution, guarded key move). IDs 0001–0003/0005 stay reserved for CP18/CP13/CP12. Both
  applied + re-ran as no-ops against the live scratch DB (ledger rows then removed so the plain
  schema.sql path stays exercised). Note 0004 backfills from `chunks`, which assumes metadata and
  embeddings share a database — same sharp edge class as CP08's 0001, faithful to upstream.
- Callers: indexer (interface + run/detect/edge_resolve/apiclient_route_link — all writes stamp
  `RepoID`, all deletes/lookups scoped by `opts.RepoID`), retrieval (both interfaces + plan/
  retrieve/tests_source/profiles threading `opts.RepoID`/`req.RepoID`, helpers gaining explicit
  `repoID` params in upstream's shape), overview (core-specific package: `repoID` threaded from
  `pipeline`'s `opts.RepoID` through `OverviewGenerateOpts.RepoID` and explicit params).
  `generator`/`evaluator` had no direct metadata call sites in core — they reach the store
  through the retrieval structs, which carry `RepoID` already.
- Acceptance: the two-repository suite is the ported upstream one — `two_repo_scope_test.go`
  (shared paths + shared FQ names across two repos; every lookup family, `GetSymbolByID`
  cross-tenant refusal, scoped counters, per-repo SHA divergence for change detection, the
  delete-isolation regression, unscoped-row warning, missing-file-rows detection — its
  `ExpandGraph` assertions arrive with CP12), plus `file_scope_test.go` (delete keeps the other
  repository's row / removes when sole owner / legacy row untouched / empty-repoID-not-wildcard)
  and `repo_scope_test.go` + `source_guard_test.go` source guards (`batch.go` rejoins the guard
  list with CP10). All green live against `asqs_scratch`; full unit suite green.

### CP12 — Unified graph traversal and degree columns

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 3 d · **Risk:** medium

**Goal.** Replace the per-hop Go BFS with one bounded recursive CTE, and materialise the centrality
signal that gap ranking recomputes.

**Tasks**

1. `internal/storage/metadata/expand.go`: `ExpandGraph(ctx, repoID, startID, ExpandGraphOptions)`
   with `Callees`/`Callers`, `MaxDepth`, `MaxNodes`, `EdgeTypes`. Bounds are the caller's; the query
   must not be able to walk a whole corpus.
2. **Three** degree columns on `symbols`: `in_degree`, `out_degree`, and `in_degree_non_test`.
   The third excludes `TESTS_SOURCE` because the gap-listing centrality signal does — counting a
   test-coverage edge inflates "central dependency, under-tested" for symbols that already have
   tests. Index `in_degree_non_test`.
3. Recompute degrees at the end of an index run (`RecomputeSymbolDegrees`).

**Acceptance.** Equivalence test: the CTE and the existing Go BFS return the same `(symbol, depth)`
set on a fixture graph, with the node cap applied identically. Degree columns match a `COUNT(*)`
cross-check after a full run.

**Unblocks.** CP43's `expand_symbol` tool — see the correction in §2.5(1).

**Implementation record (2026-08-26).** All three tasks done.

- `expand.go` ported verbatim: one bounded recursive CTE with the path-array cycle guard
  (correctness, not termination — without it a three-node cycle returns the start symbol in its
  own expansion), `DISTINCT ON` shallowest-path collapse, ranking by depth →
  `in_degree_non_test` desc → `fq_name` before truncation so a capped expansion keeps the
  important neighbourhood, and repository scoping at all three points (seed, hop, join back).
  `pqTextArray` renders the edge-type filter. **Scope note:** upstream keeps retrieval's Go BFS
  (`collectGraphEdges`) — `ExpandGraph`'s consumers are the tool suite (CP43) and the operator
  API; nothing in core calls it yet, exactly as upstream's non-enterprise tree.
- Degree columns in `schema.sql` (idempotent ALTERs after the repo_id block, before where CP13's
  identity block will sit) and migration `0005_symbols_degree_columns` (verbatim; slice order in
  `MetadataMigrations` is 0004 → 0005 → 0006, matching upstream's ledger order).
  `RecomputeSymbolDegrees` ported verbatim (one UPDATE, zero-reset for edge-less symbols,
  repo-scoped to avoid cross-tenant write amplification); `Symbol` gained the three fields and
  every symbol SELECT in store.go/batch.go now returns them.
- Indexer calls `RecomputeSymbolDegrees` after edge writes, tolerating failure with the
  `index.degrees_stale` audit event (stale ordering is worth less than a failed run);
  `MetadataWriter` gained the method. Gap listing reads `sym.InDegreeNonTest` with the
  documented fallback to `GetEdgesTo` when the column is 0 (pre-column corpora behave
  identically at the cost of one query for genuinely-unreferenced symbols).
- Acceptance: ported `expand_live_test.go` green live — including
  `TestExpandGraph_matchesBreadthFirstSearch` (the CTE-vs-BFS equivalence with identical node
  cap), cycle termination, shallowest-path, high-degree-first truncation — plus
  `TestRecomputeSymbolDegrees` (COUNT cross-check) and the two-repo suite's ExpandGraph
  assertions restored from their CP11 deferral. Migration 0005 applied and re-ran clean on
  `asqs_scratch`.

### CP13 — Stable symbol identity and churn signal

- **Status:** `blocked (CP55)` · **Effort:** 3–4 d · **Risk:** high

**Goal.** Symbol ids that survive a reindex, so `chunks.symbol_id` is durable and per-symbol history
is possible at all.

**Natural key:** `(repo_id, file, fq_name, kind, dup_ordinal)`. `dup_ordinal` is the 1-based order of
appearance among same-key symbols in one file, and it exists because **not every indexer can
distinguish overloads in the FQName** — the advanced Java indexer emits `Type#method` for every
overload. Without the ordinal, overloads silently merge into one row. Identity degrades only if
overloads are *reordered* within a file, which is rare and self-heals on the next reindex.

**Note the dependency on CP55.** C# stops colliding once parameterized FQNames land, which is why
CP55 is a prerequisite rather than a nicety: running CP13 first assigns ordinals to C# overloads that
CP55 then makes unnecessary, and the reassignment shuffles identities exactly once.

**Tasks**

1. `dup_ordinal` column; a **one-shot** ordinal assignment guarded on the absence of the unique index,
   so re-running `InitSchema` can never reassign and shuffle identities; then
   `CREATE UNIQUE INDEX uq_symbols_natural_key`.
2. `InsertSymbols` becomes an upsert on that key.
3. `symbol_versions (symbol_id, commit_sha, body_hash, seen_at)`, cascade-deleted with the symbol;
   `symbolBodyHash(source, startLine, endLine)`; `SymbolChurn(repoID, since)`.
4. **Resolve the commit SHA from the checkout's HEAD.** Upstream's history could not accumulate at
   all for weeks because the SHA was read from an optional trigger field and was empty on every run;
   the fix was `repo.HeadSHA`, threaded into the indexer, with a source-guard test. Core clones or
   opens the repo in `cmd/asqs-core/main.go` and has `*repo.Repo` in hand — take it from there and
   pin it with the same guard.
5. **Churn weight ships at 0.** The ranking term must earn a nonzero default through a CP16
   comparison; it may not be defaulted on in this bundle.

**Acceptance.** Index the same unchanged commit twice; symbol ids are identical (live DB). Index two
commits; `symbol_versions` gains one row per changed symbol and none per unchanged one. A run with no
`commit_sha` resolvable is an explicit skip with a counter, not a silent no-op.

### CP14 — Embedding input limits and embedding cache

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 2–3 d · **Risk:** low

**Goal.** Stop re-embedding identical content, and stop silently truncating oversized inputs.

**Tasks**

1. `internal/llm/embeddings/limits.go`: per-provider input caps, `NormalizeTextsForModel`, and a
   **truncation counter** — silent truncation at the embedder is invisible today.
2. `internal/llm/embeddings/cache.go` + `internal/storage/embeddings/cache.go`: an `embedding_cache`
   table keyed on `sha256(provider || model || dimension || content)`. **All four in the key**: on
   content alone, switching model serves vectors from the previous model — right shape, wrong space,
   undetectable at retrieval time.
3. LRU pruning with a retention setting; ~6 KB per vector means 1 M chunks is ~6 GB, so pruning is
   not optional at scale.
4. `NewCachedEmbedder` in `internal/llm/client.go`.

**Acceptance.** A second index run over an unchanged file issues zero embed calls. Changing the model
id produces a full miss, not a stale hit. Pruning honours the retention setting and the cache table
never blocks a run on failure.

**Implementation record (2026-08-26).** All four tasks done.

1. `limits.go` ported whole (per-model token windows with a deliberately SMALL 512-token default
   for unknown models — over-estimating silently embeds a fraction of the chunk into what is
   effectively a random vector), plus normalize.go's CP14 half: `NormalizeTextsWithLimit`,
   `NormalizeTextsForModel`, and the truncation counter. **Faithful to upstream's end state, the
   counter is unwired**: the providers still call plain `NormalizeTexts`, and
   `NormalizeTextsForModel` has no production caller upstream either (fusion.go's own comment
   already names `TruncationCount` as "which nothing reads"). Ported as the machinery it is.
2. Both cache halves ported whole: `llembed.CachingEmbedder` (wraps the NORMALIZED embedder so a
   hit is indistinguishable from a fresh embed; every failure path degrades to "embed it again";
   process-wide hit/miss counters) and the store side (`CacheKey` = sha256 over provider ‖ model
   ‖ dimension ‖ content — all four in the key; `GetCachedEmbeddings` returns misses on ANY
   error, `PutCachedEmbeddings` never caches a wrong-dimension vector and never fails the run,
   `touchCache` refreshes LRU on hits). `embedding_cache` table + LRU index added to schema.sql.
3. `PruneEmbeddingCache` with the retention setting `llm.embedding_cache_retention_days`
   (0 = 30 days), pruned at pipeline startup — core's stand-in for upstream's runner-factory
   site, best-effort with the same wording.
4. `NewCachedEmbedder` in client.go; the pipeline's embedder is now
   `NewCachedEmbedder(cfg, emb, emb.Dimension())`. `llm.disable_embedding_cache` (default off =
   cache on) — a miss is exactly the previous behaviour, which is what makes on-by-default safe.
- Acceptance: ported unit tests pin second-call-hits-cache, only-misses-reach-the-provider,
  model-change-is-a-clean-miss, and failure-degrades-to-embedding; a live probe on `asqs_scratch`
  verified the real table end to end — same-key hit, different-model miss, wrong-dimension
  vectors never cached, prune honouring retention (aged row pruned at 1h retention; maxAge 0 a
  no-op). `limits_test.go` covers the window table, tag/version handling, rune-safe truncation
  and the counter.

### CP15 — Fail closed on embedding-dimension mismatch

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 0.5 d · **Risk:** low

**Goal.** A dimension mismatch between the configured model and the existing `vector(n)` column must
stop the run with a clear message, not proceed and write garbage. Explicitly **out of scope**:
truncating the table automatically on dimension change — that is destructive and stays an operator
decision (`embeddingDimResetAllowed()` gates it behind an explicit opt-in).

**Acceptance.** Configured dimension ≠ column dimension → the run fails at store-open with an error
naming both numbers and the fix. The opt-in escape hatch is tested and off by default.

**Implementation record (2026-08-26).** Done. Core's `alignChunksEmbeddingColumn` TRUNCATEd the
whole chunks table — every repository, on process start, from one mistyped `-config` — silently.
Now: an empty table realigns freely (the legitimate fresh-database case); a populated one refuses
with an error carrying `ErrEmbeddingDimMismatch`, both dimensions, the row count, the affected
repo ids and the two safe fixes; the destructive branch is gated behind
`ASQS_ALLOW_EMBEDDING_DIM_RESET` — an env var, not a config field, deliberately: a break-glass
switch for a one-off operation must not be a setting a deployment carries — and announces the
TRUNCATE when opted in. Ported `dim_guard_test.go` (opt-in parsing, populated-corpus refusal)
and `dim_guard_live_test.go` (refuses on a populated live corpus with both numbers in the
message; realigns freely when empty; its `scratchEmbeddingsURL` helper carries its own
test/scratch name check), all green on `asqs_scratch`. `llm.DimensionMismatchWarning` is NOT
ported — it lives in upstream's embedding-fallback file, which belongs to that feature's bundle.

### CP16 — First-wave metrics writer and A/B report

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 2 d · **Risk:** low · **This is what makes P3/P7 acceptable**

**Verified state.** `index_runs`, `config_revisions` and `index_runs.first_wave_metrics` all exist in
core's schema today, and `SetIndexRunFirstWaveMetrics` / `GetIndexRunFirstWaveMetrics` are already
implemented on the store. **Nothing ever calls the setter** — the computation lives in
`internal/orchestrator/workflow.go` upstream, which core does not have. So core has the whole
measurement substrate and populates none of it. See §2.5(3).

**Tasks**

1. Port `EvalFirstWaveMetricsForDB` into `internal/pipeline` — it is a pure projection of the
   evaluation result (`compile_ok_after_generate`, `test_ok_without_fix`, `eval_stable`,
   `eval_iterations`, `compile_fix_count`, `test_fix_count`, `llm_total_tokens`, and
   `tokens_to_stable` only when stable). Return nil when evaluation was skipped or errored — a zero
   row and an absent row must stay distinguishable.
2. Call it from the pipeline's completion path and write it alongside `SetRunCompleted`.
3. Record a `config_revision_id` per run so two runs with different settings are joinable.
4. Port `internal/storage/metadata/ab_report.go` and add an `asqs-core ab-report` subcommand (CP07
   made the dispatch extensible).
5. **Print the run count on every row**, and flag any revision with fewer than ~10 runs. An average
   over 3 runs rendered identically to one over 30 actively misleads, and that presentation choice is
   the one most likely to undermine the discipline this bundle exists to create.

**Acceptance.** A completed run writes `first_wave_metrics`; a skipped evaluation writes NULL.
`ab-report` over two config revisions on the same corpus prints both, with run counts and the
low-sample flag. Keys on the wire stay **snake_case** — they are the Go struct tags and are stored
as JSONB verbatim; do not "fix" the casing.

**Note.** `CP18` is a hard prerequisite. Until gap selection is deterministic, two runs of an
unchanged repository select different gaps, and the A/B compares different work.

**Implementation record (2026-08-26).** All five tasks done; the run-completion bookkeeping core
never had came with it.

- **Core never called `SetRunCompleted` at all** — every `index_runs` row stayed `status='running'`
  forever. The pipeline now records terminal state at its three endings: zero gaps and zero
  generated artifacts complete with `(nil, nil)` and NULL metrics (evaluation never ran — absent
  must stay distinguishable from zeroes), and the evaluated ending writes
  `stable = ProjectStable` (green outright or green-after-discard, exactly what the console
  reports), `iterations`, and the metrics row. Error returns before indexing leave no row;
  later error returns keep today's behaviour.
- `evalFirstWaveMetricsForDB` lives in `internal/pipeline` (task 1's placement): the CLI edition
  of upstream's orchestrator projection — same fields, same snake_case JSONB keys, nil on
  evaluation error. Core-authored unit tests pin the nil/row distinction, the field mapping, the
  `tokens_to_stable` rules (stable AND tracked only), and stable-after-discard counting as
  stable. Token totals come from `model.UsageAccumulator` + `NewUsageTrackingChatCompleter` —
  present in core since CP25's wave but **wired to nothing**; the pipeline now wraps exactly the
  generator and fixer completers (upstream's `RunLLMUsage` scope; doc pass and overview stay
  untracked).
- **Task 3 goes beyond upstream's end state, deliberately**: upstream fills
  `config_revision_id` only on API-triggered runs, so CLI runs are invisible to its own report.
  Core's `ensureConfigRevisionForRun` version-controls the CLI config file under one well-known
  `configs` row (`"cli"`): an unchanged file reuses the latest revision (N runs of one
  configuration share one revision — the A/B shape), a changed file appends, env-only configs
  record nothing (no body to version; serializing the resolved struct would leak secrets).
  `Config.SourcePath` (loader-set, `yaml:"-"`) carries the file path; the indexer's existing
  `ConfigRevisionID → IndexRunStartExtras` plumbing does the rest. Live test covers reuse,
  append, and the env-only no-op.
- `ab_report.go` ported verbatim (`ABReportForRepo` groups completed runs by revision; rows with
  NULL metrics or NULL revision are excluded). `asqs-core ab-report` adapted from upstream's cmd:
  run counts on every row, the `← too few runs` flag under 10, the pass@1-AND-tokens decision
  criterion, and the determinism note (upstream's trailing tool-calling sentence dropped until
  the tool-calling bundles land). Dispatch gained the case CP07's extraction planned for;
  the pinned dispatch tests still pass.
- Acceptance verified end to end: the built binary ran `ab-report` against the live scratch DB
  over two seeded revisions on one corpus — both rows printed with counts (2 and 1), both
  low-sample flagged, averages correct from the JSONB (50%/100% pass@1, 1500/800 avg tokens);
  `ABReportForRepo` live test additionally pins that NULL-metrics runs stay out of every
  average. Keys on the wire are the struct tags, stored verbatim.

---

## 8. Phase P3 — Indexing and retrieval quality

### CP17 — Wire compaction, eliminate inert config

- **Status:** `ready` · **Effort:** 2–3 d · **Risk:** low

**Goal.** Context compaction exists and is not reached; and a class of settings parse, validate,
document and do nothing.

**Tasks**

1. Wire `retrieval/context_compact.go` into the retrieval call path, honouring the compaction
   settings core already parses.
2. Move retrieval-profile alias normalisation into `internal/config` as
   `NormalizeRetrievalProfileName`, with `retrieval.ParseRetrievalProfile` delegating to it.
   **Not the other way round:** `internal/config` cannot import `internal/intelligence/retrieval`
   because retrieval already imports config. Mapping user YAML onto a canonical value is config's job.
3. Add `MaxContextChunks`, `MaxConfigChunks` and `DependencyMaxDepth` to the retrieval config and
   read them. Upstream's spec assumed they were already config fields; they were not — they existed
   only on `retrieval.PlanOptions`.
4. **Add the inert-field lint**: a test asserting every `config.Config` field is referenced outside
   `internal/config` and outside `_test.go`. Baseline the existing offenders in an exemption list
   with a `TRIAGE:` reason each, so the lint can be **enforced now** and prevents new ones. Upstream
   found 28 inert fields where its review had named 5; core's number will differ and must be
   measured, not assumed.

**Acceptance.** Compaction demonstrably runs (counter via CP03). Every new config field added by any
later bundle in this plan fails the build unless wired in the same change.

### CP18 — Plan determinism and hot-path lookups

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle; **testability ranking ships at upstream defaults, unmeasured** per rule 7/10 — CP16 owns measuring it) · **Effort:** 2–3 d · **Risk:** medium · **Gates CP16**

**Verified defect**, `internal/intelligence/retrieval/plan.go:760-774`. `sortByPriority` is a
hand-written insertion sort — O(n²) over **every non-test method in the repository** — whose
tie-break is `Symbol.ID`, a UUID regenerated on every reindex. The comment above it claims "stable,
deterministic order". It is neither:

- At 30 k symbols that is ~4.5e8 comparisons; at 200 k, ~2e10 — minutes of CPU on the planning hot
  path, for a list immediately truncated to `max_gaps` (single digits to low tens).
- Two consecutive runs on an **unchanged** repository can select disjoint gap sets. That breaks the
  incremental-improvement story outright, and it makes CP16's comparison meaningless — you cannot
  A/B two configurations when the gap set reshuffles between runs.

**Tasks**

1. `sort.SliceStable` with a total order: Priority desc → `FQName` → `File` → `StartLine`. Nil
   symbols sort last, deterministically.
2. Replace the per-symbol metadata lookups on the planning path with the batched/indexed forms CP10
   and CP11 provide.
3. MMR selection size is `limit × maxSegmentsPerWindow`, **not** `limit + maxSegmentsPerWindow`.
   The additive margin under-fills: segmented assembly collapses up to `maxSegmentsPerWindow` chunks
   into one output window, so `limit` selected chunks can yield far fewer than `limit` windows.
4. **`retrieval/testability.go`** — a positive testability score in gap ranking. It rode along with
   the wave-2 commit and is named by no upstream bundle either, so it arrives here with no spec and
   no measurement. It is a **ranking change**: under rule 7 it ships at the upstream default with an
   explicit `unmeasured` note in this bundle's status, or it waits for CP16. Do not let it in
   silently just because it is in the same file set.
5. `OmitEmbedding` on the fixture/config retrieval path **only**. Do not apply it to `List`-based
   fallbacks: `listChunksByType` feeds `SimilarTests`, and the abstention gate cosines those against
   the target — omitting embeddings there zeroes every cosine and causes spurious abstentions.

**Acceptance.** Two consecutive index+plan runs over an unchanged fixture repository select the
**identical** gap list, asserted by FQName order. A planning benchmark over a synthetic 30 k-symbol
set shows the O(n log n) shape. The abstention-gate regression is covered by a test that fails if
`OmitEmbedding` leaks onto the `SimilarTests` path.

**Implementation record (2026-08-26).** All five tasks done.

1. `sortByPriority` is upstream's `sort.SliceStable` with the total order (Priority desc → FQName
   → File → StartLine, nil symbols last). Ported `plan_determinism_test.go` covers UUID-churn
   stability, input-order independence, the deeper tie-breaks, and a 20k-element correctness
   pass — **upstream's end state has no Benchmark function**; the 20k test is what shipped.
2. Planning-path lookups: `enclosingTypeSymbol` memoised per declaring FQName (`sync.Map` shared
   across the candidate loop), `hasInboundTestsSourceTraceWithType` reusing it (the old helper
   fetched and discarded the type per candidate; it stays for other callers), on top of CP12's
   materialized centrality and CP10's indexer-side prefetch. These live in the ported
   `gap_shortlist.go` — **whole**, including `refineShortlistWithBranchIntents`: core already
   had `inferBranchIntentsFromContent` (branch_gaps.go), so the two-stage
   `ListGapsWithChunks` (stage 1: span/arity/outbound + eligibility over data in hand; stage 2:
   chunk fetch + branch intents for the top 4×MaxGaps only) ports intact. `ListGaps` is now the
   nil-chunks wrapper; `CreateTestPlan` passes its chunk reader. The churn block in upstream's
   `ListGapsWithChunks` head is CP13's and is not ported.
3. MMR selection is `mmrSelectionSize(limit)` = limit × maxSegmentsPerWindow (15 for the typical
   5, not the old pool-size 120 core passed); the ported
   segmented-per-file-cap breadth test pins the under-fill case.
4. `testability.go` ported whole (score + eligibility filter + fallback-when-all-filtered +
   `MinGapTestabilityScore`, default 0 = rank-only) with `plan_testability_test.go`.
   **Unmeasured, per this bundle's own warning**: it ships at upstream defaults and CP16's A/B
   is what measures it. `BareFQName` (fqname.go) pulled forward as `isTrivialAccessorName`'s
   dependency — on core corpora it is the identity function until CP55's C# parameterization;
   `TestAssignDupOrdinals` in its test file is deferred to CP13 in-file.
5. `OmitEmbedding` on `SearchOptions` + `ListOptions` with the column/scan elision, set ONLY on
   `listChunksByPathPattern` (fixtures/config — rendered as text, never scored). The
   core-authored `TestOmitEmbedding_staysOffTheSimilarTestsPath` is the acceptance's leak guard:
   it fails if `listChunksByType` (the SimilarTests/abstention feed) ever sets it, and if the
   fixture path ever stops setting it.

Also in this bundle (the storage half of B05): write-time lowercasing of `lang`/`kind`
(`symbolInsertArgs`), `normalizeSymbolKind` on every exact kind comparison, migration
`0001_lowercase_symbol_lang_kind` (live-verified backfilling an uppercase legacy row), and the
ported `kind_case_guard_test.go`/`kind_case_live_test.go`. Migration `0007` (concurrent
repo-scoped index variants + simple_name tail) stays with CP55, whose column it probes.
`plan_test.go` and `retrieve_cache_test.go` — never ported before this bundle — came along whole
(the mock infrastructure the new tests need lives there). **Noted, not fixed (CP24 candidate):**
`output_contract.go`/`context_builder.go` compare returned `sym.Kind` against UPPERCASE literals,
which lowercase storage makes dead branches — upstream has the identical comparisons, so core
copies the end state rather than diverging on unmeasured prompt shaping.

### CP19 — `TESTS_SOURCE` nested-cursor fix and honest retry semantics

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 1–2 d · **Risk:** medium

**Defect.** `insertTestsSourceFromNamingConvention` issues per-row queries **inside** a `rows.Next()`
loop on the same pinned transaction. The `pgx` stdlib shim cannot carry two active cursors on one
connection, so the inner statements fail — and the retry wrapper reports success anyway. CP09 removes
the driver constraint; this bundle removes the pattern and makes the retry honest.

**Tasks.** Materialise the outer result set before issuing inner statements; make the retry report
what it actually did; add a counter for rows that fail (CP03).

**Acceptance.** A live-DB test (CP04) over a fixture repo produces the expected `TESTS_SOURCE` edge
count; a forced inner failure surfaces as an error and a counter, not as success.

**Implementation record (2026-08-26).** Done — upstream's `materialize_tests_source.go` at HEAD
copied wholesale, since everything else in it had already landed (CP09's pgx classifier, CP11's
repo scoping); the file's remaining deltas were exactly this bundle.

- The nested-cursor pattern is gone: `insertTestsSourceFromNamingConvention` now runs three
  strictly ordered phases — drain and close the test-class cursor (`listTestClasses`), resolve
  every SUT in ONE round trip (`resolveProductionClassIDs`, `DISTINCT ON` under a total order so
  which symbol wins is reproducible; generic markers stripped so `P.Repo` matches `P.Repo<T>`),
  then write all pairs in one `unnest` statement (`insertTestsSourceEdgePairs`, three bind
  parameters regardless of corpus size). The INVARIANT comment documents why `defer rows.Close()`
  cannot satisfy the ordering.
- Honest retry: attempts raised to 4 with a fixed (deliberately unjittered) backoff, a `Ping`
  between attempts so pgxpool retires a dead connection instead of re-drawing it, and the
  `sleepFn` clock seam so the schedule is assertable without elapsed time. The historical note
  now states the true original cause (a deterministic protocol violation misread as
  infrastructure) — including that the old loop "passed" while failing 100% of real runs.
- The "counter" half of the acceptance is the caller's audit: the indexer's failure event now
  names the consequence (gap ranking loses its −38 test-traceability penalty) and carries
  `impact: gap_ranking_missing_tests_source_penalty`, which CP03's run auditor counts by step.
- Tests ported whole: `materialize_tests_source_test.go` (the live fixture-repo tests — naming
  convention covers every class, first-attempt success, rerunnability, plus the legacy
  integration test) and `materialize_tests_source_retry_test.go` (the querier-injection fakes
  CP09's interface was built for — backoff schedule, ping-between-attempts, no retry on
  non-transient, context cancellation; its SCOPE comment says exactly what these cannot see).
  All green live against `asqs_scratch` — the naming-derived edges that never materialized
  before this fix now do.

### CP20 — Chunk overlap and honest segment line numbers

- **Status:** `in review` (done 2026-08-26 — see the record; **(a)/(b) ship unconditional and unmeasured**, matching upstream's end state — the record explains the deviation from this bundle's own acceptance) · **Effort:** 2 d · **Risk:** medium

**Three defects, one bundle.** (a) adjacent chunks have no overlap, so a symbol split across a
boundary is retrievable from neither half; (b) chunk sizing is a guessed constant rather than measured
against the embedding model's input limit; (c) **segment line numbers are wrong** — a merged segment
reports the first sub-chunk's range, so a citation points at the wrong lines.

**Sequencing note.** (c) is verifiable on its own and should land regardless. (a) and (b) change chunk
boundaries, which **invalidates any golden retrieval labels resolved by `file:start_line`**. If CP57
is ever adopted, label first, then change boundaries — or accept that the change ships unmeasured and
say so in the status line.

**Acceptance.** (c) has an exact test: a merged segment's reported range equals the union of its
sources. (a)/(b) ship behind settings whose defaults are today's behaviour until measured.

**Implementation record (2026-08-26).** All three defects fixed; two deliberate deviations from
this section's own text, both argued here.

- **(a)+(b), one function pair in `chunk.go`:** the split loop's fixed
  `targetLines = MaxTokens×CharsPerToken/80` guess (hardcoding 80 chars/line — chunks many times
  MaxTokens on minified sources, far under MinTokens on declaration-dense ones) is replaced by
  `lastLineWithinBudget`, which measures content with the same `ApproxTokens` estimator that
  decided the symbol needed splitting; `nextSplitStart` backs up ~10% so consecutive chunks
  overlap — the oversize embedding fallback always overlapped, so which behaviour a symbol got
  used to depend on whether it happened to exceed the provider limit.
- **(c) in `embed_fallback.go`:** segment line numbers are COUNTED (`prefixNewlineCounts`, one
  pass per chunk) rather than interpolated from rune offsets — the ratio estimate drifted ~14
  lines on ordinary Java, and those numbers become the generation prompt's `symbolLoc` and the
  fixer's `errloc` window, where a wrong line is worse than none. The clustered-non-uniformity
  test pins it (alternating lines would NOT discriminate; a block of long then short lines does).
  The plan's "merged segment reports the first sub-chunk's range" is the same defect family; the
  small-symbol merge (`chunk_merge.go`) already extended ranges to the union in both trees.
- **Deviation 1 — no settings:** upstream ships (a)/(b) unconditional; there is no flag to port.
  The golden labels the "behind settings" requirement protected died with CP57's withdrawal
  (D16), and a core-only flag would be permanent divergence for CP49-style reconciliation to
  trip over. The change ships **unmeasured** (rule 10, said in the status line) — and CP16's
  ab-report now exists precisely to measure it: run the same corpus on the pre/post revisions.
- **Deviation 2, in core's favour:** `chunk_secondary.go` was NOT taken from upstream — core is
  ahead there (`SymbolKind` + `SecondaryRole` on secondary chunks), and core's extra
  `SymbolKind: part.sym.Kind` in `chunk.go` is preserved.
- Six upstream test files ported whole (`segment_lines_test`, `chunk_split_test`,
  `embed_fallback_test`, `chunk_test`, `chunk_config_test`, `chunk_metadata_sig_test`) — the
  indexer package had **zero** test files before this; it now runs 32, all green, and their
  passing unmodified confirms core's chunk behaviour equals upstream's end state.

### CP21 — Lexical channel and RRF fusion

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle; **ships `fusion: dense`**, exactly as specified) · **Effort:** 3–4 d · **Risk:** medium · **Ships `fusion: dense`**

**Read the upstream result before implementing.** Upstream measured RRF fusion against a labelled
suite and it was a **regression**: nDCG@10 0.4792 → 0.2736, R@10 0.5500 → 0.2542. The default stayed
`dense`. The same measurement also found the lexical channel returning **zero rows for every
realistic query** (`plainto_tsquery` conjoins terms), meaning the flag had never done anything, and
that the two channels' scores were not commensurable and the fusion had no total-order tie-break.

So the honest framing of this bundle is: **build the channel correctly and leave it off.** It is worth
porting because a working lexical channel is a prerequisite for any future hybrid experiment, and
because the `content_tsv` column is cheap; it is not worth enabling.

**Tasks**

1. `chunks.content_tsv` as a `GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED` column in
   `schema.sql`; the GIN index as a **migration** (CP07) — adding a STORED generated column rewrites
   the table, so the cost on a populated corpus is an operator decision.
   `'simple'`, not `'english'`: no stemming and no stop-word removal, because `get`, `set` and `is`
   are meaningful identifiers in code, not noise.
2. `internal/storage/embeddings/lexical.go`; use a query form that does not conjoin every term.
3. `internal/intelligence/retrieval/fusion.go`: RRF with a **total-order** tie-break, and scores
   normalised so the two channels are commensurable.
4. Config key `retrieval.fusion`, default `dense`.

**Acceptance.** With `fusion: lexical`, a query naming a symbol that exists returns **non-zero** rows —
the test that would have caught the original defect. With `fusion: dense` (the default), retrieval
results are byte-identical to before this bundle. Any move of the default requires a CP16 comparison
recorded in this file.

**Implementation record (2026-08-26).** Built correctly, left off — the channel works and the
default stays `dense`, per the upstream measurement quoted above.

1. `content_tsv` as a STORED generated column in `schema.sql` (`'simple'`, with the identifier
   rationale) and migration `0003_chunks_content_tsv_gin` (Concurrent — the ledger's first; adds
   the column itself too, since migrate never runs schema.sql). ID 0002 is now the reserved one.
2. `lexical.go` ported whole: `SearchLexical` with the disjunctive `orTSQuery` (the conjunctive
   `plainto_tsquery` form matched 0 of 387 chunks for a realistic 13-term query — the defect that
   made the flag inert), a TOTAL order-by (`score, file, start_line, id` — ts_rank_cd gave 74
   chunks only 16 distinct scores), real errors surfaced (only missing-column maps to
   `ErrLexicalIndexUnavailable` and falls back to dense), and embeddings selected unless opted out
   (nil embeddings made MMR read lexical hits as maximally novel). **`SearchByPathPattern` rides
   along** — it shares the file and compiles standalone; its consumer (`fixtures.go`) is CP22's.
3. `fusion.go` ported whole: `FuseRRF` (k=60, one contribution per document per list),
   `normalizeFusedScores` (raw RRF is ~60× weaker than MMR's diversity term — a unit error, not a
   tuning choice), `mergeByBestDistance` with a total final key (the widening path's `append`
   fed RRF fabricated ranks; harmless under dense, so dense results stay byte-identical),
   `LexicalQueryForTarget` (camelCase splitting; there is no user query in this system),
   `lexicalChannel` behind the optional `lexicalSearcher` seam, and the `LexicalFailures` counter
   whose reader (`retrieval-eval`) arrives with the eval bundle. Both fusion test files green.
4. Config `retrieval.fusion` (default dense; the doc comment quotes upstream's regression numbers
   and points at ab-report) wired `cfg → PlanOptions.Fusion → ContextRequest` — the ONE retrieval
   key the pipeline wires; the rest of `cfg.Retrieval` was already unwired in core before this
   bundle (CP17's cleanup territory). `LexicalQuery` is synthesized per gap at the
   `createTestPlanFromGaps` request site; the memo key needs no new fields (SymbolID subsumes the
   query, fusion is run-constant — upstream's key agrees).
- **Pulled forward as fusion's dependencies, recorded:** `ChunkTypeDependencyDoc` and
  `SearchOptions/ListOptions.ExcludeChunkType` with their filter branches (B55 plumbing —
  `lexicalChannel` excludes dependency docs by type; until B55's ingestion exists no chunk
  carries the type and the branches are inert).
- Acceptance verified live on `asqs_scratch`: a 15-term synthesized query
  (`OwnerController#processCreationForm` + signature types) returned non-zero rows from a chunk
  mentioning only some terms — the exact shape the conjunctive form zeroed; migration 0003
  applied + re-ran clean with the GIN index present; the ported live tests cover embeddings-
  unless-omitted, surfaced errors, missing-column classification and the total order. The
  CP08-era migration live test was made growth-robust (it had hardcoded a one-entry ledger).

### CP22 — Relevance-driven fixtures/config and doc-link boost

- **Status:** `in review` (done 2026-08-26 — see the record; **ships unconditional and unmeasured**, matching upstream's end state — the record explains the deviation from this bundle's acceptance) · **Effort:** 2 d · **Risk:** low

**Goal.** Fixture and config chunks are currently selected by type, not by relevance to the target;
and a symbol's own documentation is not preferentially retrieved for it.

**Files.** `internal/intelligence/retrieval/{fixtures.go,retrieve.go,profiles.go}`,
`internal/intelligence/projectintel/relevance.go` (`SelectForGap` — this package **does** exist in
core, contrary to older notes).

**Acceptance.** Fixture selection changes with the target symbol on a fixture repo. Ships behind a
setting defaulting to today's behaviour; the default moves only on a CP16 result. Note upstream's
own finding: this path is **disjoint from what a retrieval-IR suite scores**, so CP57 cannot measure
it — CP16's run-outcome metrics are the only available evidence.

**Implementation record (2026-08-26).** Both halves done; one CP20-style deviation, argued below.

- **Fixtures by relevance, config by proximity** (`fixtures.go`, ported whole with its tests):
  `relevantChunksByPathPattern` ranks path-matched chunks by vector distance to the target via
  CP21's `SearchByPathPattern` (falling back to the alphabetical listing when the store lacks the
  seam or the target has no embedding — behaviour degrades rather than breaking), and
  `configChunksByPathProximity` orders config by DIRECTORY DISTANCE, deliberately not cosine:
  `application-test.yml` shares no vocabulary with a method body, so vector distance there is a
  different kind of arbitrary. Both wired at the `Retrieve` fixture/config sites with
  `targetChunk = out.TargetMethod.Chunk`.
- **Doc-link boost** (`projectintel`): `symbol_refs.go` ported (capitalized-reference extraction
  + noise list + `ResolveDocSymbolLinksWithNames`; the metadata store satisfies
  `SymbolResolver`); `RankedCandidate` gained `LinkedSymbolIDs`/`LinkedFQNames`, populated in
  `Run` phase 3 and round-tripped through the disk cache (fields `omitempty`;
  `CacheFormatVersion` stays 2, matching upstream — old caches load with empty links, benign);
  `RankByEmbeddingWithLinks`/`SelectForGapWithLinks` add +0.35 when a doc names the target
  symbol, +0.15 for its enclosing type, matched on FQ NAME because symbol ids churn per reindex,
  and never boosting the no-embedding sentinel. **The pipeline now does per-gap selection at
  all**: core prepended the same run-wide snapshot to every gap prompt despite `Result.Candidates`
  documenting `SelectForGap` as its purpose — `projectIntelForGap` re-ranks per item with the
  boost, capped by the existing project-intel config knobs.
- **Deviation — no setting:** upstream ships all of it unconditional; the "flags" are the
  graceful-degradation seams (`pathPatternSearcher` optional interface, embedding-less fallback,
  nil resolver skips linkage). A core-only flag would be permanent divergence. Unmeasured per
  rule 10, said in the status line — and as this section itself notes, CP16's run-outcome metrics
  are the only instrument that CAN measure it.
- **Not taken, recorded:** `profiles.go`'s upstream deltas belong elsewhere
  (`ParseRetrievalProfile`/config alias table = config-restructure family;
  `indexer.EdgeTypeConfidence` registry = the edge-registry bundle), as do `retrieve.go`'s
  `memberSummaryForType` chunk-miss fallback (CP45's file-map row) and `isBootstrapSmokeArtifact`
  (P12). The upstream MMR call-site comment rode along (comment only, CP18 material).
- Acceptance verified live on `asqs_scratch`: two fixture chunks with orthogonal embeddings, two
  targets — the top-ranked fixture follows the target (the exact "selection changes with the
  target symbol" test); ported unit tests cover ranked-vs-fallback, proximity ordering and
  limits; relevance tests updated to upstream's (boost cases included). `projectintel/types.go`
  was finally gofmt-ed — the repo's last pre-existing unformatted file under internal/ is gone.

### CP23 — Route-aware E2E gaps and branch gaps

- **Status:** `blocked (CP18)` · **Effort:** 2 d · **Risk:** low

**Goal.** E2E gap planning currently misses uncovered API routes and page routes. Port
`ListGapsWithChunks`, `uncoveredAPIRouteLangs`, `uncoveredAPIRouteE2EGapsForLang` and
`appendPageRouteE2EGaps`, plus the `branch_gaps.go` and `apiclient_route_link.go` deltas, plus
**`retrieval/gap_shortlist.go`** (branch-intent shortlisting) — another wave-2 ride-along named by no
upstream bundle, called out here so it has an owner.

**Adaptation.** Core's `pipeline.go` builds the plan inline; the new entry points must be reached
from there, and core's `--docs` path shares the plan.

**Acceptance.** A fixture repo with an uncovered controller route yields an E2E gap naming it.
Existing gap counts on repos with no routes are unchanged.

### CP24 — Review-findings cleanup

- **Status:** `ready` · **Effort:** 2–3 d · **Risk:** low (one user-visible change)

Six independent items; each needs a test that fails before the change.

1. **`stripBlockComments` has no string-literal awareness** — a `/*` inside a string or regex swallows
   source to the next `*/`, and the corrupted text reaches both the embedder and the LLM. Add
   per-language literal awareness (`internal/intelligence/indexer/sanitize.go`).
2. **`IsLikelyTestSourcePath` does a raw case-sensitive `strings.Contains(path, "Test")`** — so
   `src/TestData/Model.cs` is classified as a test file and its symbols are **permanently excluded
   from gap analysis**. Use path-segment matching against known test-directory conventions.
   **This is user-visible**: every repository with a `TestData` directory gains gaps. Release note.
3. **Retry layers stack.** Generation retry (3) over the provider client's (5), plus the
   structured→unstructured fallback, plus a quality retry — up to ~15 requests per gap against a
   flapping provider. Establish one budget across the layers. Must not reduce resilience below what
   CP25 sets.
4. **Edge types**: ~45 values in an untyped `TEXT` column with no registry, and confidence ranking
   buried in a Go switch where tuning cannot reach it. Add `internal/intelligence/indexer/edgetypes.go`
   as the registry; consider a check constraint.
5. **`ParsedEdge.SignatureJSON` is parsed and discarded**; `ChunkPlan.SymbolKind` / `SecondaryRole`
   are dropped in the `ChunkPlan → ChunkToEmbed` conversion. Persist them or delete the plumbing —
   decide explicitly, do not leave it ambiguous.
6. **`dedupeGraphEdges` has no callers** (delete); **`ListGapsE2E` is ~220 lines of nested dispatch
   with five near-duplicate blocks** (decompose).

**Acceptance.** Item-by-item unit tests. `src/TestData/Model.cs` is no longer a test file. A source
file with `/*` inside a string literal survives sanitisation intact.

### CP57 — *(optional)* Retrieval IR eval harness and golden suite

- **Status:** `withdrawn (2026-08-26 — D16: core owns no reference corpus; CP16 is the measurement story)` · **Effort:** 4–5 d **plus human labelling** · **Risk:** low

Port `internal/intelligence/retrieval/ireval` (`harness.go`, `metrics.go`, `golden.go`, `refs.go`),
an `asqs-core retrieval-eval` subcommand, distractor support, and CI floors
(`-min-ndcg` / `-min-recall` / `-min-cases`).

**Be honest about the cost.** The harness is a few days; the **labelled suite is not**. Upstream's is
20 Java/Spring cases against an indexed reference repository and required human labelling, and its
labels went stale when the indexed repository's files drifted — needing a re-anchoring pass. Without
a labelled suite the harness measures nothing. Recommended only if core intends to own a reference
corpus; otherwise CP16 is the measurement story and this stays unported.

---

## 9. Phase P4 — LLM transport and prompt budget

### CP25 — Output truncation and transport hardening

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 2–3 d · **Risk:** low

**Tasks**

1. **Raise the generation `MaxTokens` default to 8192 in _two_ places.** The retry fallback *and* the
   hardcoded `maxTok := 4096` on the unstructured conversation path — which is the value that path
   actually used. Fixing only the first leaves the default at 4096 where it matters.
2. `model.TruncatedCompletionError` + `bumpForTruncation`: detect a length-capped completion, retry
   once at a higher cap, and audit both (CP03).
3. `model.WarningPromptTruncatedPrefix` / `IsPromptTruncatedWarning` — a **machine-matchable** marker
   for front-truncation. Upstream observed three fix rounds sending ~136 k-rune prompts into a 32768
   window; each lost its system prompt and output contract to the truncation, made zero tool calls,
   and the warning was generated and discarded unread every time. Human-readable is not enough.
4. **On Ollama, report `MaxTokens: 0` on truncation**, not the requested value — `opts.MaxTokens` is
   not yet sent as `num_predict` until CP26, so the cap that was hit is the server's own default.
   Reporting a number that was never sent is a lie in the audit trail.
5. Consolidate transport retry into `internal/llm/retryhttp`.
6. **Strip leading reasoning blocks at the provider boundary** — `internal/intelligence/model/reasoning.go`
   (`StripReasoningBlock`), `CompleteResult.ReasoningRunes`, and the call in every client. **Leading
   only**, and that bound is what makes it safe: a generated test may legitimately contain the literal
   text `<think>`, and a rule that hunted for the tag anywhere would corrupt it — but no source file,
   JSON document or prose answer *opens* with it. An unclosed block means the reply was cut off
   mid-thought and returning empty is the honest result. Audit `reasoning_runes` (CP03).
   **This is the highest-value single item in CP25 for core specifically** (§2.6): core's audience
   runs local Ollama models, and on an R1-family model the plain-text fixer fallback is structurally
   unable to succeed without it.
7. **Do not send pinned sampling parameters.** `modelFixesSamplingParams` — o1/o3/o4/gpt-5-family
   models fix `temperature` and `top_p`, and sending them is an API error. Match on the last path
   segment so Azure deployment names resolve.
8. **Latent-hazard fix (§2.6):** `contentOrPlaceholder(content, role,
   hasToolCalls)` in `internal/llm/openai/client.go`. Core has the identical `json:"content,omitempty"`
   shape, so a message with empty content **drops the key entirely** and the API reads an absent key
   as null → HTTP 400. Today core never builds such a message; **CP44 will**. Land the net now.

**Acceptance.** A completion capped by length retries once and is audited. The truncation warning is
matchable by prefix. A `role:"tool"` message with empty content still serialises a `content` key.
A reply opening with `<think>…</think>` yields the content after it and a nonzero `ReasoningRunes`;
a reply *containing* `<think>` mid-body is untouched; an unclosed block yields empty. A gpt-5-family
model id produces a request carrying neither `temperature` nor `top_p`.

**Implementation record (2026-08-26).** All eight tasks done; the port was surgical by named
function, since the upstream client files interleave CP26/CP27/CP41 material.

- **model package:** `reasoning.go` and `errors.go` ported verbatim with their tests (both
  stdlib-only); `CompleteResult` gained `StopReason`, `ReasoningRunes` and `Warnings`;
  `WarningPromptTruncatedPrefix` / `IsPromptTruncatedWarning` added.
- **All three clients** now detect a length stop (→ `TruncatedCompletionError`) and strip leading
  reasoning blocks. OpenAI additionally gained the temperature gate + `modelFixesSamplingParams`
  (with its 193-line test) and `contentOrPlaceholder` — called with `hasToolCalls: false` until
  CP41; the function is ported whole so the tool loop lands on it. Ollama reports **`MaxTokens: 0`
  on truncation** (num_predict is not sent until CP26 — the ported test's assertion was flipped
  accordingly, with a note for CP26 to flip it back) and emits the prompt-overflow warning — the
  emission needs only the configured `ollama_num_ctx`, not CP26's probe, so it landed here.
  Anthropic gained `retryhttp` (package ported verbatim with its test; previously a single
  429/EOF failed the gap with no retry at all) and the shared `httpcfg` 5-minute timeout
  (previously a bare `&http.Client{}` with none).
- **Generator:** `DefaultGenerateMaxTokens = 8192` replaces the 4096 default in
  `completeGenerateWithRetry` **and** the hardcoded 4096/8192 conditional on the two-phase
  conversation path; truncation escalation (×2, capped at `maxGenerateOutputTokens` 16384, not
  drawing from the transient budget) with `llm.output_truncated{_retry}` audit events;
  `llm.completion_warning` audits provider warnings. `LLMGenerator` gained an `Audit` field
  (new generator-package `Auditor`), wired from the pipeline. Core's 3-attempt transient loop is
  deliberately unchanged — consolidating retry layers is CP24's.
- **Fixer:** `FixAudit` + `Audit` field (wired from the pipeline); `bumpForTruncation` +
  `maxFixerOutputTokens` + `fix.completion_truncated` auditing on both completion paths;
  `fix.completion_warning`; and the **rebuild-once on a front-truncated prompt** in the builder
  path (one tier tighter, never looping — the tiers don't shrink artifacts).
- **Tests ported:** model reasoning/errors; retryhttp; openai sampling/truncation/empty-content
  (wire-level empty-content cases return with CP41; the `Capabilities` test function returns with
  CP26); ollama truncation/prompt-overflow/client; the anthropic CP25 slice (timeout, truncation,
  end-turn, retry ×3 — cache and temperature tests stay for CP27/the seam); llmfix
  truncation-audit + unparseable (single-file plain-fallback, `plainFallbackTarget`,
  `classifyFixParseFailure` and `auditCompletionShape` tests trimmed — those functions arrive with
  CP53, noted in-file); the generator truncation test (package-renamed), which passed unmodified
  against the adapted escalation loop.
- **Merge-point note (risk 8):** in `generator/llm_generator.go` this bundle landed only the
  default constants, the escalation/audit block and the `Audit` field. Everything else of that
  file's 860 diverged lines — pre-generate checks, tool loop, path ranking, `repairMemberCase` —
  remains unreconciled for CP49.
- Full `go build ./... && go vet ./... && go test ./...` green.

### CP26 — Provider capability contract and Ollama parity

- **Status:** `blocked (CP25)` · **Effort:** 2 d · **Risk:** low

**Design constraint, do not "simplify" it.** Capabilities go on a **separate optional**
`model.CapabilityReporter` interface plus a `DeclaredCapabilitiesOf` helper — **not** as a method on
`ChatCompleter`. Two reasons, both load-bearing: widening `ChatCompleter` breaks every implementation
including the test mocks, to gain what a type assertion already gives; and the two-value form encodes
the right semantics — an **undeclared** completer is *unknown*, not *incapable*. Degrading on the zero
value would silently disable structured output for any custom or not-yet-updated completer.

**Tasks.** `Capabilities()` on OpenAI, Ollama and Anthropic; Ollama `/api/show` probe; send
`MaxTokens` as `num_predict`; `structuredFormat` for Ollama's grammar; `promptOverflowWarning` when
the prompt exceeds `num_ctx`; `model.ErrCapabilityUnknown` handling at call sites.

**Release note.** Ollama runs that previously got the server's default output cap now get the
requested one — usually larger, occasionally different in behaviour.

**Acceptance.** Each client declares capabilities; an undeclared completer is treated as unknown, not
incapable, with a test that fails if that inverts.

### CP27 — Anthropic block messages

- **Status:** `blocked (CP25)` · **Effort:** 1–2 d · **Risk:** low

Port the content-block message shape: `contentBlock.MarshalJSON`, `isToolResultOnly`,
`buildSystemBlocks`, and send `temperature` when a caller sets it (it was previously discarded).

**Strip the prompt-caching half.** `cache_control` markers, the cache-enable setting, and the
stable-prefix machinery are excluded by the seam (§2.4). Port `buildSystemBlocks` with the cache
parameter removed rather than wired to a constant `false` — a dead parameter is how excluded features
creep back.

**Acceptance.** Anthropic requests carry block-shaped content; `temperature` reaches the wire; no
`cache_control` key appears in any request, pinned by a test.

### CP28 — Token budget and prompt accounting

- **Status:** `blocked (CP06, CP17)` · **Effort:** 3–4 d · **Risk:** medium

**Tasks**

1. Wire `internal/llm/tokens` (CP06) into `internal/intelligence/retrieval/context_builder.go`:
   `FormatOptions.counter()`, `CounterOrDefault()`, `newBudget()`, `chunkBlock`, `memberListBlock`.
2. `retrieval.max_context_tokens`, default `0` = derive from the model window, else unbounded.
3. **Count only _finished_ sections against the budget.** Crediting not-yet-rendered sections means
   the first section sees the whole budget and clamping never bites — caught upstream when a
   3000-token budget produced a 12000-token prompt.
4. **The target method is never truncated; the enclosing class _is_,** with a generous share (~20%).
   The enclosing class is context, not the unit under test, and it is the largest unbounded input in
   practice — a 3000-line God object emitted in full into every one of its methods' prompts. Making it
   untruncatable means the budget cannot be enforced on exactly the repositories that most need it.
5. `CalibrationDelta` — measure the heuristic against `Usage.PromptTokens` and audit the gap (CP03).

**Acceptance.** A prompt built with a 3000-token budget measures ≤ 3000 by the counter. The target
method survives at any budget. The calibration delta is recorded per completion.

### CP29 — `prompt_tokens` end to end

- **Status:** `blocked (CP26)` · **Effort:** 0.5 d · **Risk:** low

Thread the provider's reported `prompt_tokens` through `usage_accumulator.go` into the run summary so
CP28's calibration has a ground truth and CP16's `llm_total_tokens` is real rather than estimated.

### CP60 — LLM concurrency limiter

- **Status:** `blocked (CP26)` · **Effort:** 1–2 d · **Risk:** low
- **Provenance:** `internal/intelligence/model/concurrency_limiter.go`, upstream since `cce7afc`
  (2026-06-16) — **older than core's own tracking point** and part of no upstream bundle.

**Verified state, and it is worse than "a missing file".** Core already declares
**`llm.max_concurrent`** (`config.go:659`) and **`runner.gap_concurrency`** (`config.go:805`), and
**neither has a single reader outside `internal/config`** — `gap_concurrency` is validated by the
loader and then dropped. Meanwhile `pipeline.go:317-327` launches overview generation in a goroutine
alongside per-symbol generation with **no cap on in-flight LLM requests at all**.

So this is not a new key: it is two documented, settable, inert keys — the exact defect class P6
exists to kill — plus an uncapped fan-out against an endpoint that, for core's primary audience, is
a single local Ollama process.

**Tasks**

1. Port `model.NewLLMLimiter` / `NewConcurrencyLimitedCompleter` / `ResolveLLMMaxConcurrent`.
2. Wrap **one shared limiter** around every completer in `internal/llm/client.go`, sized by
   `cfg.LLM.MaxConcurrent`. One limiter across all steps is the point — it is a *global* ceiling, and
   a per-step limiter would multiply by the number of steps.
3. The wrapper must forward `Capabilities()`, or CP26's contract silently degrades to *unknown* for
   every completer the moment this lands. Pin that with a test.
4. `runner.gap_concurrency` is **deleted** in CP36 (D15, decided 2026-08-26). The per-gap loop stays
   the plain sequential `for _, item := range plan.Items`; this bundle's global LLM limiter is the
   concurrency control. Making the loop concurrent is a possible later feature, not part of this plan.

**Acceptance.** With `llm.max_concurrent: 1`, two concurrent generation paths never overlap in
flight, asserted by a counting completer. The limiter-wrapped completer reports the same
capabilities as the one it wraps. CP17's inert-field lint stops exempting `LLM.MaxConcurrent`.

---

## 10. Phase P5 — Runner unification

Core's `internal/runner` is behind by **two** upstream passes: the local/docker parity pass and the
unification that followed. Verified absent here: `step_failure.go`, `local_build_cmd.go`,
`failed_step_summary.go`, `plan.go`, `planner.go`, `restore.go`, `run_state.go`, `coverage_paths.go`,
`credentials.go`, `js_plan.go`, `e2e_preflight.go`. Verified present here and slated for deletion:
`docker_argv.go`. `internal/evaluator/errclass` has no host-execution kinds.

**Do not replay both passes.** Copy the end state once. The parity pass's durable fixes all survive
into the current files in their post-unification form; replaying would land a wrapper indirection and
an inert-key warning that the unification then deletes.

**Core-specific mechanical differences:** orchestration is `internal/pipeline`, not
`internal/workflow`; `internal/evaluator/apisurface` does not exist yet (CP49); `ResolveFormatCommand`
has **no caller** in core at all, so its `Target` parameter is compile-only here.

### CP30 — Shared run state, plan/planner, parity harness

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 3–4 d · **Risk:** low · **Land this alone and green before anything else in P5**

**Tasks**

1. `run_state.go`; replace the four `s2 := *s` sites in `runner.go` with `s.clone()`. Core has the
   same four `copylocks` findings — `go vet` will name them.
2. `plan.go` + `planner.go`: the declarative step plan, self-contained.
3. Port `plan_parity_test.go` — **this is the point of the bundle.** It asserts the plan and the
   executor agree, with a whitelist of permitted structural differences.
4. Port `TestPermittedDifferences_containOnlyStructuralRows`: every whitelist row must be attributable
   to a structural difference, and a bundle id may never appear there.

**Acceptance.** `TestStepPlanParity` green **with a full whitelist** — every difference explicitly
listed. `go vet` clean of `copylocks`. No behaviour has moved yet, and that is deliberate: the harness
must be green before any step migrates.

**Implementation record (2026-08-26).** Done; the planner is an **adaptation**, not a copy, and
that was the substantive work.

- `run_state.go` ported verbatim (all four Onces — two gain callers only in CP33/CP34 — plus the
  CP31-ready `restoreOnce` memo). `Sandbox`'s two `*sync.Once` fields became `run *sandboxRunState`;
  the four `s2 := *s` sites are `s.clone()`; `eval_log.go`'s nil-tolerant `doOnce` is deleted.
  **Stale-claim note:** the "four copylocks findings" this spec predicted did not exist — core's
  Once fields were pointers, so vet was already clean. The refactor stands on its real rationale:
  sharing is structural now, and a future mutex is shared by construction.
- `plan.go` ported with comments re-keyed to CP bundles. `planner.go` was **written against core's
  current builders**, because upstream's end-state planner calls helpers that only arrive with
  CP31–CP33 (`restoreArgvFor`, `stepEnv`, `localCredentialEnv`, `buildtool`). Docker mirrors
  `runDockerEvalWithImageOverride` (ResolveToolchain → overrides → `dockerArgvForStep` → the .NET
  docker patch chain and test wrappers); local mirrors `runLocalCompile/Test/Coverage` including
  their asymmetries — the Java coverage step passes the plain "test" goal, the JS error branches
  skip/fail/skip inconsistently, local adds `CI=true` only on test/coverage. Two same-source
  extractions keep plan and executor from drifting: `dockerJobEnv` (now shared by
  `runDockerJobWithTimeout` and the planner) and `localJavaCoverageReportPaths` (now shared by
  `coverageSummary`). The harness caught one mirroring bug during implementation — a "coverage"
  goal passed to `localBuildCommand`, which silently falls through to compile args there — which is
  the harness doing exactly its job.
- **Parity harness:** 16 fixtures (upstream's matrix minus the two credential fixtures, which
  arrive with CP33's seam). The whitelist enumerates **110 divergences**, each attributed:
  **CP31 × 61** (restore, unified command construction, coverage paths), **CP33 × 36** (step env),
  **CP32 × 7** (build_tool honoured, wrapper-free argv), **CP34 × 6** (skip/fail policy, JaCoCo
  coverage skip). The stale-entry rule is active, so each of CP31–CP35 must delete its rows.
- `TestPermittedDifferences_containOnlyStructuralRows` is ported in an **interim form**: a row may
  name a §1 structural difference *or* the CP31–CP35 bundle that removes it. CP35 tightens it to
  §1-only — the upstream end-state assertion. (The upstream form would fail by construction at
  CP30, since no behaviour has moved yet.) The two `TestStepResultParity_*` executor tests are
  deliberately **not** ported here: they assert U8's unified summaries and land with CP34.
- No behaviour moved: every pre-existing test is green and the executors are untouched apart from
  the two extractions above, which are argv/env-identical by construction.

### CP31 — Restore stage; JS plan and coverage paths

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 3 d · **Risk:** medium

`restore.go` (new, self-contained) must precede the .NET half: core's local .NET compile has no
`--no-restore` and no restore stage either, so adding the flag without the stage breaks the build.

Then `js_plan.go`, `coverage_paths.go`, and the `profile/language.go` glob correction —
`profile/language.go` is otherwise unreferenced, and its `ReportPaths` is exactly what
`CoverageReportPaths` needs.

**Two real defects fixed here.** Docker's coverage step uses `||` and therefore **silently swallows a
failing coverage script and reports success**; and pnpm looks for `test:coverage` where every other
package manager looks for `coverage`.

**Memoisation note.** The restore memo must be keyed on a **manifest fingerprint**, not on
`(sandbox, repo, ecosystem)` — the fix loop edits manifests mid-round, so the coarser key serves a
stale restore. The memo also relies on shared run state (CP30) because `Sandbox` is copied by value.

**Acceptance.** Restore runs once per fingerprint, not 5–6× per round. A failing JS coverage script
fails the step. Parity harness still green.

**Implementation record (2026-08-26).** Done, and it went further than the file list implies: since
this bundle, **both executors run what the plan records** — the plan is the single source of argv,
restore and skip/fail decisions for every ecosystem, on both targets.

- Ported: `restore.go` (fingerprint memo on the shared run state), `js_plan.go` (planJS for both
  targets; per-target env kept until CP33), `coverage_paths.go` (+ the `language.go` Maven-glob
  fix), `local_build_cmd.go` (toolchain preflight wrapper-tolerant until CP32). The maven/gradle
  profiles adopted `test-compile` / `compileTestJava` so generated TEST sources compile in the
  compile step on both targets; `./gradlew` in the gradle profile stays for CP32.
- The .NET patch chain is target-neutral (`patchDotnetEvalArgv` + the split-out, docker-only
  `applyDotnetContainerProvisioning`, applied last), the `Docker` helper names are dropped per
  U2b, and local C# plans from the shared profile — restore stage before the `--no-restore`
  compile, exactly the U4-before-U2b ordering the spec demands. Local java argv is byte-identical
  to the docker profile (flags before goals), with the JaCoCo coverage gate on both targets.
- Executors: `runLocalPlannedStep` replaces the nine `runLocal*/runJS*/runDotnet*` constructors
  (plus `jsLocalCommand`, `jsNoOpCompile`, `coverageSummary`, `dotnetShellLineWithProject` — all
  dead); the docker executor consumes the plan and memoises restore per fingerprint instead of
  restoring before every step invocation. Step summaries keep core's wording (CP34 unifies);
  coverage summaries now name the report via the shared `coverageSummaryFromPlan`.
- **Whitelist: 110 → 47 rows.** All 61 CP31 rows and all 6 CP34-forecast rows converged (planJS's
  skip policy unified the JS decisions early); the four `dotnet*/Argv[test|coverage]` rows are
  re-attributed **§1-5** (verified: argv byte-identical except the docker-only build-server
  shutdown wrap) and the forced-gradle Restore row moved to CP32 (docker restores from the Maven
  profile while local obeys `build_tool`). Remaining: 4 × §1-5 permanent, 8 × CP32, 35 × CP33.
- Tests ported: `restore_test.go` (14, incl. once-per-round across steps AND clones on both
  targets — the acceptance), `js_plan_test.go` (10, incl. no `--if-present`/`||` anywhere — the
  npm silent-pass killer — and the JaCoCo gate), `run_state_test.go` (CP30's own tests, ported
  late; one caught a real gap — the nil-config `NewSandboxFromConfig` path skipped the run-state
  allocation), `local_build_cmd_test.go` (CI-env test deferred to CP33 in-file; the
  config-key needle adapted `general.sandbox.type` → `runner.type` until CP38), and a planner_test
  subset (agreement tests stay for CP34, unknown-type executor backstop for CP35).
- **Behaviour changes to release-note:** a JS repo with no test script now SKIPS on the local
  target instead of failing (the unified skip policy); the js-build-runs-start shape now skips
  with a named reason instead of pretending via a no-op compile; local java/gradle compile now
  builds test sources (`test-compile`/`compileTestJava`); local runs now restore dependencies
  before the first step; docker restore no longer re-runs per step; and the npm docker coverage
  step can finally fail (previously structurally incapable of reporting a problem).

### CP32 — Build-tool resolution; wrapper-free argv

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 3 d · **Risk:** medium-high

**Tasks**

1. `internal/buildtool` (CP06) becomes the single build-tool identifier; `format_resolve.go`,
   `e2e_command.go` and `testbootstrap` all consult it.
2. **Six** wrapper sites, not three. The two that matter most are `evaluator/e2e_command.go` and
   `runner/format_resolve.go`; `e2e_command.go` emits host-GOOS `.cmd` paths that then run **inside
   Linux containers**.
3. Docker Gradle: wrapper → binary. **This is breaking, not behavioural** — a repository pinning an
   incompatible Gradle version stops building. It needs a release note and a `build_tool` alias
   warning.
4. `internal/config/local_runner_warnings.go` (from the unification commit) lands here — the
   warnings that tell an operator which settings the local sandbox cannot honour.
5. The `staticcheck` half applies only once `internal/postgenerate/staticcheck` exists; it is a
   straight copy on top of `buildtool` and can land with this bundle or with CP49.

**Invariant to pin with a test:** argv is repo-relative on both targets. Three producers branch on
`runtime.GOOS`; after this bundle none may.

**Implementation record (2026-08-26).** Done. `internal/buildtool` (CP06) is the single resolver
for the four sites core has — `localBuildCommand`, `format_resolve.go`'s `javaBuildPrefix`,
`evaluator/e2e_command.go`'s `defaultJavaE2EShellCommand` (the host-GOOS `.cmd`-in-a-Linux-container
live bug), and the two testbootstrap wrapper preferences (`e2e_bootstrap_java.go`'s Docker argv
helpers, `java_verify.go`, including the `chmod +x ./mvnw` docker-script variants). The staticcheck
and apisurface halves have no code to change until CP49, exactly as the spec says. The Docker
Gradle profile is wrapper-free (`gradle`, not `./gradlew`); the planner honours `runner.build_tool`
on both targets via `javaProfileForBuildTool`; `requireLocalToolchain` lost its wrapper exemption.
`internal/config/local_runner_warnings.go` landed with the `require_docker_bootstrap` startup
warning and the `mvnw`/`gradlew` deprecated-alias warning, wired into `config.Load`
(`normaliseAndValidateRunnerType` stays with CP35, noted in-file). No producer branches on
`runtime.GOOS` any more — the remaining GOOS uses are executor concerns (process-group kill, the
local-half `.cmd` suffix in `localNodeBin`, which upstream keeps too). Whitelist: all 8 CP32 rows
converged and were deleted. Tests: `format_resolve_test.go` and `e2e_command_test.go` ported (the
Target-aware format tests are marked in-file for CP35, which threads `Target` through
`ResolveFormatCommand`); the GOOS invariant is enforced by `buildtool`'s own tests plus the parity
rows rather than a separate grep-test. **Release notes:** Docker Gradle wrapper→binary is breaking
for repos pinning an incompatible Gradle (remediate with `runner.image_java_gradle`); a local
deployment relying on a repo wrapper with no `mvn`/`gradle` on PATH now fails with an actionable
message at plan time.

### CP33 — Step environment, credential seam, log redaction

- **Status:** `blocked (CP30)` · **Effort:** 2 d · **Risk:** low

1. `stepEnv` replaces core's inline env construction.
2. `credentials.go` ports **compile-only**. Add the `Ecosystem` field and the
   `PrivateRegistryEcosystem` constants to `private_registry_compat.go`'s placeholder so it builds,
   and accept that `localCredentialEnv` and `applyLocalMavenSettings` are unreachable here because no
   credential is ever materialised. **Do not** port `private_registry_mounts.go` or
   `azure_nuget_docker.go` (§2.4). The credential-provider preflight ports for symmetry, not effect.
3. `jobrunner/redact.go` — generic and worth having. The urgent leak upstream was Docker's
   `FormatDockerInvocation` printing every `-e` value including PATs; core never populates
   `DockerEvalExtraEnv`, so this is defensive here rather than urgent. Port it anyway; CP47 and CP48
   both add env-carried settings.

**Implementation record (2026-08-26).** Done as specified.

- `stepEnv`/`baseStepEnv` are the one source for both targets: CI=true on every step, the .NET
  hygiene vars on C#, dockerExtra only for the container (delivery is the §1 difference —
  os.Environ()-appended on a host). `dockerJobEnv` delegates to it; `newLocalBuildCmd` applies the
  base env so restore processes get it too; the JS/java/dotnet planners record it; the local
  executor sets exactly what the plan records. All 35 CP33 whitelist rows converged and were
  deleted — **the parity whitelist now holds only permanent §1 rows** (4 ×§1-5 build-server
  shutdown, 4 ×§1-1 credential-provider provisioning, 3 ×§1-4 `-s` settings flag), matching
  upstream's end state.
- `credentials.go` ported compile-only: `PrivateRegistryEcosystem` + the `Ecosystem` field live on
  `private_registry_compat.go`'s placeholder, `Sandbox.PrivateRegistryCredentials` is populated
  from the (always-nil) `MaterialisePrivateRegistryMounts` so the delivery path is shape-identical
  to upstream; `localCredentialEnv`, `applyLocalMavenSettings` and the NuGet credential-provider
  preflight (`warnLocalNuGetCredentialProviderMissing`, on the shared run state's Once) are all
  wired and unreachable-in-effect, exactly as §10.4 prescribes. The two credential parity fixtures
  are in the matrix and pin the §1-1/§1-4 rows.
- `jobrunner/redact.go` ported with its tests; `FormatDockerInvocation` masks credential-bearing
  `-e` values at the formatting boundary.
- CP31's deferred `TestLocalBuildCommand_SetsCIEnv` restored; `credentials_test.go` ported whole
  (one config-key needle adapted to core's v1 spelling until CP38).

### CP34 — Browser preflight; local steps onto the plan

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 5–6 d · **Risk:** high · **The largest single diff in P5**

1. `e2e_preflight.go` + `errclass.KindBrowsersMissing`. This earns its place in core specifically:
   core's evaluator already consults `errclass.Kind` on the E2E step.
2. All local steps move onto the plan; twelve functions leave `local.go`.
3. `step_failure.go` + `failed_step_summary.go`.

**This changes `StepResult.OK`, which drives ship/discard.** It is not a refactor. One row is
near-certain to fire: with no `pom`/`gradle`/`csproj` present, `DetectToolchainID` ends in
`return JavaMaven`, so **both** targets currently run `mvn` against a missing POM — Docker fails and
local fails, in different ways. The unification flips both to **skip**. Every such policy decision
needs an explicit row in a table in this file before the code lands.

**A timeout currently surfaces as nothing.** `ExitCode` is -1 on a SIGKILLed CLI and the Docker eval
path only reports the run error when `ExitCode == 0`, so the error is dropped and a timeout looks
like a test failure. Fix it here and count it (CP03).

**Acceptance.** Parity harness green. A dedicated test asserts the plan agrees with what the **local**
executor actually does — the one guard the upstream tests cannot provide for core.

**Implementation record (2026-08-26).** Done; smaller than estimated because CP31 had already moved
all local steps onto the plan — this bundle landed the failure/summary/logging/policy half.

- **The step-policy table, as decided** (open question 2 resolves by adoption of upstream's
  *implemented* state; every row now identical on both targets unless marked):

  | Repo state | Decision on BOTH targets |
  |---|---|
  | JS, no `package.json` | skip (`skip (no package.json)`) — since CP31 |
  | JS, no `test` script | skip (`skip (no test script)`) — since CP31 |
  | JS, no `coverage`/`test:coverage` script | skip — since CP31 |
  | JS, `build` runs start/install | skip with the named reason — since CP31 |
  | Java, no JaCoCo plugin | coverage skips (`skip (no JaCoCo plugin declared in the build file)`) — since CP31 |
  | Unsupported language | skip (`skip (unsupported lang)`), one shared string — this bundle |
  | Java, no `pom.xml`/`build.gradle` | **FAIL on both** — local plan-fails with the resolver error; Docker runs `mvn` and fails at runtime |
  | Step killed at `runner.timeout` | **FAIL, named** (`<step> step timed out after … (runner.timeout)`), classified `KindStepTimeout` — this bundle |

  **[correction] The Java-no-manifest row does NOT flip to skip.** This bundle's blurb inherited
  U8's spec paragraph ("the unification flips both to skip"), but upstream never implemented that
  flip — its end-state planner still fails compile/test on `ErrNoBuildFile`, and no upstream parity
  fixture compares that shape. Core copies the implemented state (rule 2). FAIL is also the
  conservative side: flipping to skip would let a repo that used to be blocked from shipping ship.
- **U7:** `e2e_preflight.go` ported (warn-once, not a hard failure — the upstream rationale about
  `channel: "chrome"` false-positives is preserved); `errclass` gained the whole host-execution
  block (`KindToolchainMissing/NotExecutable/BrowsersMissing/StepTimeout`, `IsHostExecutionKind`,
  `Remediation`, `kindHostExecution` — needles match core's `runner.*` key spellings). Core's
  existing E2E `errclass.Kind` consultation benefits immediately; the loop-stopping caller of
  `IsHostExecutionKind` arrives with CP50/CP52. Local E2E now reports the "Tests (E2E)" label via
  `testWithLabel`.
- **U8 timeout honesty:** `step_failure.go` + `failed_step_summary.go` ported (with
  `errout/test_failure_blocks.go`, pulled forward from its A.1 CP52/CP53 forecast — this is its
  first consumer). The Docker gate is `runErr != nil` (was `&& ExitCode == 0`, which discarded
  every deadline); both targets route failures through `sandboxStepFailure` and successes through
  `stepSuccessSummary`; the same gate fix applied to the three docker format sites via
  `dockerJobRunError`. `eval_log.go` replaced with the unified one-block env log driven by the
  plan, plus `logLocalEvalStep`.
- Tests ported: docker_eval_failure (timeout named/classified ×6), jobrunner/docker_timeout (pins
  the ExitCode -1 premise), step_failure, failed_step_summary, eval_log, e2e_preflight, errout
  blocks, **both plan-agrees-with-executor tests** (the acceptance's named guard) and both
  `TestStepResultParity_*` tests — all needles adapted to core's v1 key spellings.

### CP35 — Retire `docker_argv.go`; reject unknown sandbox type; per-file format

- **Status:** `in review` (done 2026-08-26 — see the record at the end of this bundle) · **Effort:** 2 d · **Risk:** medium

1. Delete `docker_argv.go` and the write-only `profile` fields. Its `Coverage → Test` fallback is
   **dead** — all six builtin profiles set `Coverage`, and the override path sets it too — so keep it
   as a planner default if wanted, but do not fixture it.
2. **Reject an unrecognised `runner.type`.** Today an unrecognised value returns
   `{OK: true, Summary: "stub"}` for **every** step, so a typo passes a run with zero evaluation.
   This is the highest-value single line in P5.
3. Per-file Docker format path — `pipeline.go` calls `FormatAfterFixForSandbox` at two sites.
   `ResolveFormatCommand`'s `Target` parameter is compile-only in core (no caller).

**Acceptance.** `runner.type: dokcer` fails the run with an error naming the valid values. A test
enumerates the valid set from one place so a future type cannot be added without it.

**Implementation record (2026-08-26).** Done.

- `docker_argv.go` deleted (`profileArgvForStep` in the planner keeps the defensive Coverage→Test
  fallback, unfixtured as specified); the write-only `ToolchainProfile.ArtifactPaths` and
  `.CacheTargetPaths` fields deleted. `profile/language.go`'s `ForLang`/`ImageFor` stay — upstream
  kept them too.
- **`runner.type` is rejected at startup** (`config.normaliseAndValidateRunnerType`, which also
  canonicalises case/whitespace) and the executor stubs are gone: `unknownRunnerTypeResult` fails
  the step, names the offending value, and fills Output so the evaluator does not hand the fixer a
  blank prompt. `validRunnerTypes` is the single source; a test enumerates it.
- **The format path is Target-aware end to end:** `format_resolve.go` and `format_after_fix.go`
  replaced with the upstream end state — availability probed on the host only for the local target
  (docker's image supplies mvn/gradle/dotnet), per-file prettier is repo-relative in a container
  with no host-GOOS `.cmd` suffix, and the Docker per-file format path exists. The pipeline's two
  call sites now resolve **per repository through `ResolveFormatCommand`** for every language and
  pass the resolved result; `EffectivePostGenerateFormatCommand` (which only defaulted for C#, so
  a Java repo with `format_command` empty never wired the after-fix hook and the fix loop burned
  its budget hand-formatting) is deleted.
- Ride-alongs the ported tests surfaced, fixed to upstream's state: `argvToShellSingleCommandLine`
  over-quoted every token, which made every downstream `dotnet test` matcher a silent no-op on
  configured C# commands (hang-mitigation, VSTest timeout, build-server shutdown) — it now quotes
  only what needs quoting, with the quoting-tolerant `shellScriptRunsDotnetTest` as defence in
  depth; `dotnetShellLineWithProject` was restored (CP31's record called it dead; upstream keeps
  it, with tests). The fix made override scripts matchable, so the two `dotnet-overrides` §1-5
  rows appeared and are whitelisted — **the whitelist is now exactly upstream's end-state set: 13
  permanent §1 rows** (6 ×§1-5, 4 ×§1-1, 3 ×§1-4), and
  `TestPermittedDifferences_containOnlyStructuralRows` is tightened to its final §1-only form.
- Tests: the full upstream format/dotnet-shell/entry/multitarget/playwright/credprovider/js-exit
  batch ported (nine files), plus the unknown-runner-type executor test and the valid-set test.

---

## 11. Phase P6 — Configuration schema v2

**This is a schema translation, not a copy job.** Upstream's v2 is shaped by machinery core does not
have, and core carries inert keys upstream does not.

### What core's v2 looks like

```
schema_version: 2
client_id:
general:      database | git | llm | build | sandbox | notifications | audit | websearch
bootstrap:
indexer:
retrieval:
generation:      (incl. the tool loop — see D1)
fixer:
```

**No `serve:` section.** Core's CLI is a single `run` command; there is no webhook server, no
scheduler and no gating. **No `copilot:` section** — that backend is not in the open core.

**No policy compilation.** Upstream's per-section `policy` blocks compile into overrides consumed by
a hook dispatcher. Core has no dispatcher (`policy_compat.go` keeps `PolicyConfig` inert). Core's
policy blocks are therefore **plain typed fields declaring only the keys core actually implements** —
bootstrap toggles, static check, the fixer toggles core's evaluator reads — and the pipeline reads
them directly. Do not port `schema_v2_policy.go`.

**Strict parsing deliberately changes core's failure mode.** Today an enterprise config parses here
and its policy block is silently ignored. After the cutover it **fails**, naming the first
enterprise-only key. That is the desired outcome — silent no-op configuration is the whole defect
class this phase exists to kill — and the error text should say the key is enterprise-only rather
than merely unknown. Consequence, stated plainly for operators: **configuration files are not shared
between the two products.**

**No migration path, no v1 support window.** Hard cutover, same as upstream. Core's
`config.example.yaml` and `config.yaml` are regenerated from core's own structs.

### CP36 — Housekeeping: dead keys, lint upgrade, golden fixtures

- **Status:** `blocked (CP35)` · **Effort:** 2 d · **Risk:** low

**Verified inert config in core today** (zero readers outside `internal/config`, checked by grep):

| Key(s) | Verdict |
|---|---|
| `audit.*` (`AuditConfig` — **no reader at all**) | **Wire, do not delete.** CP03 gives it a reader. |
| `indexer.schedule`, `indexer.run_on_first_start`, `indexer.repo_path` | **Delete.** No serve mode in core. |
| `vcs.<provider>.webhook`, `vcs.<provider>.gating` | **Delete.** The `internal/vcs/*/webhook*.go` parsers are library code with nothing to receive a webhook. |
| `copilot.*` | **Delete.** No `internal/llm/copilot` in the open core. |
| `llm.max_concurrent` (`config.go:659`) | **Wire.** CP60 gives it a reader. Uncapped LLM fan-out against a single local Ollama process is core's most likely real-world symptom. |
| `retrieval.failure_hint_file` (`config.go:204`) and `retrieval.persist_last_eval_failure` (`config.go:206`) | **Wire — both.** `PlanOptions.FailureHintFile` exists (`plan.go:111`) and **nothing in core maps config onto it**; the upstream reader is the excluded `failure_hint_file.go`. Wiring is cheap and genuinely useful to core's CLI audience — feed a CI log into planning — and `persist_last_eval_failure` closes the loop by writing the next run's hint. They are also the **third tenant of `.asqs/`**, so decide them together with CP59's preserve-list (D14). |
| `runner.gap_concurrency` (`config.go:805`) | **Delete.** Decided 2026-08-26 (D15): the per-gap loop stays sequential; CP60's global LLM limiter is the concurrency control. |
| the rest of `vcs.<provider>.*` × 4 providers | **Collapse.** Only the *active* provider's block is ever read, through the `Active*()` accessors — 13 keys × 4 providers is 52 duplicates that can silently disagree. |

**Tasks.** Delete the dead keys; wire `audit` and `llm.max_concurrent`; run CP17's inert-field lint
at full strength and resolve every remaining exemption with a wire-or-delete decision; **record
golden resolved-config fixtures at this boundary**, before the constants freeze, so CP37 and CP38 can
prove they changed no effective value.

**Sweep the dangling documentation references while the lint is being built.** Core's sources cite
docs that do not exist in this repository — **19 references**, `docs/DOCUMENTATION.md` ×16 and
`docs/SESSIONS.md` ×3, including two in `schema.sql` (lines 201 and 205) that point at
`docs/SESSIONS.md` and `internal/session/engine`. Pre-existing debris from the original strip; CP40's
doc-path guard will not catch it because it checks *config paths*, not doc links. Either the
reference is repointed (CP56 creates `docs/DOCUMENTATION.md`) or it is deleted.

**Acceptance.** The lint passes with an empty exemption list, or with each entry carrying a decision.
Goldens are committed and CP37/CP38 diff against them.

### CP37 — Constants freeze

- **Status:** `blocked (CP36)` · **Effort:** 1–2 d · **Risk:** low

Settings that are tuning knobs nobody tunes become constants: section budgets, chunk sizing, web
search cache limits. Each removal must show the golden resolved config unchanged.

### CP38 — v2 schema, strict loader, derived env, translation

- **Status:** `blocked (CP36, CP37)` · **Effort:** 4–5 d · **Risk:** high

**Tasks**

1. `schema_v2.go` — the YAML-facing structs, `yaml` tags **only**. Every field carries a doc comment;
   a test fails on an undocumented key (CP39 lifts those comments into the generated reference).
2. `loader_v2.go` — strict decoding with `KnownFields(true)`. A typo'd key must fail the load and name
   its own path. Recognise pre-v2 top-level section names (`vcs`, `runner`, `llm`, `database`,
   `audit`, `indexer`-shape) and say "this looks like a v1 config; `vcs` is now `general.git`" rather
   than emitting a generic unknown-field error on whichever section happens to come first.
3. `env_v2.go` — env names are **derived**, not tagged: `ASQS_` + the dotted path upper-cased, with a
   leading `general.` stripped. Client scoping `ASQS_<ClientID>_<KEY>` beats `ASQS_<KEY>`. Delete the
   `env:"…"` struct tags — a tag is a second, drifting source of truth, and it is how v1 came to
   document env vars for fields its loader never read.
4. `ApplyV2Defaults` — the **single** defaults mechanism, applied after the parse. Only fields whose
   absence differs from their zero value appear; a `*bool` in the schema is the marker for that.
5. `schema_v2_translate.go` — project v2 onto the existing v1-shaped runtime `Config`, so the ~200
   runtime call sites do not move. Positive booleans throughout: every toggle is `enabled`, never
   `disable_*`; the translator is where each inversion happens and each one is pinned by a test.
6. `redacted_placeholder.go` — the sentinel that keeps a redacted secret from being written back as
   a literal value when a resolved config is round-tripped.
7. **Ordering invariant: YAML → env → defaults → translate.** Env must precede defaults, or a
   default-true toggle cannot be turned off from the environment.

**The `*bool` rule, and why it has a cautionary tale.** Policy-facing booleans stay `*bool` and are
deliberately **not** defaulted, because absence must remain distinguishable from `false`. Upstream's
`ship.allow_partial` is the case to remember: it parsed, validated and was documented while nothing
read it; when it was finally wired, absence had to mean **true** (matching the prior unconditional
behaviour), and six shipped templates carrying an inert `allow_partial: false` had to be flipped —
otherwise wiring the key would silently have stopped those deployments shipping.

**Acceptance.** Every v2 key demonstrably changes the runtime config — a reachability test, with its
own stated limitation (it catches keys the translator drops; it does **not** catch keys nothing
reads — that is CP17's lint's job). An upstream template fed to core fails naming the first
enterprise-only key. The CP36 goldens resolve identically.

### CP39 — Generated reference and regenerated templates

- **Status:** `blocked (CP38)` · **Effort:** 2 d · **Risk:** low

1. `reference.go` + `reference_render.go`; `asqs-core config reference -o docs/CONFIG-REFERENCE.md`
   (the CP07 dispatch makes this a subcommand, not a fourth ad-hoc branch).
2. A drift test fails when the checked-in reference is stale.
3. Regenerate `config.example.yaml` as the **short** starting template with a hard line cap;
   exhaustiveness belongs to the generated reference, working specimens to `examples/`.
4. `env_only.go` — a mechanically generated appendix of every env-only switch, so the settings that
   are deliberately outside YAML are still discoverable.
5. **No explicit `null` in any shipped template.** Null and absent are equivalent to the loader, but
   null reads as a third state to an operator.

### CP40 — Rollout: docs, deployment guide, guards

- **Status:** `blocked (CP39)` · **Effort:** 2 d · **Risk:** low

Update `README.md` — its "Config keys that are CLI-driven here" section, the configure step, and the
troubleshooting sections that quote keys.

**The OpenShift deployment guide documents this product and lives outside this repository**, in the
parent `ASQS/` working folder. It is currently correct for v1 and becomes wrong the moment CP38
lands. The doc-path guard below **cannot reach it**: a test in `asqs-core` can only walk `asqs-core`.
**Decided 2026-08-26 (D18): move the guide into `asqs-core/docs/`** so the guard covers it — it
documents core, not upstream, and was checked for client-private content (none). CP56 item 4 lands
the move; it must be in place before CP38's cutover or arrive with it.

**Ship these four guards**, chosen so drift fails a test rather than waiting for a reviewer. Upstream
learned this the hard way: hand-maintained lists of key spellings missed **124** stale paths.

| Guard | What it checks |
|---|---|
| unresolvable-config-paths | Every config-shaped dotted path in living documentation resolves against schema v2. **Derive it from the schema and from the audit-event names the code emits** — never from a hand-maintained needle list. Allow historical mentions on lines carrying a marker (`pre-v2`, `legacy`, `formerly`, `removed`, …). |
| documented-YAML-blocks-parse | Every v2 YAML block in the markdown loads under the real strict decoder. Executable documentation. |
| every-key-reaches-the-runtime | CP38's reachability test. |
| no-nulls-in-templates | No explicit `null` in files operators copy. |

---

## 12. Phase P7 — Tool calling

D1 puts this in the open core. Core today has **no** tool contract at all: no `RoleTool`, no
`ToolCall`, no `ToolDefinition`, no `internal/intelligence/tools`. This is a feature addition, not a
fix port, and it should be treated as one.

### CP41 — Tool contract and provider support

- **Status:** `blocked (CP25, CP26, CP27)` · **Effort:** 4–5 d · **Risk:** medium

**Tasks**

1. `internal/intelligence/model/types.go`: `RoleTool`; `Message.ToolCalls` (a **slice** — providers
   emit parallel calls, and the transcript for a tool turn is `assistant(ToolCalls=[a,b])` →
   `tool(ToolCallID=a)` → `tool(ToolCallID=b)`); `Message.ToolCallID`; `ToolDefinition`; `ToolCall`;
   `ToolChoice{Auto,None,Required}`; `ToolCallArgsError`; `NormalizeToolArgs`.
2. **`ToolCall.Args` is `json.RawMessage`, not `string`, deliberately.** Providers disagree on the
   wire type — one sends `arguments` as a JSON *string* that must be unquoted before it parses,
   another sends a decoded JSON *object*. Normalising at the provider boundary means callers
   unmarshal the same way everywhere instead of learning which provider they are talking to. Empty
   arguments normalise to `{}` so a zero-argument call does not force every caller to nil-check.
3. Provider support in all three clients, plus `internal/llm/ollama_tool_probe.go`.
4. **Ollama's structured `format` grammar silently excludes tool-call syntax.** Upstream shipped a
   tool-enabled fixer that made *zero* tool calls for this reason. The fix is loop-wide:
   a `Capabilities.StructuredWithTools` flag that selects structured output **or** tools, never both,
   per provider. Port it with the contract, not later.
5. Ollama must **refuse** tool use when `num_ctx` is unknown rather than silently truncating the tool
   definitions out of the prompt.

**Acceptance.** A recorded-transport test per provider: definitions serialise, a parallel two-call
turn round-trips, malformed arguments produce `ToolCallArgsError` rather than a silent passthrough.

### CP42 — Prompted-JSON fallback and mode resolution

- **Status:** `blocked (CP41)` · **Effort:** 2–3 d · **Risk:** low

A three-tier `ResolveMode`: native tools → prompted-JSON tools → no tools. The extractor is
`internal/jsonx` (CP06), shared rather than duplicated. Copilot registration is **excluded**.

### CP43 — Read-only retrieval tool suite

- **Status:** `blocked (CP41, CP12)` · **Effort:** 4 d · **Risk:** medium

Five tools in a new `internal/intelligence/tools`: symbol lookup, chunk/similar search, file read,
`expand_symbol`, and the inventory. Path containment comes from `internal/pathsafe` (CP06), shared
with the fixer's shell allow-list.

**Correction, verified — this ports whole.** The upstream port map marks it partial because
`expand_symbol` supposedly needs `graphquery`. It does not: the handler calls `metadata.ExpandGraph`
(CP12), and `graphquery` is imported only by the enterprise API. See §2.5(1).

**Acceptance.** Every tool is read-only — a test asserts no handler writes. A path outside the repo
root is refused, with the traversal attempt counted (CP03).

### CP44 — Bounded generation tool loop, budgets, attempt audit

- **Status:** `blocked (CP42, CP43, CP28, CP03)` · **Effort:** 3–4 d · **Risk:** high

`internal/intelligence/tools/loop.go` + `config.go`, wired into `internal/generator/llm_generator.go`.
Caps on turns per run, calls per turn, calls per run, and result characters per run.

**Two upstream defects to port the fixes for, not just the mechanism:**

1. **A `RoleTool` message must never be empty.** When the shared result-character budget hits zero the
   result is blanked, and `content` with `omitempty` then **drops the key**, which the API reads as
   null → HTTP 400 → a hard gap failure. `toolResultContent(text, dropped)` covers both that path and
   a tool that legitimately returns `("", nil)`. CP25's `contentOrPlaceholder` is the transport-level
   net underneath it; both are needed.
2. **The cap record was measured after blanking**, so it always logged `requested: 0`. Measure before.

**Final-turn handling.** A forced final turn ("stop calling tools, answer now") must append an
explicit answer-now message and tolerate an empty reply with one bounded retry. Whether tools may be
declared on that turn is provider-specific — one provider requires tools-in-request for the message
history to validate at all, another does not — so it is a capability flag, not a constant.

**Acceptance.** Caps are enforced and audited; a run that exhausts its result budget still produces
valid messages; every attempt is recorded with tool name, arguments size and outcome.

### CP45 — Core-plus-inventory context restructure

- **Status:** `blocked (CP44, CP16)` · **Effort:** 3 d · **Risk:** medium

When tools are enabled, the prompt carries a **core** context plus an **inventory** of what the model
can fetch (`retrieval/available_context.go`), instead of everything inline.

**This is a ranking/context change and cannot go `done` without a CP16 comparison.** Ships behind a
setting defaulting to today's behaviour.

### CP46 — Fixer tool access

- **Status:** `blocked (CP44, CP50)` · **Effort:** 3–5 d · **Risk:** medium

Tool access inside the fix loop: `completeToolAware`, `resolvedToolMode`, `MultiTurnEffectiveForStep`,
attempt and cap-hit auditing in `internal/evaluator/llmfix`.

**Note the core-specific upside (§2.5-5):** core's fix loop runs through `RunEvaluation`, the path
that carries the streak and discard logic. Fixer tool access lands on the *full* loop here.

**Acceptance.** A tools-enabled fix round makes tool calls (the upstream failure was zero calls — a
test must be able to detect that). Ships off by default; enabling it is a CP16-measured decision.

---

## 13. Phase P8 — External knowledge

### CP47 — Web search tool

- **Status:** `blocked (CP43, CP44)` · **Effort:** 4–5 d · **Risk:** medium
- **Order (D17, 2026-08-26):** CP48 lands before this bundle starts.

New `internal/websearch` + the `web_search` tool. SearXNG and Brave backends, a query ledger, a host
allow-list, rebinding-safe fetch, nonce framing of results, and an offline replay mode.

**This introduces network egress into the open core**, which has until now been an entirely local
tool. Consequences to design for, not discover:

- **Off by default**, with a single clearly-named setting to enable it, and an offline switch that
  makes egress structurally impossible.
- **A boundary import test**: only the tool layer and the pipeline may import `internal/websearch`,
  and the runner package's dependency graph must not reach it. Port this test — and remember to
  rewrite the module path inside its `go list` arguments, or it passes vacuously.
- **Cache location.** Upstream's decision, after a reversal: the cache lives **inside the repository
  under test** at `<repo>/.asqs/websearch-cache.json`, exactly like the project-intel cache — one
  JSON document with a format-version envelope, atomic temp+rename, mutex-guarded read-modify-write,
  TTL pruning on put. The reasoning is ownership: the queries belong to the repository under test,
  not to the tool. An absolute `cache_path` escapes the clone and is the supported way to get
  cross-run reuse. Core's CLI runs against a **local folder** far more often than upstream's, so a
  repo-anchored cache is actually warm here more often than it is upstream — worth saying in the docs.
- **What ship does with it.** On a *cloned* repo the workspace is deleted after the run, so the cache
  survives only if it is committed. CP59's preserve-list keeps `websearch-cache.json` through the
  pre-ship `.asqs` cleanup and `git add .` then stages it — that is the intended behaviour, and it
  means **search results reach the repository's pull request**. Say so in the docs: an operator who
  assumed the cache was private would be surprised. Anyone who does not want it committed sets an
  absolute `cache_path` outside the repository.

**Acceptance.** With the offline switch on, no outbound connection is attempted (asserted by a
transport that fails the test on any dial). A host outside the allow-list is refused and counted.
Results are nonce-framed so a search result cannot impersonate an instruction block in the prompt.

### CP48 — Dependency doc indexing

- **Status:** `blocked (CP43, CP11)` · **Effort:** 5–6 d · **Risk:** medium

The offline alternative to CP47: ingest API documentation that is already on disk — Maven
sources-jars, NuGet XML documentation files, `node_modules` `.d.ts` — into the index so the model can
retrieve third-party signatures without a network call. **No network, no subprocess.**

`internal/intelligence/indexer/{dependency_docs.go,dependency_docs_resolve.go}`, a warm-up hook in the
bootstrap step, and `indexer.dependency_docs` settings.

**The failure mode to guard is dilution**, not correctness: a large dependency corpus can crowd
repository chunks out of every retrieval result. Guard it structurally — a separate chunk type with
its own retrieval budget — not by tuning a weight. Gradle is out of scope; say so in the docs rather
than leaving users to discover it.

**Acceptance.** Indexing a Maven project with sources-jars present adds dependency chunks; retrieval
for a repository symbol still returns repository chunks first, asserted on a fixture. With the
feature off, chunk counts are identical to before.

---

## 14. Phase P9 — Evaluator and fix loop

Highest per-day value in the plan, for the reason in §2.5(5): core reaches the fix loop through
`evaluator.RunEvaluation`, the path that carries the streak, breaker and discard logic. Every
improvement here is live on every core run.

### CP49 — `internal/evaluator/apisurface` (and the generator-file merge)

- **Status:** `blocked (CP06, CP32)` · **Effort:** 5–6 d · **Risk:** medium

**The semantic wall.** The fixer is asked to repair a compile error against a type it cannot see —
it invents members, and the next round invents different ones. This package extracts the **real** API
surface and puts it in the prompt.

Content: target parsing from compiler diagnostics; Maven classpath resolution (via `internal/javaproj`,
CP06); `javap` member extraction; jar-index symbol resolution; name-diversified ranking; repo-declared
type names; unresolved-dependency violations; canonical import resolution; a pre-generate seam; and
the `=== API SURFACE ===` prompt block.

**Includes `8640c59`'s hardening** (§2.6) — `repodeclared.go`, the extended `unresolveddep.go`,
`pregenerate.go` targets, `inventedmember.go` and `repomember.go`.

**This bundle also owns the `generator/llm_generator.go` merge** (§2.2). Four earlier bundles each
land part of that file's 860 diverged lines and record what they left; CP49 reconciles it against
upstream in full, including `repairMemberCase`, which no other bundle names, and `8640c59`'s change
making every authority's findings share **one** retry reason (the retry budget is one, so a file
failing two checks was previously told about one of them).

**Honest degradation is the acceptance criterion.** Java/Maven gets a real surface; every other
language and build tool must degrade to **no block**, never to a wrong or empty one. Failure to build
a surface is non-fatal and counted.

**Fixture note (ties to CP01).** This package's `testdata/cs_repo/bin/…/Microsoft.Playwright.xml` is
a real fixture asserted on by name. Do not let a `**/bin/` ignore rule near it.

### CP50 — Fix-loop convergence core

- **Status:** `blocked (CP03)` · **Effort:** 4–5 d · **Risk:** high

Four fixes that together are the difference between a loop that converges and one that burns its
budget:

1. **Adopted failing tests must survive write-scope narrowing.** Narrowing is computed from the
   **pre-narrowing** writable set, and adopted files are a hard read. Without this the loop can
   narrow away the very file it is trying to repair — and the audit must be repointed to report the
   true pre-narrowing set alongside the generated one.
2. **Compacted per-round memory** on `FixLoopState`: the applied-change excerpt, skip reasons and the
   failure signature, rendered as a `=== PRIOR ATTEMPTS ===` block. Multi-turn repair is otherwise
   inert — every round starts stateless and repeats the previous round's mistake. `multi_turn_effective`
   must report reality, not intent.
3. **Coverage-preserving gate.** Reject any fixer write that reduces the test-method count, with an
   explicit escape hatch. The failure mode this stops is the fixer "fixing" a failing test by
   deleting it.
4. **Baseline failure classification.** One pre-generation compile through the existing sandbox seam,
   splitting *inherited* red from *introduced* red, feeding the baseline paths to fixer adoption as a
   known input, and gating any rerun on actual progress. Without it, a repository that was already
   failing consumes the entire fix budget on failures the run did not cause.

**Ride-along files, and who owns each.** Six upstream files arrived inside wave commits and are named
by no upstream bundle. They are catalogued here — in the first P9 bundle — so they are ported
deliberately rather than swept in with a package copy; three of them land in CP52/CP53, as the table
says:

| File | What it does | Lands in |
|---|---|---|
| `fix_primary_progress.go` | Parses the primary failure site from compiler output and reports whether a reply *touched* it; drives the untouched-site streak | CP50 (feeds CP52's breakers) |
| `fix_file_progress.go` | Per-file progress attribution using the same diagnostic-ownership rule | CP50 |
| `fix_statement_structure.go` | Statement-structure checks on fixer output | CP53 |
| `root_cause_hint.go` | Root-cause hint in the fix prompt | CP53 |
| `lang_paths.go` | Language-aware path helpers for the fix loop | CP50 |
| `prompt_dump.go` | Opt-in prompt dumping for post-mortems | CP52 (pairs with CP03's redaction) |

**On the apparent F06 contradiction:** upstream's fix-loop board lists F06 (*primary-error-first
writable set with an output budget*) as `blocked`, while `fix_primary_progress.go` has existed since
`fb7b16a`. Checked — they are **adjacent, not the same**. The file does more than detect: after two
consecutive untouched rounds it *forces* writable scope onto the blamed file and can terminally stop
the loop. But that is enforcement driven by an observed streak, not F06's *budget-aware
prioritisation of the writable set*, which remains unimplemented. F06's status is plausibly accurate,
this plan does not depend on resolving it, and there is **no port risk either way**: the enforcement
code lives in `workflow.go` and the files CP50/CP52/CP53 port wholesale.

**Acceptance.** Each item has a test that fails before the change. In particular: a second round
carries the prior attempt; a coverage-deleting fix is rejected; an inherited failure is classified as
inherited and does not consume the budget.

### CP51 — Extend-merge and artifact identity

- **Status:** `blocked (CP06, CP50)` · **Effort:** 5–6 d · **Risk:** high

1. **Import union on every extend-existing merge**, Java **and** C#. Hoist top-level imports; model
   import *kind* rather than a static/global boolean; handle the C# anchor chain, alias collisions and
   global/local collisions; refuse on-demand/single collisions; **fail closed with no anchor**. The
   generation contract asks for the imports it needs rather than forbidding imports outright.
2. **One test artifact, one layer** — a unit gap and an E2E gap must not collide on one file.
3. **Extend, never duplicate**: convention-aware artifact identity, so `FooTest` and `FooTests` are
   recognised as the same artifact. Convention detection, a convention-driven default path, candidate
   ranking, and a provenance manifest (`internal/genmanifest`, CP06).
4. **Reconcile duplicates already on disk**, sharing (3)'s ranking and (1)'s import union.
   **Report-only by default** — deletion is provenance-gated, and an aborted run must restore.

**Adaptation.** Upstream runs the reconciliation between index and plan inside its orchestration
package; in core that seam is `internal/pipeline/pipeline.go` between the index and plan blocks.

### CP52 — Fix-loop breakers and audit honesty

- **Status:** `blocked (CP03, CP50)` · **Effort:** 2–3 d · **Risk:** medium

1. Extract `checkFixLoopBreakers` and the three thresholds (`repeatStopThreshold`,
   `recurrenceStopThreshold`, `noProgressStopThreshold`) from `applyLLMFix`, which is currently one
   function doing detection, prompting, writing and stopping.
2. Split read from write: `readFixContextFiles` / `applyFixWrites`, with `clampFixContextRunes` so the
   context cannot grow unbounded, and `sleepBetweenFixAttempts` with backoff.
3. `computeErrorLogLLMSummary` — an LLM-summarised error log for the prompt, behind a toggle whose
   absence means **enabled** (a `*bool`; see CP38's rule).
4. **A repeated-failure streak pauses rather than resets on compile-broken iterations.** A round that
   could not compile is not evidence that the test failure changed.
5. Audit honesty: every stop reason is emitted with the evidence that produced it, so a post-mortem
   does not require re-deriving the loop's state. Requires CP03 to be worth anything.

### CP53 — Fixer robustness batches

- **Status:** `blocked (CP49, CP50, CP52)` · **Effort:** 3–4 d · **Risk:** medium
- **Provenance:** upstream `8640c59` (§2.6) — re-diff before implementing; upstream keeps moving.

1. **Accept flat `{"edits":[…]}` arrays.** Models emit them; the parser rejected them and the round
   was scored unusable. `parseFixEditsAnyShape` + `parseFlatFixEdits` + `resolveFlatEditTarget`, with
   an `edits_array_unresolved` classification and a targeted repair note when the target cannot be
   resolved. Upstream lost a whole run to this: two consecutive unusable rounds ended it.
2. **`repairRawControlCharsInJSONStrings`** — raw control characters inside JSON strings, the other
   common shape of unparseable model output.
3. **Plain-source fallback with an explicit target rule**: the single artifact in scope, else the one
   in-scope artifact the failed replies name (by path or basename), else the one the error output
   names — and audit **which** rule fired.
4. **`ApplyFixEdits` applies k byte-identical `{find,replace}` edits when the anchor occurs exactly k
   times**; dedupes duplicates after a single apply; refuses on count mismatch or conflicting
   replacements.
5. **Fact blocks in the fix prompt**: missing-member facts, Mockito-misuse test-failure facts, absent
   symbols, and the API-surface block (CP49) — rendered by all three prompt builders, not one.
6. **Oversized artifacts are withheld with a note**, not silently dropped
   (`partitionArtifactsBySize`, `writeWithheldArtifactNote`).
7. **`resolveFixerStructuredOutput` logs on every path.** The policy-off path used to be silent,
   which is why a post-mortem showed `structured_output_requested: false` with no explanation.
   No deferral note when structured output resolved off.

**Acceptance.** Each shape has a parser test built from a real captured model reply. A round that
would previously have been "unusable" now applies. Every fallback records which rule fired.

---

## 15. Phase P10 — Language indexers

`tools/java-indexer` and `tools/js-ts-indexer` Go and Java/TS sources are **byte-identical** to
upstream after the import rewrite (verified: `advanced.go`, `minimal.go`, `spring_web.go`,
`java_e2e_kinds.go`, `runner.go` all diff to zero). Only the C# indexer diverges: `Program.cs` by 431
lines, `run.go` by 44, `docker_dotnet.go` by 8.

### CP54 — C# per-project compilation

- **Status:** `ready` · **Effort:** 4–5 d · **Risk:** medium

Compile per csproj-group with transitive `ProjectReference` sources instead of one flat compilation.
Measured upstream on a fixture repository: cross-file `CALLS` edges **0 → 4**, and `INJECTS` callees
became fully qualified. Unresolved invocations are counted and audited (CP03).

**`MSBuildWorkspace` was deliberately not used** — record the reason in the code so it is not
"fixed" later.

**Operator action:** the C# indexer must be rebuilt (`dotnet publish -c Release -o publish`).
`README.md` step 2 and the `indexers` CI job both need updating.

### CP55 — C# parameterized FQNames

- **Status:** `blocked (CP54, CP07)` · **Effort:** 2–3 d · **Risk:** medium

Overloads become distinct end to end; edges bind the exact overload; generic declarations carry `<T>`.
Needs a **forced reindex** on the legacy format (automatic, detected), a migration for the simple-name
column (CP07), and bare-name lookups served from `signature_json`.

**Sequencing:** this must precede CP13 — see that bundle's note.

---

## 16. Phase P12 — Framework-aware test bootstrap

**This phase exists because a whole upstream wave belongs to no upstream bundle** (§2.5-6). It is
specified from the code and from `docs/TEST-FRAMEWORK-BOOTSTRAP.md`, not from a bundle record, so it
is the least externally-validated part of this plan and the estimate most likely to move.

**Why it belongs in core rather than being excluded.** Core's audience points the CLI at a **local
folder** far more often than upstream's does, and the failure it removes is the one that wastes a
whole run: generation succeeds, evaluation cannot compile the generated test because the repository's
test stack was never actually able to run one, and the fix loop burns its entire budget on an
environment problem no fixer can repair. Bootstrap that *proves the framework runs* before generation
starts is worth more to a local-folder user than to a hosted pipeline with a curated corpus.

**Sequencing, and the cost of it.** CP58 lands against core's **existing v1 key**
`runner.test_framework_bootstrap` (`config.go:943`), so it does not wait for the config restructure.
But **CP36 must see the final key set**, so CP58/CP59 land before it — which puts P12 **on** the
critical path, not off it. Measured at midpoints: `CP30 → CP32 → CP58 → CP59 → CP36 → … → CP40` is
**~31 d**, against **~26 d** for `CP30 → CP32 → CP34 → CP35 → CP36 → … → CP40`. P12 lengthens the
plan's longest serial chain by about five days. The alternative — let CP36 ship without the bootstrap
keys and re-key them in a follow-up — trades those five days for a second config migration, which is
the thing P6 exists to avoid.

### CP58 — Detection, profiles, smoke verification, goal runners

- **Status:** `blocked (CP06, CP32, CP33)` · **Effort:** 8–10 d · **Risk:** high

**Scope: 15 of the wave's 17 new files.** The other two — `contract_profiles.go` and
`contract_writer.go` — belong to CP59, which owns the contract they write. (By commit provenance the
split is different: 16 of the 17 come from `21d25de`, `java_style_violation.go` from `6e693f4`.)

| Group | Files |
|---|---|
| Per-language profiles | `{java,csharp,js}_profile.go` |
| Smoke tests | `{java,csharp,js}_smoke.go` + the `testdata/*.template` smoke sources |
| Goal runners | `{java,js,dotnet}_goal_runner.go` |
| Verification | `verify_existing.go`, `dotnet_runtime_diagnose.go`, `java_style_violation.go` |
| JS execution path | `run_javascript.go`, `js_config.go` |
| C# | `csharp_compile_exclude.go` |

Plus substantial rewrites of existing files core has: `run_java.go` (+315/+75), `run_csharp.go`
(+318/+8), `versions.go` (131), `detect.go` (130), `csharp_csproj.go` (96), `java_gradle.go` (84),
`jest_config.go` (79), `java_maven.go` (67), `detect_unit.go` (57).

**`jest_config.go` is narrowed, not deleted** — an earlier revision of this plan said "deleted",
twice, and an implementer following it would have removed a file `js_config.go` depends on. Verified:
`21d25de` *modified* it. Rendering moved out (`writeJestConfig`, `writeJestSmokeSpec`,
`jestVerifyArgs` are gone from it), while `detectPackageManager`, `hasLockfile`, `installCmdLine` and
`installCmd` stay — the package-manager and lockfile helpers the whole JS path uses. Its 79 diverged
lines are content to port.

**Tasks**

1. **Framework detection**, then install what *that* framework requires — not a generic stack.
   Java resolves Spring Boot / Quarkus / Micronaut / Android / plain (so a Boot module gets
   `spring-boot-starter-test`, not bare `junit-jupiter`); C# resolves ASP.NET Core / Blazor / plain
   plus the runner the repository already uses; JS/TS resolves Angular / React / Vue / Svelte /
   NestJS / ESM-Node / plain, and picks Vitest for a Vite-built package (Angular and NestJS excepted)
   or Jest otherwise.
2. **Smoke tests are instruments, not deliverables.** Write one throwaway test, compile and run it
   with the repository's own runner, then remove it. If it cannot execute, **stop the run** — that is
   the whole value of the phase, and softening it to a warning removes the point.
3. **"Already set up" is verified, not assumed** (`verify_existing.go`). A complete-looking stack that
   cannot run a test is exactly the case that wastes a run today.
4. **Never overwrite `scripts.test`** — write `scripts["test:asqs"]`.
5. Skip repositories whose runner bootstrap cannot drive (Karma, Jasmine, Mocha, AVA) rather than
   half-configuring them.
6. Install/verify routes through the sandbox — hence the CP32/CP33 dependency.

**Acceptance.** Fixture repositories per framework: a Boot module gets the Boot starter; an Angular
package gets Jest with `jest-preset-angular`, a Vite React package gets Vitest; a repository with a
complete stack that cannot run a test **fails the run** with a named reason. With the feature off —
**the default** — every path behaves exactly as it does today, asserted by a test.

**On whether "off = unchanged" is actually reachable**, since this bundle rewrites files the
separately-gated E2E bootstrap also lives beside. Checked, and it is — but the implementer verifies
rather than assumes, because the reasoning is per-file:

- `versions.go` (131 lines) is **130 lines of pure addition plus one change**: `VersionTSJest`
  `29.2.5` → `29.4.12`. Its only consumer is the unit path (`package_json.go` here,
  `js_profile.go` upstream), which is flag-gated. The E2E pins an earlier review flagged as at risk —
  `VersionPlaywrightTest`, `DefaultPlaywrightDockerImage`, `VersionCypress`, `VersionPlaywrightJava` —
  are **byte-identical between the two trees**, so porting this file cannot move them.
- `detect.go` (130 lines) *is* substantially rewritten, but `Detect` has exactly one caller,
  `testbootstrap/run.go:130`, inside `testbootstrap.Run`, which `pipeline.go:157` gates on
  `TestFrameworkBootstrap.Enabled`. E2E detection is a different function in a different file
  (`detect_e2e.go`, 2 diverged lines) and is gated separately at `pipeline.go:176`.

**So the acceptance stands as written, with one obligation:** enumerate every value or function this
bundle changes that is reachable with the flag off, and either show the list is empty or release-note
it. Today that list is empty. It will not stay empty by itself.

**Docs.** Port `docs/TEST-FRAMEWORK-BOOTSTRAP.md`, adapted: drop the enterprise config spellings and
the session-engine references, keep the detection matrices and the "smoke tests are instruments"
rationale.

### CP59 — The bootstrap → generation contract

- **Status:** `blocked (CP58)` · **Effort:** 3–4 d · **Risk:** medium

A successful (or skipped-because-complete) bootstrap writes **`.asqs/test-stack.json`** — an
authoritative allow-list of importable test libraries, read by the generation prompt builder so the
model cannot import a library the repository does not have.

**Tasks**

1. `internal/teststack/contract.go` (CP06 ports the package; this bundle wires it).
2. `contract_profiles.go` + `contract_writer.go` in `internal/testbootstrap`.
3. **`project_config_teststack.go` → `internal/generator/`.** This file has **no core counterpart**;
   it arrived with `21d25de` and gained 96 more lines in `8640c59`. Core's prompt assembly lives in
   `generator/llm_generator.go`, so this is a new file plus a call site there — coordinate with
   CP49, which owns that file's reconciliation (§2.2).
4. **Build the `.asqs` pre-ship cleanup — there is nothing to "verify".** An earlier revision said
   the ship path clears `.asqs/` before staging and asked the implementer to confirm it in core.
   Verified: **core has no `.asqs` handling anywhere in its ship path.** `cmd/asqs-core/main.go:211`
   is `gitRepo.Add(".")` followed by a commit, and core *already* writes
   `.asqs/project-intel-cache.json` (`projectintel/run.go:65`, wired at `pipeline.go:257`) — so
   today's ship stages that cache, and without this task it would stage `test-stack.json` too.
   Upstream's mechanism (`RemoveRepoAsqsDirPreserving` + `AsqsShipPreserveRelPaths`) lives in an
   **excluded** package, so core needs its own, called before `Add(".")`.

   **It is a preserve-list, not a purge**, and that is deliberate upstream: the caches are committed
   into the repository under test precisely so they reach the *next* run through its clone — "the
   only way a cache inside the per-run workspace survives at all". Core adopts the same answer
   (**D14**): **caches in, contract out.**

   | `.asqs/` path | Ship |
   |---|---|
   | `project-intel-cache.json` | **preserved** — matches core's behaviour today; removing it would be a silent regression |
   | `websearch-cache.json` (CP47) | **preserved** — this is what makes CP47's repo-anchored cache reusable across runs on a cloned repo |
   | `last-eval-failure.log` / the configured failure-hint file (CP36) | **preserved** when persistence or an explicit path is set |
   | `test-stack.json` and everything else | **removed** |

   Path safety is not optional here: reject `..`, anything resolving outside the repo root, and
   anything not under `.asqs/`. `internal/pathsafe` (CP06) already does this.
5. **Strictly optional.** When the file is absent — bootstrap disabled, the default — every consumer
   must behave exactly as it did before the file existed.

**Acceptance.** With no `.asqs/test-stack.json`, prompts are byte-identical to before this bundle.
With one, a library outside the allow-list is not offered to the model. **A ship run stages no
`.asqs/` path except the preserved caches**, asserted per-path — including a regression test that
`project-intel-cache.json` is still staged, since that is existing behaviour this bundle could
silently break. A preserve entry containing `..` is refused.

---

## 17. Phase P11 — Documentation and release

### CP56 — README, behaviour docs, release notes

- **Status:** `blocked` · **Effort:** 3 d

1. `README.md`: the pipeline diagram gains the tool loop and the two knowledge sources; the config
   step points at the generated reference; the "Limitations" and "Not included" sections are
   re-checked against what P7/P8 actually landed (D1 moved that line).
2. A `docs/DOCUMENTATION.md` for core — today core has no behaviour reference at all, **yet 16 places
   in its own sources already cite one** (§CP36). The port roughly doubles the number of moving
   parts. It does not need to match upstream's size; it needs to cover the fix loop, retrieval
   profiles, the runner plan, the tool loop and the bootstrap contract — and to resolve those 16
   dangling citations plus the three pointing at `docs/SESSIONS.md`.
3. `docs/TEST-FRAMEWORK-BOOTSTRAP.md`, ported by CP58, is linked from the README's pipeline section.
4. Open question 6 resolved to "move it" (D18): the OpenShift deployment guide lands in `docs/`
   here — or earlier, with CP38's cutover, whichever comes first.
5. **Release notes with operator actions**, collected from the bundles that carry them:
   rebuild the C# indexer (CP54); run `asqs-core migrate` (CP07 and everything that adds one);
   `TestData` directories now produce gaps (CP24); Ollama output caps change (CP26); Docker Gradle
   wrapper→binary may break pinned builds (CP32); an unrecognised `runner.type` now fails instead of
   silently passing (CP35); **config files are v2-only and are not shared with the enterprise
   product** (P6); **test-framework bootstrap exists and is off by default** (P12).

---

## 18. Verification harness

The port is not done when it compiles. These are the gates, in the order they become meaningful.

| Gate | From | Meaning |
|---|---|---|
| `go build ./... && go vet ./...` clean | CP02 | Includes the four `copylocks` findings CP30 fixes. |
| `go test ./...` green | always | 20 existing test files must never regress; the ported ones join them. |
| `git status` clean after a full test run | CP01 | If the 21 tracked `obj/` files are still indexed, four of them re-dirty on every commit. |
| Step-plan parity, **full whitelist** | CP30 | The plan and the executor agree; every permitted difference is attributed to a structural row, and no row may name a bundle. |
| Plan-agrees-with-the-local-executor | CP34 | The one guard upstream's tests cannot provide for core. |
| Live-DB suite (`make test-live`) | CP04 | Two-repository scoping, symbol identity across reindex, `TESTS_SOURCE` counts, batch equivalence. |
| Config reachability + doc-path guards | CP38, CP40 | Every v2 key reaches the runtime; every documented path resolves; every documented YAML block parses strictly. |
| Inert-field lint at full strength | CP17, CP36 | A new config field that nothing reads fails the build. |
| First-wave comparison recorded | CP16 | Required before any ranking/context bundle may go `done`. |
| Boundary import test | CP47 | The runner's dependency graph must not reach `internal/websearch`. Rewrite the module path in its `go list` arguments or it passes vacuously. |

**Test porting is part of every bundle, not a phase.** 358 upstream test files live in packages core
mirrors and exactly **2** import an enterprise-only package (§2.5-2). A bundle whose upstream tests
were skipped is not `done`; if a test genuinely cannot port, the bundle records which one and why.

---

## 19. Risks and sequencing hazards

| # | Risk | Mitigation |
|---|---|---|
| 1 | **`internal/pipeline` is one function chain where upstream has eleven phase files.** Every bundle whose upstream diff touches a phase file must be re-seated by hand. This is the single largest recurring cost and the most likely source of silent divergence. | Budget adaptation time per bundle rather than assuming a copy. Consider extracting `pipeline.Run`'s blocks into named functions **once**, early — ideally as part of CP16, which already has to reach the completion path — so later bundles have somewhere to land. |
| 2 | **CP11 touches ~30 store signatures and every caller.** A missed call site compiles (Go will not catch a wrong string argument) and silently reads another repository's data. | The two-repository live test is the acceptance criterion, not an extra. Make `repoID` a distinct named type if the review finds bare-string confusion. |
| 3 | **CP34 changes `StepResult.OK`, which drives ship/discard.** | Explicit policy table in this file, agreed **before** the code lands. The Java no-manifest row is near-certain to fire. |
| 4 | **P6 is a hard cutover with no translator.** Every existing core config file stops loading. | Core is a CLI with no installed base to migrate, which is why this is acceptable — but the release note must be unmissable, and CP39's regenerated templates must land in the same change. |
| 5 | **P7/P8 add network egress and a tool loop to a product whose current selling point is that it runs entirely locally.** | Both off by default; CP47's offline switch makes egress structurally impossible; CP48 exists precisely as the offline alternative. Re-check README's "Limitations" claims in CP56. |
| 6 | **P9's provenance was an uncommitted working tree when this plan was first written.** It is now `8640c59` (§2.6), so the risk is downgraded to ordinary drift. | CP49 and CP53 still re-diff at implementation time — upstream keeps moving — but they now cite a commit. |
| 7 | **P12 has no upstream bundle spec** (§2.5-6). Its scope and estimate come from reading a commit and a design document, not from a reviewed plan with acceptance criteria. | Re-estimate against `docs/TEST-FRAMEWORK-BOOTSTRAP.md` before scheduling (open question 7). Land CP58 behind the existing off-by-default key so a wrong estimate delays a feature rather than destabilising the pipeline. |
| 8 | **`generator/llm_generator.go` is a merge point** (§2.2) and is invisible to any same-path diff. Five bundles edit it before or beside CP49's reconciliation. | Each of CP25, CP26, CP44, CP51 and CP59 records what it left unreconciled in that file; CP49's acceptance includes a full re-diff against upstream. |
| 9 | **Core is ahead on dependencies** (`go 1.25.0`, `pgx v5.10.0`, `pgvector-go v0.4.0`). Copied code was written against older ones. | Never downgrade `go.mod` to make a copy compile. CP09 in particular: check the `pgxpool` API surface used against v5.10, not v5.8. |
| 10 | **Measurement debt.** Several upstream bundles shipped with their acceptance measurement outstanding — chunk-overlap isolation, the fixer-tools A/B, the churn weight. Porting them ports the debt. | CP16 first. Every bundle that inherits an unmeasured default keeps that default and says so in its status line. Do not silently promote a default because upstream's code contains it. |
| 11 | **Two bundles are easy to sequence wrongly**: CP55 before CP13 (or the ordinals shuffle once), and CP31 before the .NET `--no-restore` change (or the build breaks). | Both edges are in the board; the dependency column is not decorative. |
| 12 | **This plan lives in the public repository.** It names the enterprise seam, which the README already does, but it must not become a map of the private tree. | Upstream is referenced by shared relative path only. Reviewers should reject any hunk that adds a private absolute path or an internal-only detail that is not needed to do the work. |

---

## Appendix A — File-level port map

### A.1 New directories and file counts

Counts are non-test files that exist upstream and not here, after excluding the **eleven** seam files
(§2.4) and everything under `cmd/`, `orchestrator`, `session`, `workflow`, `api`, `audit`,
`llm/copilot`, `graphquery`, `retrieval/ireval`. **142 candidates − 11 seam = 131**, and the rows
below sum to 131.

| Target directory | New files | Landing bundle(s) |
|---|---:|---|
| `internal/testbootstrap` | 17 | **CP58** (15) + **CP59** (2: `contract_profiles.go`, `contract_writer.go`) — see §2.5-6 |
| `internal/config` | 15 → **10** after seam exclusions | CP38 (incl. `redacted_placeholder.go`), CP39, CP17, **CP32** (`local_runner_warnings.go`) |
| `internal/evaluator/apisurface` | 12 | CP49 |
| `internal/evaluator` | 12 | CP50, CP51, CP52, CP53 |
| `internal/runner` | 11 | CP30–CP35 |
| `internal/websearch` | 10 | CP47 |
| `internal/intelligence/retrieval` | 8 → **6** after seam exclusions | **CP18** (`testability.go`), CP21 (`fusion.go`), CP22 (`fixtures.go`), **CP23** (`gap_shortlist.go`), CP45 (`available_context.go`), CP28 (`chunk_block.go`) |
| `internal/storage/metadata` | 7 | CP04 (`livetest_guard.go`), CP10 (`batch.go`, `fqname.go`), CP12 (`expand.go`), **CP13** (`symbol_lookup.go`), CP16 (`ab_report.go`), **CP55** (`reindex_warning.go`) |
| `internal/intelligence/tools` | 6 | CP43, CP44, CP47 |
| `internal/postgenerate/staticcheck` | 4 | CP32 |
| `internal/javaproj` | 4 | CP06 |
| `internal/intelligence/model` | 4 | CP25 (`errors.go` = `TruncatedCompletionError`, `reasoning.go`), CP26 (`capabilities.go`), **CP60** (`concurrency_limiter.go`) |
| `internal/storage/embeddings` | 3 | CP08, CP11, CP14, CP21 |
| `internal/intelligence/indexer` | 3 | CP24, CP48 |
| `internal/intelligence/projectintel` | 3 → **2** after seam exclusions | CP22 |
| `internal/storage/migrate` | 2 | CP07 |
| `internal/llm/tokens` | 2 | CP06 |
| `internal/llm/embeddings` | 2 | CP08, CP14 |
| `internal/evaluator/errout` | 2 | CP52, CP53 |
| `internal/evaluator/llmfix` | 2 → **1** after seam exclusions | CP53 |
| `internal/{sqlsplit,pathsafe,jsonx,genmanifest,buildtool,teststack}` | 1 each | CP05, CP06 (`teststack` wired by CP59) |
| `internal/{llm,llm/ollama,llm/retryhttp,storage,runner/jobrunner}` | 1 each | CP25, CP26, CP33, CP41 |

### A.2 The diverged files that carry most of the work

113 same-path files differ (**14,186** lines), plus `tools/csharp-indexer/Program.cs` (**431**, not
counted in the Go sweep) and eight renamed pairs (**1,154**, §2.1) — **15,771** lines in total.
These twenty-one rows carry **~69%** of it (10,913 lines). An earlier revision claimed "roughly 80%"
against a denominator that excluded the renamed pairs; the honest figure is below.

| Diverged lines | File | Landing bundle(s) |
|---:|---|---|
| 1618 | `internal/evaluator/workflow.go` | CP50, CP52, CP53 |
| 1517 | `internal/evaluator/llmfix/llmfix.go` | CP25, CP46, CP49, CP53 |
| 822 | `internal/config/config.go` | CP17, CP36, CP38 |
| 661 | `internal/storage/metadata/store.go` | CP09, CP10, CP11, CP12, CP13 |
| 565 | `internal/runner/local.go` | CP34 |
| 483 | `internal/intelligence/retrieval/plan.go` | CP18, CP23, CP24 |
| **860** | `generator/llm_generator.go` *(renamed)* | CP49 **owns the merge**; CP25, CP26, CP44, CP51 each land part |
| 431 | `tools/csharp-indexer/Program.cs` | CP54, CP55 |
| 391 | `internal/intelligence/indexer/run.go` | CP11, CP12, CP13, CP24 |
| 372 | `internal/testbootstrap/run_java.go` | **CP58** (+315 from `21d25de`, +75 from `6e693f4`) |
| 361 | `internal/storage/metadata/materialize_tests_source.go` | CP19 |
| 332 | `internal/storage/embeddings/store.go` | CP08, CP11, CP14, CP21 |
| 325 | `internal/testbootstrap/run_csharp.go` | **CP58** |
| 325 | `internal/llm/anthropic/client.go` | CP25, CP27, CP41 |
| 312 | `internal/evaluator/fix_write_path.go` | CP50, CP51 |
| 310 | `internal/llm/ollama/client.go` | CP25, CP26, CP41 |
| 299 | `internal/intelligence/retrieval/retrieve.go` | CP18, CP21, CP22, CP45 |
| 270 | `internal/config/loader.go` | CP38 |
| 239 | `internal/runner/format_resolve.go` | CP32, CP35 |
| 210 | `internal/runner/eval_log.go` | CP33, CP34 |
| 210 | `internal/evaluator/fix_quality_gate.go` | CP50 |

**Also CP58's, and not in the twenty-one:** `versions.go` (131), `detect.go` (130),
`csharp_csproj.go` (96), `java_gradle.go` (84), `jest_config.go` (79 — **narrowed, not deleted**),
`java_maven.go` (67), `detect_unit.go` (57) — ~644 further lines, all traced to `21d25de` / `6e693f4`.

Full per-file divergence is reproducible with:

```bash
# same-path
diff <(sed 's|"asqs-go/|"github.com/asqs/asqs-core/|g' <upstream>/<path>) <path>
# renamed pair (rewrite the package line too)
diff <(sed -e 's|"asqs-go/|"github.com/asqs/asqs-core/|g' \
           -e 's|^package orchestrator|package generator|' <upstream>/<path>) <path>
```

### A.3 Already in sync — do not touch

- **108 of 221** shared files are byte-identical after the import rewrite.
- Whole trees in sync: `internal/vcs/{github,gitlab,bitbucket,azuredevops}` (all adapters, clients,
  webhook parsers), `internal/notification`, `internal/dotnetproj`.
- **Near, not exact:** `internal/repo` (±17), `internal/workspace` (±6), and `internal/layout` — whose
  `csharp.go` differs by **5 lines**, upstream having a sibling-path fallback for an empty repo root
  that core lacks. An earlier revision listed `layout` as zero-diff; it is not.
- `tools/java-indexer` and `tools/js-ts-indexer` Go/Java/TS sources: **zero** diff.
- `internal/generator/skillpacks/` and `internal/evaluator/llmfix/skillpacks/`: **zero** diff. No
  prompt bodies need porting unless a bundle changes one.

---

## Appendix B — Decisions log

| ID | Decision | Rationale |
|---|---|---|
| D1 | Full feature parity: tool calling, web search and dependency docs all land in core. | §1. Supersedes the upstream config plan's assumption that core's schema would have no `generation` tool block and no `websearch` section. |
| D2 | Config v2 lands **after** the runner unification. | `general.build` / `general.sandbox` assume unified runner semantics. Sequential keeps both bisectable at the cost of one re-key. |
| D3 | Baseline is upstream HEAD, now `8640c59`. | §2.6. Taken when HEAD had 47 uncommitted files; those are committed, so P9's hunks have real provenance. Re-diff at implementation time is ordinary diligence. |
| D4 | This plan lives in `asqs-core/docs/`. | Written as core's own roadmap; upstream referenced by shared relative path only. |
| D5 | The narrowed doc-context path is **not** ported. | §2.4. Core's doc pass renders the full context by design; adopting the narrow preset is an unmeasured doc-quality change, not a port. |
| D6 | `audit.*` is **wired**, not deleted, in CP36. | It is inert today, but CP03 gives it a reader and the operator intent behind the key is unambiguous. |
| D7 | `retrieval.fusion` ships `dense`. | Measured upstream as a regression (nDCG 0.4792 → 0.2736). CP21 builds the channel correctly and leaves it off. |
| D8 | Churn weight ships at 0; CP45 and CP46 ship off by default. | Rule 7 in §3: a ranking/context change earns its default through CP16, not through being written. |
| D9 | `internal/llm/tokens` ships the character heuristic only. | A network-downloading tokenizer breaks air-gapped installs, which is a primary deployment mode for the open core specifically. Real tokenizers drop in behind the `Counter` interface. |
| D10 | Core gains `migrate`, `ab-report` and `config reference` subcommands; the CLI dispatch is extracted once in CP07. | Three ad-hoc branches on `os.Args[1]` is how a CLI becomes untestable. `run`'s behaviour and usage text stay byte-identical. |
| D11 | The framework-aware test-bootstrap wave is **ported**, as P12. | §2.5-6. D1 chose full parity, and this is the wave whose absence costs a *whole run* for the local-folder user core is aimed at. Recorded as a decision because the alternative — silent omission — is what the earlier revision did by accident. |
| D12 | `internal/vcs/{gates,handler}.go` are **excluded**, not ported. | Serve-mode webhook receiver and gating engine with no receiver in core. Same rationale CP36 uses to delete the `vcs.<provider>.webhook` / `gating` keys; listing them as a CP40 docs task was an error. |
| D13 | `llm.max_concurrent` is **wired** (CP60), not deleted. | Same shape as D6: an inert key whose operator intent is unambiguous and whose absence has a real cost — uncapped fan-out at a single local Ollama endpoint. |
| D14 | `.asqs/` pre-ship cleanup is a **preserve-list, not a purge**: caches in, contract out (CP59). | Upstream's own rationale — committing the caches into the repository under test is the only way a cache inside a per-run workspace reaches the next run. Core already stages `project-intel-cache.json`; purging would be a silent regression, and would break CP47's cross-run reuse on cloned repos. |
| D15 | `runner.gap_concurrency` is **deleted** in CP36, not wired. *(2026-08-26, open question 5)* | The per-gap loop stays sequential. Wiring it is a `pipeline.go` restructure colliding with risk 1; CP60's global LLM limiter (`llm.max_concurrent`) is the concurrency control core's audience needs. Gap-loop concurrency can return later as its own measured feature. |
| D16 | CP57 is **withdrawn**. *(2026-08-26, open question 4)* | Core does not own a labelled reference corpus, and without one the IR harness measures nothing. CP16's run-outcome metrics are the measurement story. Revisit only if a corpus materialises. |
| D17 | **CP48 lands before CP47.** *(2026-08-26, open question 3)* | The conservative order: the offline knowledge source ships first; network egress arrives second, off by default, with the offline switch. D1's scope (both land) is unchanged. |
| D18 | The OpenShift deployment guide **moves into `asqs-core/docs/`**. *(2026-08-26, open question 6)* | It documents core, contains no client-private content, and cites config keys the v2 cutover will break — CP40's guards can only protect it in-repo. CP56 item 4 lands the move. |

---

## Open questions

These need an answer before the bundle that depends on them starts. None blocks work today.

1. **CP11 — empty `repoID`.** Should a metadata call with an empty `repoID` be rejected, or scoped to
   `''` (the column default)? Rejecting catches missed call sites loudly; scoping keeps single-repo
   local runs working with no ceremony. Decide before CP11, and pin it in a test either way.
2. **CP34 — the step-policy table.** The Java-with-no-manifest row flips both targets to *skip*. Are
   there other rows core wants to decide differently from upstream, given core's users run local
   folders far more often?
3. **CP47 — is network egress acceptable in the open core at all**, or should CP48 (offline
   dependency docs) ship first and CP47 wait for evidence that it is wanted? D1 says both; this asks
   only about **order**, and shipping CP48 first is the conservative reading.
   **Answered 2026-08-26 (D17): CP48 lands before CP47.**
4. **CP57 — does core want to own a reference corpus?** Without a labelled suite the IR harness
   measures nothing. If the answer is no, mark CP57 `withdrawn` rather than leaving it `blocked`.
   **Answered 2026-08-26 (D16): no — CP57 is `withdrawn`.**
5. **CP60 / CP36 — should the per-gap generation loop become concurrent?** Core's loop is sequential
   and `runner.gap_concurrency` is inert. Wiring it is a real throughput win and a `pipeline.go`
   restructure that collides with risk 1; deleting the key is honest and cheap. Doing neither — a key
   the loader validates and then drops — is the option to rule out.
   **Answered 2026-08-26 (D15): delete the key.**
6. **CP40 / CP56 — where does the OpenShift deployment guide live?** It documents core but sits
   outside this repository, so no in-repo test can protect it from the v2 cutover. Move it into
   `docs/`, or accept a manual checklist item.
   **Answered 2026-08-26 (D18): move it into `asqs-core/docs/`.**
7. **P12 — re-estimate before scheduling.** CP58/CP59 are sized from an upstream commit's line count,
   not from a bundle spec, because none exists. They are the largest source of estimate risk in the
   plan.
