# Symbol-aware retrieval

Retrieval is **symbol-aware**: it uses the symbol table and dependency graph (metadata) plus chunks (embeddings store) to fetch the **right** context for test/docs generation, not "nearest paragraphs".

For a **target symbol** (e.g. a method), `Retrieve(ctx, meta, chunks, ContextRequest)` returns:

| Section | Source |
|--------|--------|
| **Target method + container** | Symbol + chunk for the target; enclosing **class / interface / struct / record**, or **REACT_COMPONENT**, **NEST_CONTROLLER**, **ANGULAR_COMPONENT**, etc. in the same file (line span). |
| **Dependencies** | **Outgoing** `GetEdgesFrom` for all profiles; **incoming** `GetEdgesTo` for non-`java_unit` profiles (e.g. **ROUTE_TO_HANDLER** from **API_ROUTE** → handler). Sorted by **profile-specific edge-type priority**. Edge labels append **` ←`** for inbound relations. |
| **Domain models** | Types in the same file + signature-resolved FQ names (classes/interfaces/structs/records). |
| **Similar reference chunks** | Per profile, vector **Search** per **`chunk_type`** into a **pool** (larger than the output cap), then **MMR** (Maximal Marginal Relevance, cosine in embedding space; default λ=0.5, configurable **`retrieval.similar_mmr_lambda`** / **`ContextRequest.SimilarMMRLambda`**) to reduce near-duplicate neighbors; listing fallbacks if the pool is empty. **Caps:** **`retrieval.max_similar_tests`**, **`max_dependency_chunks`**, **`max_fixtures`** and **`retrieval.profile_budgets`** (see **`budgets.go`**, **`docs/DOCUMENTATION.md`** — Retrieval profile budgets). |
| **Related chunks** | For **`http_api`** and **`full_stack`**: extra **`api_contract`** snippets (no driving symbol row). |
| **Fixtures / build helpers** | Chunks with type `fixture` or path heuristics. |
| **Config** | Chunks from paths matching config/context/spring/test-config. |

## Retrieval profiles

Set **`ContextRequest.Profile`** (type **`RetrievalProfile`**) or **`PlanOptions.RetrievalProfile`** (YAML **`retrieval.profile`**):

- **`java_unit`** — Default. Matches legacy behavior: outgoing edges only, **`dependencyEdgePriority`** (calls first), similar chunks **`test`** only.
- **`http_api`** — Handlers + routes + DTOs/guards; inbound edges; similar: **`test`**, **`route`**, **`api_contract`**.
- **`e2e_playwright`** — Selectors, client→route links, routes; similar: **`test`**, **`e2e_pattern`**, **`page`**, **`route`**.
- **`react_feature`** — Component graph (**RENDERS**, hooks, **IMPORTS** inbound).
- **`nest_module`** — **INJECTS**, module wiring, routes.
- **`full_stack`** — Combines **`react_feature`** + **`http_api`** (routes, DTOs/guards, **TARGETS_API_ROUTE**), plus light **Playwright**-style **USES_SELECTOR** ordering; similar chunks: **`test`**, **`definition`**, **`route`**, **`api_contract`**, **`e2e_pattern`**, **`page`** (chunk types for search — includes SPA **`PAGE_ROUTE`**-related **`page`** chunks). Same container-sibling / **api_contract** behavior as **`http_api`**. On JS/TS, **`PAGE_ROUTE` E2E gap listing** is **on** (only explicit **`java_unit`** disables it). Aliases: **`fullstack`**, **`react_http_api`**, **`ui_and_api`**, …

Aliases are normalized in **`NormalizeRetrievalProfile`** (e.g. `e2e` → **`e2e_playwright`**).

Use **`RetrieveWithProfile(ctx, meta, chunks, profile, baseRequest)`** as a thin wrapper.

## Storage filters used by retrieval

- **`embeddings.Store.Search`**: besides **`chunk_type`**, **`repo_id`**, **`lang`**, non-**`java_unit`** profiles pass **`FilePrefix`** = `dir(target_file)/` so similar chunks prefer the same folder (falls back to type listing if empty). When the target **`files.module`** is non-empty and **`ContextRequest.DisableHybridModuleFilter`** is false, **Search** also sets **`Module`** = that value (**`chunk_metadata->>'module'`**). If strict module-filtered hits are below a small threshold, retrieval **widens** with a second search without **`Module`** and merges (dedupe + **MMR** unchanged). **`SearchOptions.MetadataContains`** (JSON object, **`@>`**) is available on the store for future or custom callers; the default similar-chunk path uses **module** + **types** + **FilePrefix**.
- **`embeddings.Store.List`**: **`http_api`**, **`full_stack`**, and **`nest_module`** list extra **RelatedChunks** with **`ParentSymbolID`** = the enclosing controller/module symbol when **`chunks.parent_symbol_id`** was set at index time. List fallbacks for similar chunks may pass **`Module`** when narrowing by package/module.

## Audit log (plan phase)

When **`PlanOptions.Audit`** is set, **`CreateTestPlan`** logs:

