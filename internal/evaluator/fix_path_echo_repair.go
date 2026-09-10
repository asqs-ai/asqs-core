package evaluator

import (
	"path/filepath"
	"strings"
)

// Removal of the artifact path an LLM sometimes writes as the first line of the artifact itself —
// the label of a code block whose fence it did not emit.
//
// Repaired rather than rejected, for the same reason RepairIllegalEscapes is: the generate path
// has no previous version to fall back on, so refusing the payload costs the whole gap. Java's
// javaStrayTokenBeforeTypeReason DID catch this shape and refused it, and run
// an asqs-go Java run lost PetTests.java to that refusal
// ("generated_not_written") — one deleted line short of a usable artifact.
//
// On a JS/TS artifact nothing caught it at all, because the echo is valid TypeScript: a lone
// `src/app/AppLayout.test.tsx` parses as `src / app / AppLayout.test.tsx`, a chain of divisions
// over undeclared identifiers. In run an asqs-go React run it reached disk, vitest
// failed to collect the suite (`ReferenceError: src is not defined`, reported as `(0 test)`), and
// six fixer rounds over 91 minutes went to a file one deleted line would have fixed.

// StripLeadingPathEcho removes a leading line that is nothing but the artifact's own path,
// reporting whether it removed one. Leading blank lines are skipped; the first line carrying any
// other content ends the scan, so only an echo at the very top is ever removed.
//
// Callers write test sources. The rule "a line that is only a path is not code" holds for every
// language the evaluator drives, but it would NOT hold for prose — do not reuse this on generated
// documentation, where a bare path is legitimate content.
func StripLeadingPathEcho(path, content string) (string, bool) {
	want := strings.TrimSpace(filepath.ToSlash(path))
	if want == "" || content == "" {
		return content, false
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rest, ok := stripPathEchoFromLine(line, want)
		if !ok {
			return content, false
		}
		// Everything up to the echo goes: any blank lines above it are the rest of the same stray
		// label, and a source file that starts at its first real line is what the generator was
		// asked for. What FOLLOWED the echo on its line is kept — the model sometimes runs the
		// label straight into the first statement, and dropping the line would take that with it.
		if strings.TrimSpace(rest) == "" {
			return strings.Join(lines[i+1:], "\n"), true
		}
		return strings.Join(append([]string{rest}, lines[i+1:]...), "\n"), true
	}
	return content, false
}

// stripPathEchoFromLine removes a leading mention of want from line, returning what remains and
// whether an echo was there at all.
//
// Deliberately strict about WHERE: the path must open the line. A line that merely contains it is
// source — an import, a comment — and eating that would silently delete a line of a correct
// artifact, which costs more than missing an echo. It is deliberately relaxed about what FOLLOWS,
// because the observed failures were not bare labels: run a later asqs-go Java run
// refused PetTests.java over a line that began with the path and continued.
func stripPathEchoFromLine(line, want string) (rest string, ok bool) {
	s := strings.TrimSpace(line)
	// Decoration the model wraps a label in, in whatever order it applied it. Only leading
	// decoration is consumed here; a trailing quote or colon is handled with the token below.
	for {
		t := strings.TrimLeft(s, "`\"'")
		t = strings.TrimSpace(t)
		if t == s {
			break
		}
		s = t
	}
	if s == "" {
		return "", false
	}
	// A comment naming the file is source the author meant to keep. Excluded explicitly because
	// `//src/app/Foo.test.tsx` ends with "/" + the path, which the container-prefix case accepts.
	for _, marker := range []string{"//", "/*", "*", "#", "--", "<!--"} {
		if strings.HasPrefix(s, marker) {
			return "", false
		}
	}
	// The token is everything up to the first whitespace: a path cannot contain one, so this is
	// the whole candidate and never a prefix of a longer word.
	token, tail := s, ""
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		token, tail = s[:i], s[i+1:]
	}
	token = strings.TrimSpace(strings.Trim(strings.TrimSuffix(strings.TrimSpace(token), ":"), "`\"'"))
	if !pathEchoMatches(strings.ReplaceAll(token, `\`, "/"), want) {
		return "", false
	}
	return strings.TrimSpace(tail), true
}

// pathEchoMatches reports whether a bare token names the artifact.
func pathEchoMatches(token, want string) bool {
	switch {
	case token == want:
		return true
	case strings.HasSuffix(want, "/"+token):
		// A tail of the artifact path — most often the base name on its own.
		return true
	case strings.HasSuffix(token, "/"+want):
		// The same path seen from the sandbox root, e.g. /workspace/src/app/Foo.test.tsx.
		return true
	}
	return false
}
