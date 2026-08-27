package apisurface

import (
	"regexp"
	"sort"
	"strings"
)

// MemberCaseRepair records one identifier rewritten by RepairMemberCase.
type MemberCaseRepair struct {
	// From is the identifier the model wrote, To is the member that actually exists.
	From, To string
	// FQCN is the type whose complete member list settled it.
	FQCN string
	// Count is how many call sites were rewritten.
	Count int
}

var (
	// dottedCallRE matches a method call on a receiver: `.isOk(`. Deliberately not bare calls —
	// a statically imported `mock(` has no receiver to attribute to a resolved surface, and
	// rewriting one on a name collision would be a guess.
	dottedCallRE = regexp.MustCompile(`\.([A-Za-z_]\w*)\s*\(`)
	// localMethodDeclRE matches a method DECLARATION in the generated file, so a test that defines
	// its own helper is never rewritten to match an unrelated classpath member.
	localMethodDeclRE = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|static|final|abstract|synchronized|default|native|strictfp|async|override|virtual|internal|sealed|partial)\s+)*[\w<>\[\], .?]+\s+([A-Za-z_]\w*)\s*\([^()]*\)(?:\s*throws\s+[\w.,\s]+)?\s*\{`)
)

// RepairMemberCase rewrites method calls whose identifier differs from a real member ONLY in
// letter case, using the complete member lists already resolved from the compile classpath.
//
// The failure it closes, from run api-3fdd28e8f16a37247fa6494315ff6176: the generator was handed
// `APIResponseAssertions (2 member(s), truncated=false)` — a two-line list, rendered under "these
// are the ONLY members that exist" and "(complete member list: a member not shown above does not
// exist)" — and still wrote `isOk()`. The real member is `isOK()`. `isOk` is chai's name; the model
// reached for the JavaScript spelling while writing Java.
//
// Nothing about that needs a model or a compile round to settle. We are holding the exhaustive
// member list for the exact type, so the correction is a lookup. Doing it here saves a ~28s
// containerised compile plus a full LLM repair round for a one-character slip.
//
// Four guards keep it from ever inventing a change:
//
//   - Only surfaces with Truncated=false take part. A truncated list cannot prove a name is wrong.
//   - The identifier must case-insensitively match EXACTLY ONE member. Two candidates is ambiguity,
//     and ambiguity is what this package exists to avoid.
//   - An identifier that exactly matches any known member (from any surface, truncated or not) is
//     left alone — it is already right.
//   - An identifier the file itself DECLARES as a method is left alone, so a local helper named
//     `isOk` is never rewritten into a Playwright call.
//
// Returns the content unchanged and no repairs when nothing qualifies.
func RepairMemberCase(content string, surfaces []TypeSurface) (string, []MemberCaseRepair) {
	if strings.TrimSpace(content) == "" || len(surfaces) == 0 {
		return content, nil
	}
	// exact: every member name we know about, complete lists and truncated ones alike.
	// candidates: lowercase -> the exact names that a COMPLETE list can vouch for.
	exact := map[string]bool{}
	candidates := map[string]map[string]string{} // lower -> name -> fqcn
	for _, s := range surfaces {
		for _, decl := range s.Members {
			name := memberName(decl)
			if name == "" {
				continue
			}
			exact[name] = true
			if s.Truncated {
				continue
			}
			lower := strings.ToLower(name)
			if candidates[lower] == nil {
				candidates[lower] = map[string]string{}
			}
			candidates[lower][name] = s.FQCN
		}
	}
	if len(candidates) == 0 {
		return content, nil
	}
	declaredLocally := map[string]bool{}
	for _, m := range localMethodDeclRE.FindAllStringSubmatch(content, -1) {
		declaredLocally[m[1]] = true
	}

	repairs := map[string]*MemberCaseRepair{}
	var b strings.Builder
	b.Grow(len(content))
	last := 0
	for i := 0; i < len(content); {
		if next, skipped := skipJavaNonCode(content, i); skipped {
			i = next
			continue
		}
		if content[i] != '.' {
			i++
			continue
		}
		loc := dottedCallRE.FindStringSubmatchIndex(content[i:])
		if loc == nil || loc[0] != 0 {
			i++
			continue
		}
		nameStart, nameEnd := i+loc[2], i+loc[3]
		name := content[nameStart:nameEnd]
		i = nameEnd
		if exact[name] || declaredLocally[name] {
			continue
		}
		byName := candidates[strings.ToLower(name)]
		if len(byName) != 1 {
			continue
		}
		var want, fqcn string
		for n, f := range byName {
			want, fqcn = n, f
		}
		if want == name {
			continue
		}
		b.WriteString(content[last:nameStart])
		b.WriteString(want)
		last = nameEnd
		key := name + "\x00" + want
		if r, ok := repairs[key]; ok {
			r.Count++
		} else {
			repairs[key] = &MemberCaseRepair{From: name, To: want, FQCN: fqcn, Count: 1}
		}
	}
	if len(repairs) == 0 {
		return content, nil
	}
	b.WriteString(content[last:])

	out := make([]MemberCaseRepair, 0, len(repairs))
	for _, r := range repairs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	return b.String(), out
}

// skipJavaNonCode returns the index just past a comment, string, text block, template
// literal, or char literal
// starting at i, and whether one was found.
//
// A `.isOk(` inside a string literal or a comment is text, not a call, and rewriting it would
// corrupt an expected-value assertion or a doc line.
func skipJavaNonCode(s string, i int) (int, bool) {
	if i+1 < len(s) && s[i] == '/' && s[i+1] == '/' {
		i += 2
		for i < len(s) && s[i] != '\n' {
			i++
		}
		return i, true
	}
	if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
		i += 2
		for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
			i++
		}
		if i+1 < len(s) {
			i += 2
		}
		return i, true
	}
	// Java text block: """ … """
	if strings.HasPrefix(s[i:], `"""`) {
		if end := strings.Index(s[i+3:], `"""`); end >= 0 {
			return i + 3 + end + 3, true
		}
		return len(s), true
	}
	// Backtick covers TypeScript template literals. Anything inside `${…}` is skipped with the
	// literal rather than rewritten, which is the conservative direction.
	if s[i] == '"' || s[i] == '\'' || s[i] == '`' {
		quote := s[i]
		i++
		for i < len(s) && s[i] != quote {
			if s[i] == '\\' {
				i++
			}
			i++
		}
		if i < len(s) {
			i++
		}
		return i, true
	}
	return i, false
}
