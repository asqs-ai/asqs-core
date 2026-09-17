package apisurface

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Generation-time check for members invented on the REPOSITORY'S OWN TypeScript/JavaScript classes.
//
// The TypeScript counterpart of RepoInventedMemberReason. Run api-7425be21b83f608e66318fdd99528522
// generated a controller test that spelled OrdersService.createOrder as `create` three ways: a
// typed call and a jest.spyOn (which the post-generate tsc gate caught, one repair round later) and
// a provider mock `{ provide: OrdersService, useValue: { create: jest.fn() } }` — which nothing
// caught, because Nest types `useValue` as any, and which failed at run time with "createOrder is
// not a function" one fixer round after that. A mock shaped against the class's real members is
// what this check enforces before the file is written.
//
// Bounded by provability, like the Java check:
//
//   - A type is judged only when the test resolves it to a repository source through a RELATIVE
//     import (`from './orders.service'`, `require('../cart')`); a package import is not ours.
//   - A local is attributed only from evidence written in the test: a type annotation (including
//     jest.Mocked<X> / Partial<X>), `new X(`, `module.get<X>(` / `module.get(X)` / `.resolve(X)`,
//     or an `as X` / `as unknown as X` cast on its initializer.
//   - The class and its `extends` chain must stay in the repository and be readable as plain
//     class syntax. A package base class, a mixin call in `extends`, or an index signature makes
//     the type unprovable and the check silent.
//   - Member truth is collected liberally (every declaration name at class-body depth, constructor
//     parameter properties, accessors, inherited members), so parsing slop only widens the allowed
//     set.

// tsRelativeImportRE matches ESM import bindings from a relative specifier. Group 1 is the
// binding clause, group 2 the specifier.
var (
	tsImportRE  = regexp.MustCompile(`(?m)^\s*import\s+(?:type\s+)?([^'";]+?)\s+from\s+['"]([^'"]+)['"]`)
	tsRequireRE = regexp.MustCompile(`(?m)(?:const|let|var)\s+(\{[^}]*\}|\w+)\s*=\s*require\s*\(\s*['"]([^'"]+)['"]\s*\)`)

	tsTypeWrapperRE = `(?:jest\.Mocked|jest\.MockedObject|Partial|Required|Readonly|DeepMocked|MockProxy|MockedObject)`
	// Local attribution.
	tsAnnotatedLocalRE = regexp.MustCompile(`\b(?:let|const|var)\s+(\w+)\s*:\s*(?:` + tsTypeWrapperRE + `\s*<\s*)?([A-Z]\w*)\b`)
	tsNewLocalRE       = regexp.MustCompile(`\b(\w+)\s*(?::[^=;]*)?=\s*(?:await\s+)?new\s+([A-Z]\w*)\s*[(<]`)
	tsDIGetLocalRE     = regexp.MustCompile(`\b(\w+)\s*(?::[^=;]*)?=\s*(?:await\s+)?[\w.]+\.(?:get|resolve|create)\s*(?:<\s*([A-Z]\w*)\s*>)?\s*\(\s*([A-Z]\w*)?\s*[),]`)
	tsCastLocalRE      = regexp.MustCompile(`(?s)\b(\w+)\s*(?::[^=;]*)?=[^;]*?\bas\s+(?:unknown\s+as\s+)?(?:` + tsTypeWrapperRE + `\s*<\s*)?([A-Z]\w*)\s*>?\s*;`)
	// Mock literal sources.
	tsProvideUseValueRE = regexp.MustCompile(`\bprovide\s*:\s*([A-Z]\w*)\s*,\s*useValue\s*:\s*`)
	tsLiteralCastRE     = regexp.MustCompile(`\}\s*as\s+(?:unknown\s+as\s+)?(?:` + tsTypeWrapperRE + `\s*<\s*)?([A-Z]\w*)`)
	tsWrappedAnnotRE    = regexp.MustCompile(`:\s*` + tsTypeWrapperRE + `\s*<\s*([A-Z]\w*)\s*>\s*=\s*\{`)
	tsSpyOnRE           = regexp.MustCompile(`\bspyOn\s*\(\s*([\w.]+)\s*,\s*['"](\w+)['"]`)
	// Class parsing.
	tsClassDeclRE = regexp.MustCompile(`\b(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+(\w+)\b([^{;]*)\{`)
	tsExtendsRE   = regexp.MustCompile(`\bextends\s+([\w.]+)\s*(\(|<|\{|implements|$)`)
	tsMemberModRE = regexp.MustCompile(`^(?:public|private|protected|static|readonly|async|abstract|override|declare|accessor|get|set)\s+`)
	tsParamPropRE = regexp.MustCompile(`\b(?:public|private|protected|readonly)\s+(?:readonly\s+)?(\w+)`)
	tsObjectKeyRE = regexp.MustCompile(`^\s*(?:async\s+)?(?:get\s+|set\s+)?(?:\*\s*)?(?:(['"])(\w+)['"]|(\w+))\s*(?:[:(,?}]|$)`)
)

