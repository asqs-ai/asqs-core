package dotnetproj

import (
	"regexp"
	"sort"
	"strings"
)

// This file holds the C# declaration facts that both halves of the pipeline read: the fixer, when
// it states that a member does not exist, and the generator, when it refuses a call to one before
// the file is written. They have to agree — a member the generator allows and the fixer then calls
// invented would put the loop into an argument with itself — so the extraction lives here, below
// both, rather than being written twice.

var (
	// reDeclaredMember matches members worth offering as call targets: public and internal, never
	// private. Offering a private member would trade one compile error for another.
	reDeclaredMember = regexp.MustCompile(
		`(?m)^\s*(?:public|internal|protected internal)\s+(?:static\s+|virtual\s+|override\s+|sealed\s+|async\s+|readonly\s+|required\s+|partial\s+|extern\s+|new\s+)*(?:[\w.<>\[\],?]+\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:[({=;]|=>)`)

	// reTypeKeyword is the set of words the member pattern can capture from a declaration LINE
	// rather than from a member: `public class Foo` matches it with "class".
	typeKeywords = map[string]bool{
		"class": true, "struct": true, "interface": true, "enum": true, "record": true,
		"delegate": true, "event": true, "const": true, "using": true, "namespace": true,
		"operator": true, "implicit": true, "explicit": true,
	}
)

// DeclaredMemberNames lists the members a caller may use on a type: public and internal methods,
// properties and fields, enum members, and a record's positional parameters.
//
// The scan is over the whole source rather than a brace-matched type body. In a file that declares
// one type — the C# convention, and what the repository index produces per file — the two are the
// same; in a file that declares several, the result is a superset. A superset is the safe direction
// for every caller here: it makes an absence claim rarer, never wider.
//
// Positional record parameters are properties and nothing in the body declares them, so a scan that
// missed them would call every use of a record's own fields invented.
func DeclaredMemberNames(typeName, src string) []string {
	src = StripCSharpCommentsAndStrings(src)

	if body, isEnum := enumBodyFor(typeName, src); isEnum {
		var out []string
		for _, part := range strings.Split(body, ",") {
			name := strings.TrimSpace(part)
			if i := strings.Index(name, "="); i >= 0 {
				name = strings.TrimSpace(name[:i])
			}
			if name != "" {
				out = append(out, name)
			}
		}
		return out
	}

	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		switch {
		case name == "" || seen[name]:
			return
		case name == typeName:
			return // the constructor; described by its own fact
		case typeKeywords[name]:
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, p := range recordPositionalParameters(typeName, src) {
		add(p)
	}
	for _, m := range reDeclaredMember.FindAllStringSubmatch(src, -1) {
		add(m[1])
	}
	sort.Strings(out)
	return out
}

// enumBodyFor returns the body of the enum with exactly this name.
//
// Both halves of that sentence were wrong before. The type was matched with
// `strings.Contains(src, "enum "+typeName)`, which is a PREFIX test — "enum BasketState" contains
// "enum Basket" — and the body then came from a pattern that found the first enum in the file
// whatever it was called. A class declared beside an enum whose name merely starts with the class's
// was therefore read as that enum and given its members.
//
// A validation run is the case, and it is the worst shape this can fail in: the generator told the
// model that Basket.Add() does not exist and offered it the members of BasketState. Add is a public
// method of Basket. A correct test was rejected, its one retry spent, and the model was handed a
// false statement about the very type it was testing.
func enumBodyFor(typeName, strippedSrc string) (string, bool) {
	re := regexp.MustCompile(`\benum\s+` + regexp.QuoteMeta(typeName) +
		`\s*(?::\s*[\w.]+\s*)?\{([^}]*)\}`)
	m := re.FindStringSubmatch(strippedSrc)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// recordPositionalParameters returns the property names a record declares in its parameter list.
func recordPositionalParameters(typeName, strippedSrc string) []string {
	re := regexp.MustCompile(`\brecord\s+(?:class\s+|struct\s+)?` + regexp.QuoteMeta(typeName) +
		`\s*(?:<[^>(]*>)?\s*\(([^)]*)\)`)
	m := re.FindStringSubmatch(strippedSrc)
	if m == nil {
		return nil
	}
	var out []string
	for _, part := range splitTopLevel(m[1]) {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		// `int Id` and `IReadOnlyList<string> Tags` both end in the parameter name.
		out = append(out, strings.TrimSuffix(fields[len(fields)-1], ","))
	}
	return out
}

// BaseTypeNames returns the simple names of the types a declaration derives from or implements.
//
// C# writes base classes and interfaces in one comma-separated list with no keyword to tell them
// apart, so both come back together. That is what the caller needs: it is asking whether the whole
// member set is in view, and an unresolvable entry of either kind means it is not.
func BaseTypeNames(typeName, src string) []string {
	src = StripCSharpCommentsAndStrings(src)
	re := regexp.MustCompile(`\b(?:class|record|struct|interface)\s+(?:class\s+|struct\s+)?` +
		regexp.QuoteMeta(typeName) + `\s*(?:<[^>(]*>)?\s*(?:\([^)]*\))?\s*:([^{;]*)`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return nil
	}
	list := m[1]
	// A generic constraint is not a base type: `class Repo<T> : Base where T : IEntity`.
	if i := strings.Index(list, " where "); i >= 0 {
		list = list[:i]
	}
	var out []string
	for _, part := range splitTopLevel(list) {
		name := strings.TrimSpace(part)
		if i := strings.IndexAny(name, "<("); i >= 0 {
			name = name[:i] // IEnumerable<Order> -> IEnumerable; a primary-constructor call's args
		}
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:] // Shop.Core.BaseService -> BaseService
		}
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// splitTopLevel splits on commas that are not inside brackets, so a generic argument list stays in
// one piece.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}
