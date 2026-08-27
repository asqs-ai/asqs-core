package evaluator

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Mockito-misuse facts for the test-step fixer prompt.
//
// The motivating evidence is run api-12aa1935d113c9ea8b50a516fd275660: OwnerTests and PetTests
// failed six consecutive fixer rounds on `when(owner.getPet(...))` / `given(pet.getVisits())`
// where `owner`/`pet` are constructed with `new` in the test class, plus four stubbings the tested
// code never consumes. The exception text (MissingMethodInvocationException,
// UnnecessaryStubbingException, WrongTypeOfReturnValue) and the production bodies were BOTH in the
// prompt every round, and the model still repaired around the misuse instead of naming it —
// WrongTypeOfReturnValue in particular actively misleads, blaming whichever mock happened to be
// stubbed last rather than the non-mock receiver.
//
// Everything stated here is provable without a model: the exception class comes from the failure
// output, the blamed line from its stack frame into a generated artifact, the receiver from that
// source line, and its non-mockness from the same file's declarations. The proof bar is
// deliberately one-sided — a fact is stated only when the receiver has `new`-assignment evidence
// and NO mock/spy evidence anywhere in the file; anything ambiguous stays silent, because a wrong
// "X is not a mock" claim would be worse than the six blind rounds it exists to prevent.

// maxTestFailureFacts bounds the block for the same reason maxMissingMemberFacts does: the first
// few misuses are the primary failure, and a long tail dilutes the instruction.
const maxTestFailureFacts = 6

var (
	// mockitoStubOnNonMockRE matches the two exceptions whose shared root cause is stubbing a
	// receiver that is not a mock. WrongTypeOfReturnValue is included because that is how the same
	// defect presents when an UNRELATED mock invocation was recorded last (Mockito latches the
	// when()/given() onto it and blames a method the test never mentions).
	mockitoStubOnNonMockRE = regexp.MustCompile(`\b(MissingMethodInvocationException|WrongTypeOfReturnValue)\b`)
	mockitoUnnecessaryRE   = regexp.MustCompile(`\bUnnecessaryStubbingException\b`)
	// javaStackFrameRE captures File.java:NN from "at pkg.Class.method(File.java:NN)" frames; the
	// numbered "1. -> at ..." variant UnnecessaryStubbingException uses matches too, because the
	// expression is unanchored.
	javaStackFrameRE = regexp.MustCompile(`at\s+[\w.$]+\(([\w$]+\.java):(\d+)\)`)
	// stubReceiverRE extracts the receiver identifier of a when(x....) / given(x....) stubbing.
	stubReceiverRE = regexp.MustCompile(`\b(?:when|given)\s*\(\s*([A-Za-z_$][\w$]*)\s*\.`)
)

// testFailureFacts derives Mockito-misuse facts from a test step's failure output and the
// generated artifacts' source. Returns nil for non-test steps, for outputs without the relevant
// exceptions, and for occurrences it cannot prove.
func testFailureFacts(ctx context.Context, step SandboxStep, errorOutput string, files map[string]string, artifactPaths []string, audit Auditor) []string {
	if step != StepTest && step != StepTestE2E {
		return nil
	}
	if !mockitoStubOnNonMockRE.MatchString(errorOutput) && !mockitoUnnecessaryRE.MatchString(errorOutput) {
		return nil
	}
	// Artifact lookup by basename: stack frames carry File.java, not repo-relative paths. Two
	// artifacts sharing a basename would make a frame ambiguous, so such basenames are excluded.
	byBase := map[string]string{}
	ambiguous := map[string]bool{}
	for _, p := range artifactPaths {
		p = normalizePathForFix(p)
		base := p
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if _, dup := byBase[base]; dup {
			ambiguous[base] = true
			continue
		}
		if _, ok := files[p]; ok {
			byBase[base] = p
		}
	}

	var facts, auditEntries []string
	seen := map[string]bool{}
	add := func(key, fact, entry string) {
		if seen[key] || len(facts) >= maxTestFailureFacts {
			return
		}
		seen[key] = true
		facts = append(facts, fact)
		auditEntries = append(auditEntries, entry)
	}

	lines := strings.Split(errorOutput, "\n")
	for i, line := range lines {
		switch {
		case mockitoStubOnNonMockRE.MatchString(line):
			exc := mockitoStubOnNonMockRE.FindString(line)
			file, lineNo, ok := firstArtifactFrameAfter(lines, i, byBase, ambiguous)
			if !ok {
				continue
			}
			rel := byBase[file]
			recv, ok := stubReceiverAt(files[rel], lineNo)
			if !ok {
				continue
			}
			if !receiverProvablyNotMock(files[rel], recv) {
				continue
			}
			add(
				fmt.Sprintf("nonmock:%s:%d", rel, lineNo),
				fmt.Sprintf("%s:%d — `%s` is NOT a mock (this test class assigns it with `new`), but when()/given() at that line stubs it; that is what Mockito's %s means here. Stub a @Mock/@Spy field instead, or drive the real `%s` directly and assert its state — do not stub it.",
					file, lineNo, recv, exc, recv),
				fmt.Sprintf("%s:%d#%s (stub-on-non-mock)", file, lineNo, recv),
			)
		case mockitoUnnecessaryRE.MatchString(line):
			for _, site := range artifactFramesAfter(lines, i, byBase, ambiguous) {
				add(
					fmt.Sprintf("unnecessary:%s:%d", site.file, site.line),
					fmt.Sprintf("%s:%d — the stubbing at this line is never used by the code under test (Mockito UnnecessaryStubbingException with strict stubs). Delete that when()/given(), or the test stubs a method the tested path does not call.",
						site.file, site.line),
					fmt.Sprintf("%s:%d (unnecessary-stubbing)", site.file, site.line),
				)
			}
		}
	}
	if len(facts) == 0 {
		return nil
	}
	if audit != nil {
		sortedEntries := append([]string(nil), auditEntries...)
		sort.Strings(sortedEntries)
		audit.Log(ctx, "evaluator.fix_test_failure_facts", map[string]interface{}{
			"message": fmt.Sprintf("Stated %d runtime-verified Mockito-misuse fact(s) for generated tests: %s.",
				len(facts), strings.Join(sortedEntries, ", ")),
			"facts": sortedEntries,
			"count": len(facts),
		})
	}
	return facts
}