- **`retrieve.plan_start`** — **`retrieval_profile`**, **`gaps_count`**, repo/lang.
- **`retrieve.gap_retrieved`** — per gap: **`deps_count`**, **`similar_chunks_count`**, **`similar_segmented_count`**, **`similar_reassembled_count`**, **`related_chunks_count`**, **`domain_models_count`**, **`fixtures_count`**, **`config_chunks_count`**, **`dependency_edge_types_sample`**, plus **`fq_name`** / **`symbol_id`**. Dependency rows include provenance (`Depth`, `GraphPath`).
- **`retrieve.gap_abstained_retrieval`** — when **[retrieval sufficiency / abstention](../../docs/DOCUMENTATION.md#retrieval-sufficiency-abstention)** thresholds fail (resolved **`PlanOptions`** mins — default **no** similar-test count floor, **cosine ≥ 0.5** only when similar chunks exist, unless **`abstention_disabled`**); gap is not added to the plan.
- **`retrieve.plan_done`** — **`items_count`**, **`abstained_count`**, **`total_deps`**, **`total_similar`**, **`total_similar_segmented`**, **`total_similar_reassembled`**, **`total_related`**, **`retrieval_profile`**, **`retrieve_within_run_cache_fast_hits`**, **`retrieve_within_run_cache_coalesce_hits`** (see [Within-run memoization of Retrieve](../../docs/DOCUMENTATION.md#within-run-retrieve-cache)).

## Task 16 additions

- **Transitive graph expansion:** `ContextRequest.DependencyMaxDepth` (default 2) expands dependency graph beyond direct neighbors with deterministic ordering.
- **Section budget allocator:** optional `ContextRequest.MaxContextChunks` with per-section caps (`MaxDependencyChunks`, `MaxSimilarTests`, `MaxFixtures`, `MaxConfigChunks`) keeps fixtures/config from starvation.
- **Context provenance:** dependency paths (`GraphPath`) and fixture/config chunk metadata (`retrieval_source_kind`, `retrieval_reason`) are rendered into LLM context.

## Task 17 additions

- **Branch intent inference:** retrieval infers branch-intent candidates from target and similar-test chunks (`if/else`, `switch/default`, exception, null, boundary patterns).
- **Existing-test-aware context steering:** `BuildLLMContextForGap` includes an explicit block that lists covered intents and missing intents (when inferred), instructing the LLM to avoid regenerating already-covered behavior.
- **Audit KPIs:** `retrieve.gap_retrieved` adds `existing_tests_detected`, `covered_branch_intents_count`, `missing_branch_intents_count`; `retrieve.plan_done` adds `items_with_existing_tests`, `total_missing_branch_intents`.

## Task 18 additions

- **Prompt skill packs:** generation paths now consume versioned skill packs from `internal/orchestrator/skillpacks/`:
  - `unit-v1.md` for unit items,
  - `e2e-v1.md` for e2e items,
  - `docs-v1.md` for per-symbol documentation generation.
- **Purpose:** keep quality bars/anti-pattern contracts centralized and testable while preserving existing retrieval context responsibilities.

## Test plan

**CreateTestPlan** runs **ListGaps**, then **Retrieve** per gap (passing **`opts.RetrievalProfile`** into each **`ContextRequest`**). **Sufficiency** checks (**`AssessSimilarReferenceSufficiency`**) omit gaps from **`Items`** when configured minima fail (**`orchestrator.BuildPlanOptions`**: default **0** similar tests required, **cosine** floor applies only if **≥1** similar chunk). The result is a small, prioritized test plan with full **RetrievalContext** per item.

**CreateE2ETestPlan** / **ListGapsE2E** (when **`MaxGapsE2E` > 0**): **Java** — uncovered **`API_ROUTE`** (Spring/Nest-style HTTP mappings) first; else gaps from **`E2E_SPEC`**, **`PAGE_OBJECT`**, and **`USER_FLOW`** in test files. **C#** — uncovered **`API_ROUTE`** (ASP.NET Core) first when any exist; else **`E2E_SPEC`** in C# test files (no **`PAGE_OBJECT`** / **`USER_FLOW`** path yet). **JS/TS** — uncovered **`API_ROUTE`** (e.g. Nest) first; else **`E2E_SPEC`** in test files; also **`PAGE_ROUTE`** in non-test files unless **`profile_e2e`** is explicitly **`java_unit`** / **`java`** (React Router and other SPA routers still produce gaps when **`profile_e2e: react`** / **`react_feature`**). See **`SupportsE2EGapListing`**. Default **`profile_e2e`** when empty: **`e2e_playwright`** (JS/TS) or **`http_api`** (**Java** / **C#**) via **`DefaultRetrievalProfileE2E`**.

**MetaReader** must implement **`GetEdgesTo`** (inbound edges); **`metadata.Store`** does.

## IR evaluation harness (offline)

**`qualitybot retrieval-eval -golden suite.yaml`** runs **MRR**, **nDCG@k**, **P@k**, **R@k** on **chunk UUIDs** using either the production **similar-reference** pipeline (**`SimilarReferenceRankedChunks`**) or a single **dense Search**. See **`ireval/`** and **[DOCUMENTATION.md — Retrieval IR evaluation harness](../../docs/DOCUMENTATION.md#retrieval-ir-evaluation-harness)**.
