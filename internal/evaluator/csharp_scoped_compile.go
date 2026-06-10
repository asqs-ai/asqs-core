package evaluator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Scoped-compile fallback for C# evaluations.
//
// Problem: ASQS (and dotnet in general) typically builds the top-level .sln in the workspace. When one of the
// projects in that sln references packages from a private/authenticated NuGet feed and credentials are not
// provided to the build container, `dotnet restore` fails with NU1301 (or NU1101/NU1102/NU1103/NU1403/NU5036)
// on that project. MSBuild aborts the build even though the artifact under evaluation and its transitive
// ProjectReference graph may not depend on the failing sibling at all. Consumers see the sibling's failure
// cascade into bare CS0234/CS0246 on their own code, but no code-level change fixes it.
//
// Fix: when we detect a NuGet restore failure in compile output AND we can derive a "target" project from
// the artifact (or the compile errors), retry the compile scoped to just that project. MSBuild's restore
// and build graph then contain only the target and its transitive ProjectReferences, which excludes the
// failing sibling. `dotnet build <csproj>` is safe to run against any SDK-style project and only pulls in
// what it depends on.
//
// This is a controlled fallback: it is tried at most once per evaluation, only after auto-patchers
// (ProjectReference + sln inclusion + sln Build.0) have been given a chance and after reportNuGetRestoreFailure
// has emitted the structured diagnostic. If the scoped compile succeeds, subsequent Test/Coverage commands
// are also scoped via opts.UnitTestCommand so the unit pass doesn't reopen the sln-level restore.

// reNuGetErrorOnProject finds `.../path/to/Project.csproj : error NUxxxx: ...` lines. The leading path may
// be a container-absolute path (e.g. /workspace/...). The project path is captured for exclusion-set matching.
var reNuGetErrorOnProject = regexp.MustCompile(`(?mi)^\s*(?:[A-Za-z]:)?(?:/|\\)?([^\s:]+?\.csproj)\s*:\s*error\s+NU\d+\b`)

// reCS0234SampleProject finds the `[/workspace/.../Project.csproj]` suffix that the MSBuild console logger
// appends to CS0234 errors. The compile output we get back has these sprinkled wherever a compiler error is
// reported against a specific project. We use the *consumer* project (the one whose Compile failed) as a
// fallback when an artifact doesn't map cleanly to a project.
var reCS0234SampleProject = regexp.MustCompile(`(?mi)error\s+CS02(?:34|46)\b[^\n]*\[([^\]]+\.csproj)\]`)

// nuGetRestoreFailureDetected returns true when the compile output contains any NuGet restore/feed error code.
// The check mirrors reportNuGetRestoreFailure so the two are always consistent; we can't call the reporter
// for the boolean because it only emits audit events and has no return value.
func nuGetRestoreFailureDetected(output string) bool {
	if strings.TrimSpace(output) == "" {
		return false
	}
	if reNuGetServiceIndex.MatchString(output) {
		return true
	}
	if reNuGetServiceIndexGeneric.MatchString(output) {
		return true
	}
	if reNuGetNotFound.MatchString(output) {
		return true
	}
	if reNuGetAuthFail.MatchString(output) {
		return true
	}
	return false
}

// nuGetFailingProjectsNorm returns the set of repo-normalised project paths that appear on the LHS of NU*
// error lines in the compile output. These are the projects whose restore failed — i.e. exactly the ones the
// scoped retry must exclude from its dependency graph.
func nuGetFailingProjectsNorm(output string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range reNuGetErrorOnProject.FindAllStringSubmatch(output, -1) {
		if len(m) < 2 {
			continue
		}
		p := normRepoRel(m[1])
		if p != "" {
			out[p] = struct{}{}
		}
	}
	return out
}

// deriveScopedCompileProject picks a repo-relative .csproj to scope a retry compile against.
//
// Priority:
//  1. The enclosing .csproj of the first artifact path (the test project generated/edited by ASQS). Building
//     the test project is the most conservative target because its transitive graph always includes the
//     consumer-under-test, and because a passing test pass requires the test project to build anyway.
//  2. The consumer project extracted from a CS0234/CS0246 error sample in the compile output. This handles
//     the case where an artifact has no clean enclosing .csproj (e.g. a generated file placed outside any
//     existing test project) but the consumer is unambiguously identified by the compile error itself.
//
// Returns "" when no suitable project can be derived (in which case the caller must not trigger a scoped
// retry — the fallback exists to *fix* things, not to guess).
func deriveScopedCompileProject(repoAbs string, opts EvalOptions, compileOutput string, excludeFailing map[string]struct{}) string {
	if strings.TrimSpace(repoAbs) == "" {
		return ""
	}
	// 1. From artifacts.
	for _, art := range opts.ArtifactPaths {
		art = strings.TrimSpace(art)
		if art == "" {
			continue
		}
		if !strings.EqualFold(filepath.Ext(art), ".cs") {
			continue
		}
		if cs := findEnclosingCsprojForArtifact(repoAbs, art); cs != "" {
			n := normRepoRel(cs)
			if _, bad := excludeFailing[n]; !bad {
				return cs
			}
		}
	}
	// 2. From CS0234/CS0246 error samples.
	for _, m := range reCS0234SampleProject.FindAllStringSubmatch(compileOutput, -1) {
		if len(m) < 2 {
			continue
		}
		cs := normRepoRel(m[1])
		if cs == "" {
			continue
		}
		if _, bad := excludeFailing[cs]; bad {
			continue
		}
		abs := filepath.Join(repoAbs, filepath.FromSlash(cs))
		if _, err := os.Stat(abs); err == nil {
			return cs
		}
	}
	return ""
}

