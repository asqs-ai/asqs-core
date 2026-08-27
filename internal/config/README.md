# Configuration

Everything external — Postgres, the git platform, LLM APIs, the sandbox — is configured through one
`Config`. The YAML surface is **schema v2**, organised by pipeline step rather than by accretion.

## Loading

- **From file**: `config.Load(config.LoadOptions{ConfigPath: "config.yaml"})`
- **From env**: set `ASQS_CONFIG_PATH` to a YAML path, or call `config.LoadFromEnv()` with no file at
  all (every value from the environment).
- **Per-client**: set `ASQS_CLIENT_ID=acme`; `ASQS_ACME_*` then beats `ASQS_*`.

The order is **YAML → env → defaults → translate**, and it is load-bearing. Applying defaults before
the environment overlay would materialise a `true` for every default-true toggle, and the overlay
could no longer tell "operator set false" from "unset variable" — a default-true feature would become
impossible to switch off from the environment.

## Unknown keys are an error

Decoding is strict. A misspelled or misplaced key fails the load and names its own path. This is
deliberate: the previous lenient parser accepted a misspelled `max_similair_tests` with no error, no
warning and no effect, which is the same invisible-no-op failure the schema restructure existed to
remove, except self-inflicted.

A pre-v2 file is recognised as such and told which sections moved, rather than being reported as a
typo in whichever section happens to come first. There is no automatic migration — start from
`config.example.yaml`.

## Environment variables are derived, not tagged

There is no `env:` struct tag anywhere. A variable's name is `ASQS_` plus the key's dotted path,
upper-cased, with a leading `general.` stripped:

| Key | Variable |
|---|---|
| `general.database.metadata_url` | `ASQS_DATABASE_METADATA_URL` |
| `general.llm.provider` | `ASQS_LLM_PROVIDER` |
| `general.git.token` | `ASQS_GIT_TOKEN` |
| `general.sandbox.type` | `ASQS_SANDBOX_TYPE` |
| `indexer.policy.max_gaps` | `ASQS_INDEXER_POLICY_MAX_GAPS` |
| `generation.policy.tools.max_turns` | `ASQS_GENERATION_POLICY_TOOLS_MAX_TURNS` |
| `fixer.iterations.max` | `ASQS_FIXER_ITERATIONS_MAX` |

One rule replaced a hand-maintained tag per field, and that is what fixed a whole defect class: the
old tag-driven walker handled string, int and bool and silently skipped everything else, so every
documented float and list variable did nothing at all. Here an undecodable value is an **error**, so
a field of a new type cannot ship as an inert variable.

Three variables are not derived from the schema, because they are about finding the config rather
than being in it:

| Variable | Description |
|---|---|
| `ASQS_CONFIG_PATH` | Path to the YAML file |
| `ASQS_CLIENT_ID` | Tenant id for per-client overrides |
| `ASQS_LOG_RESOLVED_LLM_ENDPOINTS` | `1`/`true`: log resolved Ollama chat and embed URLs when clients are built |

## Positive booleans

Every toggle is `enabled` (or a positive noun) — never `disable_*`. Eight v1 keys were inverted to get
here. The runtime struct still carries the negative fields, and `TranslateV2ToRuntime` is the single
place each inversion happens; every one is pinned by a test, because a missing `!` would flip a
feature for every deployment without changing a visible key.

## Constants, not knobs

Settings with no plausible per-deployment value are constants at their consumers, not keys — chunk
sizing, section budgets, the project-intel scan shape, cache locations. A default earns a change
through measurement, which an operator has no basis to do from a config file.
