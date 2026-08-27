<!--
Ported from the engine's docs/TEST-FRAMEWORK-BOOTSTRAP.md and adapted for asqs-core: config keys are
rewritten to core's v1 spellings (runner.test_framework_bootstrap.*, runner.docker_image_*), and the
enterprise session-engine paths are omitted. The detection matrices and the "smoke tests are
instruments" rationale are the substance and are kept verbatim.

Two things to know before enabling this:
  * it is OFF by default, and with it off every path behaves exactly as it did before;
  * a repository whose stack looks complete but cannot run a test FAILS the run, deliberately —
    that is the whole value of the verification phase, and softening it to a warning removes it.
-->

# Test framework auto-bootstrap

When **`runner.test_framework_bootstrap.enabled`** is `true`, asqs-core can **detect** whether the target repo already has a test runner for the dominant stack. If not, it **patches the build** and **verifies** compilation:

- **JavaScript / TypeScript** — **framework-aware**. Bootstrap detects **Angular**, **React** (with or without Vite), **NestJS**, **ESM Node** or plain, picks the runner that framework can actually use (**Jest** or **Vitest**), installs the matching stack — jsdom + Testing Library for React, `jest-preset-angular` for Angular, `@nestjs/testing` for Nest — then **runs** a smoke test to prove it. It no longer overwrites `scripts.test`. See [JS/TS detection](#javascript--typescript-1) and [JS/TS smoke tests](#jsts-smoke-tests).
- **Java** — **framework-aware**. Bootstrap detects the application framework (**Spring Boot**, **Quarkus**, **Micronaut**, **Android**, or plain) from `pom.xml` / `build.gradle(.kts)` and installs the test stack that framework actually needs, then **compiles and runs** a smoke test to prove it. A Spring Boot module gets **`spring-boot-starter-test`**; a plain module gets **JUnit 5 + Mockito + AssertJ + Surefire**. See [Java detection](#java) and [Java smoke tests](#java-smoke-tests).
- **C#** — **framework-aware**. Bootstrap detects the application framework (**ASP.NET Core**, **Blazor WASM**, **MAUI/mobile/desktop** → declined, or plain) and the test runner the repo already uses (**xUnit** / **NUnit** / **MSTest**), installs the stack that combination needs — including **Moq** and **FluentAssertions**, plus **Microsoft.AspNetCore.Mvc.Testing** pinned to the TargetFramework — then **builds and runs** a smoke test to prove it. See [C# detection](#c-1) and [C# smoke tests](#c-smoke-tests).

This runs **after** checkout and **before** the language indexer, so metadata and runner commands see the new setup.

## Configuration

```yaml
bootstrap:
  policy:
    test_framework:
      enabled: false          # set true to enable
      mode: auto              # auto | jest | junit | xunit | off
      pin_versions: true      # exact semver in package.json (JS/TS only)
      allow_lockfile_change: true   # false → npm ci / frozen lockfile when lock exists (JS/TS only)
```

| Field | Meaning |
|--------|---------|
| `enabled` | Master switch. |
| `mode` | `auto`: default stack per language when no framework (Jest / JUnit 5 / xUnit). `jest` / `junit` / `xunit`: force one stack (others skip). `off`: disable even if `enabled: true`. |
| `pin_versions` | Use exact versions (e.g. `29.7.0`) instead of `^…` (package.json). |
| `allow_lockfile_change` | If `false` and a lockfile exists, install uses `npm ci`, `pnpm install --frozen-lockfile`, or `yarn install --frozen-lockfile`. **Does not apply** to the **first** install after bootstrap **patches** `package.json` (Jest / Playwright / Cypress): that install always refreshes the lockfile (`pnpm install --no-frozen-lockfile`, `npm install`, `yarn install` + `YARN_ENABLE_IMMUTABLE_INSTALLS=false`) so new devDependencies can be written even under `CI=true`. |

**pnpm:** Bootstrap runs `pnpm install` with **`--store-dir`** outside the repo (host: OS user cache `…/asqs-pnpm-store`; Docker: `/root/.local/share/pnpm/store`) so a project `.npmrc` that sets `store-dir=.pnpm-store` does not create thousands of files under the repo. If `.pnpm-store/` is not already ignored, bootstrap **appends** it to **`.gitignore`** (same for **E2E** bootstrap when the repo uses pnpm).

**Environment:** `ASQS_BOOTSTRAP_POLICY_TEST_FRAMEWORK_ENABLED`, `ASQS_BOOTSTRAP_POLICY_TEST_FRAMEWORK_MODE`, etc. (see `internal/config/config.go`).

Subprocess timeouts for install/verify use **`runner.timeout`** (capped internally for the bootstrap phase; see `internal/testbootstrap/helpers.go`).

## Detection (no-op when already set up)

### JavaScript / TypeScript

A config file (`jest.config.*`, `vitest.config.*`, `karma.conf.*`, `jasmine.json`, `.mocharc.*`) still short-circuits detection. Otherwise the question is **which required packages are missing**, framework by framework — "jest is in devDependencies" is not enough, because a React package with plain Jest runs in a `node` environment with no `document` and no Testing Library.

**Vite decides the runner.** Any package that builds with Vite gets **Vitest**, whatever it is written in — React, Vue, Svelte, Solid, Lit, or vanilla TypeScript. Two frameworks override that because they bring their own answer: **Angular** ships its own builder and `jest-preset-angular` (Vitest there needs `@analogjs/vitest-angular`, which bootstrap does not wire), and **NestJS** is a CommonJS server framework whose own convention is Jest.

| Detected | Detected from | Runner | Adds |
|---|---|---|---|
| **Angular** | `@angular/core` | Jest | `jest-preset-angular` (peer-matched), `jest`, `@types/jest`, `setup-jest.ts`, `tsconfig.spec.json` |
| **NestJS** | `@nestjs/core` | Jest | `ts-jest`, `@types/jest`, **`@nestjs/testing` at the matching major** |
| **React + Vite** | `react` + a `vite.config.*` | **Vitest** | `vitest` (Vite-major matched), `jsdom`, `@testing-library/react` (React-major matched), `@testing-library/jest-dom` |
| **Vue** | `vue` | **Vitest** | `vitest`, `jsdom`, **`@vue/test-utils` at the Vue-major line**, `@testing-library/jest-dom` |
| **Svelte** | `svelte` | **Vitest** | `vitest`, `jsdom`, `@testing-library/svelte`, `@testing-library/jest-dom`, plus `resolve.conditions: ['browser']` |
| **Any other Vite package** | a `vite.config.*` | **Vitest** | `vitest`, plus `jsdom` when the package renders to a DOM |
| **React without Vite** | `react` | Jest | `jest-environment-jsdom`, Testing Library, `ts-jest` when TS |
| **ESM Node** | `"type": "module"` | **Vitest** | `vitest` |
| **plain** | anything else | Jest | `jest` (+ `ts-jest`, `@types/*` when TS) |

"Renders to a DOM" means a known UI library is present (`react`, `vue`, `svelte`, `solid-js`, `preact`, `lit`, …; React, Vue and Svelte have dedicated profiles above, the rest fall here) **or** the package has an `index.html` — the Vite app entry point. Those get `environment: 'jsdom'`; a Vite library or CLI gets `node`.

**Why two runners.** For a Vite package, Vitest reads the repo's own `vite.config`, so the JSX/TSX transform, path aliases, CSS handling and `import.meta.env` all behave exactly as they do in the app build; reproducing that under Jest means a parallel transform stack that drifts on every config change. For an ESM package, Jest runs ESM only behind `--experimental-vm-modules` — the previous bootstrap sidestepped that by writing a CommonJS config and a `.cjs` smoke test, which passes verification and then cannot run the ESM test files generation produces.

**Versions.** Three couplings are resolved from the repo rather than pinned globally:

- `@testing-library/react` tracks the React major (the 16 line declares `react ^18 || ^19`; the 12 line declares `<18`).
- `vitest` tracks the Vite major (v4 declares `vite ^6 || ^7 || ^8` as a peer, so it cannot be installed beside Vite 5).
- `@vue/test-utils` is hard-split by the Vue major: the 2 line declares peer `vue 3.x`, the 1 line declares `2.x`. (`@testing-library/svelte` 5 declares `svelte ^3 || ^4 || ^5`, so one pin covers every current Svelte line.)
- `jest-preset-angular` tracks **both** the Angular major and the Jest major, and deliberately is **not** the newest release: `@angular-devkit/build-angular` declares `peerOptional jest ^29.5.0` through Angular 19, so the newer preset lines (which need Jest 30) make `npm install` fail with `ERESOLVE`. The Jest 29 preset line is used through Angular 20; only Angular 21+ moves to Jest 30.

**`scripts.test` is no longer overwritten.** Bootstrap writes its runner into **`scripts["test:asqs"]`** and only sets `scripts.test` when the package has none. The previous code assigned `scripts.test = "jest"` unconditionally, which destroyed `ng test` on Angular repos and any custom harness elsewhere.

**Monorepos.** Detection resolves the package the way bootstrap does — walking to a nested `package.json` when the repo root has none — instead of stat-ing the root and returning the OS error. The JS walk also no longer skips `packages/`, which is in the shared skip list for legacy NuGet output but is *the* conventional npm/pnpm/Yarn workspace layout.

### Java

Detection answers **"which coordinates does this framework require, and which are missing"** — not "does the word `junit` appear somewhere". The old substring rule let a Spring Boot module carrying only `junit-jupiter` skip bootstrap, after which every generated test referencing `@SpringBootTest`, Mockito or AssertJ failed to compile against a manifest the fix loop is not allowed to write.

| Framework | Detected from | Required test stack |
|---|---|---|
| **Spring Boot** | `spring-boot-starter-parent`, the `spring-boot-dependencies` BOM, a `spring-boot.version` property, or the `org.springframework.boot` Gradle plugin | `spring-boot-starter-test` (brings JUnit 5, Mockito, AssertJ, Hamcrest, `spring-test`) |
| **Quarkus** | `io.quarkus` coordinates or Gradle plugin | `quarkus-junit5`, `quarkus-junit5-mockito`, `assertj-core` |
| **Micronaut** | `micronaut-parent`, `io.micronaut` BOM, or the `io.micronaut.application` / `.library` Gradle plugin | `micronaut-test-junit5`, Mockito, `assertj-core` |
| **Android** | `com.android.application` / `com.android.library` Gradle plugin | **declined** — Android needs `testOptions { unitTests.all { useJUnitPlatform() } }`; adding dependencies without it yields a suite that silently runs zero tests |
| **plain** | nothing else matched | `junit-jupiter`, `mockito-core`, `mockito-junit-jupiter`, `assertj-core` (+ `junit-platform-launcher` on Gradle) |

Bootstrap **skips** only when every required coordinate is already present. A **partially** equipped module is bootstrapped.

**Versions.** Framework-coupled artifacts (`spring-boot-starter-test`, `quarkus-junit5`, `micronaut-test-junit5`) are emitted **without a `<version>`** whenever a parent POM or BOM manages them — pinning them is how a Boot 3.2 app ends up compiling against a mismatched Spring Test major. Standalone libraries (Mockito, AssertJ) are pinned in `internal/testbootstrap/versions.go`, and Mockito drops to the **4.x** line on Java 8 (Mockito 5 needs Java 11 and otherwise fails at class-load with `UnsupportedClassVersionError`).

### C#

Same shape as Java: the question is **which required packages are missing**, not "is any test package referenced". A test project with a bare xUnit reference used to satisfy detection, so bootstrap skipped and generated tests using Moq, FluentAssertions or `WebApplicationFactory` failed to compile against a `.csproj` the fix loop may not write.

| Detected | Detected from | Adds |
|---|---|---|
| **ASP.NET Core** | `Sdk="Microsoft.NET.Sdk.Web"` | `Microsoft.AspNetCore.Mvc.Testing` **at the project's .NET major** |
| **EF Core** (additive) | a `Microsoft.EntityFrameworkCore*` PackageReference | `Microsoft.EntityFrameworkCore.InMemory` at the EF major already referenced |
| **Blazor WASM** | `Sdk="Microsoft.NET.Sdk.BlazorWebAssembly"` | plain set only — `WebApplicationFactory` does not apply |
| **MAUI / iOS / Android / Windows desktop** | optional-workload TFMs or `Microsoft.Maui.Sdk` | **declined** — these need `dotnet workload install`, which stock SDK containers lack |
| **plain** | anything else | base runner + `Moq` + `FluentAssertions` |

The **test runner** is whatever the repo already established (`xunit` / `nunit` / `mstest`, read from existing test projects); xUnit is the default only when nothing is established, so bootstrap never drags a second runner into a solution.

**Versions.** `Microsoft.AspNetCore.Mvc.Testing` and `Microsoft.EntityFrameworkCore.InMemory` ship in lockstep with the runtime and are chosen from the TargetFramework — Mvc.Testing 10.x simply cannot be referenced from a `net8.0` project. Standalone libraries are pinned in `internal/testbootstrap/versions.go`.

> **FluentAssertions is pinned to the 7.x line on purpose.** 7.2.2 declares `Apache-2.0`; **8.0.0 switched to a commercial licence** requiring payment for commercial use. ASQS writes this `PackageReference` into customer repositories, so bumping the pin would impose a licensing obligation on every client the bootstrap touches. A unit test guards the pin.

**Central Package Management** is honoured throughout: when a `Directory.Packages.props` governs versions, `PackageVersion` entries are merged there and `PackageReference` carries no `Version`.

**Other languages** (e.g. Kotlin-only): **`test_bootstrap.skip_lang`** and continue.

## Files written

### JS/TS

- `package.json` — merged `devDependencies` from the profile, plus **`scripts["test:asqs"]`**. `scripts.test` is set only when absent.
- `jest.config.cjs` / `vitest.config.ts` — rendered from the profile, including **`testEnvironment` / `environment`** (`jsdom` for React and Angular, `node` otherwise). Only written when absent or when the existing file carries the ASQS header, so a hand-written config is never clobbered. Both set ignore patterns for `e2e/` and `cypress/` so Playwright/Cypress specs are not picked up.
- **The Vitest config MERGES the repo's Vite config** (`mergeConfig(viteConfig, …)`). Vitest prefers `vitest.config.*` over `vite.config.*` and does not merge on its own, so a standalone file would silently drop the React plugin and the path aliases — and the tests would fail on JSX or on an unresolved `@/…` import.
- `setup-jest.ts` (Angular, via `jest-preset-angular/setup-env/zone`) or `vitest.setup.ts` / `jest.setup.ts` (React, importing `@testing-library/jest-dom`).
- `tsconfig.spec.json` (Angular) — the preset resolves `<rootDir>/tsconfig.spec.json`, which the Angular CLI generates only for projects that already have a test target. `module`/`moduleResolution` are overridden together because Angular's own tsconfig pairs `ES2022` with `bundler`, and Jest needs CommonJS.
- `__tests__/asqs-bootstrap-smoke.test.*` — the mandatory smoke test.

Package manager is inferred from `pnpm-lock.yaml` / `yarn.lock` / default npm.

### JS/TS smoke tests

Verification used to be **`npx jest --showConfig`**, which proves only that a config file parses. A config parses perfectly while the transform cannot handle TSX, the environment has no `document`, or the framework preset is missing — all of which appear only when a test actually runs. Bootstrap now runs one:

1. **`__tests__/asqs-bootstrap-smoke.test.*` (mandatory).** Written with the extension that exercises the real transform (`.ts` on a TypeScript package, not `.cjs`), and executed with the profile's runner. On a `jsdom` profile it also creates an element and asserts on `document`, so a runner left on the default `node` environment — the single most common reason a generated component test fails — is caught by the gate rather than by the first generated test. Failure fails bootstrap.
2. **`__tests__/asqs-framework-smoke.test.*` (advisory).** React renders a component with Testing Library and asserts on the DOM; Angular compiles and creates a standalone component through `TestBed`; NestJS compiles a testing module and resolves a provider.

   **Vue and Svelte additionally get a companion component** — `__tests__/AsqsSmoke.vue` / `__tests__/AsqsSmoke.svelte` — which the test imports. A `.vue` or `.svelte` file compiles only through that framework's Vite plugin, so importing a real one is what proves the plugin actually reaches the test run through the merged `vite.config`. That is the entire reason Vitest is chosen for these stacks, and nothing short of a real component file tests it.

   Failure is logged as `test_bootstrap.framework_smoke_failed`, the test **and its companion** are **deleted**, and the run continues on the verified runner stack.

   > **Svelte needs `resolve.conditions: ['browser']`.** Under Vitest's default conditions Svelte's exports map resolves to its *server* build, and `render()` throws `mount(...) is not available on the server` even though the jsdom environment is correct. The condition is written only into the generated Vitest config, so the app's own `vite build` is unaffected.

### Java

- **`pom.xml`** — the profile's missing test-scoped dependencies, plus `maven-surefire-plugin` **only when no framework parent already configures it**.
- **`build.gradle`** / **`build.gradle.kts`** — appended `dependencies { … }` block with the profile's missing coordinates, plus `useJUnitPlatform()` wiring when absent. Without it Gradle drives tests with the JUnit 4 runner, matches no JUnit 5 class, and reports success having executed **zero** tests.
- **`src/test/java/com/asqs/bootstrap/AsqsBootstrapSmokeTest.java`** — the mandatory smoke test (see below).
- **`AsqsFrameworkSmokeTest.java`** — the framework smoke test, when the profile has one. For Spring Boot it is written into the **application's own base package**, not `com.asqs.bootstrap`.

Only the **repository root** build file is modified (single-module assumption).

### Java smoke tests

Bootstrap no longer reports success on the strength of `mvn test-compile` alone. On a module with no test files yet, that command compiles nothing and passes in about two seconds — which is exactly how a module with a missing test dependency was handed to generation.

Two tests are staged and **executed**:

1. **`com.asqs.bootstrap.AsqsBootstrapSmokeTest` (mandatory).** Mocks a `Supplier`, stubs it, and asserts with AssertJ — so JUnit 5, Mockito and AssertJ must all resolve, compile **and run**. Any failure fails bootstrap, with `test_bootstrap.verify_failed` naming the stack and the required coordinates. Failing here at minute one is the point: the alternative is a full generate-and-repair cycle that cannot converge.
2. **`AsqsFrameworkSmokeTest` (framework-representative).** `@SpringBootTest` / `@QuarkusTest` / `@MicronautTest` — loading the application context *is* the assertion. Failure policy differs by confidence:
   - **Spring Boot**, a **compile** failure is fatal: the dependency set is exact, so generated `@SpringBootTest` classes could not compile either.
   - **Quarkus / Micronaut**, a **compile** failure downgrades to unit-only (`test_bootstrap.framework_smoke_unsupported`). Both need annotation-processor wiring that a manifest patcher cannot infer safely, and a run that still produces unit tests beats no run.
   - **Any framework**, a test that compiles but does not **run** is an environment fact (no database, no free port), never a stack fact: always advisory, logged as `test_bootstrap.framework_smoke_failed`.

   A framework smoke this run created and could not get passing is **deleted**, so the evaluator never inherits a permanently broken file. A module with no application class under `src/main/java` (a library) skips step 2 entirely.

The Spring smoke lives in the application's base package because `@SpringBootTest` resolves configuration by walking **up** the package tree looking for `@SpringBootConfiguration`; parked anywhere else it fails with `Unable to find a @SpringBootConfiguration` regardless of the dependencies.

### C# smoke tests

Two tests are staged in the test project and **executed** — `dotnet build` alone proves only that MSBuild ran, the same vacuous check the Java path used to make:

1. **`Asqs.Bootstrap.AsqsBootstrapSmokeTest` (mandatory).** Mocks an interface with Moq, stubs it, asserts with FluentAssertions, and verifies the call — so the runner, Moq and FluentAssertions must all restore, compile **and run**. Failure fails bootstrap. When the failure is a **missing .NET shared runtime** (the project targets `net8.0`, the host has only .NET 10), the audit says exactly that with `reason: dotnet_runtime_missing` and a remediation, instead of dumping the host's URL-laden output. That is deliberately *not* auto-corrected with `DOTNET_ROLL_FORWARD`: evaluation would hit the same wall, and silently running a `net8.0` suite on a .NET 10 runtime changes what is being verified.
2. **`Asqs.Bootstrap.AsqsFrameworkSmokeTest` (ASP.NET Core, advisory).** Boots the real host through `WebApplicationFactory` — `CreateClient()` starting the application *is* the assertion.

   It is **always advisory**, unlike the Spring Boot equivalent. `@SpringBootTest` resolves configuration deterministically from the package tree; `WebApplicationFactory<T>` needs a **public type from the web assembly**, and top-level statements make the generated `Program` class *internal*. Bootstrap therefore infers an entry-point type — an explicitly `public Program` when one exists, otherwise the shallowest public non-static, non-generic class or record — rather than knowing one. A wrong inference must not cost a run whose unit stack is already verified. A framework smoke this run wrote and could not get passing is **deleted**.

To enable ASP.NET Core integration-style tests deterministically, add `public partial class Program { }` to the web project; bootstrap will then bind to it.

**Production projects are not given test packages.** When production projects exist and no unit test project does, bootstrap creates a dedicated project under `tests/` referencing them. Because SDK-style projects glob `**/*.cs` downwards, a production project at the **repository root** would otherwise compile every generated test itself without any test package on its classpath — so bootstrap adds `<Compile Remove="tests/**" />` to exactly those production projects whose directory contains the test root.

### C# (SDK-style)

- **First root `.csproj`** (alphabetically first basename) — appended `ItemGroup` with missing `PackageReference` entries only.

Non–SDK-style projects (no `Sdk="Microsoft.NET.Sdk"`) are **rejected** with a clear error.

## Smoke tests are instruments, not deliverables

Every smoke test bootstrap writes is **removed once it has answered its question**, on success as well as on failure. What it proved is recorded in the audit and in the contract's `framework_smoke.status`, not in a leftover file.

This is not tidiness. A smoke test is a foreign file in someone else's build, and plenty of projects validate style on every compile — spring-petclinic binds `spring-javaformat:validate` to the `validate` phase. A leftover file that the project's own rules reject would fail every subsequent compile in the run, in a file the fix loop has no mandate to repair.

### Formatters and linters never abort a run

Bootstrap verification asks one question: does the test classpath resolve, and can a test run. A project's formatter has no bearing on that — but most of them bind to `validate`, which runs **before** compile, so a style violation kills the build before the question is asked.

Two defences:

1. **Verification skips them.** Maven invocations carry `-Dspring-javaformat.skip=true`, `-Dcheckstyle.skip=true`, `-Dspotless.check.skip=true`, `-Dpmd.skip=true`, `-Dformatter.skip=true`, `-Dlicense.skip=true`, `-Denforcer.skip=true` and friends. Unknown `-D` properties are ignored by Maven, so listing plugins a project does not use costs nothing. Gradle has no equivalent convention (excluding a task that does not exist is an error there), so it relies on the second defence.
2. **A style failure is never fatal.** When the build fails and the output is a formatter/linter rejection *and carries no compiler diagnostic*, bootstrap logs `test_bootstrap.verify_style_violation`, removes the smoke test, marks the contract `verified: false` with a note, and **continues**. A compiler diagnostic in the same output always wins — that is the failure the gate exists to catch.

A real run on spring-petclinic hit exactly this before the fix: `spring-javaformat` rejected the smoke test's javadoc wrapping and the whole run aborted at minute one. `TestIntegration_bootstrapJavaWithFormatterValidation` reproduces it against a fixture with the plugin bound, and the Java smoke templates are now `spring-javaformat:apply` output so they are clean under the most common Spring configuration regardless.

## "Already set up" is verified, not assumed

Detection reads manifests. "Every required dependency is declared" and "a test actually runs here" are different claims, and only the first is visible in a `pom.xml`, a `.csproj` or a `package.json`. A repository can be fully declared and still unable to execute a test:

- a Gradle build with JUnit 5 and no `useJUnitPlatform()` runs **zero** tests and reports success;
- a Jest config can parse and still fail to transform the repository's own syntax;
- a .NET solution can restore cleanly with no matching shared runtime installed;
- a `jest.config.*` can be present but invalid — which is enough for detection to stop looking.

So when detection finds the stack complete, bootstrap **still proves it**: it writes one throwaway smoke test, runs it, removes it again, and records the outcome in the contract as `verified: true` / `false`.

Two rules separate this from the bootstrap path:

1. **Nothing is configured.** The repository already has a runner and a config, and those are the ones under test — writing ours would verify a stack generation is not going to use. For JS/TS this means driving the runner the repo *established*: a Vite repository already using Jest is verified with Jest, not with the Vitest bootstrap would have chosen. Repositories on Karma, Jasmine, Mocha or AVA are left alone entirely (`test_bootstrap.verify_existing_skipped`), because a Jest-shaped smoke test there would fail for reasons that say nothing about the repository.
2. **The smoke test is removed afterwards, pass or fail.** Bootstrap changed nothing else in this repository, so leaving an artifact behind would be gratuitous. (On the bootstrap path the smoke test stays: it is part of the stack that was just established.)

If the smoke test cannot run, the run **stops** — the same reasoning as everywhere else in this step: generated tests would fail identically, and the repair loop cannot fix a runner or a build manifest. The audit event `test_bootstrap.verify_existing_failed` carries the command and the output.

There is no switch for this. Bootstrap is already opt-in (`enabled: false` by default); an operator who has turned it on has asked for a working test stack, and "the stack is declared but cannot run a test" is not a state worth proceeding from. Repositories bootstrap cannot drive are skipped automatically rather than configured away — see the Karma/Jasmine/Mocha/AVA rule above.

For JS/TS the check installs dependencies first when `node_modules` is absent (frozen lockfile — `package.json` was not modified), because `npx` would otherwise fetch a runner from the registry and execute it without the repository's own dependencies, proving nothing.

## The bootstrap → generation contract (`.asqs/test-stack.json`)

When bootstrap runs it writes a small JSON file describing the stack it just established, and generation reads it as an **authoritative allowlist** of importable test libraries:

```json
{
  "version": 1,
  "language": "java",
  "framework": "spring-boot",
  "framework_version": "3.2.5",
  "runner": "junit5",
  "stack": "spring-boot-test",
  "available_packages": ["org.springframework.boot:spring-boot-starter-test"],
  "available_imports": [
    "org.assertj.core.*", "org.hamcrest.*", "org.junit.jupiter.*",
    "org.mockito.*", "org.springframework.boot.test.*", "org.springframework.test.*"
  ],
  "verified": true,
  "framework_smoke": { "kind": "spring-boot", "status": "passed" }
}
```

`available_imports` is the field that matters. Coordinates and import roots are different namespaces on the JVM — `spring-boot-starter-test` is one coordinate that brings six unrelated roots — so a raw manifest dump cannot answer "may I import Mockito?". The generator prompt now states the answer directly, together with *why it is not negotiable*: adding a library is a build-manifest change, and the repair loop may never edit build manifests, so a test importing an unavailable library cannot be fixed later, only discarded.

### Absence is normal, and never breaks anything

Every consumer must behave exactly as it did before the contract existed when the file is missing. That is the common case, not an error path:

| Situation | Contract | Behaviour |
|---|---|---|
| Bootstrap disabled (the default) | not written | prompt is byte-identical to before; raw manifests only |
| Bootstrap `mode: off` | not written | same |
| Repo already fully equipped | **written**; `verified: true` once the existing stack ran a smoke test, `false` when its runner could not be driven | prompt states the stack, and flags it when nothing was executed to confirm it |
| Bootstrap ran | written, `verified: true` | prompt states the stack and the framework-smoke outcome |
| File malformed, empty, or a future `version` | treated as absent | prompt falls back |

`teststack.Read` returns `(Contract, ok bool)` and **no error**, precisely so no caller can propagate one: every failure mode has the same correct response.

### Proving it was actually used

Writing the contract and reading it are separate events, and bootstrap and generation resolve the repository path independently — a mono-repo workspace mismatch would break the link silently and look exactly like "the model ignored the allowlist". So generation emits its own event, once per run at plan time:

```
test_bootstrap.contract_written                  Wrote .asqs/test-stack.json: spring-boot/junit5, 6 import root(s), verified=true.
generate.test_stack_contract                     Generation is constrained by .asqs/test-stack.json: spring-boot/junit5, 6 importable root(s), verified=true.
```

Absence is logged too (`generate.test_stack_contract_unavailable`), on purpose: bootstrap is off by default, so when an operator is looking at generated tests importing unavailable libraries, "was there an allowlist in the prompt at all?" is the first question and deserves a direct answer rather than an inference from rejection counts.

It is emitted at plan time rather than at the read site because `BuildProjectConfigSection` reads the contract once per *item* — auditing there would repeat an identical payload for every gap and every doc in the run.

The already-equipped row is the one worth calling out. A repository that arrives with its test tooling complete never reaches an apply step, yet detection has just computed the whole profile — so bootstrap records it anyway, marked unverified.

### It is deliberately not committed

Nothing extra is needed for this: the ship path already deletes the whole `.asqs/` directory before staging (`orchestrator.RemoveRepoAsqsDirForShip`), preserving only the cache paths an operator explicitly configured. The contract is therefore never part of a pull request.

That is a correctness property, not just diff hygiene. Bootstrap is off by default, so a **committed** contract would hand a later run a statement about library availability that nothing in that run verified, and which may no longer be true. Untracked, it only ever describes the run that wrote it.

Bootstrap briefly also wrote a `.gitignore` entry for the file. That was removed: it was redundant given the ship-time cleanup, and it silently modified a tracked file in the customer's repository without even reporting it in `files_changed`. `TestRun_doesNotTouchGitignore` guards against its return.

## Auditing

Audit steps (under **Test framework bootstrap** in `qualitybot audit report`):

| Step | When |
|------|------|
| `test_bootstrap.start` | Bootstrap phase entered (enabled, mode not `off`). |
| `test_bootstrap.skip` | `mode: off`. |
| `test_bootstrap.skip_wrong_mode` | e.g. `jest` on Java/C#, `junit` on JS/TS/C#, `xunit` on non–C#. |
| `test_bootstrap.skip_detected` | Framework already present. |
| `test_bootstrap.skip_lang` | Language has no apply path (unknown / unsupported lang). |
| `test_bootstrap.apply_start` | About to patch build / package files. |
| `test_bootstrap.verify_style_violation` | The build failed a formatter/linter rather than the compiler. Non-fatal: the smoke test is removed and the run continues with `verified: false`. |
| `test_bootstrap.verify_existing_start` | An already-complete stack is about to be proven with a throwaway smoke test. |
| `test_bootstrap.verify_existing_ok` | The repository's own stack compiled and ran a test. |
| `test_bootstrap.verify_existing_failed` | It did not. The run stops; payload carries the command and the output. |
| `test_bootstrap.verify_existing_skipped` | Nothing drivable: Karma/Jasmine/Mocha/AVA, or no test project to run a smoke test in. |
| `test_bootstrap.contract_written` | The bootstrap → generation contract was written; payload carries framework, runner, verified flag and the import allowlist. |
| `generate.test_stack_contract` | Generation READ the contract; payload repeats framework, runner, verified flag and the import allowlist. Emitted once per run at plan time. |
| `generate.test_stack_contract_unavailable` | Generation found no usable contract and falls back to the raw build files. Expected whenever bootstrap is disabled. |
| `test_bootstrap.contract_write_failed` | The contract could not be written. Non-fatal: generation falls back to raw manifests. |
| `test_bootstrap.js_profile` | JS/TS only: detected framework, chosen runner, test environment, TS/ESM flags, Vite major, detection evidence, and the full required dependency list. |
| `test_bootstrap.csharp_profile` | C# only: detected app framework, established test runner, TargetFramework, EF Core usage, detection evidence, and the full required package list. |
| `test_bootstrap.java_profile` | Java only: detected framework, version, whether versions are BOM-managed, the detection evidence, and the full required coordinate list. |
| `test_bootstrap.skip_framework_unsupported` | The framework is detected but deliberately not bootstrapped (Android; .NET optional-workload solutions). |
| `test_bootstrap.framework_smoke_skipped` | Nothing to boot: a Java library module, or a web project with no public type for `WebApplicationFactory<T>`. |
| `test_bootstrap.framework_smoke_unsupported` | The framework smoke will not compile (Quarkus/Micronaut, or an ASP.NET Core entry-point inference that did not hold); continuing with the unit stack. |
| `test_bootstrap.framework_smoke_failed` | The application context / host does not start in this environment; unit stack still verified. |
| `test_bootstrap.patched` | List of changed manifest/build files (Java / C#), with the coordinates added. |
| `test_bootstrap.apply_failed` | Patch failed (error level). |
| `test_bootstrap.install` | JS: package manager install; Java/C#: verify command about to run. |
| `test_bootstrap.install_failed` | JS install failed (error level). |
| `test_bootstrap.verify_failed` | Verify step failed (error level). |
| `test_bootstrap.apply_ok` | Success. |
| `test_bootstrap.run_failed` | Top-level failure returned to workflow (error level). |
| `test_bootstrap.detect_error` | Could not read/parse repo for detection. |

## E2E framework bootstrap (Playwright)

When **`retrieval.max_gaps_e2e` > 0** and **`runner.e2e_framework_bootstrap.enabled`** is `true`, asqs-core runs **E2E detection** for the workflow language. **JS/TS:** runs **after** unit test bootstrap (when present) and **before** the JS/TS indexer:

1. **Detect** Playwright/Cypress via config files, dependencies, and scripts (same signals as runtime `E2EFramework`).
2. If nothing is detected and **`mode`** is `auto` or `playwright`, **merge** `@playwright/test` into `package.json`, add **`scripts.test:e2e`** (`playwright test`), write **`playwright.config.ts`** if missing, write **`e2e/smoke.spec.ts`** if missing (so **`playwright test --list`** finds at least one test), run **npm/pnpm/yarn install**, then **`npx playwright test --list`** as verify.

**`mode: cypress`:** merges **`cypress`** into `package.json`, **`scripts.test:e2e`** (`cypress run`), writes **`cypress.config.ts`** and a minimal **`cypress/e2e/smoke.cy.ts`** if missing, then install + **`npx cypress verify`**.

**`mode: auto`:** installs **Playwright** (same as `playwright`) when no E2E stack is detected.

**Java / C# (same `max_gaps_e2e` gate):** **`DetectE2E`** scans **`pom.xml`** / **`build.gradle(.kts)`** (Java) or root **`*.csproj`** (C#). If nothing is detected and **`mode`** is `auto` or `playwright`, **Java** adds **Playwright Java** (test scope) + JUnit/Surefire/Maven exec or Gradle task **`asqsPlaywrightInstall`**, writes **`src/test/java/com/asqs/e2e/AsqsPlaywrightSmokeE2E.java`**, resolves deps, runs **`playwright` CLI `install chromium --with-deps`**, then the smoke test. **When `runner.type`/execution use ephemeral Docker**, Java E2E bootstrap uses **`mcr.microsoft.com/playwright/java`** (see **`runner.docker_image_playwright_java`**) instead of **`maven:*` / `gradle:*`**, so Chromium and OS libraries match the Playwright version; bootstrap containers also use **`docker run --ipc=host`** (Playwright’s recommendation). On the **host** (local execution), **`install chromium --with-deps`** still applies. **C#** adds **Microsoft.Playwright** + xUnit test SDK references if missing, writes **`E2E/AsqsPlaywrightSmokeE2E.cs`**, **`dotnet build`**, **`playwright.sh` install chromium** (in Docker, **`playwright.sh`** under **`bin/`** is used; on Windows hosts **`playwright.ps1`** may be used), then **`dotnet test`** filtered to that class. **C# E2E bootstrap in Docker** uses **`mcr.microsoft.com/playwright/dotnet`** (**`runner.docker_image_playwright_dotnet`**, default tag aligned with the **Microsoft.Playwright** NuGet pin in code), not the plain **`runner.docker_image_dotnet`** SDK image, so browser bundles match the package version; containers use **`--ipc=host`** like other Playwright images. **`mode: cypress`** is not applicable on JVM/.NET — bootstrap **falls back to Playwright** (audit **`e2e_bootstrap.mode_fallback`**). Unsupported languages still log **`e2e_bootstrap.skip_apply`**.

**Why `AsqsPlaywrightSmokeE2E.java` (and the C# counterpart)?** After adding Playwright dependencies, bootstrap must **prove** the toolchain works: JUnit can run, the Playwright driver loads, and **Chromium** is installed. A tiny test that launches headless Chromium and opens a **`data:`** HTML URL does that without your application or a server. It is only created when the file is missing; it is not meant to replace your real E2E tests—only to **verify install** and satisfy **`mvn test` / `gradle test`** during bootstrap. The Java smoke source is **`spring-javaformat:apply` output**, including **tab characters for indentation** (the Spring formatter rejects space-indented versions of the same file). If you still see an older copy in the repo, delete **`src/test/java/com/asqs/e2e/AsqsPlaywrightSmokeE2E.java`** once so bootstrap can recreate it, or run **`./mvnw spring-javaformat:apply -q`** on that path.

### Configuration

```yaml
retrieval:
  plan:
    e2e:
      max_gaps: 0          # > 0 enables E2E plan branch + optional bootstrap
      max_gaps_per_file: 0 # 0 = default cap (2)
  profile_e2e: ""          # empty → e2e_playwright for E2E plan retrieval

bootstrap:
  policy:
    e2e_framework:
      enabled: false
      mode: auto             # auto (= playwright apply) | playwright | cypress | off
      pin_versions: true
      allow_lockfile_change: true
```

### Audit steps (`e2e_bootstrap.*`)

Grouped under **Test framework bootstrap** in `qualitybot audit report` (same section as `test_bootstrap.*`): `start`, `skip`, `skip_lang`, `skip_apply` (unsupported language), `mode_fallback` (cypress → Playwright on Java/C#), `skip_detected`, `apply_start`, `patched`, **`docker`** (ephemeral container install/verify; E2E JS may set **`js_stack`** = **`mcr.microsoft.com/playwright`**; E2E Java may set **`java_stack`** = **`mcr.microsoft.com/playwright/java`**; E2E C# may set **`dotnet_stack`** = **`mcr.microsoft.com/playwright/dotnet`**), `install`, `install_failed`, `verify_failed`, `apply_ok`, `apply_failed`, `detect_error`, `run_failed`.

## Requirements

- **`runner.test_framework_bootstrap.execution`** / **`runner.e2e_framework_bootstrap.execution`:** **`auto`** (default) — when **`runner.type: docker`**, **TS/JS**, **Java**, and **C#** install/verify run in **`docker run --rm`** (**`internal/runner/jobrunner`**). **C# unit** bootstrap uses the **csharp-dotnet** eval image (**`runner.docker_image_dotnet`**). **`local`** — always host **`exec`**. **`docker`** — force Docker (fails if toolchain cannot be resolved).
- **`runner.test_framework_bootstrap.require_docker`:** when **true**, a TS/JS, **Java**, or **C#** bootstrap that would **install on the host** (no ephemeral container) **fails fast** after detection shows work is needed. Use with **`runner.type: docker`** and/or **`execution: docker`**.
- **JS/TS images:** **Unit** bootstrap (`runner.test_framework_bootstrap`) uses the same **Node**-based image as eval (**`runner.docker_image_node`** / **`runner.build_tool`** for npm vs pnpm vs yarn lockfiles). **E2E** bootstrap (`runner.e2e_framework_bootstrap`) uses **`mcr.microsoft.com/playwright`** (**`runner.docker_image_playwright`**, default tag pinned with **`@playwright/test`** in code) so browsers match the Playwright stack. **Docker evaluation:** the **unit** test pass still uses the **Node** profile; the **second (E2E) pass** for **Playwright/Cypress** uses **`runner.docker_image_playwright`** so browsers are available in the eval container.
- **Java E2E bootstrap in Docker** uses **`mcr.microsoft.com/playwright/java`** (**`runner.docker_image_playwright_java`**, default tag aligned with the Playwright Java pin in code), not **`image_java_maven` / `image_java_gradle`**, so the JDK/Maven image already includes matching Chromium and OS libraries. **Unit** bootstrap and **docker eval** for Java still use the Maven/Gradle profile images.
- **C# E2E bootstrap in Docker** uses **`mcr.microsoft.com/playwright/dotnet`** (**`runner.docker_image_playwright_dotnet`**, default tag aligned with **Microsoft.Playwright** in code). The default image includes **.NET SDK 8** only; repos targeting **net9.0** / **net10.0** should set **`runner.docker_image_playwright_dotnet`** to a **custom image** (see **[PLAYWRIGHT-DOTNET-DOCKER.md](./PLAYWRIGHT-DOTNET-DOCKER.md)** and **`docker/runner/playwright-dotnet-sdk10/Dockerfile`**). **C# unit** bootstrap in Docker uses **`runner.docker_image_dotnet`** (same as **csharp-dotnet** eval).
- **npm, pnpm, yarn in Docker:** for **pnpm** and **yarn**, install runs **`npm install -g corepack@latest`** then **`corepack enable`** before **`pnpm install` / `yarn install`** so the shims work and **Corepack’s signing keys match the registry** (avoids `Cannot find matching keyid` on older Node-bundled Corepack). **npm** installs only run **`corepack enable`** (best-effort). Lockfile detection unchanged (**`package-lock.json`**, **`pnpm-lock.yaml`**, **`yarn.lock`**).
- When **`execution`** implies **local** (default **`runner.type: local`**): **JS/TS** need **Node** + **npm** (or yarn/pnpm); **Java** needs **Maven** or **Gradle** (or wrappers); **C#** needs **.NET SDK**.

Network may be needed for first-time dependency resolution (`npm`, NuGet).

Further refinement: see **[PLAN.md](./PLAN.md)** §3 (ephemeral Docker bootstrap polish, yarn berry, private feeds).

## Integration tests

```bash
go test -tags=integration ./internal/testbootstrap/... -count=1
```

Requires network where applicable. **JS** test needs Node; **C#** host test needs `dotnet` on PATH (skipped otherwise). **Integration:** `TestIntegration_bootstrapCSharpDocker` / `TestIntegration_requireDockerBootstrap_rejectsHostCSharp` need Docker when enabled.

## Limitations (v1)

- **Monorepos / workspaces:** only the **repository root** is modified.
- **ESM-only** JS repos may need manual Jest tweaks after bootstrap.
- **Java:** multi-module layouts and unusual POM structures may need manual fixes.
- **C#:** **SDK-style** `.csproj` only; monorepo discovery picks a primary project (see **`internal/testbootstrap`**). **NuGet Central Package Management** (**`Directory.Packages.props`** on the path from the project to the repo root): bootstrap adds **`PackageVersion`** entries there and **versionless** **`PackageReference`** lines (avoids **NU1008**). For **Playwright .NET** in Docker with **net9+**, use a custom **`image_playwright_dotnet`** if bootstrap still reports **NETSDK1045** (see **[PLAYWRIGHT-DOTNET-DOCKER.md](./PLAYWRIGHT-DOTNET-DOCKER.md)**).

The remaining bootstrap backlog lived in an upstream planning document that was not carried into
the open core; `docs/IMPLEMENTATION-PLAN-PARITY-PORT.md` (P12) is the record here.
