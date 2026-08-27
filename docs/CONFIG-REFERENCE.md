# Configuration reference

**Generated — do not edit.** Produced from the schema structs in `internal/config/schema_v2.go`
by `asqs-core config reference`. A drift test regenerates it and fails when this file and the
structs disagree, which is what keeps a generated mirror from going stale the way a hand-maintained
one does.

This is the exhaustive list. The shipped `config.example.yaml` is deliberately much smaller — it
shows the keys a deployment usually sets, and everything else is documented here and omitted when it
is at its default.

**Reading the table.** *Default* is the effective value when the key is absent, not a suggestion.
`unset` means the key distinguishes "not written" from "written as false", which is how a
default of true can exist. *Env* is derived from the path — `ASQS_` plus the dotted path
upper-cased, with a leading `general.` stripped — so there is no lookup table to fall out of
date.

## (top level)

Keys at the root of the document.

| Key | Type | Default | Env | Description |
|---|---|---|---|---|
| `client_id` | string | `""` | `ASQS_CLIENT_ID` | ClientID scopes environment overrides: ASQS_<ClientID>_<KEY> beats ASQS_<KEY>. |
| `schema_version` | int | `0` | `ASQS_SCHEMA_VERSION` | SchemaVersion is absent or 2. Anything else is rejected, so a future schema change has an anchor to branch on instead of guessing from the document's shape. |

## general

Shared by more than one pipeline step: connections, credentials, models, how to build the client repo, and the sandbox every containerised step runs in.