var tsSourceExts = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

// jsObjectMemberNames are callable or readable on every object.
var jsObjectMemberNames = map[string]bool{
	"constructor": true, "toString": true, "toLocaleString": true, "valueOf": true,
	"hasOwnProperty": true, "isPrototypeOf": true, "propertyIsEnumerable": true, "prototype": true,
}

// tsImportBinding is one local name bound by an import: the specifier it came from and the name
// exported there (which differs from the local under `as`).
type tsImportBinding struct {
	specifier string
	exported  string
}

// RepoInventedMemberReasonTS reports EVERY member access, spy name and mock key on a repo-owned
// class that provably does not exist on it, or "" when nothing can be proven. testRel is the test
// file's repo-relative path: relative imports resolve from its directory, and for an extend payload
// (which carries no imports) the target file's own imports are read from disk.
func RepoInventedMemberReasonTS(repoRoot, testRel, testContent string) string {
	if strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(testRel) == "" || strings.TrimSpace(testContent) == "" {
		return ""
	}
	testRel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(testRel)))
	noComments := stripJSComments(testContent)
	stripped := stripJSStrings(noComments)

	imports := tsImportBindings(noComments)
	if b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(testRel))); err == nil {
		for name, bind := range tsImportBindings(stripJSComments(string(b))) {
			if _, own := imports[name]; !own {
				imports[name] = bind
			}
		}
	}
	testDir := filepath.ToSlash(filepath.Dir(testRel))

	// typeName -> resolved repo file, "" when not ours or not resolvable. Types the test declares
	// itself shadow any import.
	resolveType := func(name string) string {
		if regexp.MustCompile(`\b(?:class|interface|type|enum)\s+` + name + `\b`).MatchString(stripped) {
			return ""
		}
		bind, ok := imports[name]
		if !ok {
			return ""
		}
		return resolveTSRelativeImport(repoRoot, testDir, bind.specifier)
	}
	memberCache := map[string]*tsClassMembers{}
	membersFor := func(typeName string) (*tsClassMembers, string) {
		src := resolveType(typeName)
		if src == "" {
			return nil, ""
		}
		exported := typeName
		if bind, ok := imports[typeName]; ok && bind.exported != "" {
			exported = bind.exported
		}
		key := src + "#" + exported
		if m, ok := memberCache[key]; ok {
			return m, src
		}
		m, ok := collectTSClassMembers(repoRoot, src, exported, 0)
		if !ok {
			m = nil
		}
		memberCache[key] = m
		return m, src
	}

	// Local attribution: ident -> type name; a local seen with two types is dropped.
	locals := map[string]string{}
	drop := map[string]bool{}
	record := func(ident, typ string) {
		if ident == "" || typ == "" {
			return
		}
		if prev, ok := locals[ident]; ok && prev != typ {
			drop[ident] = true
			return
		}
		locals[ident] = typ
	}
	for _, m := range tsAnnotatedLocalRE.FindAllStringSubmatch(stripped, -1) {
		record(m[1], m[2])
	}
	for _, m := range tsNewLocalRE.FindAllStringSubmatch(stripped, -1) {
		record(m[1], m[2])
	}
	for _, m := range tsDIGetLocalRE.FindAllStringSubmatch(stripped, -1) {
		typ := m[2]
		if typ == "" {
			typ = m[3]
		}
		record(m[1], typ)
	}
	for _, m := range tsCastLocalRE.FindAllStringSubmatch(stripped, -1) {
		record(m[1], m[2])
	}

	type finding struct{ typ, src, name, how string }
	var findings []finding
	seen := map[string]bool{}
	add := func(typ, src, name, how string) {
		key := typ + "#" + name + "#" + how
		if seen[key] {
			return
		}
		seen[key] = true
		findings = append(findings, finding{typ, src, name, how})
	}
	declared := map[string]*tsClassMembers{}

	// 1. Member accesses on attributed locals.
	idents := make([]string, 0, len(locals))
	for ident := range locals {
		if !drop[ident] {
			idents = append(idents, ident)
		}
	}
	sort.Strings(idents)
	for _, ident := range idents {
		typ := locals[ident]
		members, src := membersFor(typ)
		if members == nil {
			continue
		}
		declared[typ] = members
		accessRE := regexp.MustCompile(`(^|[^\w.$])` + regexp.QuoteMeta(ident) + `\s*\.\s*(\w+)`)
		for _, m := range accessRE.FindAllStringSubmatch(stripped, -1) {
			name := m[2]
			if members.names[name] || jsObjectMemberNames[name] {
				continue
			}
			add(typ, src, name, "access")
		}
	}

	// 2. jest.spyOn(local, 'name') and jest.spyOn(Class.prototype, 'name').
	for _, m := range tsSpyOnRE.FindAllStringSubmatch(noComments, -1) {
		receiver, name := m[1], m[2]
		typ := ""
		if strings.HasSuffix(receiver, ".prototype") {
			typ = strings.TrimSuffix(receiver, ".prototype")
		} else if t, ok := locals[receiver]; ok && !drop[receiver] {
			typ = t
		}
		if typ == "" {
			continue
		}
		members, src := membersFor(typ)
		if members == nil || members.names[name] || jsObjectMemberNames[name] {
			continue
		}
		declared[typ] = members
		add(typ, src, name, "spy")
	}

	// 3. Mock object literals shaped against a class.
	checkLiteral := func(typ string, start int) {
		keys, ok := tsObjectLiteralKeys(noComments, start)
		if !ok {
			return
		}
		members, src := membersFor(typ)
		if members == nil {
			return
		}
		declared[typ] = members
		for _, k := range keys {
			if members.names[k] || jsObjectMemberNames[k] {
				continue
			}
			add(typ, src, k, "mock")
		}
	}
	literalAfter := func(pos int) int {
		// pos points just after a `useValue:`-style prefix; a literal starts with `{`, an
		// identifier names a `const X = {` declaration elsewhere in the file.
		rest := noComments[pos:]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		off := pos + (len(rest) - len(trimmed))
		if strings.HasPrefix(trimmed, "{") {
			return off
		}
		if m := regexp.MustCompile(`^(\w+)`).FindStringSubmatch(trimmed); m != nil {
			declRE := regexp.MustCompile(`\b(?:const|let|var)\s+` + regexp.QuoteMeta(m[1]) + `\s*(?::[^=;]*)?=\s*\{`)
			if loc := declRE.FindStringIndex(noComments); loc != nil {
				return loc[1] - 1
			}
		}
		return -1
	}
	for _, m := range tsProvideUseValueRE.FindAllStringSubmatchIndex(noComments, -1) {
		typ := noComments[m[2]:m[3]]
		if start := literalAfter(m[1]); start >= 0 {
			checkLiteral(typ, start)
		}
	}
	for _, m := range tsLiteralCastRE.FindAllStringSubmatchIndex(noComments, -1) {
		typ := noComments[m[2]:m[3]]
		if start := openingBraceBefore(noComments, m[0]); start >= 0 {
			checkLiteral(typ, start)
		}
	}
	for _, m := range tsWrappedAnnotRE.FindAllStringSubmatchIndex(noComments, -1) {
		typ := noComments[m[2]:m[3]]
		checkLiteral(typ, m[1]-1)
	}

	if len(findings) == 0 {
		return ""
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].typ != findings[j].typ {
			return findings[i].typ < findings[j].typ
		}
		return findings[i].name < findings[j].name
	})
	var parts []string
	listed := map[string]bool{}
	for _, f := range findings {
		var s string
		switch f.how {
		case "spy":
			s = "spy on " + f.name + " of " + f.typ + " (" + f.src + "), which declares no such member anywhere in its repository type hierarchy"
		case "mock":
			s = "mock for " + f.typ + " (" + f.src + ") defines " + f.name + ", which " + f.typ + " declares nowhere in its repository type hierarchy — the code under test will call the real member name and fail at run time"
		default:
			s = "access to " + f.name + " on " + f.typ + " (" + f.src + "), which declares no such member anywhere in its repository type hierarchy"
		}
		if !listed[f.typ] {
			listed[f.typ] = true
			if members := declared[f.typ]; members != nil && len(members.ordered) > 0 {
				s += "; " + f.typ + " declares: " + strings.Join(members.ordered, ", ")
			}
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; also ")
}

// tsImportBindings maps each local name an import or require binds to where it came from.
func tsImportBindings(src string) map[string]tsImportBinding {
	out := map[string]tsImportBinding{}
	bindClause := func(clause, spec string) {
		clause = strings.TrimSpace(clause)
		// Namespace imports bind a namespace, not a class; members are reached as ns.X and are
		// not attributed here.
		if strings.HasPrefix(clause, "*") {
			return
		}
		named := ""
		if i := strings.Index(clause, "{"); i >= 0 {
			if j := strings.Index(clause[i:], "}"); j >= 0 {
				named = clause[i+1 : i+j]
				clause = strings.TrimSpace(clause[:i] + clause[i+j+1:])
			}
		}
		for _, part := range strings.Split(named, ",") {
			part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "type "))
			if part == "" {
				continue
			}
			exported, local := part, part
			if fields := strings.Fields(part); len(fields) == 3 && fields[1] == "as" {
				exported, local = fields[0], fields[2]
			} else if fields := strings.Fields(strings.ReplaceAll(part, ":", " ")); len(fields) == 2 {
				exported, local = fields[0], fields[1] // require destructuring `{ A: B }`
			}
			out[local] = tsImportBinding{specifier: spec, exported: exported}
		}
		// A default import (or a bare require binding) names whatever the module's default is; it
		// is looked up by the local name and falls back to the file's only class.
		clause = strings.Trim(clause, ", \t")
		if clause != "" && regexp.MustCompile(`^\w+$`).MatchString(clause) {
			out[clause] = tsImportBinding{specifier: spec, exported: ""}
		}
	}
	for _, m := range tsImportRE.FindAllStringSubmatch(src, -1) {
		bindClause(m[1], m[2])
	}
	for _, m := range tsRequireRE.FindAllStringSubmatch(src, -1) {
		bindClause(m[1], m[2])
	}
	return out
}

