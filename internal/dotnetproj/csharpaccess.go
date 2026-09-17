package dotnetproj

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// accessModifiers matches a C# access modifier, longest form first so `protected internal` is not
// captured as the bare `protected` with `internal` left to the modifier run.
const accessModifiers = `(?:public|protected\s+internal|internal\s+protected|private\s+protected|private|protected|internal)`

var (
	memberAccessCacheMu sync.Mutex
	memberAccessCache   = map[string]*regexp.Regexp{}
)

// memberAccessPattern builds the pattern for ONE named member, cached because a fix round asks
// about the same handful of names repeatedly.
//
// Anchoring on the member's own name is what makes this safe to run over a whole file. The general
// member pattern in csharpdecl.go requires an access modifier precisely so a bare statement cannot
// masquerade as a declaration; this one can afford to make the modifier optional — which it must, to
// see a member that has none — because the name it is looking for is already fixed.
func memberAccessPattern(member string) *regexp.Regexp {
	memberAccessCacheMu.Lock()
	defer memberAccessCacheMu.Unlock()
	if re, ok := memberAccessCache[member]; ok {
		return re
	}
	re := regexp.MustCompile(
		`(?m)^[ \t]*` + memberAttributes +
			`(` + accessModifiers + `\s+)?` + memberModifiers +
			`(?:` + memberType + `\s+)?` +
			regexp.QuoteMeta(member) + `\s*(?:<[^<>()\n]*>\s*)?(?:[({=;]|=>)`)
	memberAccessCache[member] = re
	return re
}

// DeclaredMemberAccess reports the access modifier a type declares a member with.
//
// It exists to tell "the type has no such member" apart from "the type has it and you cannot reach
// it". Those are different facts with different remedies, and the compiler renders both as CS0117
// "does not contain a definition for" when the caller is in another assembly — so the error text
// alone cannot distinguish them.
//
// Run api-2555a79ee2660a8a5cd95c8c860090f5 is what conflating them costs. A gap was planned against
// `internal static string DescribeBlockingLegacy(...)`, and the fact built for the fixer said the
// type "has NO member" while listing that same member among the ones to use instead — because
// DeclaredMemberNames offers internal members as call targets. The model was handed a contradiction
// and spent three rounds deleting tests to satisfy it.
//
// ok is false when no declaration of that name is found, which the caller must read as "cannot
// tell" rather than "absent": this scans text, and a member declared through a construct this
// pattern does not model is a member it simply does not see.
func DeclaredMemberAccess(typeName, member, src string) (access string, ok bool) {
	member = strings.TrimSpace(member)
	if member == "" || strings.TrimSpace(src) == "" {
		return "", false
	}
	stripped := StripCSharpCommentsAndStrings(src)

	// An interface member carries no modifier and is public by definition; reporting "private" for
	// one (the class default below) would invent an inaccessibility that does not exist.
	if body, isInterface := interfaceBodyFor(typeName, stripped); isInterface {
		if memberAccessPattern(member).MatchString(body) {
			return "public", true
		}
		return "", false
	}

	m := memberAccessPattern(member).FindStringSubmatch(stripped)
	if m == nil {
		return "", false
	}
	if a := normalizeAccess(m[1]); a != "" {
		return a, true
	}
	// C# defaults a class or struct member with no modifier to private.
	return "private", true
}

// normalizeAccess collapses the whitespace C# allows inside a two-word modifier, so a caller
// comparing against "protected internal" does not have to.
func normalizeAccess(raw string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

// AccessIsAssemblyScoped reports whether an access modifier confines a member to its own assembly
// in a way an InternalsVisibleTo grant can open.
//
// `protected internal` is deliberately included: it is "this assembly OR a derived type", so a test
// in another assembly that is not a subclass needs the same grant. `private protected` is not — it
// is "this assembly AND derived", which no grant alone opens.
func AccessIsAssemblyScoped(access string) bool {
	switch normalizeAccess(access) {
	case "internal", "protected internal", "internal protected":
		return true
	}
	return false
}

// DescribeAccessRemedy renders what a caller in another assembly can do about an access modifier.
// Empty when the member is public, which is not an accessibility problem at all.
func DescribeAccessRemedy(typeName, member, access string) string {
	switch {
	case AccessIsAssemblyScoped(access):
		return fmt.Sprintf(
			"%s declares %q as `%s`, so it is visible only inside its own assembly. A test in a separate "+
				"assembly reaches it only if the production project grants InternalsVisibleTo to the test "+
				"assembly, which this run has not done. Do not call it: test the behaviour through a public "+
				"entry point instead.",
			typeName, member, normalizeAccess(access))
	case normalizeAccess(access) == "public":
		return ""
	default:
		return fmt.Sprintf(
			"%s declares %q as `%s`, which no project-file grant opens to another assembly. Do not call it: "+
				"test the behaviour through a public entry point instead.",
			typeName, member, normalizeAccess(access))
	}
}
