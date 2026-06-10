package evaluator

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// GapTarget describes the scope of a per-gap evaluation step. It carries the gap's identity,
// the artifact paths the gap owns, and the optional source-file target. Per-gap test/lint/fixer
// helpers use these fields to scope the operation to one gap's artifacts only — they do not run
// the project-wide evaluation that legacy `RunEvaluation` performs.
//
// Per-gap evaluation contract (per docs/SESSIONS.md per-gap pipeline):
//   - Compile: project-wide (shared), see RunSharedCompile.
//   - Test: per gap (only the gap's tests).
//   - Lint: per gap (only the gap's artifacts).
//   - Coverage: project-wide (run inside RunRunFinalEval).
//   - Fixer: per gap (only this gap's failures and artifacts).
//   - Final eval + project fixer: run-scope, called once per per-gap-loop join.
type GapTarget struct {
	// GapID is the gap_session_id used by audit/feedback rows. Required.
	GapID string
	// SymbolFQName is the fully-qualified symbol the gap targets. Used to build
	// framework-native test name filters when artifact paths are insufficient.
	SymbolFQName string
	// Layer is the gap's layer ("unit"|"e2e"|"doc"). Selects between UnitTestCommand and
	// E2ETestCommand on EvalOptions and drives framework selection.
	Layer string
	// ArtifactPaths is the set of repo-relative paths the gap wrote. Per-gap test/lint use
	// this to scope filters; per-gap fixer uses it to scope the LLM prompt.
	ArtifactPaths []string
	// SourceFile is the source-file the gap targets. Lint may use this to narrow scope.
	SourceFile string
}

// FinalEvalResult is the outcome of one run-scope final-eval iteration: a compile + test +
// (optional E2E) + lint + coverage block. Stable is true iff no step failed (coverage is
// always non-fatal; its result lands in StepResults but does not flip Stable).
//
// The SessionRunner uses Stable to decide whether to invoke RunRunFixerOnce and re-enter the
// final-loop iteration. LastFixAction hints which fixer prompt shape the caller should use
// when invoking RunRunFixerOnce.
type FinalEvalResult struct {
	Stable        bool
	StepResults   []StepResult
	LastFixAction FixAction
	// FailingStep names the step whose failure caused Stable=false. Empty for Stable=true.
	FailingStep SandboxStep
	// FailingOutput is the (truncated) error output of the failing step, ready to feed into
	// RunRunFixerOnce. Empty for Stable=true.
	FailingOutput string
}

// RunSharedCompile invokes the compile step at run scope. It is a thin alias over RunCompile
// kept as a separate entrypoint so callers expressing the per-gap pipeline can spell their
// intent — "shared compile, not per-gap" — without going through the deprecated RunEvaluation
// budgeted loop. Caching by dirty-set digest is the caller's responsibility (SessionRunner
// tracks dirty-state across gaps).
func RunSharedCompile(ctx context.Context, runner SandboxRunner, opts EvalOptions) StepResult {
	return RunCompile(ctx, runner, opts)
}

// RunGapTest runs the test step scoped to one gap's artifacts. It builds a per-framework
// filter from `opts.Lang` / `opts.TestFramework` / `gap.ArtifactPaths` / `gap.SymbolFQName`.
// When no filter can be derived for the language, falls back to RunTest with the project-wide
// command (graceful degradation — per-gap test then runs the project suite).
func RunGapTest(ctx context.Context, runner SandboxRunner, opts EvalOptions, gap GapTarget) StepResult {
	cmd := BuildGapTestCommand(opts, gap)
	return RunTest(ctx, runner, opts, cmd)
}

// RunGapTestE2E runs the E2E test step scoped to one gap's artifacts. Mirrors RunGapTest but
// uses the E2E command path. When the language/framework cannot be scoped, falls back to
// project-wide E2E.
func RunGapTestE2E(ctx context.Context, runner SandboxRunner, opts EvalOptions, gap GapTarget) StepResult {
	cmd := BuildGapE2ECommand(opts, gap)
	return RunTestE2E(ctx, runner, opts, cmd)
}

// RunGapLint runs the lint/format-check step. Today the underlying SandboxRunner.Lint is
// always project-wide; this entrypoint exists so the runner can express "lint for this gap"
// in audit rows and so future per-gap lint implementations have a stable seam. Returns the
// project-wide lint result. Callers that want a fast lint should set opts.LintCommand (when
// the underlying runner supports it) before invoking.
func RunGapLint(ctx context.Context, runner SandboxRunner, opts EvalOptions, gap GapTarget) StepResult {
	_ = gap
	return RunLint(ctx, runner, opts)
}

