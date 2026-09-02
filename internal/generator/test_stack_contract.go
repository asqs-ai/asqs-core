package generator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
	"github.com/asqs/asqs-core/internal/teststack"
)

// testStackLLMBlock renders the bootstrap contract as a hard allowlist for the generator prompt.
//
// Returns "" whenever the contract is absent, unreadable or from an unknown schema version — which
// is the common case, not an error path: bootstrap is off by default, and a repository that already
// has its test tooling was skipped. The prompt then contains exactly what it contained before this
// existed, so nothing regresses.
//
// When the contract IS present it is strictly better than leaving the model to infer availability
// from raw build files. The run this whole mechanism came from had twenty generated candidates
// rejected for importing org.mockito and org.assertj into a module carrying neither, and nine
// written anyway — because the prompt shipped a raw pom.xml and left the inference to the model.
func testStackLLMBlock(repoPath string) string {
	c, ok := teststack.Read(repoPath)
	if !ok {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Test stack in this repository (verified by bootstrap)\n\n")

	framework := strings.TrimSpace(c.Framework)
	if framework == "" {
		framework = "not detected"
	} else if v := strings.TrimSpace(c.FrameworkVersion); v != "" {
		framework += " " + v
	}
	fmt.Fprintf(&b, "- **Application framework:** %s\n", framework)
	if r := strings.TrimSpace(c.Runner); r != "" {
		fmt.Fprintf(&b, "- **Test runner:** %s\n", r)
	}
	if e := strings.TrimSpace(c.TestEnvironment); e != "" {
		fmt.Fprintf(&b, "- **Test environment:** %s", e)
		if e == "node" {
			b.WriteString(" — there is no DOM here; do not render components or touch `document`")
		}
		b.WriteString("\n")
		// A DOM environment still EXECUTES on Node, so `global` exists at run time — but nothing in
		// a browser stack DECLARES it, because @types/node is deliberately not installed there: with
		// no compilerOptions.types it would be ambient over the whole app and license `process.env`
		// inside browser code that has no process at run time.
		//
		// So `global.fetch = vi.fn()` runs and then fails the compile step with
		// `TS2304: Cannot find name 'global'` — 3 of the 112 errors on 2026-09-01. `globalThis` is
		// the standard spelling, is declared by the ES2020+ lib every one of these projects already
		// targets, and needs no dependency at all. Saying so once here is cheaper than repairing it
		// per test, and unlike a hand-written ambient declaration it fixes the cause.
		if isDOMTestEnvironment(e) {
			b.WriteString("- **Use `globalThis`, never `global`.** This environment runs on Node, so `global` exists at run " +
				"time, but nothing here declares it to TypeScript and `global.fetch = ...` fails the compile step with " +
				"`Cannot find name 'global'`. `globalThis` is declared by the standard library and always type-checks.\n")
		}
	}

	if len(c.AvailableImports) > 0 {
		fmt.Fprintf(&b, "- **Test libraries available to import:** %s\n", strings.Join(c.AvailableImports, ", "))
		// The roots state which LIBRARIES are on the classpath. They are derived from build
		// coordinates, which cannot see a version, so they must not be presented as proof that any
		// particular sub-package under them exists — that reading is what licensed
		// org.springframework.boot.test.autoconfigure.web.servlet on a Spring Boot 4 project whose
		// classpath has no such package.
		b.WriteString("- **Import from no other library.** A root absent from that list is not on the test classpath at all. " +
			"Adding one is a build-manifest change, and the repair loop is never allowed to edit build manifests — " +
			"a test that imports an unavailable library cannot be fixed later, only discarded.\n")
		b.WriteString("- **These roots name libraries, not packages.** They were read from the build coordinates, which do not " +
			"carry a version, so a sub-package under a listed root may still not exist in the version this project uses. " +
			"For the types listed below, use the exact import given; otherwise import only what you can see declared in the " +
			"sources and API surfaces in this prompt.\n")
	}

	if len(c.CanonicalImports) > 0 {
		b.WriteString("- **Exact imports for framework test types, read from THIS project's compile classpath:**\n")
		for _, simple := range sortedImportKeys(c.CanonicalImports) {
			fmt.Fprintf(&b, "  - `import %s;` — the only %s on this classpath\n", c.CanonicalImports[simple], simple)
		}
		b.WriteString("  These were resolved against the jars this project actually compiles against, so they are correct for " +
			"its framework version. Where one of them contradicts an import you would otherwise write — including anything " +
			"suggested by the package roots above or by your own recollection of this framework — **this list wins**.\n")
	}

	switch c.Smoke.Status {
	case teststack.SmokePassed:
		fmt.Fprintf(&b, "- **%s integration tests work here:** a smoke test using them compiled and ran.\n", c.Smoke.Kind)
	case teststack.SmokeFailed, teststack.SmokeSkipped:
		fmt.Fprintf(&b, "- **Avoid %s integration-style tests:** bootstrap could not get one running in this environment. "+
			"Prefer unit tests over the public API.\n", nonEmptyContractField(c.Smoke.Kind, c.Framework))
	}

	if !c.Verified {
		b.WriteString("- The stack above was read from the build files; bootstrap did not compile or run anything to confirm it.\n")
	} else if len(c.VerifiedImports) > 0 {
		// What "verified" covers, stated rather than implied. The Spring smoke test imports one
		// class under org.springframework.boot.test.*, so a bare "verified" invited the reading
		// that every package under every listed root had been exercised.
		fmt.Fprintf(&b, "- **What bootstrap actually compiled and ran:** %s. That proves the libraries resolve; it does not "+
			"establish that any other package under the roots above exists.\n", strings.Join(c.VerifiedImports, ", "))
	}
	for _, n := range c.Notes {
		if s := strings.TrimSpace(n); s != "" {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}

	b.WriteString("\nThis section is authoritative for which test LIBRARIES are available and for the exact imports it spells " +
		"out; where it disagrees with the raw build files elsewhere in this prompt, follow this section. It is not a " +
		"statement that every package under a listed root exists.")
	return strings.TrimSpace(b.String())
}

// testStackSystemBlock is the system-message form of the contract.
//
// It belongs in the system message for the same reason the framework API surface does: the contract
// is fixed for the whole run, so repeating it per gap would vary nothing and cost tokens on every
// call. The leading blank lines match how pregenerateAPISurface appends, and it must be added
// BEFORE the structured-output suffix so the output-format instruction stays last.
func (g *LLMGenerator) testStackSystemBlock() string {
	s := testStackLLMBlock(g.RepoPath)
	if s == "" {
		return ""
	}
	return "\n\n" + s
}

func nonEmptyContractField(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return "framework"
}

// ResolveTestStackCanonicalImports fills in the classpath-derived half of the bootstrap contract,
// once per run, before the generation fan-out reads it.
//
// Bootstrap cannot do this itself: it runs its toolchain in an ephemeral container, while the
// API-surface provider resolves a classpath on the host. The two halves of the contract therefore
// have different authors — bootstrap states what it installed, this states what the classpath
// actually provides — and only the second one can tell Spring Boot 3 apart from Spring Boot 4.
//
// Best-effort in every direction. No provider, no contract, an unresolvable classpath or a write
// failure all leave the contract exactly as bootstrap wrote it, which is the behaviour that existed
// before this function.
func ResolveTestStackCanonicalImports(ctx context.Context, audit Auditor, provider apisurface.Provider, repoPath, lang string) {
	if provider == nil || strings.TrimSpace(repoPath) == "" {
		return
	}
	c, ok := teststack.Read(repoPath)
	if !ok {
		return
	}
	imports := apisurface.ResolveCanonicalImports(ctx, provider, repoPath, lang)
	if len(imports) == 0 {
		return
	}
	c.CanonicalImports = imports
	if err := teststack.Write(repoPath, c); err != nil {
		if audit != nil {
			audit.Log(ctx, "generate.test_stack_canonical_imports_unavailable", map[string]interface{}{
				"message": fmt.Sprintf("Resolved %d canonical framework import(s) but could not persist them to %s: %v. Generation falls back to the coordinate-derived import roots.", len(imports), teststack.RelPath, err),
				"path":    teststack.RelPath,
				"error":   err.Error(),
			})
		}
		return
	}
	if audit == nil {
		return
	}
	rendered := make([]string, 0, len(imports))
	for _, simple := range sortedImportKeys(imports) {
		rendered = append(rendered, simple+" -> "+imports[simple])
	}
	audit.Log(ctx, "generate.test_stack_canonical_imports", map[string]interface{}{
		"message": fmt.Sprintf("Resolved %d framework test type(s) against this project's compile classpath; generation is told these exact imports, which override the version-blind package roots.", len(imports)),
		"path":    teststack.RelPath,
		"lang":    lang,
		"imports": rendered,
	})
}

// isDOMTestEnvironment reports whether a JS/TS test environment provides a DOM. Anything that is not
// Node's bare environment is one: jsdom is what this bootstrap installs, and happy-dom is the common
// substitute in repositories that configured their own.
func isDOMTestEnvironment(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "", "node":
		return false
	default:
		return true
	}
}

func sortedImportKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AuditTestStackContract records, once per run, whether generation has a bootstrap contract to
// constrain it.
//
// Without this the audit trail proves only that bootstrap WROTE the contract; whether generation
// actually read it could be inferred only indirectly, from the disappearance of "library not on the
// classpath" rejections. The two are separable — bootstrap and generation resolve the repository path
// independently, and a mono-repo workspace mismatch would break the link silently.
//
// Absence is logged too, and deliberately. Bootstrap is off by default, so most runs have no
// contract; when an operator is looking at generated tests that import unavailable libraries, "was
// there an allowlist in the prompt at all?" is the first question, and it deserves a direct answer
// rather than an inference.
func AuditTestStackContract(ctx context.Context, audit Auditor, repoPath string) {
	if audit == nil || strings.TrimSpace(repoPath) == "" {
		return
	}
	c, ok := teststack.Read(repoPath)
	if !ok {
		audit.Log(ctx, "generate.test_stack_contract_unavailable", map[string]interface{}{
			"message": fmt.Sprintf("No %s: generation states test-library availability from the raw build files "+
				"only. Either the test-framework bootstrap is off (the default), or it ran and did not "+
				"finish — check the test_bootstrap.* events in this run before assuming the former.", teststack.RelPath),
			"path":   teststack.RelPath,
			"reason": "absent_or_unreadable",
		})
		return
	}
	audit.Log(ctx, "generate.test_stack_contract", map[string]interface{}{
		"message": fmt.Sprintf("Generation is constrained by %s: %s/%s, %d importable root(s), verified=%v.",
			teststack.RelPath, c.Framework, c.Runner, len(c.AvailableImports), c.Verified),
		"path":              teststack.RelPath,
		"framework":         c.Framework,
		"framework_version": c.FrameworkVersion,
		"runner":            c.Runner,
		"stack":             c.Stack,
		"test_environment":  c.TestEnvironment,
		"verified":          c.Verified,
		"smoke_status":      string(c.Smoke.Status),
		"available_imports": c.AvailableImports,
		"canonical_imports": c.CanonicalImports,
		"verified_imports":  c.VerifiedImports,
	})
}
