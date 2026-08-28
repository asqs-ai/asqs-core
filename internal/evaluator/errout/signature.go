package errout

import "regexp"

// Line/column patterns across the toolchains this project drives. Each keeps the file name and the
// diagnostic text and blanks only the positional numbers.
var (
	// javac / Maven:  /workspace/src/.../Foo.java:[12,5] error: ...
	reMavenLineCol = regexp.MustCompile(`\.(java|kt):\[\d+,\d+\]`)
	// javac / gcc / tsc / go:  Foo.java:123:45:  or  foo.ts:12:3
	// RE2 has no lookahead, so the delimiter after the position is captured and re-emitted.
	reColonLineCol = regexp.MustCompile(`(\.[A-Za-z]{1,5}):\d+(?::\d+)?([:\s]|$)`)
	// MSBuild / roslyn:  Foo.cs(11,19): error CS1002: ...
	reParenLineCol = regexp.MustCompile(`(\.[A-Za-z]{1,5})\(\d+,\d+\)`)
	// Prose forms emitted by some runners: "at line 42", "line 42, column 7"
	reWordLine = regexp.MustCompile(`(?i)\bline \d+(, ?column \d+)?`)
)

// SignatureNormalize returns a position-insensitive form of an error log, for STALL DETECTION ONLY.
//
// It exists because the run-scope fix loop's circuit breaker compares consecutive failures for
// identity. A fixer that rewrites one file every round shifts every line number in the diagnostics
// while the actual failure is unchanged — so a byte-comparison never matches, the breaker never
// trips, and the loop runs to its full budget. In the run this was built for, that was ~20 rounds
// and ~50 minutes during which the first compiler error never changed.
//
// This must NEVER be used for the fixer's own prompt input: the model needs real line numbers to
// locate the defect. CanonicalForFixLoop remains the canonical text; this is strictly a hashing
// aid layered on top of it.
func SignatureNormalize(lang, raw string) string {
	s := CanonicalForFixLoop(lang, raw)
	s = reMavenLineCol.ReplaceAllString(s, ".$1:[L,C]")
	s = reParenLineCol.ReplaceAllString(s, "$1(L,C)")
	s = reColonLineCol.ReplaceAllString(s, "${1}:L${2}")
	s = reWordLine.ReplaceAllString(s, "line L")
	return s
}
