# C# indexer (Roslyn)

Emits **one JSON line per `.cs` file** (same contract as `internal/intelligence/indexer/lang.go` → `LangIndexerJSON`) for the Go pipeline: symbols, edges (`calls`, `imports`, `CALLS_API`, `ROUTE_TO_HANDLER`), ASP.NET `API_ROUTE`, Playwright **`E2E_SPEC`** stubs, and **`API_CLIENT_REQUEST`** for `HttpClient` call sites.

## Build

Requires **.NET SDK 10** (project targets **net10.0**).

```bash
dotnet publish tools/csharp-indexer/CSharpIndexer.csproj -c Release -o tools/csharp-indexer/publish
```

From the **`asqs-go`** repo root you can use **`make publish-csharp-indexer`** (same command). **GitHub Actions** (`.github/workflows/ci.yml`) runs this **`dotnet publish`** on push/PR so the project stays buildable.

## Run (local)

```bash
dotnet tools/csharp-indexer/publish/CSharpIndexer.dll /path/to/repo
```

## Config (QualityBot)

```yaml
indexer:
  csharp_indexer_dll_path: "tools/csharp-indexer/publish/CSharpIndexer.dll"
  execution: local   # or docker
  docker_dotnet_indexer_image: "mcr.microsoft.com/dotnet/sdk:10.0"
```

## Method FQ names

Methods use **`Namespace.Type#Member`** so Go `indexer.Run` can synthesize **`contains`** edges like Java (`Type#method` → enclosing type).

## Docs

See **[`docs/CSHARP-PARITY.md`](../../docs/CSHARP-PARITY.md)** and **[`docs/PLAN.md` — C# parity](../../docs/PLAN.md#c-first-class-parity-java-reference)**.
