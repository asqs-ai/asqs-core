package fixslice

import "strings"

// sliceTypeScript returns a signature-only rendering of a .ts / .tsx / .js / .jsx source. TS is
// more varied than Java/C# (top-level functions, arrow functions, export statements, type
// aliases, interface bodies without method bodies). We take a conservative approach:
//
//   - Inside a class body, emit method signatures with elidedBody and skip the body.
//   - At module scope, keep `import`, `export`, `type`, `interface`, `const`/`let`/`var` decls,
//     and `function` signatures (with elided body); skip the bodies of top-level `function`
//     declarations.
//   - Arrow-function assignments (`const foo = (...) => { … }`) at module scope are emitted in
//     full because rewriting them safely requires lexer-level work we don't do here.
func sliceTypeScript(body string) string {
	s := newScanner(body, tsPolicy{})
	if !s.run() {
		return body
	}
	return s.out.String()
}

type tsPolicy struct{}

func (tsPolicy) isClassLikeHeader(trimmed string) bool { return isTSClassLikeHeader(trimmed) }
func (tsPolicy) looksLikeMethod(trimmed string) bool   { return looksLikeTSMethodLine(trimmed) }

func isTSClassLikeHeader(trimmed string) bool {
	head := trimmed
	if i := strings.Index(head, "{"); i >= 0 {
		head = head[:i]
	}
	head = " " + strings.TrimSpace(head) + " "
	for _, k := range []string{" class ", " interface ", " enum ", " namespace ", " module "} {
		if strings.Contains(head, k) {
			return true
		}
	}
	return false
}

// looksLikeTSMethodLine matches both class methods (`foo(x: T): U { … }`) and top-level `function`
// declarations (`function foo(...): U { … }`). Arrow-function assignments are intentionally NOT
// treated as methods — collapsing them is error prone and low value.
func looksLikeTSMethodLine(trimmed string) bool {
	open := strings.Index(trimmed, "(")
	if open <= 0 {
		return false
	}
	// We need a matching `)` on this line (or detect a multi-line signature opener).
	close := strings.LastIndex(trimmed, ")")
	if close < open {
		// multi-line signature opener.
		if strings.HasSuffix(trimmed, ",") || strings.HasSuffix(trimmed, "(") {
			return true
		}
		return false
	}
	tail := strings.TrimSpace(trimmed[close+1:])
	endsLikeDecl := strings.HasSuffix(tail, "{") || tail == "{" || strings.HasSuffix(tail, ";")
	if !endsLikeDecl && !(tail != "" && strings.Contains(tail, "{")) {
		return false
	}
	prefix := strings.TrimSpace(trimmed[:open])
	if prefix == "" {
		return false
	}
	// Strip leading method modifiers so we can test the final identifier.
	tokens := strings.Fields(prefix)
	modifierSet := map[string]bool{
		"public": true, "private": true, "protected": true, "static": true, "readonly": true,
		"override": true, "async": true, "export": true, "function": true, "abstract": true,
		"*": true, "get": true, "set": true,
	}
	name := tokens[len(tokens)-1]
	for _, tok := range tokens[:len(tokens)-1] {
		if !modifierSet[tok] {
			// Anything unexpected in the prefix disqualifies (e.g. a type annotation expression).
			return false
		}
	}
	if name == "" {
		return false
	}
	// Require the name to be a plain identifier (letters/digits/_/$) — reject expressions like
	// `this.owners.find` that happen to precede a `(`.
	for _, r := range name {
		if !(r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
