# Configuration

All external systems (Postgres, GitHub, LLM APIs, runner) are configured via the central `Config`. Config can be changed per client using files or environment variables.

## Loading

- **From file**: `config.Load(config.LoadOptions{ConfigPath: "config.yaml"})`
- **From env**: Set `ASQS_CONFIG_PATH` to a YAML path, or use `config.LoadFromEnv()` with no file (all settings from env).
- **Per-client**: Set `ASQS_CLIENT_ID=acme`; then env vars `ASQS_ACME_*` override the base config (e.g. `ASQS_ACME_DATABASE_METADATA_URL`).

Precedence: explicit option > env (client-prefixed if set) > env (base prefix) > file > defaults.

## Environment variables (prefix `ASQS_` by default)

| Variable | Description |
|----------|-------------|
| `ASQS_CONFIG_PATH` | Path to YAML config file |
| `ASQS_CLIENT_ID` | Client/tenant ID for per-client env overrides |
| `ASQS_DATABASE_METADATA_URL` | Postgres URL for metadata store |
| `ASQS_DATABASE_EMBEDDINGS_URL` | Postgres URL for embeddings (defaults to metadata URL) |
| `ASQS_DATABASE_EMBEDDINGS_DIMENSION` | Vector dimension (default 1536) |
| `ASQS_DATABASE_MAX_OPEN_CONNS` | Connection pool size |
| `ASQS_VCS_PROVIDER` | `github` (default) or `gitlab` |
| `ASQS_GITHUB_TOKEN` | GitHub PAT |
| `ASQS_GITHUB_BASE_URL` | GitHub Enterprise API base URL |
| `ASQS_GITHUB_DEFAULT_OWNER` / `ASQS_GITHUB_DEFAULT_REPO` | Default repo for PRs |
| `ASQS_LLM_PROVIDER` | `openai`, `anthropic`, `azure_openai` |
| `ASQS_LLM_API_KEY` | API key (or use `ASQS_LLM_API_KEY_FROM_ENV`) |
| `ASQS_LLM_MODEL` | Chat model ID |
| `ASQS_LLM_EMBEDDING_MODEL` | Embedding model ID |
| `ASQS_LLM_BASE_URL` | Overrides YAML **`llm.base_url`** after load (Azure, proxy, Ollama gateway URL, …) |
| `ASQS_LLM_OLLAMA_NUM_CTX` | Ollama only: positive int → `options.num_ctx` on POST `/api/chat` |
| `ASQS_LOG_RESOLVED_LLM_ENDPOINTS` | `1` / `true`: log resolved Ollama chat + embed HTTP URLs when clients are built (debug) |
| `ASQS_RUNNER_TYPE` | `docker` (default) or `local` |
| `ASQS_RUNNER_DOCKER_ENDPOINT` | Docker API endpoint |
| `ASQS_INDEXER_SCHEDULE` | Cron expression (e.g. `0 1 * * *` = daily at 01:00) |
| `ASQS_INDEXER_RUN_ON_FIRST_START` | Run indexer once at startup when no previous run exists (`true`/`false`) |
| `ASQS_AUDIT_FILE_PATH` | Optional file to append audit entries (one JSON line per step); empty = DB only |

For a client `acme`, set `ASQS_CLIENT_ID=acme` and e.g. `ASQS_ACME_DATABASE_METADATA_URL=...` to override only that client’s database.
