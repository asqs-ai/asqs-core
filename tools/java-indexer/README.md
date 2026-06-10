# Java indexer

Two backends share the same **`ParsedFile`** contract for `internal/intelligence/indexer`:

| Mode | Entry | Use case |
|------|--------|----------|
| **Minimal** | Go `javaindexer.Index` | Line-based; Spring `API_ROUTE` heuristics; no JVM. |
| **Advanced** | `mvn package` → JAR, `javaindexer.RunJAR` | JavaParser + symbol solver; richer graph (below). |

## Running the advanced JAR in Docker (no host JVM)

Set **`indexer.execution: docker`** in config (advanced Java only). QualityBot runs:

`docker run --rm` with **`indexer.docker_java_image`** (default **`eclipse-temurin:21-jre-jammy`**), bind-mounts the repo at **`/workspace`** (read-only) and the JAR at **`/indexer/java-indexer.jar`**, **`--network none`**, optional **`docker_memory`** / **`docker_cpus`**. JSONL is read from container stdout; file paths are normalized to repo-relative.

Integration smoke (Docker daemon + built JAR):

```bash
go test -tags=integration ./tools/java-indexer/... -count=1 -run TestRunJARDocker_smoke
```

See **`docs/PLAN-INDEXER-DOCKER.md`** (P0).

## Advanced JAR (`JavaIndexer.java`)

```bash
mvn -q package
java -jar target/java-indexer-0.1.0.jar /path/to/java/repo
```

Stdout is **JSONL**. The first line may be **`kind: java_meta`** (Phase-1–style discovery: Maven/Gradle counts, `mavenRootModules`, Java file count). The Go runner **ignores** that line when building `ParsedFile` maps.

Per **top-level** `class` / `interface` / **`record`** / `enum` (nested types omitted):

- **Package & module graph:** `packageName` → **`MODULE`** symbol; `imports[]` → **`IMPORTS`** edges from the **host type** (`fqName`) to each imported type. Import lines are normalized (`import pkg.Type;` → `pkg.Type`) so edges resolve to indexed classes (cross-file overview graph).
- **Type span:** `startLine` / `endLine`, `javadocSummary`, `annotations[]` (in symbol `signature` JSON where applicable).
- **Inheritance:** `extendsTypes[]` / `implementsTypes[]` → **`EXTENDS`** / **`IMPLEMENTS`** edges.
- **Members:** `fieldDetails` → **`field`** symbols; **`method`** and **`constructor`** symbols with resolved **`calls`** (qualified signatures when possible).
- **Spring Web:** `springRoutes[]` (from `@RestController` / `@Controller` + mapping annotations) → **`API_ROUTE`**, **`CONTAINS`**, **`ROUTE_TO_HANDLER`** (same naming as `spring_web.go`).

**Filtered paths:** `target/`, `build/`, `.git`, `node_modules`, `.gradle`, `out`, `dist` (and similar) are skipped when collecting `.java` files.

## Go tests

```bash
go test ./tools/java-indexer/... -count=1
```
