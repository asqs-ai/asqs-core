package evaluator

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/errout"
)

// EnforcePrimarySiteAfterUntouchedRounds is how many consecutive untouched-primary-site rounds
// (same site, same line-insensitive failure signature) are tolerated before the next round is
// FORCED onto the blamed file: writable scope collapses to that file and the prompt carries an
// explicit directive. Two, not one: TouchedPrimarySite is a heuristic, and a single miss can be a
// legitimate two-part repair; two misses against an unchanged failure is the pattern that ran four
// rounds unchallenged in api-7549a0ea57f8950449087ff85f1c4ce6 (`fix_primary_site_untouched` ×4,
// every one advisory).
const EnforcePrimarySiteAfterUntouchedRounds = 2

// StopPrimarySiteAfterUntouchedRounds ends the loop when even the FORCED round (single writable
// file, explicit directive) came back without acting on the blamed site. At that point the model
// has demonstrated it will not perform this specific repair, and the remaining budget buys
// repetition, not convergence. Terminal (FixSkipPrimarySiteNeverTouched) — the run-scope loop
// treats unknown skip reasons as non-retryable.
const StopPrimarySiteAfterUntouchedRounds = 3

// primarySiteDiagnosticBlock returns only the blamed file's own diagnostics, attributed the same
// way FileDiagnostics attributes them: a diagnostic owns the text from its `path:[line,col]`
// location up to the next one, keeping javac's indented `symbol:`/`location:` detail lines with
// it. Blocks are sorted so reordering is not mistaken for change. Empty when the output carries
// no parseable diagnostic for the site's file.
func primarySiteDiagnosticBlock(site PrimaryFailureSite, errorOutput string) string {
	var blocks []string
	rest := errorOutput
	offset := 0
	for {
		m := primaryDiagnosticRE.FindStringSubmatchIndex(rest[offset:])
		if m == nil {
			break
		}
		start := offset + m[0]
		pathTok := rest[offset+m[2] : offset+m[3]]
		end := len(rest)
		if next := primaryDiagnosticRE.FindStringIndex(rest[offset+m[1]:]); next != nil {
			end = offset + m[1] + next[0]
		}
		if sameDiagnosticFile(site.Path, pathTok) {
			blocks = append(blocks, rest[start:end])
		}
		offset += m[1]
	}
	if len(blocks) == 0 {
		return ""
	}
	sort.Strings(blocks)
	return strings.Join(blocks, "\n")
}