| Key | Type | Default | Env | Description |
|---|---|---|---|---|
| `general.audit.dump_prompts` | bool | `false` | `ASQS_AUDIT_DUMP_PROMPTS` | DumpPrompts restores full prompt and completion text in audit payloads. Off by default: prompt bodies carry repository source, extracted configuration and compiler output, so the sink stores {sha256, len} instead. Turn it on for a post-mortem, not for normal running. |
| `general.audit.file_path` | string | `""` | `ASQS_AUDIT_FILE_PATH` | FilePath appends one JSON object per step to this file. Empty = stderr step lines only. |
| `general.build.build_tool` | string | `""` | `ASQS_BUILD_BUILD_TOOL` | BuildTool forces mvn or gradle for Java. "auto" or empty detects. |
| `general.build.compile_command` | string | `""` | `ASQS_BUILD_COMPILE_COMMAND` | CompileCommand overrides the detected compile step, run from the repository root. |
| `general.build.dotnet_fallback_target_framework` | string | `""` | `ASQS_BUILD_DOTNET_FALLBACK_TARGET_FRAMEWORK` | DotNetFallbackTargetFramework is used when a .csproj names no target framework. |
| `general.build.e2e_test_command` | string | `""` | `ASQS_BUILD_E2E_TEST_COMMAND` | E2ETestCommand overrides the end-to-end test step. |
| `general.build.format_command` | string | `""` | `ASQS_BUILD_FORMAT_COMMAND` | FormatCommand runs after generation and after each fix, before evaluation. |
| `general.build.test_command` | string | `""` | `ASQS_BUILD_TEST_COMMAND` | TestCommand overrides the detected whole-suite test step. |
| `general.build.toolchain` | string | `""` | `ASQS_BUILD_TOOLCHAIN` | Toolchain pins the evaluation image family (java-maven, java-gradle, typescript, csharp). "auto" or empty detects from the repository. v1 spelled it runner.eval_profile. |
| `general.build.unit_test_command` | string | `""` | `ASQS_BUILD_UNIT_TEST_COMMAND` | UnitTestCommand overrides the unit-only test step when it differs from the whole suite. |
| `general.build.workspace.path` | string | `""` | `ASQS_BUILD_WORKSPACE_PATH` | Path is the repo-relative directory holding the project to index and generate for. Empty = the whole repository. No "..". |
| `general.build.workspace.test_path` | string | `""` | `ASQS_BUILD_WORKSPACE_TEST_PATH` | TestPath is where generated tests go when the test tree does not sit under Path. Empty = Path. |
| `general.database.embeddings_url` | string | `""` | `ASQS_DATABASE_EMBEDDINGS_URL` | EmbeddingsURL is the pgvector database for chunks. Empty = the same database as MetadataURL. |
| `general.database.max_open_conns` | int | `0` | `ASQS_DATABASE_MAX_OPEN_CONNS` | MaxOpenConns caps both connection pools. 0 = the pgxpool default. |
| `general.database.metadata_url` | string | `""` | `ASQS_DATABASE_METADATA_URL` | MetadataURL is the Postgres URL for symbols, edges, files and run records. Required. |
| `general.git.azure_devops.organization` | string | `""` | `ASQS_GIT_AZURE_DEVOPS_ORGANIZATION` | Organization is the Azure DevOps organization name. |
| `general.git.azure_devops.project` | string | `""` | `ASQS_GIT_AZURE_DEVOPS_PROJECT` | Project is the team project containing the repository. |
| `general.git.azure_devops.repository` | string | `""` | `ASQS_GIT_AZURE_DEVOPS_REPOSITORY` | Repository is the git repository name within the project. |
| `general.git.azure_devops.token` | string | `""` | `ASQS_GIT_AZURE_DEVOPS_TOKEN` | Token is the Azure PAT, honoured for dev.azure.com remotes regardless of git.provider. |
| `general.git.base_url` | string | `""` | `ASQS_GIT_BASE_URL` | BaseURL points at a self-hosted instance. Empty = the provider's cloud host. |
| `general.git.default_owner` | string | `""` | `ASQS_GIT_DEFAULT_OWNER` | DefaultOwner names the owning account when it cannot be inferred from the clone URL. Each provider's own word for it — GitLab namespace, Bitbucket workspace, Azure organization — maps onto this single key. |
| `general.git.default_repo` | string | `""` | `ASQS_GIT_DEFAULT_REPO` | DefaultRepo is the repository slug when it cannot be inferred from the clone URL. |
| `general.git.provider` | string | `""` | `ASQS_GIT_PROVIDER` | Provider selects which platform's API and clone URLs are used: github, gitlab, bitbucket or azure_devops. |
| `general.git.ship.base_branch` | string | `""` | `ASQS_GIT_SHIP_BASE_BRANCH` | BaseBranch is the target branch for the PR (e.g. "main"). |
| `general.git.ship.branch` | string | `""` | `ASQS_GIT_SHIP_BRANCH` | Branch is the stable branch name (e.g. "quality-bot"). Same name every run so the PR is updated instead of creating a new one. |
| `general.git.ship.draft_pr` | bool | `false` | `ASQS_GIT_SHIP_DRAFT_PR` | DraftPR creates the PR as draft when true. |
| `general.git.ship.enabled` | bool | `false` | `ASQS_GIT_SHIP_ENABLED` | Enabled: when true, after a stable evaluation with generated artifacts, commit, push, and create a PR (only if none exists for the branch). |
| `general.git.token` | string | `""` | `ASQS_GIT_TOKEN` | Token authenticates the ACTIVE provider. v1 had four provider-specific variables; a single active provider needs one. |
| `general.llm.api_key` | string | `""` | `ASQS_LLM_API_KEY` | APIKey is the credential in clear text. There is no ${VAR} interpolation anywhere in the loader — a literal "${MY_KEY}" here is sent verbatim. Use APIKeyFromEnv instead. |
| `general.llm.api_key_from_env` | string | `""` | `ASQS_LLM_API_KEY_FROM_ENV` | APIKeyFromEnv names an environment variable to read the credential from, so a config can be committed without a secret in it. It wins over APIKey when both are set. |
| `general.llm.base_url` | string | `""` | `ASQS_LLM_BASE_URL` | BaseURL overrides the provider endpoint — Azure, a corporate proxy, or an Ollama gateway. |
| `general.llm.embeddings.api_key` | string | `""` | `ASQS_LLM_EMBEDDINGS_API_KEY` | APIKey authenticates the embedding provider when it differs from the chat one. Empty falls back to the general credential. |
| `general.llm.embeddings.api_key_from_env` | string | `""` | `ASQS_LLM_EMBEDDINGS_API_KEY_FROM_ENV` | APIKeyFromEnv reads that credential from an environment variable instead. |
| `general.llm.embeddings.cache.enabled` | bool | `true` | `ASQS_LLM_EMBEDDINGS_CACHE_ENABLED` | Enabled inverts v1's llm.disable_embedding_cache. Default true: a cache miss is exactly the pre-cache behaviour, so there is nothing to lose by leaving it on. |
| `general.llm.embeddings.cache.retention_days` | int | `0` | `ASQS_LLM_EMBEDDINGS_CACHE_RETENTION_DAYS` | RetentionDays prunes rows unused for this long. 0 = 30 days. |
| `general.llm.embeddings.dimension` | int | `0` | `ASQS_LLM_EMBEDDINGS_DIMENSION` | Dimension must match the model's output width. It lived under database.* in v1, which put it arbitrarily far from the model that determines it. Changing it fails closed on a populated table rather than silently mixing widths. |
| `general.llm.embeddings.fallback` | string | `""` | `ASQS_LLM_EMBEDDINGS_FALLBACK` | Fallback is "", "auto", or an explicit Ollama model used when the primary embedder fails. |
| `general.llm.embeddings.model` | string | `""` | `ASQS_LLM_EMBEDDINGS_MODEL` | Model is the embedding model id. Its output width must match Dimension below. |
| `general.llm.embeddings.provider` | string | `""` | `ASQS_LLM_EMBEDDINGS_PROVIDER` | Provider defaults to general.llm.provider. |
| `general.llm.http.disable_keep_alives` | bool | `false` | `ASQS_LLM_HTTP_DISABLE_KEEP_ALIVES` | DisableKeepAlives forces a new connection per request; some proxies mishandle pooled ones. |
| `general.llm.http.response_header_timeout` | string | `""` | `ASQS_LLM_HTTP_RESPONSE_HEADER_TIMEOUT` | ResponseHeaderTimeout bounds the wait for the first response header, separately from the body — the distinction that tells a hung proxy apart from a slow model. |
| `general.llm.http.timeout` | string | `""` | `ASQS_LLM_HTTP_TIMEOUT` | Timeout bounds one whole completion request, e.g. "10m". |
| `general.llm.max_concurrent` | int | `0` | `ASQS_LLM_MAX_CONCURRENT` | MaxConcurrent bounds in-flight completions across the whole run. This is the lever for a provider rate limit, and for a single local Ollama process it is the difference between a working run and a thrashing one. |
| `general.llm.model` | string | `""` | `ASQS_LLM_MODEL` | Model is the chat model id. It also determines the context window the prompt budget derives from, so an unknown model leaves the budget unbounded rather than guessed. |
| `general.llm.ollama_num_ctx` | int | `0` | `ASQS_LLM_OLLAMA_NUM_CTX` | OllamaNumCtx sets options.num_ctx on Ollama chat calls. 0 = the model's own default. Native tool calling requires it, so leaving it unset disables tools on Ollama. |
| `general.llm.provider` | string | `""` | `ASQS_LLM_PROVIDER` | Provider names the API to talk to: openai, anthropic, azure_openai, ollama. |
| `general.notifications.human_in_the_loop_email` | string | `""` | `ASQS_NOTIFICATIONS_HUMAN_IN_THE_LOOP_EMAIL` | HumanInTheLoopEmail receives the notification. Empty disables it. |
| `general.notifications.smtp.from` | string | `""` | `ASQS_NOTIFICATIONS_SMTP_FROM` | From is the envelope sender address. |
| `general.notifications.smtp.host` | string | `""` | `ASQS_NOTIFICATIONS_SMTP_HOST` | Host is the SMTP server hostname. Empty disables mail entirely. |
| `general.notifications.smtp.password` | string | `""` | `ASQS_NOTIFICATIONS_SMTP_PASSWORD` | Password authenticates to the relay. |
| `general.notifications.smtp.port` | int | `0` | `ASQS_NOTIFICATIONS_SMTP_PORT` | Port is the SMTP port. |
| `general.notifications.smtp.user` | string | `""` | `ASQS_NOTIFICATIONS_SMTP_USER` | User authenticates to the relay. |
| `general.sandbox.caches.cypress` | string | `""` | `ASQS_SANDBOX_CACHES_CYPRESS` | Cypress is the host Cypress binary cache. |
| `general.sandbox.caches.gradle` | string | `""` | `ASQS_SANDBOX_CACHES_GRADLE` | Gradle is the host Gradle user home. |
| `general.sandbox.caches.maven` | string | `""` | `ASQS_SANDBOX_CACHES_MAVEN` | Maven is the host path mounted as the Maven repository. |
| `general.sandbox.caches.npm` | string | `""` | `ASQS_SANDBOX_CACHES_NPM` | Npm is the host npm cache directory. |
| `general.sandbox.caches.nuget` | string | `""` | `ASQS_SANDBOX_CACHES_NUGET` | NuGet is the host NuGet packages directory. |
| `general.sandbox.caches.pnpm` | string | `""` | `ASQS_SANDBOX_CACHES_PNPM` | Pnpm is the host pnpm store directory. |
| `general.sandbox.docker.binary` | string | `""` | `ASQS_SANDBOX_DOCKER_BINARY` | Binary is the docker CLI to invoke. Empty = "docker" on PATH; set it for podman or a wrapper. |
| `general.sandbox.docker.offline_test` | bool | `true` | `ASQS_SANDBOX_DOCKER_OFFLINE_TEST` | OfflineTest runs the test phase with networking disabled once restore has populated the caches. Inverts v1's runner.docker_disable_offline_test. Default true — an offline test phase is what makes a run reproducible. |
| `general.sandbox.images.dotnet` | string | `""` | `ASQS_SANDBOX_IMAGES_DOTNET` | DotNet is the image for C# projects. |
| `general.sandbox.images.java` | string | `""` | `ASQS_SANDBOX_IMAGES_JAVA` | Java is the default JDK image when neither Maven nor Gradle is detected. |
| `general.sandbox.images.java_gradle` | string | `""` | `ASQS_SANDBOX_IMAGES_JAVA_GRADLE` | JavaGradle is the image for Gradle projects. |
| `general.sandbox.images.java_maven` | string | `""` | `ASQS_SANDBOX_IMAGES_JAVA_MAVEN` | JavaMaven is the image for Maven projects. |
| `general.sandbox.images.node` | string | `""` | `ASQS_SANDBOX_IMAGES_NODE` | Node is the image for JavaScript and TypeScript projects. |
| `general.sandbox.images.playwright` | string | `""` | `ASQS_SANDBOX_IMAGES_PLAYWRIGHT` | Playwright is the image for JS/TS end-to-end runs, which need browsers. |
| `general.sandbox.images.playwright_dotnet` | string | `""` | `ASQS_SANDBOX_IMAGES_PLAYWRIGHT_DOTNET` | PlaywrightDotNet is the Playwright image with the .NET SDK. |
| `general.sandbox.images.playwright_java` | string | `""` | `ASQS_SANDBOX_IMAGES_PLAYWRIGHT_JAVA` | PlaywrightJava is the Playwright image with a JDK. |
| `general.sandbox.network.restore` | string | `""` | `ASQS_SANDBOX_NETWORK_RESTORE` | Restore is the docker --network value for the dependency-restore phase, which legitimately needs a registry. Empty = the default for the target. |
| `general.sandbox.network.test` | string | `""` | `ASQS_SANDBOX_NETWORK_TEST` | Test is the --network value for compile and test. Empty = the default, which is isolated. |
| `general.sandbox.registries.azure_devops_nuget_feed_endpoints` | list of string | empty | `ASQS_SANDBOX_REGISTRIES_AZURE_DEVOPS_NUGET_FEED_ENDPOINTS` | AzureDevOpsNuGetFeedEndpoints lists feed URLs for the Azure artifacts credential envelope. |
| `general.sandbox.registries.credentials` | list of blocks | empty | `— (YAML only)` | Credentials are per-registry credentials for restore inside the sandbox. |
| `general.sandbox.resources.cpus` | float | `0` | `ASQS_SANDBOX_RESOURCES_CPUS` | CPUs limits container CPU, e.g. 2. 0 = no limit. |
| `general.sandbox.resources.memory` | string | `""` | `ASQS_SANDBOX_RESOURCES_MEMORY` | Memory limits container memory, e.g. "4g". Empty = no limit. |
| `general.sandbox.resources.pids_limit` | int | `0` | `ASQS_SANDBOX_RESOURCES_PIDS_LIMIT` | PidsLimit caps processes in the container, bounding a fork bomb in a repository's own build. |
| `general.sandbox.resources.readonly_rootfs` | bool | `false` | `ASQS_SANDBOX_RESOURCES_READONLY_ROOTFS` | ReadonlyRootfs mounts the container root read-only, writing only to the mounted workspace. |
| `general.sandbox.timeout` | string | `""` | `ASQS_SANDBOX_TIMEOUT` | Timeout bounds one sandbox step, e.g. "30m". |
| `general.sandbox.type` | string | `""` | `ASQS_SANDBOX_TYPE` | Type is "docker" or "local". An unrecognised value fails at startup rather than silently falling back — a typo used to green-light a run that compiled nothing and shipped. |
| `general.websearch.allowed_hosts` | list of string | empty | `ASQS_WEBSEARCH_ALLOWED_HOSTS` | AllowedHosts gates which hosts may be fetched. Exact names or "*.example.org". EMPTY DISABLES FETCH — an empty allow-list fails closed, never open. |
| `general.websearch.api_key` | string | `""` | `ASQS_WEBSEARCH_API_KEY` | APIKey authenticates a hosted provider. |
| `general.websearch.api_key_from_env` | string | `""` | `ASQS_WEBSEARCH_API_KEY_FROM_ENV` | APIKeyFromEnv reads that credential from an environment variable instead. |
| `general.websearch.enabled` | bool | `false` | `ASQS_WEBSEARCH_ENABLED` | Enabled turns the search and fetch tools on. Off by default, and off means the tools are not registered at all rather than registered and refusing. |
| `general.websearch.endpoint` | string | `""` | `ASQS_WEBSEARCH_ENDPOINT` | Endpoint is the SearXNG base URL. Operator-configured infrastructure, so cluster-internal http is legitimate here — the https-only rule guards model-chosen URLs, not this. |
| `general.websearch.offline` | bool | `false` | `ASQS_WEBSEARCH_OFFLINE` | Offline serves only from the replay cache and never egresses. A cache miss becomes an answer, not a network call. |
| `general.websearch.provider` | string | `""` | `ASQS_WEBSEARCH_PROVIDER` | Provider selects the backend: "searxng" (self-hosted, queries stay inside your boundary) or "brave" (hosted, needs a key). |

