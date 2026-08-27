package apisurface

import (
	"regexp"
	"strings"
)

// The generation-time counterpart to RepairMemberCase: a member that is not a near-miss of a real
// one but simply does not exist.
//
// Run api-5e5535208f4ba61613f60c345ba9b567 wrote APIResponseAssertions.hasStatus(int) and
// .hasHeader(String,String). Neither exists — the interface has two members — and the E2E prompt
// carried that exact two-line list under "these are the ONLY members that exist" with a
// complete-list footer. Delivering the member list was not sufficient. Both errors then cost a
// ~25s containerised compile and a share of eleven LLM repair rounds.
//
// Nothing about that needs a compiler. The complete member lists for the types the prompt showed
// are in hand at the moment the file comes back, so the check is a lookup.

// assertThatCallRE matches the start of a Playwright assertion chain, anchored so it can be tested
// at one position at a time. Bare or qualified, because a generated file writes both
// `assertThat(page)` and `PlaywrightAssertions.assertThat(page)`.
var assertThatCallRE = regexp.MustCompile(`^(?:PlaywrightAssertions\.)?assertThat\s*\(`)

// chainedCallRE matches the `.name(` link of a chain, anchored at the dot.
var chainedCallRE = regexp.MustCompile(`^\.\s*([A-Za-z_]\w*)\s*\(`)

// assertJImportRE matches any AssertJ import. Its presence disables the check — see
// InventedAssertionMemberReason.
var assertJImportRE = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?org\.assertj\.`)

// playwrightAssertionTypes are the types a Playwright `assertThat(...)` can return. The chain
// members must come from one of these; the static factory itself is not one of them.
var playwrightAssertionTypes = map[string]bool{
	"com.microsoft.playwright.assertions.LocatorAssertions":     true,
	"com.microsoft.playwright.assertions.PageAssertions":        true,
	"com.microsoft.playwright.assertions.APIResponseAssertions": true,
}

// InventedAssertionMemberReason reports a member called on a Playwright assertion chain that no
// resolved assertion type declares, or "" when nothing can be proven.
//
// Provable is the operative word, and three conditions bound it:
//
//   - Only chains rooted at `assertThat(` are walked. Attributing an arbitrary `.foo(` to a surface
//     would need the receiver's type, which is not knowable here; guessing it would reject valid
//     calls on repo objects.
//   - All three assertion types must have resolved. With one missing, a member it declares would
//     read as invented.
//   - A file that imports AssertJ is skipped entirely. After AssertJ joined the E2E prompt block
//     (e2eAssertionTargets), `assertThat(someString).isEqualTo(...)` is a correct chain whose
//     members live on AssertJ's returned assert types, which are not resolved here. Skipping is the
//     only honest answer: the check degrades to a no-op rather than to a false rejection.
//
// Truncation is deliberately NOT one of them any more. Membership is answered from
// TypeSurface.AllMemberNames, which is complete by construction, so the prompt's rendering budget
// no longer decides whether the check may speak. Gating on Truncated made this unreachable: in run
// api-f34f51a6e1fb10a79f2f57314aae3d23 LocatorAssertions rendered `40 member(s), truncated=true`
// and PageAssertions `7 member(s), truncated=true` — the per-name overload cap — on every Java
// Playwright gap, so the check returned "" every time and hasStatus/hasHeader reached the compiler
// exactly as before.
//
// Returns EVERY violation, deduplicated by member name and in source order. Naming one and stopping
// left the rest to be discovered one compile round at a time, against a retry budget of one.
func InventedAssertionMemberReason(content string, surfaces []TypeSurface) string {
	if strings.TrimSpace(content) == "" || len(surfaces) == 0 {
		return ""
	}
	if assertJImportRE.MatchString(content) {
		return ""
	}
	members := map[string]bool{}
	saw := 0
	for _, s := range surfaces {
		if !playwrightAssertionTypes[s.FQCN] {
			continue
		}
		saw++
		for _, name := range s.AllMemberNames {
			members[name] = true
		}
	}
	// All three types must have been resolved. With one missing, a member it declares would read as
	// invented.
	if saw != len(playwrightAssertionTypes) || len(members) == 0 {
		return ""
	}
	declaredLocally := map[string]bool{}
	for _, m := range localMethodDeclRE.FindAllStringSubmatch(content, -1) {
		declaredLocally[m[1]] = true
	}
	var reasons []string
	reported := map[string]bool{}
	for _, start := range assertThatCallStarts(content) {
		i, ok := skipBalancedArgs(content, start)
		if !ok {
			continue
		}
		for {
			next, name, found := nextChainedCall(content, i)
			if !found {
				break
			}
			if !members[name] && !declaredLocally[name] && !reported[name] {
				reported[name] = true
				reasons = append(reasons, "call to "+name+"() on a Playwright assertion, which declares no such member")
			}
			j, ok := skipBalancedArgs(content, next)
			if !ok {
				break
			}
			i = j
		}
	}
	if len(reasons) > 0 {
		return strings.Join(reasons, "; also ")
	}
	return ""
}

// assertThatCallStarts returns the index of the '(' that opens each assertThat call, skipping
// comments and literals so a chain quoted in a doc comment is never walked.
func assertThatCallStarts(content string) []int {
	var out []int
	for i := 0; i < len(content); {
		if next, skipped := skipJavaNonCode(content, i); skipped {
			i = next
			continue
		}
		// Identifier boundary: `myAssertThat(` and `Foo.assertThat(` are different calls.
		if i > 0 {
			c := content[i-1]
			if c == '.' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
				i++
				continue
			}
		}
		m := assertThatCallRE.FindStringIndex(content[i:])
		if m == nil {
			i++
			continue
		}
		out = append(out, i+m[1]-1)
		i += m[1]
	}
	return out
}

// skipBalancedArgs takes the index of an opening '(' and returns the index just past its match,
// ignoring parens inside comments and literals. Reports false on an unbalanced tail.
func skipBalancedArgs(content string, open int) (int, bool) {
	if open >= len(content) || content[open] != '(' {
		return 0, false
	}
	depth := 0
	for i := open; i < len(content); {
		if next, skipped := skipJavaNonCode(content, i); skipped {
			i = next
			continue
		}
		switch content[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
		i++
	}
	return 0, false
}

// nextChainedCall reports the `.name(` immediately following i, allowing whitespace and line breaks
// so a formatted multi-line chain still reads as one. Anything else ends the chain.
func nextChainedCall(content string, i int) (openParen int, name string, ok bool) {
	j := i
	for j < len(content) && (content[j] == ' ' || content[j] == '\t' || content[j] == '\n' || content[j] == '\r') {
		j++
	}
	if j >= len(content) || content[j] != '.' {
		return 0, "", false
	}
	m := chainedCallRE.FindStringSubmatchIndex(content[j:])
	if m == nil {
		return 0, "", false
	}
	return j + m[1] - 1, content[j+m[2] : j+m[3]], true
}