// primarySiteStreakSignature is the streak identity: blamed path + line-insensitive signature of
// THE BLAMED FILE'S OWN diagnostics — not the whole output. fixLoopSignature is not reused
// because it hashes the canonical (line-bearing) full output; and hashing even the normalized
// full output proved too broad: in run api-7b38aac91623c962b588a0e0a9fbb2f6 the primary site
// (OwnerTests.java:93, byte-identical diagnostic) recurred for six rounds while a sibling
// artifact's diagnostics churned, so the whole-output signature kept changing, the streak reset
// 1→2→1→2, and the enforcement built for exactly that stall never fired. Falls back to the full
// output only when no per-file block can be attributed.
func primarySiteStreakSignature(lang string, site PrimaryFailureSite, errorOutput string) string {
	basis := errorOutput
	if block := primarySiteDiagnosticBlock(site, errorOutput); block != "" {
		basis = block
	}
	h := sha1.New()
	h.Write([]byte(filepath.ToSlash(site.Path)))
	h.Write([]byte{0})
	h.Write([]byte(errout.SignatureNormalize(lang, basis)))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Did the round touch the thing that was actually broken?
//
// `evaluator.fix_applied` fires whenever a write lands, which is not the same claim. One run
// recorded a successful fix to PetTests.java in all seven rounds while the primary diagnostic
// stayed at PetTests.java:[32,57] byte for byte — the fixer was editing around the defect, and in
// one round introduced a new error on line 33 while leaving 32 alone. The audit called every one of
// those rounds a repair.
//
// This does not block anything. It reports, so a round that changed a file without touching the
// line the compiler named is visible immediately instead of after seven of them.

// primaryDiagnosticRE captures the first diagnostic location in a compiler output, in either shape
// the toolchains this project drives emit:
//
//	javac / Maven   OwnerTests.java:[149,17] cannot find symbol
//	javac / go      Foo.java:12:5: ...        internal/foo.go:12:3: ...
//	tsc / MSBuild   src/app/AppLayout.test.tsx(34,22): error TS2339: ...
//	                Foo.cs(11,19): error CS1002: ...
//
// The parenthesised alternative is not cosmetic. Without it this pattern matched NOTHING in a
// TypeScript compile log, and every breaker built on it was silently inert on TS and C#:
// FileDiagnostics returned nil so stalledFiles compared two empty maps and
// evaluator.fix_file_no_progress could never fire, and ParsePrimaryFailureSite never reported OK so
// the primary-site guard and evaluator.fix_primary_site_untouched could never fire either. The run
// of 2026-09-01 is what that looks like from outside: a fix loop visibly repeating itself with not
// one progress signal in the audit, on 112 errors none of which this pattern could see.
//
// A column is still required after the line in the paren form (the trailing [,:] class), so
// `foo.ts(3)` appearing in prose cannot open a bogus diagnostic bucket. tsc and MSBuild both always
// emit line AND column on error lines.
//
// `@` is in the path class because scoped npm packages put it mid-path: without it the token for
// `node_modules/@jest/core/build/TestScheduler.js:133:18` began at `jest/…`, which no vendored-path
// test could recognise. See vendoredDiagnosticFrame for the second line of defence.
var primaryDiagnosticRE = regexp.MustCompile(`([\w./\\@-]+\.(?:java|kt|cs|ts|tsx|js|jsx|go))(?::\[?|\()(\d+)[,:]`)

// vendoredFramePrefixRE matches a vendored tree opening a path earlier on the same line than the
// token primaryDiagnosticRE captured — the case where the token started mid-path because of a
// character the path class does not admit.
var vendoredFramePrefixRE = regexp.MustCompile(`(?:^|[\s(\['"=:])(?:node_modules|vendor)/`)

// vendoredDiagnosticFrame reports whether the diagnostic at match m is a frame inside a vendored
// tree: either the captured path itself has a vendored segment, or the line it sits on opens a
// vendored path before the token.
func vendoredDiagnosticFrame(errorOutput string, m []int) bool {
	if pathIsVendored(errorOutput[m[2]:m[3]]) {
		return true
	}
	lineStart := strings.LastIndex(errorOutput[:m[0]], "\n") + 1
	return vendoredFramePrefixRE.MatchString(errorOutput[lineStart:m[2]])
}

// PrimaryFailureSite is the file and line the first diagnostic blames.
type PrimaryFailureSite struct {
	Path string
	Line int
	OK   bool
	// ResolutionFailure is true when the primary diagnostic is a NAME- or OVERLOAD-resolution
	// failure — the class of error the import block repairs. It matters because the repair for
	// those does not appear on the blamed line at all:
	//
	//	OwnerTests.java:[149,17] cannot find symbol
	//	  symbol: method verify(Pet)   location: class OwnerTests
	//
	// is fixed by `import static org.mockito.Mockito.verify;`, and `verify(newPet)` on line 149 is
	// already correct and must stay exactly as it is. Likewise
	//
	//	WelcomeControllerE2EIT.java:[63,17] no suitable method found for assertThat(int)
	//	  method PlaywrightAssertions.assertThat(Page) is not applicable
	//
	// is fixed by importing AssertJ's assertThat overload, again without touching line 63.
	//
	// Without this distinction TouchedPrimarySite reads a correct import-only repair as "left the
	// blamed line unchanged" and the caller reverts it. Runs api-a9d3283aee54232a7d377e624b2690c5
	// and api-5e5535208f4ba61613f60c345ba9b567 both died that way, the second after fixing two of
	// its three blocking errors.
	ResolutionFailure bool
	// AmbiguousReference narrows ResolutionFailure to the subset repaired by REMOVING an import.
	// `reference to assertThat is ambiguous` means two static imports supply the same name, and
	// the fix is to drop the competing one — the import block shrinks rather than grows.
	//
	// Direction matters, so it is tracked rather than folded into "the import block changed". For
	// `cannot find symbol` only an ADDITION can help; a round that merely deletes an import has
	// made things strictly worse, and banking it would let the next failure differ enough to slip
	// past the repeat-fingerprint breaker too.
	AmbiguousReference bool
	// ParseFailure is true when the primary diagnostic is a SYNTAX error — the file did not parse.
	//
	// The line-based test is meaningless for these, and worse than meaningless: javac blames the
	// token where the parser choked, and the repair for a mangled statement routinely lands on a
	// different line, or restructures the method so the blamed line's text survives verbatim
	// somewhere else in the file. TouchedPrimarySite then reads the repair as "left the blamed line
	// unchanged" and the caller reverts it — to content that does not parse.
	//
	// That is the trap, and it is a trap with no exit. The revert restores the round-start file,
	// which is the broken file, so the identical parse error is guaranteed and the next round is
	// reverted for the same reason. Run api-f34f51a6e1fb10a79f2f57314aae3d23 died exactly there:
	// one fixer round wrote OwnerTests.java with `not a statement` at line 90, and rounds 2 through
	// 5 each rewrote the file, each left line 90, each were rolled back onto the corruption, and
	// the parse error masked every remaining semantic error in the other five files.
	//
	// So for a parse failure ANY change to the file counts as acting on the primary site. The
	// round banks, the compiler gets to judge it, and the run-scope repeat-fingerprint breaker
	// bounds the retries — which is the correct division of labour: this guard exists to stop
	// churn, not to defend a file that is already broken.
	ParseFailure bool
}

// parseFailureRE matches diagnostics emitted by the PARSER, before any name or type is resolved.
//
// Membership is decided by javac/roslyn/tsc phase, not by severity: `cannot find symbol` is an
// attribution error against a file that parsed fine, and reverting it is sound. Everything here
// means the file is not valid source, and reverting to the version that produced it cannot help.
var parseFailureRE = regexp.MustCompile(`(?i)` + strings.Join([]string{
	// javac parser.
	`not a statement`,
	`illegal start of (?:expression|type)`,
	`reached end of file while parsing`,
	`unclosed (?:string literal|character literal|comment)`,
	`'[^']{1,3}' expected`,
	`<identifier> expected`,
	`class, interface, enum, or record expected`,
	// C# parser: CS1002 "; expected", CS1513 "} expected", CS1519, CS1022.
	`CS(?:1002|1003|1022|1026|1513|1519|1525)`,
	// TypeScript parser: TS1005 "',' expected", TS1128 "Declaration or statement expected".
	`TS(?:1005|1109|1128|1131|1160|1435)`,
}, "|"))

// resolutionFailureRE matches the diagnostics the import block can repair, across the toolchains
// this project drives.
//
// Leaning PERMISSIVE is deliberate, because the two error directions cost wildly different things.
// A false REVERT destroys a correct repair, burns the whole budget and fails the run — observed
// three runs running. A false ACCEPT banks one useless round, after which the failure repeats
// against an unreverted tree, so lastRoundReverted is false and the run-scope repeat-fingerprint
// breaker stops it within two. The lax direction is self-limiting; the strict one is not.
//
// Pure inclusion, no exclusion patterns: `incompatible types: Collection<V> cannot be converted to
// Set<V>` and `class A.B is already defined` match nothing here and stay outside the class, so a
// round that adds an unrelated import while leaving a genuine semantic error alone still reverts.
// Exclusions were tempting for the "cannot be converted to" text, but that phrase also appears in
// the argument-mismatch detail under `no suitable method found`, which IS in the class.
var resolutionFailureRE = regexp.MustCompile(`(?i)` + strings.Join([]string{
	// javac: unresolved name, and unresolved package behind a qualified use.
	`cannot find symbol`,
	`package [\w.$]+ does not exist`,
	// javac: overload resolution. An unimported static overload is exactly this shape, and the
	// repair is an import rather than an edit at the call site.
	`no suitable method found`,
	`is not applicable`,
	// javac: two static imports supplying the same name. Repaired by REMOVING one — which is why
	// changedImportBlock has to count removals, not just additions.
	`reference to \w+ is ambiguous`,
	// C#: CS0103, CS0246, CS0121, CS1061 (missing using for an extension method).
	`does not exist in the current context`,
	`could not be found`,
	`call is ambiguous`,
	`no accessible extension method`,
	// TypeScript: TS2304, TS2305.
	`cannot find name`,
	`has no exported member`,
	`is not defined`,
}, "|"))

// playwrightTestListLineRE matches the lines Playwright's list reporter prints per test:
//
//	✘  1 [chromium] › routes/announcements.spec.tsx:4:3 › Announcements Page › should display …
//	  1) [chromium] › routes/announcements.spec.tsx:4:3 › Announcements Page › should display …
//	    [chromium] › routes/home.spec.tsx:32:3 › Home Page Route › should navigate …
//
// The `file:line:col` there is where the `test(` call STARTS, not where anything failed. The
// failing assertion is in the `at /workspace/e2e/routes/announcements.spec.tsx:8:24` frame that
// follows. Run api-9f854a955e0110668e02fec8d45198a5 blamed `announcements.spec.tsx:4` on three
// rounds — the header line — and reported every rewrite as having "left the blamed line
// unchanged", which would have narrowed the next round onto a line no repair ever touches.
var playwrightTestListLineRE = regexp.MustCompile(`(?m)^[^\n]*\[[\w-]+\] › [^\n]*:\d+:\d+ › `)

// primaryDiagnosticLocations returns every primaryDiagnosticRE match that is a diagnostic rather
// than a test-list header or a vendored stack frame, in output order.
//
// Vendored frames are dropped for the same reason readFixContextFiles drops them from the fixer's
// context: a path under node_modules/ or vendor/ is where a runtime error was REPORTED, not where
// the fault is, and nothing the fixer writes can change it. The run of 2026-09-03 (Angular fixture,
// jest) blamed `node_modules/@jest/core/build/TestScheduler.js:133` for five consecutive rounds —
// the frame jest prints under "Your test suite must contain at least one test" — so every
// fix_primary_site_untouched line named a file no round could have written, and the file jest
// actually refused to run (checkout.component.test.ts, cited only by its FAIL header) was never the
// primary site.
func primaryDiagnosticLocations(errorOutput string) [][]int {
	skip := playwrightTestListLineRE.FindAllStringIndex(errorOutput, -1)
	inSkipped := func(pos int) bool {
		for _, r := range skip {
			if pos >= r[0] && pos < r[1] {
				return true
			}
		}
		return false
	}
	var out [][]int
	for _, m := range primaryDiagnosticRE.FindAllStringSubmatchIndex(errorOutput, -1) {
		if len(m) < 6 || inSkipped(m[0]) {
			continue
		}
		if vendoredDiagnosticFrame(errorOutput, m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// ParsePrimaryFailureSite reads the first diagnostic location out of compiler or test output.
func ParsePrimaryFailureSite(errorOutput string) PrimaryFailureSite {
	return ParsePrimaryFailureSiteAmong(errorOutput, nil)
}

// ParsePrimaryFailureSiteAmong is ParsePrimaryFailureSite with a preference: when `prefer` names
// files (repo-relative, typically the writable artifacts), the first diagnostic located in one of
// them wins over an earlier diagnostic located elsewhere. The first location overall remains the
// fallback, so output that cites no preferred file behaves exactly as before.
//
// The preference exists because a test runner's first location is routinely a frame in the code
// UNDER test: in the run of 2026-09-03 round 4 blamed `legacy-invoice-bridge.service.ts:22` — a
// production file the fixer may not write — while the failing test file was cited four lines
// later. A primary site the fixer cannot act on makes every downstream signal (untouched-site
// streaks, enforcement) inert or wrong.
func ParsePrimaryFailureSiteAmong(errorOutput string, prefer []string) PrimaryFailureSite {
	locs := primaryDiagnosticLocations(errorOutput)
	if len(locs) == 0 {
		return PrimaryFailureSite{}
	}
	m := locs[0]
	if len(prefer) > 0 {
	pick:
		for _, loc := range locs {
			tok := errorOutput[loc[2]:loc[3]]
			for _, p := range prefer {
				if sameDiagnosticFile(tok, p) {
					m = loc
					break pick
				}
			}
		}
	}
	line := 0
	for _, r := range errorOutput[m[4]:m[5]] {
		line = line*10 + int(r-'0')
	}
	if line <= 0 {
		return PrimaryFailureSite{}
	}
	// Classify from the primary diagnostic's own text only — the window from its location to the
	// next diagnostic — so a later, unrelated "cannot find symbol" in a cascading log cannot make
	// an `incompatible types` primary look resolvable by an import.
	window := primaryDiagnosticWindow(errorOutput, m[1])
	resolution := resolutionFailureRE.MatchString(window)
	ambiguous := ambiguousReferenceRE.MatchString(window)
	// An ambiguity between two overloads of the SAME type is not an import problem in either
	// direction, so neither import carve-out may apply to it. Both flags are cleared rather than
	// just AmbiguousReference: ResolutionFailure's carve-out is an ADDED import, and no import
	// added or removed changes which of `Owner.getPet(String)` and `Owner.getPet(Integer)` a bare
	// `getPet(null)` binds to. The repair is a cast at the call site, on the blamed line, which is
	// exactly what the line test already asks for.
	//
	// Cleared only when the candidates' declaring types were positively parsed AND found equal —
	// see sameTypeAmbiguity. An unrecognised shape keeps the lax reading, because the two error
	// directions still cost what resolutionFailureRE's comment says they cost.
	if sameTypeAmbiguity(window) {
		resolution, ambiguous = false, false
	}
	return PrimaryFailureSite{
		Path:               normalizePathForFix(errorOutput[m[2]:m[3]]),
		Line:               line,
		OK:                 true,
		ResolutionFailure:  resolution,
		AmbiguousReference: ambiguous,
		ParseFailure:       parseFailureRE.MatchString(window),
	}
}

var (
	// javacAmbiguityCandidateRE captures the declaring type of each candidate in javac's
	// "both method m(A) in a.b.C and method m(B) in a.b.D match" detail line. The capture stops at
	// `<` so a generic declaring type resolves to its raw name, which is what makes two candidates
	// on the same generic class compare equal.
	javacAmbiguityCandidateRE = regexp.MustCompile(`\bin\s+([\w.$]+)`)
	// csharpAmbiguityCandidateRE captures the declaring type of each candidate in roslyn's CS0121
	// "The call is ambiguous between the following methods or properties: 'A.M(int)' and 'A.M(string)'".
	csharpAmbiguityCandidateRE = regexp.MustCompile(`'([\w.$]+)\.\w+\s*\(`)
)

// sameTypeAmbiguity reports whether an ambiguity diagnostic's candidates were all declared by one
// type — provably, from the compiler's own candidate list.
//
// The two shapes javac writes with the same words are repaired completely differently:
//
//	reference to assertThat is ambiguous
//	  both method assertThat(T) in org.assertj.core.api.Assertions
//	  and method assertThat(Page) in …playwright.assertions.PlaywrightAssertions match
//
// is two static imports supplying one name, repaired by dropping one — the case AmbiguousReference
// was built for. Whereas
//
//	reference to getPet is ambiguous
//	  both method getPet(java.lang.String) in …owner.Owner
//	  and method getPet(java.lang.Integer) in …owner.Owner match
//
// is two overloads of one class, repaired by casting the argument. Run
// api-f536b62286a895e5e824fc6a214dbf04 ended on the second shape while the classifier read it as
// the first, which handed any round that merely shuffled an import a free pass through the
// primary-site guard.
//
// Returns false whenever fewer than two candidates parse, so an unfamiliar phrasing keeps the
// previous behaviour instead of silently becoming strict.
func sameTypeAmbiguity(window string) bool {
	if !ambiguousReferenceRE.MatchString(window) {
		return false
	}
	var types []string
	for _, m := range javacAmbiguityCandidateRE.FindAllStringSubmatch(window, -1) {
		types = append(types, m[1])
	}
	if len(types) < 2 {
		types = nil
		for _, m := range csharpAmbiguityCandidateRE.FindAllStringSubmatch(window, -1) {
			types = append(types, m[1])
		}
	}
	if len(types) < 2 {
		return false
	}
	for _, t := range types[1:] {
		if t != types[0] {
			return false
		}
	}
	return true
}

// primaryDiagnosticWindow returns the text of the first diagnostic: from the end of its location
// match up to the next diagnostic location, or the end of the output.
func primaryDiagnosticWindow(errorOutput string, from int) string {
	rest := errorOutput[from:]
	if next := primaryDiagnosticRE.FindStringIndex(rest); next != nil {
		return rest[:next[0]]
	}
	return rest
}

// TouchedPrimarySite reports whether a write to `path` changed the source line the primary
// diagnostic named. Returns ok=false when the question cannot be answered (different file,
// unparseable site, line out of range), so the caller stays silent rather than asserting.
//
// The test is content-based, not positional: take the exact text of the blamed line from `before`,
// and ask whether that text still appears in `after`. Positional comparison fails both ways here —
// an edit above the line shifts it, and a window around it counts neighbours. Round 2 of the
// motivating run added a brand-new error on line 33 while leaving 32 untouched, so a ±2 window
// would have called that round a repair.
//
// Whitespace is normalised, so a reformat alone does not read as a fix.
func TouchedPrimarySite(site PrimaryFailureSite, path, before, after string) (touched bool, ok bool) {
	if !site.OK || !sameDiagnosticFile(site.Path, path) {
		return false, false
	}
	blamed := lineAt(before, site.Line)
	if strings.TrimSpace(blamed) == "" {
		return false, false
	}
	if before == after {
		return false, true
	}
	// Occurrences, not presence.
	//
	// Presence was blind to a repeated statement, and a generated test file repeats statements
	// constantly — the same call exercised from two test methods is the normal shape, not the
	// exotic one. Run api-f536b62286a895e5e824fc6a214dbf04 ended with
	//
	//	OwnerTests.java:[136,35] reference to getPet is ambiguous
	//	OwnerTests.java:[250,35] reference to getPet is ambiguous
	//
	// two byte-identical call sites, and a fix at 136 left the text still present at 250. The
	// guard read that as "left the blamed line unchanged" and reverted it, four rounds running,
	// over one missing cast.
	//
	// Counting keeps everything presence gave us — position independence, so an edit above the
	// line does not read as a repair, and a reformat still does not either — while making a
	// partial repair visible. It stays blind in one direction: a round that fixes the blamed site
	// AND introduces a fresh copy of the old text elsewhere nets to zero. That is no worse than
	// before and needs a parser to do better.
	if countNormalizedLine(after, blamed) < countNormalizedLine(before, blamed) {
		return true, true
	}
	// A file that does not parse has no defensible base to revert to — see ParseFailure. Any change
	// is progress by definition, because the alternative is restoring the corruption.
	if site.ParseFailure {
		return true, true
	}
	// The blamed line survived — which is the CORRECT outcome when the diagnostic is a name- or
	// overload-resolution failure. `verify(newPet)` was never the defect; the missing
	// `import static org.mockito.Mockito.verify;` was. Requiring the blamed line to change would
	// reject the only repair that can work, so for this diagnostic class a change to the import
	// block counts as acting on the primary site.
	//
	// Restricted to that class on purpose. If a round rewrites the import block while leaving an
	// `incompatible types` line untouched, that is still churn and still reverts — otherwise one
	// junk import per round would disable the guard entirely.
	if site.ResolutionFailure {
		if addedImportLine(before, after) {
			return true, true
		}
		// Only an ambiguity is repaired by dropping an import, so only there does a shrinking
		// import block count as progress.
		if site.AmbiguousReference && changedImportBlock(before, after) {
			return true, true
		}
	}
	return false, true
}

// importLineRE matches an import/using directive across the toolchains this project drives.
// Anchored at the start of a line so a mention inside a string or comment does not count.
var importLineRE = regexp.MustCompile(`(?m)^[ \t]*(?:import\s+static\s+[\w.$]+\s*;|import\s+[\w.$]+\s*;|(?:global\s+)?using\s+(?:static\s+)?[\w.$=\s]+;|import\s+.+?\s+from\s+['"\x60][^'"\x60]+['"\x60]\s*;?|import\s+['"\x60][^'"\x60]+['"\x60]\s*;?)`)

// ambiguousReferenceRE matches the SHAPE of an ambiguity diagnostic. Whether its repair actually
// removes an import is decided by sameTypeAmbiguity, which reads the candidate list.
var ambiguousReferenceRE = regexp.MustCompile(`(?i)reference to \w+ is ambiguous|call is ambiguous`)

// importDirectives returns the set of normalised import/using lines in s.
func importDirectives(s string) map[string]bool {
	out := make(map[string]bool)
	for _, m := range importLineRE.FindAllString(s, -1) {
		out[collapseWhitespace(m)] = true
	}
	return out
}

// addedImportLine reports whether `after` declares an import that `before` did not.
func addedImportLine(before, after string) bool {
	was := importDirectives(before)
	for k := range importDirectives(after) {
		if !was[k] {
			return true
		}
	}
	return false
}

// changedImportBlock reports whether the set of import directives differs at all — added, removed,
// or swapped. Used only for ambiguity diagnostics, where removal is the repair.
func changedImportBlock(before, after string) bool {
	was, now := importDirectives(before), importDirectives(after)
	if len(was) != len(now) {
		return true
	}
	for k := range now {
		if !was[k] {
			return true
		}
	}
	return false
}

// lineAt returns the 1-indexed line, or "" when out of range.
func lineAt(s string, line int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return lines[line-1]
}

// countNormalizedLine counts the lines of s equal to want, ignoring all whitespace differences.
// Collapsing internal runs as well as the margins matters: a reformat is not a repair, and
// `Set<Visit>  a` differing from `Set<Visit> a` by one space must not read as one.
func countNormalizedLine(s, want string) int {
	w := collapseWhitespace(want)
	n := 0
	for _, ln := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if collapseWhitespace(ln) == w {
			n++
		}
	}
	return n
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// sameDiagnosticFile reports whether a compiler-reported path and a repo-relative path name the
// same file.
//
// Compilers report the path they were given, which for a containerised build is absolute inside the
// container ("/workspace/src/test/…") and shares no prefix with the repo root. Matching on equality
// silently answered "different file" for every diagnostic in a Docker run, which disabled this
// check entirely. Suffix matching on a segment boundary is what the rest of the system already does
// (see errout.tryResolveUnderRepo).
func sameDiagnosticFile(diagPath, repoRel string) bool {
	a := normalizePathForFix(diagPath)
	b := normalizePathForFix(repoRel)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if strings.HasSuffix(a, "/"+b) {
		return true
	}
	return strings.HasSuffix(b, "/"+a)
}

// primarySiteBase is a short display form for audit messages.
func primarySiteBase(site PrimaryFailureSite) string {
	return filepath.Base(site.Path)
}