// RunGapFixOnce runs exactly one LLM fixer round scoped to the gap's artifacts. The fixer's
// FixRequest only includes files in gap.ArtifactPaths plus their declared dependencies.
// `step` and `errorOutput` describe the failing per-gap step that triggered the fix. The
// SessionRunner owns the per-gap fix loop; this helper performs one round and returns.
func RunGapFixOnce(ctx context.Context, opts EvalOptions, gap GapTarget, step SandboxStep, errorOutput string, audit Auditor, attempt, maxAttempts int, loopState *FixLoopState) FixStepResult {
	scoped := opts
	if len(gap.ArtifactPaths) > 0 {
		scoped.ArtifactPaths = filterArtifactPaths(opts.ArtifactPaths, gap.ArtifactPaths)
		if len(scoped.ArtifactPaths) == 0 {
			// No overlap between the run's artifact set and the gap's paths means we have
			// no signal for the fixer; surface this as a no-op fix step so the runner
			// records attribution without burning a real round.
			return FixStepResult{
				Attempt:     attempt,
				MaxAttempts: maxAttempts,
				Applied:     false,
			}
		}
	}
	// Always make the symbol-under-test's source available to the fixer and never let it be
	// signature-sliced: attach gap.SourceFile as a declared dependency of each scoped artifact.
	// It lands in the fixer's read set and both slicing keep-sets (full body kept) but is never
	// added to ArtifactPaths, so the fixer still cannot rewrite production source. The dependency
	// map is cloned before mutation so the shared map the caller passed in is left untouched.
	if src := strings.TrimSpace(gap.SourceFile); src != "" && len(scoped.ArtifactPaths) > 0 {
		scoped.ArtifactDependencies = cloneDepMapWith(scoped.ArtifactDependencies, scoped.ArtifactPaths, src)
	}
	scoped.GapSessionID = strings.TrimSpace(gap.GapID)
	return RunFix(ctx, scoped, step, errorOutput, audit, attempt, maxAttempts, loopState)
}

// cloneDepMapWith returns a copy of m with dep appended (deduplicated, path-normalized) under each
// key in keys. The input map is never mutated, so callers may pass a map shared with EvalOptions.
func cloneDepMapWith(m map[string][]string, keys []string, dep string) map[string][]string {
	out := make(map[string][]string, len(m)+len(keys))
	for k, v := range m {
		out[k] = append([]string(nil), v...)
	}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !containsPathFix(out[k], dep) {
			out[k] = append(out[k], dep)
		}
	}
	return out
}

// containsPathFix reports whether want is already present in list after path normalization.
func containsPathFix(list []string, want string) bool {
	w := normalizePathForFix(want)
	for _, p := range list {
		if normalizePathForFix(p) == w {
			return true
		}
	}
	return false
}

// RunRunFinalEval runs one run-scope final-eval iteration (compile + test + optional E2E +
// lint + coverage). This is the project-wide block the SessionRunner invokes after all
// per-gap loops finish. Returns a FinalEvalResult the runner uses to decide whether to call
// RunRunFixerOnce.
//
// Coverage is invoked unconditionally as a project-wide step and recorded in StepResults but
// does not flip Stable; the caller's CoverageGate policy decides whether to act on a
// non-passing coverage result.
func RunRunFinalEval(ctx context.Context, runner SandboxRunner, opts EvalOptions, audit Auditor) FinalEvalResult {
	if runner == nil {
		return FinalEvalResult{Stable: false, FailingStep: StepCompile, FailingOutput: "evaluator: SandboxRunner required"}
	}
	out := FinalEvalResult{StepResults: make([]StepResult, 0, 5)}

	// Compile
	cr := RunCompile(ctx, runner, opts)
	out.StepResults = append(out.StepResults, cr)
	auditFinalStep(ctx, audit, cr)
	if !cr.OK {
		out.Stable = false
		out.LastFixAction = FixImportsMocks
		out.FailingStep = StepCompile
		out.FailingOutput = cr.Output
		return out
	}

	// Test (unit pass)
	testCmd := opts.UnitTestCommand
	if strings.TrimSpace(testCmd) == "" {
		testCmd = opts.TestCommand
	}
	tr := RunTest(ctx, runner, opts, testCmd)
	out.StepResults = append(out.StepResults, tr)
	auditFinalStep(ctx, audit, tr)
	if !tr.OK {
		out.Stable = false
		out.LastFixAction = FixAssumptions
		out.FailingStep = StepTest
		out.FailingOutput = tr.Output
		return out
	}

	// Optional E2E test pass
	if opts.RunE2ETestPass {
		e2eCmd := opts.E2ETestCommand
		if strings.TrimSpace(e2eCmd) == "" {
			e2eCmd = opts.TestCommand
		}
		er := RunTestE2E(ctx, runner, opts, e2eCmd)
		out.StepResults = append(out.StepResults, er)
		auditFinalStep(ctx, audit, er)
		if !er.OK {
			out.Stable = false
			out.LastFixAction = FixStabilize
			out.FailingStep = StepTestE2E
			out.FailingOutput = er.Output
			return out
		}
	}

	// Lint
	lr := RunLint(ctx, runner, opts)
	out.StepResults = append(out.StepResults, lr)
	auditFinalStep(ctx, audit, lr)
	if !lr.OK {
		out.Stable = false
		out.LastFixAction = FixAssumptions
		out.FailingStep = StepLint
		out.FailingOutput = lr.Output
		return out
	}

	// Coverage (non-fatal; recorded in StepResults but does not flip Stable).
	cv := RunCoverage(ctx, runner, opts, testCmd)
	out.StepResults = append(out.StepResults, cv)
	auditFinalStep(ctx, audit, cv)

	out.Stable = true
	return out
}