// resolveTSRelativeImport maps a relative specifier to an existing repo-relative source file, or ""
// when the specifier is not relative or resolves to nothing.
func resolveTSRelativeImport(repoRoot, fromDir, spec string) string {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return ""
	}
	base := filepath.ToSlash(filepath.Clean(filepath.Join(fromDir, spec)))
	if strings.HasPrefix(base, "../") || strings.Contains(base, "node_modules") {
		return ""
	}
	candidates := []string{base}
	ext := filepath.Ext(base)
	hasSourceExt := false
	for _, e := range tsSourceExts {
		if ext == e {
			hasSourceExt = true
		}
	}
	if !hasSourceExt {
		// `./x.js` in an ESM TypeScript project names ./x.ts on disk.
		if ext == ".js" || ext == ".mjs" || ext == ".cjs" {
			stem := strings.TrimSuffix(base, ext)
			candidates = append(candidates, stem+".ts", stem+".tsx", stem+".mts", stem+".cts")
		}
		for _, e := range tsSourceExts {
			candidates = append(candidates, base+e)
		}
		for _, e := range tsSourceExts {
			candidates = append(candidates, base+"/index"+e)
		}
	} else if ext == ".js" || ext == ".mjs" || ext == ".cjs" {
		stem := strings.TrimSuffix(base, ext)
		candidates = append(candidates, stem+".ts", stem+".tsx", stem+".mts", stem+".cts")
	}
	for _, c := range candidates {
		if fileExistsUnder(repoRoot, c) {
			return c
		}
	}
	return ""
}

