package retrieval

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// specMarkerFilter answers whether an E2E_SPEC candidate is a file with a test in it.
//
// An E2E_SPEC gap means "extend this end-to-end test". A file in a test project that declares no
// test is not one: it is the shared harness, a base class, a fixtures module, a global-usings file.
// Anchoring a gap to one invites the generator to write a test INTO the code every other test
// depends on. Run api-8d5367b3383017e25f09e53dacf6c275 did exactly that — two of its four E2E
// anchors were a WebApplicationFactory subclass and a GlobalUsings.cs — and it ended with every
// functional test in the repository throwing
// "Cannot create an instance of CustomWebApplicationFactory`1[TProgram]".
//
// The C# indexer has since been narrowed to require a test method, which is where the bad data came
// from. This is the second line of defence, and it earns its place twice over: an index built before
// that change still holds the old symbols, and the Java and JS/TS indexers make the same judgement
// independently.
//
// A zero value (no repository path) allows everything, matching reachabilityFilter.
type specMarkerFilter struct {
	repoRoot string
}

func newSpecMarkerFilter(opts PlanOptions) specMarkerFilter {
	return specMarkerFilter{repoRoot: strings.TrimSpace(opts.RepoPath)}
}

// keep drops the candidates this filter can prove declare no test, preserving order.
func (f specMarkerFilter) keep(syms []*metadata.Symbol) []*metadata.Symbol {
	if f.repoRoot == "" || len(syms) == 0 {
		return syms
	}
	out := make([]*metadata.Symbol, 0, len(syms))
	for _, s := range syms {
		if s == nil || f.allows(s.File, s.Lang) {
			out = append(out, s)
		}
	}
	return out
}

// allows reports whether the candidate stands. Every branch that cannot make the negative claim
// returns true: no marker set for the language, no readable file, an empty file. Filtering on
// ignorance removes work the run should have done and nothing reports it — the same rule
// csharpReachableFilter applies one filter over.
func (f specMarkerFilter) allows(file, lang string) bool {
	markers := testMarkersForLang(lang)
	if len(markers) == 0 {
		return true
	}
	rel := strings.TrimSpace(file)
	if rel == "" {
		return true
	}
	b, err := os.ReadFile(filepath.Join(f.repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		// Not in the working tree — written this run, or filtered out of the checkout. Unknowable.
		return true
	}
	src := string(b)
	for _, m := range markers {
		if strings.Contains(src, m) {
			return true
		}
	}
	return false
}

// testMarkersForLang returns the substrings that mark a declared test, or nil when the language has
// no known set — which means "do not judge", never "no tests".
//
// Substring matching, not parsing: the claim only has to be good enough to tell a spec from a
// harness, and every inaccuracy it has runs towards KEEPING a candidate. `/re/.test(x)` in a
// JavaScript helper reads as a test marker and the file stands; that is the direction to be wrong in.
func testMarkersForLang(lang string) []string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "csharp", "cs":
		// xUnit, NUnit and MSTest. "[Test" covers [TestCase] and [TestMethod] as well.
		return []string{"[Fact", "[Theory", "[Test"}
	case "java":
		// "@Test" covers @TestFactory and @TestTemplate; the other two are their own annotations.
		return []string{"@Test", "@ParameterizedTest", "@RepeatedTest"}
	case "javascript", "typescript", "js", "ts":
		// Playwright, Jest, Vitest, Mocha and Cypress all declare through one of these.
		return []string{"it(", "test(", "it.each", "test.each", "test.describe"}
	default:
		return nil
	}
}