// RunRunFixerOnce runs exactly one LLM fixer round with the project-wide failure context.
// The SessionRunner caps the run-scope fixer loop at evaluator.MaxFixIterations rounds (the
// same budget the legacy `RunEvaluation` consumed). `step` is the failing step from the
// preceding RunRunFinalEval; `errorOutput` is that step's output.
//
// Equivalent to one iteration of the legacy `evaluator.RunEvaluation` budgeted loop without
// the surrounding iteration accounting.
func RunRunFixerOnce(ctx context.Context, opts EvalOptions, step SandboxStep, errorOutput string, audit Auditor, attempt, maxAttempts int) FixStepResult {
	return RunFix(ctx, opts, step, errorOutput, audit, attempt, maxAttempts, nil)
}

// BuildGapTestCommand derives a test command scoped to one gap's artifacts. Returns the
// resulting command (suitable for TestWithCommand-capable runners) or the project-wide
// `opts.TestCommand` when no per-framework filter can be derived.
//
// Per-language strategies (mirrors the test runner conventions):
//   - Go: append `-run '^TestName$'` derived from gap.SymbolFQName and the test files.
//   - Java (Maven): append `-Dtest=ClassName` derived from gap.SymbolFQName.
//   - Java (Gradle): append `--tests "ClassName"`.
//   - JS/TS (Vitest/Jest): append the test file path(s).
//   - .NET (dotnet test): append `--filter "FullyQualifiedName~Symbol"`.
//   - Playwright/Cypress: append the spec path.
//
// Languages/frameworks not in this set fall back to the project-wide command. This is a
// conservative default: the per-gap loop still runs but exercises the full suite, which is
// correct (no false negatives) at the cost of speed.
func BuildGapTestCommand(opts EvalOptions, gap GapTarget) string {
	base := opts.UnitTestCommand
	if strings.TrimSpace(base) == "" {
		base = opts.TestCommand
	}
	if gap.Layer == "e2e" {
		return BuildGapE2ECommand(opts, gap)
	}
	if strings.TrimSpace(base) == "" {
		return base
	}
	if filter := gapTestFilterFor(opts, gap); filter != "" {
		return base + " " + filter
	}
	return base
}

// BuildGapE2ECommand mirrors BuildGapTestCommand for the E2E pass.
func BuildGapE2ECommand(opts EvalOptions, gap GapTarget) string {
	base := opts.E2ETestCommand
	if strings.TrimSpace(base) == "" {
		base = opts.TestCommand
	}
	if strings.TrimSpace(base) == "" {
		return base
	}
	if filter := gapE2EFilterFor(opts, gap); filter != "" {
		return base + " " + filter
	}
	return base
}

func gapTestFilterFor(opts EvalOptions, gap GapTarget) string {
	lang := strings.ToLower(strings.TrimSpace(opts.Lang))
	framework := strings.ToLower(strings.TrimSpace(opts.TestFramework))
	switch {
	case lang == "go" || strings.HasPrefix(lang, "go"):
		if testNames := goTestNames(gap); len(testNames) > 0 {
			return "-run '^(" + strings.Join(testNames, "|") + ")$'"
		}
		// Limit to the artifact's package paths if we cannot extract names.
		if pkgs := goTestPackages(gap); len(pkgs) > 0 {
			return strings.Join(pkgs, " ")
		}
	case lang == "java":
		if tool := strings.ToLower(strings.TrimSpace(opts.BuildTool)); tool == "gradle" {
			if names := javaTestClasses(gap); len(names) > 0 {
				return "--tests " + quote(strings.Join(names, ","))
			}
		} else {
			if names := javaTestClasses(gap); len(names) > 0 {
				return "-Dtest=" + strings.Join(names, ",")
			}
		}
	case lang == "csharp" || lang == "dotnet" || lang == "c#":
		if names := csharpTestNames(gap); len(names) > 0 {
			parts := make([]string, 0, len(names))
			for _, n := range names {
				parts = append(parts, "FullyQualifiedName~"+n)
			}
			return "--filter " + quote(strings.Join(parts, "|"))
		}
	case lang == "javascript" || lang == "typescript" || lang == "js" || lang == "ts":
		_ = framework
		if specs := tsJsTestSpecs(gap); len(specs) > 0 {
			return strings.Join(specs, " ")
		}
	}
	return ""
}

