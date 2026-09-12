package evaluator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/evaluator/errloc"
)

var (
	// nsubNoLastCallRE is the one .NET mock failure with an unambiguous root cause: NSubstitute was
	// told to stub a call on an object that is not a substitute, so there was no recorded call to
	// attach the return value to. It is the direct analogue of Mockito's
	// MissingMethodInvocationException, and the fixer can act on it without guessing.
	nsubNoLastCallRE = regexp.MustCompile(`\bCouldNotSetReturnDueToNoLastCallException\b`)
	// nsubReceivedRE is an assertion failure, not a misuse: the substitute did not get the call.
	nsubReceivedRE = regexp.MustCompile(`\bReceivedCallsException\b`)
	// moqStrictRE is a strict mock reached through a member nothing set up. The message names the
	// behaviour explicitly, which is what makes this distinguishable from a verification failure.
	moqStrictRE = regexp.MustCompile(`(?i)invocation failed with mock behavior Strict`)
	// moqNeverPerformedRE is Moq's verification failure.
	moqNeverPerformedRE = regexp.MustCompile(`(?i)Expected invocation on the mock (?:at least once|[^,\n]*), but was never performed`)
	// fakeItEasyRE is FakeItEasy's single assertion exception.
	fakeItEasyRE = regexp.MustCompile(`\bFakeItEasy\.ExpectationException\b`)

	// nsubStubReceiverRE extracts the receiver of a `x.Foo(...).Returns(...)` stubbing, which is the
	// object NSubstitute was told to stub.
	nsubStubReceiverRE = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*\.\s*\w+\s*\([^)]*\)\s*\.\s*Returns\b`)
	// csharpSubstituteCreationRE recognises the three ways a variable becomes a test double.
	csharpSubstituteCreationRE = `(?:Substitute\s*\.\s*For|A\s*\.\s*Fake|new\s+Mock\s*<)`
)

// csharpMockFailureFacts turns a .NET mock-framework failure into a statement the fixer can act on.
//
// The Mockito arm has existed since the Java work; C# produced nothing, so a Moq or NSubstitute
// failure reached the fixer as a stack trace and a class name. Each fact below is bounded to what
// the output plus the artifact's own source can prove:
//
//   - NSubstitute's CouldNotSetReturnDueToNoLastCallException means the receiver is not a
//     substitute. The receiver is named only when the artifact's source shows it being constructed
//     with `new`, which is the case that is provable; a receiver whose origin is not visible gets
//     the general statement instead.
//   - Moq's strict-behaviour failure is a missing Setup, and the message says so.
//   - A verification failure — Moq's "never performed", NSubstitute's ReceivedCallsException,
//     FakeItEasy's ExpectationException — is the test asserting a call the code did not make. The
//     fact worth stating is that this is a claim about the code under test, not about the mock.
func csharpMockFailureFacts(errorOutput string, files map[string]string, artifactPaths []string) []string {
	var facts []string
	add := func(f string) {
		for _, existing := range facts {
			if existing == f {
				return
			}
		}
		facts = append(facts, f)
	}

	if nsubNoLastCallRE.MatchString(errorOutput) {
		if recv, path, ok := csharpNonSubstituteReceiver(errorOutput, files, artifactPaths); ok {
			add(fmt.Sprintf(
				"NSubstitute could not set a return value: %q (%s) is constructed with `new`, so it is a real object and "+
					"`.Returns(...)` on it has no recorded call to attach to. Create it with `Substitute.For<T>()`, or drop the "+
					"stubbing and assert on the real object's behaviour.", recv, path))
		} else {
			add("NSubstitute could not set a return value: `.Returns(...)` was called on an object that is not a substitute. " +
				"Only a value produced by `Substitute.For<T>()` can be stubbed.")
		}
	}
	if moqStrictRE.MatchString(errorOutput) {
		add("This Moq mock was created with MockBehavior.Strict, so every member the code under test calls needs its own " +
			"`Setup(...)`. Add a Setup for the member named in the message, or create the mock with the default loose behaviour.")
	}
	if moqNeverPerformedRE.MatchString(errorOutput) {
		add("The Moq verification failed because the call was never performed: the code under test did not invoke that member. " +
			"This is a statement about the code, not about the mock — check the path the test exercises before changing the " +
			"expectation, and do not weaken the verification to make it pass.")
	}
	if nsubReceivedRE.MatchString(errorOutput) {
		add("The NSubstitute `Received(...)` assertion failed: the substitute did not get that call. " +
			"This is a statement about the code under test, not about the substitute.")
	}
	if fakeItEasyRE.MatchString(errorOutput) {
		add("The FakeItEasy assertion failed: the fake did not get the call the test asserted. " +
			"This is a statement about the code under test, not about the fake.")
	}
	return facts
}

// csharpNonSubstituteReceiver names the stubbed receiver when the artifact's own source proves it is
// a real object — constructed with `new` and never produced by a substitute factory.
//
// The proof is deliberately one-directional. A receiver this cannot see is left unnamed rather than
// guessed at: naming the wrong variable would send the round to rewrite correct code.
func csharpNonSubstituteReceiver(errorOutput string, files map[string]string, artifactPaths []string) (recv, path string, ok bool) {
	artifacts := map[string]bool{}
	for _, p := range artifactPaths {
		artifacts[normalizePathForFix(p)] = true
	}
	for _, loc := range errloc.ParseLocations(errorOutput) {
		for candidate, body := range files {
			if !artifacts[normalizePathForFix(candidate)] {
				continue
			}
			if !strings.HasSuffix(strings.ToLower(loc.File), strings.ToLower(pathBaseOf(candidate))) {
				continue
			}
			src := dotnetproj.StripCSharpCommentsAndStrings(body)
			lines := strings.Split(src, "\n")
			if loc.Line < 1 || loc.Line > len(lines) {
				continue
			}
			m := nsubStubReceiverRE.FindStringSubmatch(lines[loc.Line-1])
			if m == nil {
				continue
			}
			name := m[1]
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*` + csharpSubstituteCreationRE).MatchString(src) {
				return "", "", false // it IS a substitute; the cause is something else
			}
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*new\s+\w`).MatchString(src) {
				return name, candidate, true
			}
		}
	}
	return "", "", false
}

func pathBaseOf(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
