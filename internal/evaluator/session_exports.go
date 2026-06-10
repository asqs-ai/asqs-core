package evaluator

// These thin wrappers expose helpers that the new session.Normalizer needs to classify failures
// without duplicating parsing logic. They are stable aliases over private functions; existing
// callers within this package continue to use the lowercase originals. Kept in a dedicated file so
// `grep` makes the re-export boundary obvious.

// NuGetRestoreFailureDetected reports whether compile output indicates a .NET NuGet restore
// failure (NU1301/NU1101/NU1102/NU1103/NU1403/NU5036). Mirrors nuGetRestoreFailureDetected.
func NuGetRestoreFailureDetected(output string) bool {
	return nuGetRestoreFailureDetected(output)
}

// CompileErrorTouchesArtifactScope reports whether the compile error cites any of the generated
// artifacts or their declared dependencies. Mirrors compileErrorTouchesArtifactScope.
func CompileErrorTouchesArtifactScope(errorOutput string, opts EvalOptions) bool {
	return compileErrorTouchesArtifactScope(errorOutput, opts)
}

// SortedFailureFingerprint returns a stable fingerprint for a set of failing paths; used by the
// session repeated-failure policy.
func SortedFailureFingerprint(paths []string) string {
	return sortedFailureFingerprint(paths)
}

// FailingNuGetFeedURLs returns the private-feed URLs cited by NU1301-class errors (if any),
// sorted for deterministic output.
func FailingNuGetFeedURLs(output string) []string {
	return sortedSet(failingNuGetFeedURLs(output))
}

// NormalizePathForFix returns the canonical fix-loop path (slash-normalized, trimmed). Stable
// across call sites.
func NormalizePathForFix(p string) string { return normalizePathForFix(p) }

// TestOutputWithoutPassLines returns test stderr/stdout with PASS lines filtered out. The
// normalizer uses this before calling ParseFailingTestPaths so noisy green lines do not trigger
// false-positive path matches.
func TestOutputWithoutPassLines(output string) string {
	return testOutputWithoutPassLines(output)
}