// findEnclosingCsprojForArtifact walks up from the artifact's repo-relative path and returns the first
// repo-relative .csproj encountered, or "" if none is found. It stops at the repo root.
func findEnclosingCsprojForArtifact(repoAbs, artifactRel string) string {
	artRel := filepath.FromSlash(strings.TrimSpace(artifactRel))
	artRel = strings.TrimLeft(artRel, string(os.PathSeparator))
	absArt := filepath.Join(repoAbs, artRel)
	dir := filepath.Dir(absArt)
	for {
		entries, err := os.ReadDir(dir)
		if err == nil {
			csprojs := []string{}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if strings.EqualFold(filepath.Ext(e.Name()), ".csproj") {
					csprojs = append(csprojs, e.Name())
				}
			}
			if len(csprojs) > 0 {
				sort.Strings(csprojs)
				rel, err := filepath.Rel(repoAbs, filepath.Join(dir, csprojs[0]))
				if err == nil {
					return filepath.ToSlash(rel)
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir || !strings.HasPrefix(parent, repoAbs) && parent != repoAbs {
			return ""
		}
		dir = parent
	}
}

// buildScopedDotnetBuildCommand produces an sh-c safe `dotnet build <csproj> -c Release` command line.
// We intentionally do NOT pass --no-restore: scoped restore is the whole point of the fallback — it pulls
// only the target's transitive graph and avoids the sibling's failing feed. When passingSources is
// non-empty, a `/p:RestoreSources="src1;src2;..."` argument is appended so the scoped restore never
// visits the failing feed even when a repo-level nuget.config still lists it. `/p:RestoreIgnoreFailedSources=true`
// is always appended: it turns transient/auth failures on individual sources into warnings instead of
// hard errors, so a private feed that's down or unauthenticated doesn't defeat restore when the packages
// it would have served are actually available from another configured source.
func buildScopedDotnetBuildCommand(csprojRel string, passingSources []string) string {
	base := fmt.Sprintf("dotnet build %s -c Release /p:RestoreIgnoreFailedSources=true", shellQuote(csprojRel))
	if src := joinRestoreSourcesForMSBuild(passingSources); src != "" {
		base += " " + src
	}
	return base
}

// buildScopedDotnetTestCommand produces an sh-c safe `dotnet test <csproj> -c Release` command. Callers
// set opts.UnitTestCommand to this string after a successful scoped compile so subsequent test passes also
// skip the sln-level restore. --no-build is intentionally omitted: the test projects themselves may incremental-
// build after the scoped compile and that's fine; the point is to avoid re-entering the sibling's failing
// NuGet restore graph, which is exactly what dotnet test on a specific csproj does. The same RestoreSources
// and RestoreIgnoreFailedSources overrides are applied so the test command doesn't re-enter the failing
// feed on its incremental restore.
func buildScopedDotnetTestCommand(csprojRel string, passingSources []string) string {
	base := fmt.Sprintf("dotnet test %s -c Release /p:RestoreIgnoreFailedSources=true", shellQuote(csprojRel))
	if src := joinRestoreSourcesForMSBuild(passingSources); src != "" {
		base += " " + src
	}
	return base
}

// joinRestoreSourcesForMSBuild renders an MSBuild-compatible RestoreSources property argument. The
// property value uses `;` as separator on every platform — MSBuild doesn't honour the OS PATH separator
// here. When sources is empty we return "" so the caller can append unconditionally without ever
// producing a trailing-space command line.
func joinRestoreSourcesForMSBuild(sources []string) string {
	clean := make([]string, 0, len(sources))
	for _, s := range sources {
		if v := strings.TrimSpace(s); v != "" {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	return fmt.Sprintf("/p:RestoreSources=%s", shellQuote(strings.Join(clean, ";")))
}

// shellQuote returns a POSIX-safe quoted form of s. We keep this local to avoid pulling in a shellquote
// dependency for a single use; the implementation is sufficient for repo-relative paths (no newlines, no
// control chars) and rejects single quotes defensively by switching to double-quoting only when needed.
func shellQuote(s string) string {
	if s == "" {
		return "\"\""
	}
	if !strings.ContainsAny(s, " \t\"'$\\`!*?[]{}|&;<>()~#") {
		return s
	}
	// Prefer single quotes; if s has a single quote, fall back to POSIX $'...' escaping.
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteString("\"")
	for _, r := range s {
		switch r {
		case '"', '\\', '$', '`':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteString("\"")
	return b.String()
}

// tryScopedCompileForNuGetFailure is the entry point invoked from the workflow's compile-failure branch.
// It returns the scoped StepResult (with OK=true on success) together with the scoped project and the
// audit-friendly reason. When no scoped attempt is made (runner doesn't support CompileWithCommand, not a
// C# run, no NuGet restore failure detected, no derivable target, etc.), it returns ok=false with an empty
// project so the caller can fall through to its existing behaviour.
//
// Side effects on success: sets opts.UnitTestCommand (when empty) so subsequent test passes are scoped too.
func tryScopedCompileForNuGetFailure(
	ctx context.Context,
	runner SandboxRunner,
	opts *EvalOptions,
	compileOutput string,
	audit Auditor,
) (scoped StepResult, csprojRel string, attempted bool) {
	if opts == nil || runner == nil {
		return StepResult{}, "", false
	}
	if !strings.EqualFold(strings.TrimSpace(opts.Lang), "csharp") {
		return StepResult{}, "", false
	}
	if !nuGetRestoreFailureDetected(compileOutput) {
		return StepResult{}, "", false
	}
	scr, ok := runner.(CompileWithCommandRunner)
	if !ok {
		return StepResult{}, "", false
	}
	exclude := nuGetFailingProjectsNorm(compileOutput)
	csproj := deriveScopedCompileProject(opts.RepoPath, *opts, compileOutput, exclude)
	if csproj == "" {
		if audit != nil {
			audit.Log(ctx, "evaluator.compile_scoped_retry_skipped", map[string]interface{}{
				"message":          "Scoped compile retry skipped: could not derive a target project outside the failing NuGet set.",
				"failing_projects": sortedSet(exclude),
				"artifacts":        opts.ArtifactPaths,
			})
		}
		return StepResult{}, "", false
	}
	// Compute the failing feed URLs so we can exclude them from the scoped retry's RestoreSources. The
	// initial sln-level failure is almost always a shared repo-level nuget.config listing a private feed
	// that's unreachable from the build container; scoping the *project* doesn't help because the config
	// chain is still evaluated. Restricting RestoreSources to the non-failing subset bypasses the feed
	// entirely while preserving legitimate restore sources for the target.
	failingURLs := failingNuGetFeedURLs(compileOutput)
	passingSources := passingNuGetSources(opts.RepoPath, csproj, failingURLs)
	// Transitive ProjectReference overlap detection. If the scope target transitively references one of
	// the failing sibling projects, feed exclusion alone cannot rescue the build (the sibling still
	// restores its own packages). We still attempt the retry — cached packages sometimes save us — but
	// we surface the overlap so the operator immediately sees the structural cause if the retry fails.
	transitive := walkTransitiveProjectRefs(opts.RepoPath, csproj, 0)
	transitiveOverlap := intersectionKeys(transitive, exclude)
	// The toolchain cwd for mono-repo sandboxes is `repoRoot/<EvalWorkSubpath>` (e.g. /workspace/projects/upper
	// for solutions at projects/upper/*.sln). Our derived csproj is repo-relative, so when the sandbox cwd
	// is a subpath we must strip that subpath before emitting the command — otherwise MSBuild fails with
	// "MSB1009: Project file does not exist" because it resolves "projects/upper/..." against a cwd that is
	// already inside "projects/upper/". When the runner doesn't report a subpath, this is a no-op.
	subpath := ""
	if sp, ok := runner.(EvalWorkSubpathReporter); ok {
		subpath = strings.Trim(strings.ReplaceAll(strings.TrimSpace(sp.ReportEvalWorkSubpath()), "\\", "/"), "/")
	}
	cmdCsproj, rewritten := stripRepoSubpathPrefix(csproj, subpath)
	cmd := buildScopedDotnetBuildCommand(cmdCsproj, passingSources)
	if audit != nil {
		payload := map[string]interface{}{
			"message":               "Retrying compile scoped to the artifact's project to bypass a sibling's NuGet restore failure.",
			"scoped_project":        csproj,
			"command":               cmd,
			"failing_projects":      sortedSet(exclude),
			"failing_feed_urls":     sortedSet(failingURLs),
			"restore_sources":       sortedSourceList(passingSources),
			"transitive_refs_count": len(transitive),
			"transitive_overlap":    transitiveOverlap,
		}
		if subpath != "" {
			payload["eval_work_subpath"] = subpath
			payload["scoped_project_cwd_relative"] = cmdCsproj
			payload["scoped_project_subpath_stripped"] = rewritten
		}
		audit.Log(ctx, "evaluator.compile_scoped_retry", payload)
	}
	res := scr.CompileWithCommand(ctx, opts.RepoPath, opts.Lang, cmd)
	if res.OK {
		if strings.TrimSpace(opts.UnitTestCommand) == "" {
			opts.UnitTestCommand = buildScopedDotnetTestCommand(cmdCsproj, passingSources)
			if audit != nil {
				audit.Log(ctx, "evaluator.test_scoped_promoted", map[string]interface{}{
					"message":         "Unit test command scoped to the artifact's project so subsequent test passes bypass the failing NuGet sibling.",
					"test_command":    opts.UnitTestCommand,
					"restore_sources": sortedSourceList(passingSources),
				})
			}
		}
		if audit != nil {
			audit.Log(ctx, "evaluator.compile_scoped_ok", map[string]interface{}{
				"message":         "Scoped compile succeeded; continuing with scoped test command.",
				"scoped_project":  csproj,
				"restore_sources": sortedSourceList(passingSources),
			})
		}
	} else if audit != nil {
		// Re-scan the scoped retry's own output for NuGet errors so the audit attributes the failure to
		// the right root cause. If the scoped retry still fails on NuGet feeds (same or different), the
		// operator can see exactly which feed and for which project.
		scopedFailingURLs := failingNuGetFeedURLs(res.Output)
		scopedFailingProjects := nuGetFailingProjectsNorm(res.Output)
		audit.LogError(ctx, "evaluator.compile_scoped_failed", map[string]interface{}{
			"message":                        "Scoped compile retry failed; falling back to previous behaviour.",
			"scoped_project":                 csproj,
			"scoped_project_cwd_relative":    cmdCsproj,
			"eval_work_subpath":              subpath,
			"summary":                        res.Summary,
			"restore_sources":                sortedSourceList(passingSources),
			"transitive_refs_count":          len(transitive),
			"transitive_overlap":             transitiveOverlap,
			"scoped_retry_failing_projects":  sortedSet(scopedFailingProjects),
			"scoped_retry_failing_feed_urls": sortedSet(scopedFailingURLs),
		})
	}
	return res, csproj, true
}

// stripRepoSubpathPrefix rewrites a repo-relative path into a path that resolves against a toolchain cwd
// of `<repoRoot>/<subpath>`. When subpath is empty or the path does not start with subpath, the input is
// returned unchanged. The second return value is true when a rewrite actually happened, so the caller can
// include diagnostic fields in the audit when relevant.
//
// Example: stripRepoSubpathPrefix("projects/upper/Sources/X/X.csproj", "projects/upper")
//
//	→ ("Sources/X/X.csproj", true)
//
// The match is case-sensitive because the toolchain ultimately runs on a case-sensitive filesystem
// (Linux container). If the caller passes a csproj path whose casing does not match the disk, that is a
// separate bug and should not be silently papered over here.
func stripRepoSubpathPrefix(pathRel, subpath string) (string, bool) {
	p := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(pathRel)), "./")
	sp := strings.Trim(filepath.ToSlash(strings.TrimSpace(subpath)), "/")
	if sp == "" || p == "" {
		return p, false
	}
	if !strings.HasPrefix(p, sp+"/") {
		return p, false
	}
	return strings.TrimPrefix(p, sp+"/"), true
}

// intersectionKeys returns the sorted list of keys present in both a and b. Used to report the overlap
// between the scope target's transitive ProjectReference graph and the failing-sibling exclusion set.
func intersectionKeys(a, b map[string]struct{}) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	var out []string
	for k := range a {
		if _, ok := b[k]; ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// normRepoRel collapses any absolute container-style prefix (e.g. "/workspace/") off a path and returns a
// forward-slash, lowercased repo-relative path suitable for set lookups. Empty input and paths that are
// not .csproj pass through unchanged (but lowercased) so callers can use a single exclusion map.
func normRepoRel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	// Strip common container-absolute prefixes (both with and without the leading slash, since upstream
	// regexes may have already consumed the slash as part of a leading anchor).
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "workspace/")
	p = strings.TrimPrefix(p, "./")
	return strings.ToLower(filepath.Clean(p))
}

func sortedSet(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
