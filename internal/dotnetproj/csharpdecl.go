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

// The pieces a member declaration is made of. They are spelled once and composed, because the same
// shapes have to be recognised twice: with an access modifier in a class body, and without one in an
// interface body, where C# does not permit modifiers at all.
const (
	// memberAttributes are the attribute groups a member may carry on its own line.
	// `[JsonPropertyName("id")] public string Id { get; set; }` is one line, and anchoring the
	// modifier at the start of it hid every property written that way.
	//
	// The content runs to the last `]` on the line rather than the first, because an attribute
	// argument may itself contain brackets — `[Values(new[] { 1, 2 })]`. Stopping at the first one
	// put the scan in the middle of the attribute, where no access modifier follows.
	memberAttributes = `(?:\[[^\n]*\][ \t]*)*`

	// memberModifiers are the non-access modifiers that may sit between the access modifier and the
	// return type. `const` belongs here: a public constant is a member a test may read, and leaving
	// it out made `Order.MaxItems` read as invented.
	memberModifiers = `(?:(?:static|virtual|override|sealed|abstract|async|readonly|required|partial|` +
		`extern|new|const|unsafe|volatile|event|ref)\s+)*`

	// genericArguments matches a `<…>` list three levels deep. Three, rather than a character class,
	// because a type argument list is written WITH SPACES by everyone — `Dictionary<string, int>`,
	// `Func<int, string>`, `Task<Dictionary<string, List<int>>>` — and a character run ends at the
	// first space. That hid the member exactly as the generic-METHOD hole did, and at the same two
	// claim sites. A regular expression cannot balance brackets to arbitrary depth; three covers
	// every signature anyone writes, and a fourth simply falls back to not matching, which offers
	// one member fewer rather than a wrong one.
	genericArgumentsL1 = `<[^<>\n]*>`
	genericArgumentsL2 = `<(?:[^<>\n]|` + genericArgumentsL1 + `)*>`
	genericArguments   = `<(?:[^<>\n]|` + genericArgumentsL2 + `)*>`

	// memberType is the return or field type: a tuple, or a name with optional type arguments and
	// any number of array and nullable suffixes. The tuple alternative is why it is not one
	// character class: `public (int, string) Summary()` is an ordinary C# signature, and a pattern
	// built only from identifier characters cannot see past the parentheses.
	memberType = `(\([^()\n]*\)|[\w.?\[\],]+(?:` + genericArguments + `)?(?:\[\]|\?)*)`

	// memberNameAndOpener is the member's own name, its type parameters when it is generic, and the
	// token that ends the declaration. The type-parameter list is the reason generic methods were
	// invisible: in `public T Get<T>(int id)` the name is followed by `<`, never by `(`.
	memberNameAndOpener = `([A-Za-z_][A-Za-z0-9_]*)\s*(?:<[^<>()\n]*>\s*)?`
)

var (
	// reDeclaredMember matches members worth offering as call targets: public and internal, never
	// private. Offering a private member would trade one compile error for another.
	//
	// Both orders of the two-word access modifier are accepted. C# allows `protected internal` and
	// `internal protected` interchangeably, and only the first was listed.
	reDeclaredMember = regexp.MustCompile(
		`(?m)^[ \t]*` + memberAttributes +
			`(?:public|internal|protected\s+internal|internal\s+protected)\s+` +
			memberModifiers + `(?:` + memberType + `\s+)?` + memberNameAndOpener + `(?:[({=;]|=>)`)

	// reInterfaceMember matches a member of an INTERFACE, which carries no access modifier — so the
	// pattern above, which requires one, returned an empty set for every interface in the
	// repository while still reporting the type as fully known. A test holding
	// `IOrderService svc = new OrderService();` then had every call on svc called invented.
	//
	// It is only ever run against a brace-matched interface body, where every declaration is a
	// member. Run over a whole file it would capture statements.
	reInterfaceMember = regexp.MustCompile(
		`(?m)^[ \t]*` + memberAttributes +
			`(?:(?:public|internal|protected|static|abstract|virtual|sealed|new|async|ref|readonly|event)\s+)*` +
			memberType + `\s+` + memberNameAndOpener + `(?:[({;]|=>)`)

	// typeKeywords is the set of words the member pattern can capture from a declaration LINE rather
	// than from a member: `public class Foo` matches it with "class".
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
	if body, isInterface := interfaceBodyFor(typeName, src); isInterface {
		for _, m := range reInterfaceMember.FindAllStringSubmatch(body, -1) {
			addMemberMatch(add, m[1], m[2])
		}
		sort.Strings(out)
		return out
	}
	for _, m := range reDeclaredMember.FindAllStringSubmatch(src, -1) {
		addMemberMatch(add, m[1], m[2])
	}
	sort.Strings(out)
	return out
}

// addMemberMatch records one pattern match, unless what it captured is a TYPE's name rather than a
// member's.
//
// `public class OrderDto {` matches the member pattern: the type slot takes "class" and the name
// slot takes "OrderDto". Offering that to the model as a member of the type beside it is the same
// class of wrong statement this file exists to avoid — the model is invited to call something that
// is not there.
func addMemberMatch(add func(string), typeToken, name string) {
	if typeKeywords[strings.TrimSpace(typeToken)] {
		return
	}
	add(name)
}

// interfaceBodyFor returns the brace-matched body of the interface with exactly this name.
//
// Everything between the name and the opening brace is skipped wholesale — type parameters, the
// base-interface list and a `where` clause can all appear there, in that order or not at all, and
// enumerating them is how the pattern would come to miss one. The `;` in the character class stops
// the scan from running past the declaration if the brace never comes.
func interfaceBodyFor(typeName, strippedSrc string) (string, bool) {
	re := regexp.MustCompile(`\binterface\s+` + regexp.QuoteMeta(typeName) + `\b[^{;]*\{`)
	loc := re.FindStringIndex(strippedSrc)
	if loc == nil {
		return "", false
	}
	open := loc[1] - 1 // the '{' the pattern ends on
	depth := 0
	for i := open; i < len(strippedSrc); i++ {
		switch strippedSrc[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strippedSrc[open+1 : i], true
			}
		}
	}
	// Unbalanced: the rest of the file is the best available answer, and a superset is the safe
	// direction here — it makes an absence claim rarer, never wider.
	return strippedSrc[open+1:], true
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