## bootstrap

The optional pre-index step that installs a test framework in a repo that has none.

| Key | Type | Default | Env | Description |
|---|---|---|---|---|
| `bootstrap.e2e_framework.allow_lockfile_change` | bool | `false` | `ASQS_BOOTSTRAP_E2E_FRAMEWORK_ALLOW_LOCKFILE_CHANGE` | AllowLockfileChange permits updating a lockfile. Off keeps the repository's pinned graph intact even when that means bootstrap cannot finish. |
| `bootstrap.e2e_framework.enabled` | bool | `false` | `ASQS_BOOTSTRAP_E2E_FRAMEWORK_ENABLED` | Enabled turns the stage on. Off by default: bootstrap writes to the repository under test. |
| `bootstrap.e2e_framework.execution` | string | `""` | `ASQS_BOOTSTRAP_E2E_FRAMEWORK_EXECUTION` | Execution is "docker" or "local" for the bootstrap's own commands. Empty follows general.sandbox.type. |
| `bootstrap.e2e_framework.mode` | string | `""` | `ASQS_BOOTSTRAP_E2E_FRAMEWORK_MODE` | Mode is how far it may go — "detect" reports only, "apply" edits build manifests. |
| `bootstrap.e2e_framework.pin_versions` | bool | `false` | `ASQS_BOOTSTRAP_E2E_FRAMEWORK_PIN_VERSIONS` | PinVersions writes exact dependency versions rather than ranges, so a later run gets the same stack it was verified against. |
| `bootstrap.require_docker` | bool | `false` | `ASQS_BOOTSTRAP_REQUIRE_DOCKER` | RequireDocker fails a bootstrap fast when it would install on the HOST rather than in an ephemeral container. It applies to both stages, which is why it sits here rather than on each: a deployment that wants host installs refused wants that for the whole step. |
| `bootstrap.test_framework.allow_lockfile_change` | bool | `false` | `ASQS_BOOTSTRAP_TEST_FRAMEWORK_ALLOW_LOCKFILE_CHANGE` | AllowLockfileChange permits updating a lockfile. Off keeps the repository's pinned graph intact even when that means bootstrap cannot finish. |
| `bootstrap.test_framework.enabled` | bool | `false` | `ASQS_BOOTSTRAP_TEST_FRAMEWORK_ENABLED` | Enabled turns the stage on. Off by default: bootstrap writes to the repository under test. |
| `bootstrap.test_framework.execution` | string | `""` | `ASQS_BOOTSTRAP_TEST_FRAMEWORK_EXECUTION` | Execution is "docker" or "local" for the bootstrap's own commands. Empty follows general.sandbox.type. |
| `bootstrap.test_framework.mode` | string | `""` | `ASQS_BOOTSTRAP_TEST_FRAMEWORK_MODE` | Mode is how far it may go — "detect" reports only, "apply" edits build manifests. |
| `bootstrap.test_framework.pin_versions` | bool | `false` | `ASQS_BOOTSTRAP_TEST_FRAMEWORK_PIN_VERSIONS` | PinVersions writes exact dependency versions rather than ranges, so a later run gets the same stack it was verified against. |