type artifactFrame struct {
	file string
	line int
}

// firstArtifactFrameAfter finds the first stack frame at or after lines[from] that lands in an
// unambiguous generated artifact. The scan is bounded: a frame more than 40 lines past the
// exception belongs to a different failure block.
func firstArtifactFrameAfter(lines []string, from int, byBase map[string]string, ambiguous map[string]bool) (string, int, bool) {
	frames := artifactFramesAfter(lines, from, byBase, ambiguous)
	if len(frames) == 0 {
		return "", 0, false
	}
	return frames[0].file, frames[0].line, true
}

func artifactFramesAfter(lines []string, from int, byBase map[string]string, ambiguous map[string]bool) []artifactFrame {
	const scanWindow = 40
	var out []artifactFrame
	for j := from; j < len(lines) && j <= from+scanWindow; j++ {
		// A new failure block ends the scan: frames past the next exception marker belong to a
		// different defect and must not be attributed to this one.
		if j > from && (mockitoStubOnNonMockRE.MatchString(lines[j]) || mockitoUnnecessaryRE.MatchString(lines[j]) || strings.Contains(lines[j], "<<<")) {
			break
		}
		for _, m := range javaStackFrameRE.FindAllStringSubmatch(lines[j], -1) {
			base := m[1]
			if ambiguous[base] {
				continue
			}
			if _, ok := byBase[base]; !ok {
				continue
			}
			n := 0
			fmt.Sscanf(m[2], "%d", &n)
			if n > 0 {
				out = append(out, artifactFrame{file: base, line: n})
			}
		}
	}
	return out
}

// stubReceiverAt extracts the when()/given() receiver at the 1-based line, scanning up to two
// lines above it — Mockito blames the line of the failing call, and a fluent stub may wrap.
func stubReceiverAt(content string, lineNo int) (string, bool) {
	lines := strings.Split(content, "\n")
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	for j := lineNo - 1; j >= 0 && j >= lineNo-3; j-- {
		if m := stubReceiverRE.FindStringSubmatch(lines[j]); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// receiverProvablyNotMock reports whether the identifier is assigned with `new` somewhere in the
// file AND carries no mock/spy evidence anywhere in it. Mock evidence is deliberately cheap to
// satisfy (an @Mock/@Spy annotation within a few lines of a declaration, or a mock()/spy()
// assignment): over-detecting mockness only silences a fact, while under-detecting it would state
// a false one.
func receiverProvablyNotMock(content, recv string) bool {
	q := regexp.QuoteMeta(recv)
	mockAssign := regexp.MustCompile(`\b` + q + `\s*=\s*(?:Mockito\s*\.\s*)?(?:mock|spy)\s*\(`)
	if mockAssign.MatchString(content) {
		return false
	}
	annotated := regexp.MustCompile(`@(?:Mock|Spy|MockBean|MockitoBean)\b[^;{}]{0,200}?\b` + q + `\s*(?:;|=)`)
	if annotated.MatchString(content) {
		return false
	}
	newAssign := regexp.MustCompile(`\b` + q + `\s*=\s*new\s+[A-Z]`)
	return newAssign.MatchString(content)
}
