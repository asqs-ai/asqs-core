// Package errout trims and classifies raw tool/compiler/test output before it is passed to the LLM
// fixer. It is a thin, allocation-light layer that sits on top of internal/evaluator/errloc (which
// does the detailed file:line extraction). The three public helpers are used in three different
// places of the evaluator workflow:
//
//   - Sanitize: trims noisy, duplicated, and non-actionable log epilogues (currently Maven/Gradle;
//     other toolchains pass through unchanged) so both the LLM prompt and the evaluator audit
//     record stop shipping the same 10-line block twice.
//   - IsCompileShaped: returns true when the log carries compiler diagnostics (as opposed to runtime
//     test failures). Used to route StepTest-phase failures that are actually javac/kotlinc/scalac/
//     csc/tsc errors to the compile-role conditioning block instead of the test-role one.
//   - AllCitedRepoPaths: unbounded, order-preserving superset of errloc.ExtraReadableRepoPaths with
//     one added hint — Java "symbol: class <FQCN>" / "location: ... of type <FQCN>" lines resolve to
//     src/main/java/<fqcn>.java when the file exists under repoRoot.
package errout

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/errloc"
)

// Sanitize returns a copy of raw with tool-specific noise stripped. For java/kotlin/scala (Maven and
// Gradle wrappers), the function (1) trims the long duplicated `[ERROR]` block Maven prints after the
// `Failed to execute goal` marker, (2) drops the generic Maven epilogue (`[Help 1]` / `-e switch` /
// `-X switch` / `MojoFailureException` link / "For more information" footer), (3) collapses runs of
// blank lines. For any other language the function returns raw unchanged so the caller is never
// surprised by non-local sanitization rules. Idempotent.
func Sanitize(lang, raw string) string {
	if raw == "" {
		return raw
	}
	normLang := strings.ToLower(strings.TrimSpace(lang))
	switch normLang {
	case "java", "kotlin", "scala":
		return sanitizeMavenLike(raw)
	default:
		return raw
	}
}

// sanitizeMavenLike implements the Maven/Gradle-wrapper–specific rules documented on Sanitize. It is
// intentionally conservative: the primary error block (before `Failed to execute goal`) is kept
// verbatim. The epilogue filter is applied as a safety net even when the cut marker is absent so
// Gradle output (which has similar help footers) also gets trimmed.
func sanitizeMavenLike(raw string) string {
	lines := strings.Split(raw, "\n")
	cutIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "[ERROR] Failed to execute goal") {
			cutIdx = i
			break
		}
	}
	if cutIdx >= 0 {
		lines = lines[:cutIdx+1]
	}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if isMavenGradleEpilogueLine(line) {
			continue
		}
		kept = append(kept, line)
	}
	joined := collapseBlankRuns(strings.Join(kept, "\n"))
	return strings.TrimRight(joined, "\n")
}

// mavenEpiloguePatterns lists line substrings we consider non-actionable for an LLM fixer. Each is
// matched case-sensitively against the full line (not TrimSpaced); the patterns are chosen so that a
// bare `[ERROR]` line from the middle of a real stacktrace never matches.
var mavenEpiloguePatterns = []string{
	"[ERROR] -> [Help ",
	"[ERROR] To see the full stack trace of the errors",
	"[ERROR] Re-run Maven using the -X switch",
	"[ERROR] Re-run Maven with the -X switch",
	"[ERROR] For more information about the errors and possible solutions",
	"[ERROR] [Help 1] http",
	"[ERROR] [Help 2] http",
	"MojoFailureException",
	// Gradle variants
	"> Run with --stacktrace option to get the stack trace",
	"> Run with --info or --debug option to get more log output",
	"> Get more help at https://help.gradle.org",
}

// isMavenGradleEpilogueLine reports whether the line is a pure help/footer line with no actionable
// content. Lines with substantive error text (even if they contain a "[Help 1]" fragment as part of
// a larger diagnostic) are kept intact.
func isMavenGradleEpilogueLine(line string) bool {
	// Blank or whitespace-only `[ERROR]` lines Maven emits between epilogue paragraphs add nothing.
	trimmed := strings.TrimSpace(line)
	if trimmed == "[ERROR]" {
		return true
	}
	for _, p := range mavenEpiloguePatterns {
		if strings.Contains(line, p) {
			return true
		}
	}
	return false
}