## indexer

How the language indexers are located and executed.

| Key | Type | Default | Env | Description |
|---|---|---|---|---|
| `indexer.csharp.indexer_dll_path` | string | `""` | `ASQS_INDEXER_CSHARP_INDEXER_DLL_PATH` | IndexerDLLPath is the published Roslyn indexer DLL. Empty disables C# indexing. Rebuild it after upgrading — a stale DLL emits the pre-B25 FQName format and forces a full reindex. |
| `indexer.dependency_docs.enabled` | bool | `false` | `ASQS_INDEXER_DEPENDENCY_DOCS_ENABLED` | Enabled turns ingestion on. Off by default. |
| `indexer.dependency_docs.maven_repo_dir` | string | `""` | `ASQS_INDEXER_DEPENDENCY_DOCS_MAVEN_REPO_DIR` | MavenRepoDir overrides the local Maven repository location. |
| `indexer.dependency_docs.nuget_packages_dir` | string | `""` | `ASQS_INDEXER_DEPENDENCY_DOCS_NUGET_PACKAGES_DIR` | NuGetPackagesDir overrides the local NuGet packages location. |
| `indexer.docker.cli` | string | `""` | `ASQS_INDEXER_DOCKER_CLI` | CLI is the docker binary for indexer containers. Empty = "docker". |
| `indexer.docker.dotnet_indexer_image` | string | `""` | `ASQS_INDEXER_DOCKER_DOTNET_INDEXER_IMAGE` | DotNetIndexerImage is the image for the C# indexer. |
| `indexer.docker.java_image` | string | `""` | `ASQS_INDEXER_DOCKER_JAVA_IMAGE` | JavaImage is the image for the advanced Java indexer. |
| `indexer.docker.memory` | string | `""` | `ASQS_INDEXER_DOCKER_MEMORY` | Memory limits an indexer container, e.g. "2g". |
| `indexer.docker.node_heap_mb` | int | `0` | `ASQS_INDEXER_DOCKER_NODE_HEAP_MB` | NodeHeapMB sets --max-old-space-size for the Node indexer on large repositories. |
| `indexer.docker.node_image` | string | `""` | `ASQS_INDEXER_DOCKER_NODE_IMAGE` | NodeImage is the image for the JS/TS indexer. |
| `indexer.execution` | string | `""` | `ASQS_INDEXER_EXECUTION` | Execution is "local" or "docker" for the external language indexers. Empty = local. |
| `indexer.java.jar_path` | string | `""` | `ASQS_INDEXER_JAVA_JAR_PATH` | JarPath locates the JavaParser indexer JAR. Empty = no advanced indexing. |
| `indexer.java.mode` | string | `""` | `ASQS_INDEXER_JAVA_MODE` | Mode is auto \| advanced \| minimal. "auto" (the default) picks the advanced JavaParser indexer when a JAR path is configured and falls back to the line-based one otherwise. v1 required BOTH `indexer.type: advanced` AND a jar path, so a deployment that had built the JAR and set its path but never set the type silently got line-based indexing — full AST and symbol resolution quietly replaced by heuristics, with nothing in the run to say so. Making the path sufficient removes the trap; "minimal" remains available for someone who wants the heuristics deliberately. |
| `indexer.jsts.indexer_path` | string | `""` | `ASQS_INDEXER_JSTS_INDEXER_PATH` | IndexerPath is the Node indexer entry point. Empty disables JS/TS indexing. |
| `indexer.policy.critical_module_prefixes` | list of string | empty | `ASQS_INDEXER_POLICY_CRITICAL_MODULE_PREFIXES` | CriticalModulePrefixes raises the priority of gaps under these path prefixes. |
| `indexer.policy.max_gaps` | int | `0` | `ASQS_INDEXER_POLICY_MAX_GAPS` | MaxGaps caps unit-test gaps per run. The --max-gaps flag overrides it. |
| `indexer.policy.max_gaps_e2e` | int | `0` | `ASQS_INDEXER_POLICY_MAX_GAPS_E2E` | MaxGapsE2E caps end-to-end gaps per run. |
| `indexer.policy.max_gaps_per_file` | int | `0` | `ASQS_INDEXER_POLICY_MAX_GAPS_PER_FILE` | MaxGapsPerFile stops one large file consuming the whole budget. |
| `indexer.policy.max_gaps_per_file_e2e` | int | `0` | `ASQS_INDEXER_POLICY_MAX_GAPS_PER_FILE_E2E` | MaxGapsPerFileE2E is the same cap for end-to-end gaps. |
| `indexer.policy.skip_path_prefixes` | list of string | empty | `ASQS_INDEXER_POLICY_SKIP_PATH_PREFIXES` | SkipPathPrefixes excludes paths from indexing entirely — generated code, vendored trees. |