// tsClassMembers is the member set of a class including everything inherited inside the repo.
type tsClassMembers struct {
	names   map[string]bool
	ordered []string // the class's OWN declarations in source order, for the retry prompt
}

// collectTSClassMembers reads the class named `name` (or the file's only class when name is empty
// or absent, which is how a default import resolves) and gathers its members plus those of every
// base class reachable through relative imports. ok=false means unprovable.
func collectTSClassMembers(repoRoot, rel, name string, depth int) (*tsClassMembers, bool) {
	if depth > 8 {
		return nil, false
	}
	body, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		return nil, false
	}
	src := stripJSStrings(stripJSComments(string(body)))
	decls := tsClassDeclRE.FindAllStringSubmatchIndex(src, -1)
	var chosen []int
	for _, d := range decls {
		if name == "" || src[d[2]:d[3]] == name {
			chosen = d
			break
		}
	}
	if chosen == nil {
		if name != "" && len(decls) == 1 {
			chosen = decls[0] // `import X from` names the module's default export
		} else {
			return nil, false
		}
	}
	header := src[chosen[4]:chosen[5]]
	open := chosen[1] - 1
	end := matchingBrace(src, open)
	if end < 0 {
		return nil, false
	}
	out := &tsClassMembers{names: map[string]bool{}}
	if !tsClassBodyMembers(src[open+1:end], out) {
		return nil, false
	}
	if m := tsExtendsRE.FindStringSubmatch(header); m != nil {
		if m[2] == "(" {
			return nil, false // mixin: extends Mixin(A, B)
		}
		base := m[1]
		if strings.Contains(base, ".") {
			return nil, false // namespace-qualified base: not resolvable here
		}
		var parent *tsClassMembers
		ok := false
		if regexp.MustCompile(`\bclass\s+` + base + `\b`).MatchString(src) {
			parent, ok = collectTSClassMembers(repoRoot, rel, base, depth+1)
		} else {
			bind, has := tsImportBindings(stripJSComments(string(body)))[base]
			if !has {
				return nil, false
			}
			parentRel := resolveTSRelativeImport(repoRoot, filepath.ToSlash(filepath.Dir(rel)), bind.specifier)
			if parentRel == "" {
				return nil, false // a package base class: a member could live on the unseen parent.
			}
			parent, ok = collectTSClassMembers(repoRoot, parentRel, bind.exported, depth+1)
		}
		if !ok {
			return nil, false
		}
		for n := range parent.names {
			out.names[n] = true
		}
	}
	return out, true
}