func gapE2EFilterFor(opts EvalOptions, gap GapTarget) string {
	lang := strings.ToLower(strings.TrimSpace(opts.Lang))
	switch lang {
	case "javascript", "typescript", "js", "ts":
		if specs := tsJsTestSpecs(gap); len(specs) > 0 {
			return strings.Join(specs, " ")
		}
	case "java":
		if tool := strings.ToLower(strings.TrimSpace(opts.BuildTool)); tool == "gradle" {
			if names := javaTestClasses(gap); len(names) > 0 {
				return "--tests " + quote(strings.Join(names, ","))
			}
		} else {
			if names := javaTestClasses(gap); len(names) > 0 {
				return "-Dtest=" + strings.Join(names, ",")
			}
		}
	case "csharp", "dotnet", "c#":
		if names := csharpTestNames(gap); len(names) > 0 {
			parts := make([]string, 0, len(names))
			for _, n := range names {
				parts = append(parts, "FullyQualifiedName~"+n)
			}
			return "--filter " + quote(strings.Join(parts, "|"))
		}
	}
	return ""
}

func goTestNames(gap GapTarget) []string {
	sym := strings.TrimSpace(gap.SymbolFQName)
	if sym == "" {
		return nil
	}
	// Take the last segment after `.` or `/` as the test function leaf.
	leaf := sym
	if idx := strings.LastIndexAny(leaf, "./"); idx >= 0 {
		leaf = leaf[idx+1:]
	}
	if leaf == "" {
		return nil
	}
	return []string{"Test" + leaf, "Test_" + leaf}
}

func goTestPackages(gap GapTarget) []string {
	seen := map[string]struct{}{}
	for _, p := range gap.ArtifactPaths {
		dir := filepath.ToSlash(filepath.Dir(strings.TrimSpace(p)))
		if dir == "" || dir == "." {
			continue
		}
		// Prefix with `./` so go test recognises it as a package directory.
		key := "./" + strings.TrimPrefix(dir, "./")
		seen[key] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func javaTestClasses(gap GapTarget) []string {
	seen := map[string]struct{}{}
	for _, p := range gap.ArtifactPaths {
		base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(p)), ".java")
		if base == "" {
			continue
		}
		seen[base] = struct{}{}
	}
	if sym := strings.TrimSpace(gap.SymbolFQName); sym != "" {
		// Guess SymbolTest / SymbolE2EIT names a Maven Failsafe / Surefire runner picks up.
		leaf := sym
		if idx := strings.LastIndexAny(leaf, "./"); idx >= 0 {
			leaf = leaf[idx+1:]
		}
		if leaf != "" {
			seen[leaf+"Test"] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func csharpTestNames(gap GapTarget) []string {
	seen := map[string]struct{}{}
	for _, p := range gap.ArtifactPaths {
		base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(p)), ".cs")
		if base == "" {
			continue
		}
		seen[base] = struct{}{}
	}
	if sym := strings.TrimSpace(gap.SymbolFQName); sym != "" {
		leaf := sym
		if idx := strings.LastIndexAny(leaf, "./"); idx >= 0 {
			leaf = leaf[idx+1:]
		}
		if leaf != "" {
			seen[leaf+"Tests"] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func tsJsTestSpecs(gap GapTarget) []string {
	seen := map[string]struct{}{}
	for _, p := range gap.ArtifactPaths {
		clean := strings.TrimSpace(filepath.ToSlash(p))
		if clean == "" {
			continue
		}
		seen[clean] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func quote(s string) string {
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func filterArtifactPaths(all, want []string) []string {
	if len(all) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(want))
	for _, p := range want {
		set[normalizePathForFix(p)] = struct{}{}
	}
	out := make([]string, 0, len(want))
	for _, p := range all {
		if _, ok := set[normalizePathForFix(p)]; ok {
			out = append(out, p)
		}
	}
	return out
}

func auditFinalStep(ctx context.Context, audit Auditor, sr StepResult) {
	if audit == nil {
		return
	}
	payload := map[string]any{
		"step":        string(sr.Step),
		"ok":          sr.OK,
		"summary":     sr.Summary,
		"duration_ms": sr.DurationMs,
	}
	if sr.Err != nil {
		payload["error"] = sr.Err.Error()
	}
	if !sr.OK {
		audit.LogError(ctx, fmt.Sprintf("evaluator.final.%s", string(sr.Step)), payload)
		return
	}
	audit.Log(ctx, fmt.Sprintf("evaluator.final.%s", string(sr.Step)), payload)
}