## retrieval

Planning budgets and the context assembled for each gap.

| Key | Type | Default | Env | Description |
|---|---|---|---|---|
| `retrieval.context.compact.enabled` | bool | `true` | `ASQS_RETRIEVAL_CONTEXT_COMPACT_ENABLED` | Enabled turns the feature on. Absent means the documented default, which is why it is a pointer: for a default-true toggle, absent and false are different instructions. |
| `retrieval.context.dependency_max_depth` | int | `0` | `ASQS_RETRIEVAL_CONTEXT_DEPENDENCY_MAX_DEPTH` | DependencyMaxDepth is how many edges out from the target to walk. 0 = the built-in 2. |
| `retrieval.failure_hint.file` | string | `""` | `ASQS_RETRIEVAL_FAILURE_HINT_FILE` | File is a repo-relative compiler or CI log read before planning, so retrieval weights the code the failure implicates. Must stay under the repository root. |
| `retrieval.failure_hint.persist` | bool | `false` | `ASQS_RETRIEVAL_FAILURE_HINT_PERSIST` | Persist writes this run's failing compile and test output for the next run to read, and removes the file when a run goes green. |
| `retrieval.fusion` | string | `""` | `ASQS_RETRIEVAL_FUSION` | Fusion selects how dense and lexical channels combine: "dense" or "rrf". Dense is the default because rrf measured as a regression. |
| `retrieval.max_context_tokens` | int | `0` | `ASQS_RETRIEVAL_MAX_CONTEXT_TOKENS` | MaxContextTokens caps the assembled prompt. 0 = derive it from the model's context window. |
| `retrieval.policy.abstention.enabled` | bool | `true` | `ASQS_RETRIEVAL_POLICY_ABSTENTION_ENABLED` | Enabled turns the feature on. Absent means the documented default, which is why it is a pointer: for a default-true toggle, absent and false are different instructions. |
| `retrieval.policy.hybrid_module_filter.enabled` | bool | `true` | `ASQS_RETRIEVAL_POLICY_HYBRID_MODULE_FILTER_ENABLED` | Enabled turns the feature on. Absent means the documented default, which is why it is a pointer: for a default-true toggle, absent and false are different instructions. |
| `retrieval.policy.min_similar_tests_for_generation` | int | `0` | `ASQS_RETRIEVAL_POLICY_MIN_SIMILAR_TESTS_FOR_GENERATION` | MinSimilarTestsForGeneration abstains when fewer than this many similar tests were found — generating without an example of the repository's conventions produces tests it rejects. |
| `retrieval.policy.min_similarity_cosine` | float | `0` | `ASQS_RETRIEVAL_POLICY_MIN_SIMILARITY_COSINE` | MinSimilarityCosine is the floor below which a retrieved chunk is not considered similar. |
| `retrieval.profile` | string | `""` | `ASQS_RETRIEVAL_PROFILE` | Profile selects the retrieval shape for unit gaps: java_unit, http_api, react_feature, nest_module, full_stack. |
| `retrieval.profile_budgets` | mapping of string to blocks | empty | `— (YAML only)` | ProfileBudgets overrides the section caps per profile. This is where budget tuning lives; the global caps were frozen into constants because per-profile is the only level at which the numbers mean anything. |
| `retrieval.profile_e2e` | string | `""` | `ASQS_RETRIEVAL_PROFILE_E2E` | ProfileE2E is the profile for end-to-end gaps, usually e2e_playwright. |