// tsClassBodyMembers collects declaration names at class-body depth into out. False when the body
// carries an index signature, which admits any member.
func tsClassBodyMembers(body string, out *tsClassMembers) bool {
	add := func(n string) {
		if n == "" || strings.HasPrefix(n, "#") {
			return
		}
		if !out.names[n] {
			out.names[n] = true
			out.ordered = append(out.ordered, n)
		}
	}
	depth, paren := 0, 0
	start := 0 // start of the current depth-0 statement
	flush := func(stmt string) bool {
		stmt = strings.TrimSpace(stmt)
		for stmt != "" {
			switch {
			case strings.HasPrefix(stmt, "@"):
				// Decorator: skip its name and balanced arguments.
				i := 1
				for i < len(stmt) && (isWordByte(stmt[i]) || stmt[i] == '.') {
					i++
				}
				if i < len(stmt) && stmt[i] == '(' {
					if j, ok := skipBalancedArgs(stmt, i); ok {
						i = j
					} else {
						return true
					}
				}
				stmt = strings.TrimSpace(stmt[i:])
				continue
			case tsMemberModRE.MatchString(stmt):
				stmt = strings.TrimSpace(tsMemberModRE.ReplaceAllString(stmt, ""))
				continue
			case strings.HasPrefix(stmt, "*"):
				stmt = strings.TrimSpace(stmt[1:])
				continue
			}
			break
		}
		if stmt == "" {
			return true
		}
		if stmt[0] == '[' {
			return false // index signature `[key: string]: any`
		}
		i := 0
		for i < len(stmt) && (isWordByte(stmt[i]) || stmt[i] == '#' || stmt[i] == '$') {
			i++
		}
		name := stmt[:i]
		if name == "constructor" {
			if j := strings.Index(stmt, "("); j >= 0 {
				if k, ok := skipBalancedArgs(stmt, j); ok {
					for _, m := range tsParamPropRE.FindAllStringSubmatch(stmt[j:k], -1) {
						add(m[1])
					}
				}
			}
			return true
		}
		add(name)
		return true
	}
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		case '{':
			if paren == 0 {
				depth++
			}
		case '}':
			if paren == 0 {
				depth--
				if depth == 0 {
					if !flush(body[start : i+1]) {
						return false
					}
					start = i + 1
				}
			}
		case ';', '\n':
			if depth == 0 && paren == 0 {
				if !flush(body[start:i]) {
					return false
				}
				start = i + 1
			}
		}
	}
	return flush(body[start:])
}