// collapseBlankRuns collapses any run of 2+ blank lines into a single blank line. Preserves the
// trailing newline count at most 1.
func collapseBlankRuns(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blankRun := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun <= 1 {
				out = append(out, "")
			}
			continue
		}
		blankRun = 0
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// compileSignalPatterns is the set of regexes we consider evidence of a *compile*-shaped failure
// (as distinct from a runtime test failure). Matches across java/kotlin/scala/csharp/typescript.
// NuGet restore errors (NU####) are intentionally not included — they are an environment-level
// failure handled upstream by nuGetRestoreFailureDetected and must not flip compile-role routing.
var compileSignalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\[ERROR\]\s+COMPILATION ERROR`),
	regexp.MustCompile(`cannot find symbol`),
	regexp.MustCompile(`no suitable method found`),
	regexp.MustCompile(`incompatible types`),
	regexp.MustCompile(`(?m)^\s*javac:`),
	regexp.MustCompile(`package\s+[\w.]+\s+does not exist`),
	// Kotlin compiler per-file diagnostics: "e: /path/File.kt:12:5 ..."
	regexp.MustCompile(`(?m)^e:\s+[^\n]+\.kt(?:s)?:\d+`),
	// Scala: "[error] File.scala:12: ..."
	regexp.MustCompile(`(?m)^\[error\]\s+[^\n]+\.scala:\d+`),
	// C# (csc / MSBuild) — excludes NU\d+ via a negative char class around the prefix.
	regexp.MustCompile(`error\s+CS\d{4}\b`),
	// TypeScript: "TS1234:" style codes
	regexp.MustCompile(`\bTS\d{4}\b`),
	// Generic typescript tsc textual "error TSxxxx:"
	regexp.MustCompile(`error\s+TS\d{4}`),
}

// IsCompileShaped reports whether raw carries at least one signal matching a compiler diagnostic.
// It is scan-and-bail-fast and allocates nothing on a miss.
func IsCompileShaped(raw string) bool {
	if raw == "" {
		return false
	}
	for _, re := range compileSignalPatterns {
		if re.MatchString(raw) {
			return true
		}
	}
	return false
}

// mavenBracketFilePattern matches Maven's compiler-plugin diagnostic shape `path.java:[LINE,COL]`
// (and .kt / .kts / .scala), which errloc.ParseLocations intentionally does not handle — the base
// regex over there expects a bare `:digit` after the extension. We only need the path, not the line
// number, so the capture group stops at the extension.
var mavenBracketFilePattern = regexp.MustCompile(`([A-Za-z0-9_./\\~-]+\.(?:java|kt|kts|scala)):\[\d+,\s*\d+\]`)

// javaFQCNLocationPattern matches Java compiler diagnostics of the form
//
//	location: variable foo of type com.example.Bar
//	location: class com.example.Bar
//
// capturing the fully-qualified class name. Used only when errout itself resolves FQCNs; callers
// that just want raw path hits should use errloc.ParseLocations instead.
var javaFQCNLocationPattern = regexp.MustCompile(`(?m)location:\s+(?:variable\s+\w+\s+of\s+type|class)\s+([a-zA-Z_][\w.]*)`)

// javaFQCNSymbolPattern captures `symbol: class <FQCN>`. Plain method/variable symbol lines are not
// captured because their type only makes sense together with the accompanying `location:` line and
// that one is already covered above.
var javaFQCNSymbolPattern = regexp.MustCompile(`(?m)symbol:\s+class\s+([a-zA-Z_][\w.]*)`)

// AllCitedRepoPaths returns repo-relative, forward-slashed, de-duplicated paths cited anywhere in
// raw that resolve to a real file under repoRoot. Ordering reflects first appearance. The result is
// a superset of errloc.ExtraReadableRepoPaths — additionally, Java "location:/symbol:" FQCN hints
// are resolved to src/main/java/<fqcn>.java when that file exists. An empty log or empty repoRoot
// yields nil. The function does not enforce any count cap — callers impose their own budget.
func AllCitedRepoPaths(raw, repoRoot string) []string {
	if raw == "" || repoRoot == "" {
		return nil
	}
	cleanRoot := filepath.Clean(repoRoot)
	seen := make(map[string]bool)
	var out []string

	addIfNew := func(rel string) {
		n := errloc.NormalizePath(rel)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}

	for _, loc := range errloc.ParseLocations(raw) {
		rel, ok := tryResolveUnderRepo(cleanRoot, loc.File)
		if !ok {
			continue
		}
		addIfNew(rel)
	}

	// Maven compiler-plugin uses `path:[line,col]` rather than `path:line`, which errloc's base
	// regexes don't match. Scan those explicitly so the primary artifact under fix is always picked
	// up before the FQCN hint pass runs.
	for _, m := range mavenBracketFilePattern.FindAllStringSubmatch(raw, -1) {
		if len(m) < 2 {
			continue
		}
		if rel, ok := tryResolveUnderRepo(cleanRoot, m[1]); ok {
			addIfNew(rel)
		}
	}

	for _, m := range javaFQCNLocationPattern.FindAllStringSubmatch(raw, -1) {
		if len(m) < 2 {
			continue
		}
		if rel := resolveJavaFQCN(cleanRoot, m[1]); rel != "" {
			addIfNew(rel)
		}
	}
	for _, m := range javaFQCNSymbolPattern.FindAllStringSubmatch(raw, -1) {
		if len(m) < 2 {
			continue
		}
		if rel := resolveJavaFQCN(cleanRoot, m[1]); rel != "" {
			addIfNew(rel)
		}
	}

	return out
}

// resolveJavaFQCN maps com.example.Bar to src/main/java/com/example/Bar.java (forward-slashed,
// repo-relative). Returns "" when the file does not exist, when the FQCN has no package prefix, or
// when the name looks like a language built-in (java.lang.*, java.util.*, etc.). Built-in filtering
// is intentionally permissive — any false miss just means the path is not injected, which is safe.
func resolveJavaFQCN(repoRoot, fqcn string) string {
	fqcn = strings.TrimSpace(fqcn)
	if fqcn == "" || !strings.Contains(fqcn, ".") {
		return ""
	}
	if isJavaBuiltinFQCN(fqcn) {
		return ""
	}
	rel := "src/main/java/" + strings.ReplaceAll(fqcn, ".", "/") + ".java"
	full := filepath.Join(repoRoot, filepath.FromSlash(rel))
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		return ""
	}
	return rel
}

// isJavaBuiltinFQCN checks a conservative prefix allow-list for common stdlib / framework packages
// so we don't hit the disk with impossible candidates. Unknown prefixes fall through and get the
// os.Stat check in resolveJavaFQCN.
func isJavaBuiltinFQCN(fqcn string) bool {
	builtins := []string{
		"java.", "javax.", "jakarta.",
		"sun.", "com.sun.",
		"kotlin.", "scala.",
	}
	for _, p := range builtins {
		if strings.HasPrefix(fqcn, p) {
			return true
		}
	}
	return false
}

// tryResolveUnderRepo is a forward-slash-normalised lookup: the raw path from the log is stripped
// of container prefixes (e.g. /workspace/) and then probed against repoRoot. Returns the repo-
// relative path when the resolved file exists, ("", false) otherwise. The function is intentionally
// a near-duplicate of the unexported helper in errloc/extra_paths.go so errout stays an independent
// package with no cross-package exports of internal plumbing.
func tryResolveUnderRepo(repoRoot, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "file://")
	raw = strings.ReplaceAll(raw, "\\", "/")
	if raw == "" {
		return "", false
	}
	var candidates []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "/")
		if s != "" {
			candidates = append(candidates, s)
		}
	}
	add(raw)
	if after, ok := strings.CutPrefix(raw, "/workspace/"); ok {
		add(after)
	}
	if i := strings.Index(raw, "src/"); i >= 0 {
		add(raw[i:])
	}
	low := strings.ToLower(raw)
	if i := strings.Index(low, "/tests/"); i >= 0 {
		add(raw[i+1:])
	}
	for _, c := range candidates {
		full := filepath.Join(repoRoot, filepath.FromSlash(c))
		full = filepath.Clean(full)
		rel, err := filepath.Rel(repoRoot, full)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		st, err := os.Stat(full)
		if err != nil || st.IsDir() {
			continue
		}
		return filepath.ToSlash(rel), true
	}
	return "", false
}