## generation

Test generation, plus the symbol-doc and overview workstreams that run in the same phase.

| Key | Type | Default | Env | Description |
|---|---|---|---|---|
| `generation.docs.llm.api_key` | string | `""` | `ASQS_GENERATION_DOCS_LLM_API_KEY` | APIKey overrides the general credential for this step, in clear text. Empty falls back. |
| `generation.docs.llm.api_key_from_env` | string | `""` | `ASQS_GENERATION_DOCS_LLM_API_KEY_FROM_ENV` | APIKeyFromEnv names an environment variable to read that credential from instead. |
| `generation.docs.llm.model` | string | `""` | `ASQS_GENERATION_DOCS_LLM_MODEL` | Model overrides general.llm.model for this step. Empty falls back. |
| `generation.docs.llm.provider` | string | `""` | `ASQS_GENERATION_DOCS_LLM_PROVIDER` | Provider overrides general.llm.provider for this step. Empty falls back — which is the point of a step override: set only what differs. |
| `generation.docs.overview.enabled` | bool | `true` | `ASQS_GENERATION_DOCS_OVERVIEW_ENABLED` | Enabled inverts v1's indexer.disable_overview_doc_generation. Default true. |
| `generation.docs.overview.full_rewrite` | bool | `false` | `ASQS_GENERATION_DOCS_OVERVIEW_FULL_REWRITE` | FullRewrite regenerates the whole overview instead of applying a delta to the existing one. |
| `generation.docs.overview.max_files_per_slice` | int | `0` | `ASQS_GENERATION_DOCS_OVERVIEW_MAX_FILES_PER_SLICE` | MaxFilesPerSlice caps files described in one overview request. 0 = the built-in cap. |
| `generation.docs.overview.max_index_runes_per_slice` | int | `0` | `ASQS_GENERATION_DOCS_OVERVIEW_MAX_INDEX_RUNES_PER_SLICE` | MaxIndexRunesPerSlice caps index text in one overview request. 0 = the built-in cap; -1 removes the clamp entirely. |
| `generation.docs.overview.path` | string | `""` | `ASQS_GENERATION_DOCS_OVERVIEW_PATH` | Path is where the overview is written, repo-relative. |
| `generation.llm.api_key` | string | `""` | `ASQS_GENERATION_LLM_API_KEY` | APIKey overrides the general credential for this step, in clear text. Empty falls back. |
| `generation.llm.api_key_from_env` | string | `""` | `ASQS_GENERATION_LLM_API_KEY_FROM_ENV` | APIKeyFromEnv names an environment variable to read that credential from instead. |
| `generation.llm.model` | string | `""` | `ASQS_GENERATION_LLM_MODEL` | Model overrides general.llm.model for this step. Empty falls back. |
| `generation.llm.provider` | string | `""` | `ASQS_GENERATION_LLM_PROVIDER` | Provider overrides general.llm.provider for this step. Empty falls back — which is the point of a step override: set only what differs. |
| `generation.policy.format.only_added` | bool | `false` | `ASQS_GENERATION_POLICY_FORMAT_ONLY_ADDED` | OnlyAdded runs the formatter per written file instead of repository-wide, so a run does not reformat code it did not touch. The command is general.build.format_command. |
| `generation.policy.prefer_default_test_suffix` | bool | `false` | `ASQS_GENERATION_POLICY_PREFER_DEFAULT_TEST_SUFFIX` | PreferDefaultTestSuffix always emits the convention default path instead of extending an existing test file that uses a different suffix. |
| `generation.policy.project_intel.enabled` | bool | `true` | `ASQS_GENERATION_POLICY_PROJECT_INTEL_ENABLED` | Enabled turns the scan on. Default on — it needs no configuration to be useful. |
| `generation.policy.project_intel.extra_doc_globs` | list of string | empty | `ASQS_GENERATION_POLICY_PROJECT_INTEL_EXTRA_DOC_GLOBS` | ExtraDocGlobs adds repo-relative globs to treat as documentation on this repository's layout. |
| `generation.policy.project_intel.extra_skill_globs` | list of string | empty | `ASQS_GENERATION_POLICY_PROJECT_INTEL_EXTRA_SKILL_GLOBS` | ExtraSkillGlobs adds globs to treat as agent skill files. |
| `generation.policy.project_intel.use_embeddings_rank` | bool | `false` | `ASQS_GENERATION_POLICY_PROJECT_INTEL_USE_EMBEDDINGS_RANK` | UseEmbeddingsRank reranks candidate docs by embedding cosine rather than lexical score alone. Without an embedder the run degrades to lexical ranking rather than failing. |
| `generation.policy.reconcile_duplicate_test_artifacts` | bool | `false` | `ASQS_GENERATION_POLICY_RECONCILE_DUPLICATE_TEST_ARTIFACTS` | ReconcileDuplicateTestArtifacts merges a generated artifact into the existing test file it duplicates, instead of leaving two files that test the same symbol. Report-only by default. |
| `generation.policy.structured_output.enabled` | bool | `true` | `ASQS_GENERATION_POLICY_STRUCTURED_OUTPUT_ENABLED` | Enabled turns the feature on. Absent means the documented default, which is why it is a pointer: for a default-true toggle, absent and false are different instructions. |
| `generation.policy.tools.enabled` | bool | unset | `ASQS_GENERATION_POLICY_TOOLS_ENABLED` | Enabled turns native tool calling on for generation. |
| `generation.policy.tools.max_calls_per_run` | int | `0` | `ASQS_GENERATION_POLICY_TOOLS_MAX_CALLS_PER_RUN` | MaxCallsPerRun bounds total calls for one gap. 0 = the built-in cap. |
| `generation.policy.tools.max_calls_per_turn` | int | `0` | `ASQS_GENERATION_POLICY_TOOLS_MAX_CALLS_PER_TURN` | MaxCallsPerTurn bounds parallel tool calls accepted in one turn. 0 = the built-in cap. |
| `generation.policy.tools.max_result_chars` | int | `0` | `ASQS_GENERATION_POLICY_TOOLS_MAX_RESULT_CHARS` | MaxResultChars caps one tool result, which draws on the same prompt allowance as the context. 0 = the built-in cap. |
| `generation.policy.tools.max_turns` | int | `0` | `ASQS_GENERATION_POLICY_TOOLS_MAX_TURNS` | MaxTurns bounds model→tool→model round trips for one gap. 0 = the built-in cap. |
| `generation.policy.tools.prompted_fallback` | bool | unset | `ASQS_GENERATION_POLICY_TOOLS_PROMPTED_FALLBACK` | PromptedFallback offers the tools through the prompt when the provider has no native tool API. |
| `generation.policy.two_phase.enabled` | bool | `true` | `ASQS_GENERATION_POLICY_TWO_PHASE_ENABLED` | Enabled turns the feature on. Absent means the documented default, which is why it is a pointer: for a default-true toggle, absent and false are different instructions. |