// tsObjectLiteralKeys returns the keys of the object literal opening at src[start] == '{'. ok is
// false when the literal cannot be read or carries a spread or computed key, which admits members
// the text does not spell out.
func tsObjectLiteralKeys(src string, start int) ([]string, bool) {
	if start < 0 || start >= len(src) || src[start] != '{' {
		return nil, false
	}
	end := matchingBrace(src, start)
	if end < 0 {
		return nil, false
	}
	body := src[start+1 : end]
	var keys []string
	depth := 0
	seg := 0
	entries := []string{}
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
		case ',':
			if depth == 0 {
				entries = append(entries, body[seg:i])
				seg = i + 1
			}
		}
	}
	entries = append(entries, body[seg:])
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.HasPrefix(e, "...") || strings.HasPrefix(e, "[") {
			return nil, false
		}
		m := tsObjectKeyRE.FindStringSubmatch(e)
		if m == nil {
			return nil, false
		}
		k := m[2]
		if k == "" {
			k = m[3]
		}
		keys = append(keys, k)
	}
	return keys, true
}

// openingBraceBefore finds the `{` that the `}` at or before pos closes, scanning backwards over
// balanced braces. Returns -1 when the text does not balance.
func openingBraceBefore(src string, pos int) int {
	i := pos
	for i >= 0 && src[i] != '}' {
		i--
	}
	depth := 0
	for ; i >= 0; i-- {
		switch src[i] {
		case '}':
			depth++
		case '{':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// matchingBrace returns the index of the `}` closing the `{` at open, or -1.
func matchingBrace(src string, open int) int {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// stripJSComments blanks // and /* */ comments, preserving offsets and newlines. String contents
// are left intact because spy names and object keys may be quoted.
func stripJSComments(src string) string {
	b := []byte(src)
	i := 0
	for i < len(b) {
		switch {
		case b[i] == '"' || b[i] == '\'' || b[i] == '`':
			quote := b[i]
			i++
			for i < len(b) && b[i] != quote {
				if b[i] == '\\' {
					i++
				}
				i++
			}
			i++
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				b[i] = ' '
				i++
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				if b[i] != '\n' {
					b[i] = ' '
				}
				i++
			}
			if i+1 < len(b) {
				b[i], b[i+1] = ' ', ' '
			}
			i += 2
		default:
			i++
		}
	}
	return string(b)
}

// stripJSStrings blanks the contents of string and template literals (quotes kept), so a member
// name inside a message cannot look like an access.
func stripJSStrings(src string) string {
	b := []byte(src)
	i := 0
	for i < len(b) {
		if b[i] == '"' || b[i] == '\'' || b[i] == '`' {
			quote := b[i]
			i++
			for i < len(b) && b[i] != quote {
				if b[i] == '\\' {
					b[i] = ' '
					i++
					if i < len(b) {
						b[i] = ' '
					}
				} else if b[i] != '\n' {
					b[i] = ' '
				}
				i++
			}
			i++
			continue
		}
		i++
	}
	return string(b)
}