## fixer

The run-scope evaluate/fix loop. Evaluation is part of this step: what an operator tunes about evaluation is how hard the fixer tries.

| Key | Type | Default | Env | Description |
|---|---|---|---|---|
| `fixer.iterations.max` | int | `0` | `ASQS_FIXER_ITERATIONS_MAX` | Max is the ceiling on fix iterations for the run. 0 = the built-in default. |
| `fixer.iterations.start` | int | `0` | `ASQS_FIXER_ITERATIONS_START` | Start is the budget the first run gets, when a deployment escalates across reruns rather than spending everything at once. 0 = use Max. |
| `fixer.llm.api_key` | string | `""` | `ASQS_FIXER_LLM_API_KEY` | APIKey overrides the general credential for this step, in clear text. Empty falls back. |
| `fixer.llm.api_key_from_env` | string | `""` | `ASQS_FIXER_LLM_API_KEY_FROM_ENV` | APIKeyFromEnv names an environment variable to read that credential from instead. |
| `fixer.llm.model` | string | `""` | `ASQS_FIXER_LLM_MODEL` | Model overrides general.llm.model for this step. Empty falls back. |
| `fixer.llm.provider` | string | `""` | `ASQS_FIXER_LLM_PROVIDER` | Provider overrides general.llm.provider for this step. Empty falls back — which is the point of a step override: set only what differs. |
| `fixer.policy.abort_on_unrecoverable_env_compile_failure` | bool | `false` | `ASQS_FIXER_POLICY_ABORT_ON_UNRECOVERABLE_ENV_COMPILE_FAILURE` | AbortOnUnrecoverableEnvCompileFailure stops the loop when compilation fails for a reason clearly outside the generated artifact, such as a private feed needing credentials the container cannot supply. Without it every iteration burns identically. |
| `fixer.policy.backoff` | string | `""` | `ASQS_FIXER_POLICY_BACKOFF` | Backoff waits between fix attempts, e.g. "5s". Empty = no wait. |
| `fixer.policy.circuit_breakers.no_progress_stop_threshold` | int | `0` | `ASQS_FIXER_POLICY_CIRCUIT_BREAKERS_NO_PROGRESS_STOP_THRESHOLD` | NoProgressStopThreshold stops after this many rounds that changed nothing measurable. |
| `fixer.policy.circuit_breakers.recurrence_stop_threshold` | int | `0` | `ASQS_FIXER_POLICY_CIRCUIT_BREAKERS_RECURRENCE_STOP_THRESHOLD` | RecurrenceStopThreshold stops when a previously fixed failure keeps coming back. |
| `fixer.policy.circuit_breakers.repeat_stop_threshold` | int | `0` | `ASQS_FIXER_POLICY_CIRCUIT_BREAKERS_REPEAT_STOP_THRESHOLD` | RepeatStopThreshold stops after this many rounds producing an identical diagnostic. |
| `fixer.policy.circuit_breakers.repeated_test_failure_threshold` | int | `0` | `ASQS_FIXER_POLICY_CIRCUIT_BREAKERS_REPEATED_TEST_FAILURE_THRESHOLD` | RepeatedTestFailureThreshold discards a generated test whose failure fingerprint recurs this many times, so one bad artifact cannot hold the whole run unstable. |
| `fixer.policy.compile_once_per_eval` | bool | `false` | `ASQS_FIXER_POLICY_COMPILE_ONCE_PER_EVAL` | CompileOncePerEval compiles the project once per round instead of per artifact. |
| `fixer.policy.context_runes_max` | int | `0` | `ASQS_FIXER_POLICY_CONTEXT_RUNES_MAX` | ContextRunesMax caps the repair prompt's file context. 0 = the built-in cap. |
| `fixer.policy.dependency_signature_only` | bool | `false` | `ASQS_FIXER_POLICY_DEPENDENCY_SIGNATURE_ONLY` | DependencySignatureOnly sends dependency signatures rather than whole bodies, so the repair prompt spends its budget on the failing code. |
| `fixer.policy.error_log_llm_summary.enabled` | bool | `true` | `ASQS_FIXER_POLICY_ERROR_LOG_LLM_SUMMARY_ENABLED` | Enabled turns the feature on. Absent means the documented default, which is why it is a pointer: for a default-true toggle, absent and false are different instructions. |
| `fixer.policy.skip_on_infrastructure_failure` | bool | `false` | `ASQS_FIXER_POLICY_SKIP_ON_INFRASTRUCTURE_FAILURE` | SkipOnInfrastructureFailure gives up immediately when the failure is a missing database or a bad connection string — no repair to the generated test can fix the environment. |
| `fixer.policy.structured_output.enabled` | bool | `true` | `ASQS_FIXER_POLICY_STRUCTURED_OUTPUT_ENABLED` | Enabled turns the feature on. Absent means the documented default, which is why it is a pointer: for a default-true toggle, absent and false are different instructions. |
| `fixer.policy.structured_user_message` | bool | `false` | `ASQS_FIXER_POLICY_STRUCTURED_USER_MESSAGE` | StructuredUserMessage sends the fix request as structured JSON rather than prose. |
| `fixer.policy.tools.enabled` | bool | unset | `ASQS_FIXER_POLICY_TOOLS_ENABLED` | Enabled turns tool calling on for repair. |
| `fixer.policy.tools.max_turns` | int | `0` | `ASQS_FIXER_POLICY_TOOLS_MAX_TURNS` | MaxTurns bounds model→tool→model round trips per fix attempt. 0 = the built-in cap, which is lower than generation's: a fixer already has the diagnostic. |

## Appendix: environment-only settings

Every schema key already has a derived environment variable, so this list is short by construction.
What is on it is here for a reason: a break-glass switch that should not be easy to leave on in a
config file, a variable the platform or a toolchain already defines that asqs-core reads rather than
reinventing, or a test input. The cost of env-only is discoverability, which is what this appendix
pays back.

The list is checked against a mechanical sweep of every `os.Getenv` and
`os.LookupEnv` call under `cmd/`, `internal/` and `tools/`; a
test fails when it goes stale.

### Security-relevant

Setting these weakens a protection. Read the description before using one.

| Variable | Read by | What it does |
|---|---|---|
| `ASQS_ALLOW_EMBEDDING_DIM_RESET` | `internal/storage/embeddings` | Break glass: permits changing the embedding dimension on a POPULATED table, which drops every stored vector. Deliberately NOT a config key — an operator should have to reach for it once, not leave it set in a file where a later run silently discards a corpus. |

### asqs-core settings

Set by whoever deploys asqs-core.

| Variable | Read by | What it does |
|---|---|---|
| `ASQS_LOG_RESOLVED_LLM_ENDPOINTS` | `internal/llm/embeddings` | 1/true logs the resolved Ollama chat and embedding URLs when clients are built. A debugging aid for base-URL and proxy problems, where the question is always what the client actually resolved. |
| `RUNNER_DOTNET_FALLBACK_TARGET_FRAMEWORK` | `internal/testbootstrap` | Target framework for a generated C# test project when no .csproj in the repository names one. NOTE: this is read WITHOUT the ASQS_ prefix, unlike every schema-derived variable — a v1 leftover, and the schema key general.build.dotnet_fallback_target_framework is the supported spelling. |

### Inherited from the environment

Not asqs-core settings — variables the platform or a toolchain already defines, which asqs-core reads rather than reinventing. Listed so it is clear what the process depends on.

| Variable | Read by | What it does |
|---|---|---|
| `AWS_REGION` | `internal/llm/embeddings` | AWS region for a Bedrock embedding endpoint. Standard AWS variable; asqs-core reads it rather than adding a key that would disagree with the rest of the toolchain. |
| `AWS_BEDROCK_MODEL_ID` | `internal/llm/embeddings` | Overrides the Bedrock embedding model id. |
| `GOOGLE_PROJECT_ID` | `internal/llm/embeddings` | Google Cloud project for a Vertex AI embedding endpoint. Standard Google variable. |
| `VERTEXAI_MODEL_ID` | `internal/llm/embeddings` | Overrides the Vertex AI embedding model id. |
| `VERTEXAI_TOKEN` | `internal/llm/embeddings` | Bearer token for Vertex AI, when not using application default credentials. |
| `NUGET_PACKAGES` | `internal/evaluator/apisurface` | The .NET SDK's own global packages folder. The API-surface resolver reads it to locate assemblies rather than assuming the default path. |
| `PATH` | `internal/runner` | Used to locate build toolchains for the local sandbox. Its absence is what produces the 'is not on PATH' remediation. |
| `PLAYWRIGHT_BROWSERS_PATH` | `internal/runner` | Playwright's own browser cache location. The E2E preflight reads it to tell 'browsers not installed' apart from 'installed somewhere else'. |
| `CYPRESS_CACHE_FOLDER` | `internal/runner` | Cypress's own binary cache location, read by the E2E preflight for the same reason. |

### Test and benchmark only

Unset means the corresponding test SKIPS rather than fails, so a green run does not by itself prove these ran.

| Variable | Read by | What it does |
|---|---|---|
| `ASQS_TEST_METADATA_URL` | `internal/storage (tests)` | Postgres URL for the live-database tests. UNSET MEANS THOSE TESTS SKIP, so a green run does not by itself prove they executed. |

