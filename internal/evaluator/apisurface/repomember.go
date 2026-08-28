package apisurface

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Generation-time check for members invented on the REPOSITORY'S OWN types.
//
// InventedAssertionMemberReason covers classpath types whose surfaces the prompt carried; nothing
// covered the repo's own classes, and that is where run api-a4678e03289277effe4a01043c1bc3ca lost
// most of its fix budget: the generated tests called owner.setPets(...), vets.setVets(...) and
// vets.getVets() — none of which exist (Owner's pets are managed through addPet, Vets declares
// getVetList) — and every one reached a containerised compile and then ten LLM repair rounds that
// never converged, because repairing them means rewriting test logic, not fixing an import.
//
// The check is bounded by provability, in the same spirit as the assertion-member check:
//
//   - Only calls on locals whose DECLARED type is written in the test itself are attributed
//     (`Owner owner = …`, `var owner = new Owner(…)`). Anything else needs type inference.
//   - The declared type must resolve to repo source deterministically: through an explicit import
//     of a repo-owned fully-qualified name, or as a same-package neighbour. Ambiguity is silence.
//   - The full supertype chain must stay inside the repository. One `extends`/`implements` edge
//     that leaves the repo (a classpath base class, an unresolvable interface that is not a
//     well-known marker) makes the type unprovable: a member could live on the unseen parent.
//   - A source that imports Lombok is unprovable: its annotations synthesize members the source
//     never spells out.
//
// Member truth is collected liberally (every `name(` at class-body brace depth, plus record
// components and enum built-ins), so parsing slop can only ADD allowed names — the failure
// direction is a silent check, never a false rejection.

var (
	repoJavaPackageRE = regexp.MustCompile(`(?m)^\s*package\s+([\w.]+)\s*;`)
	// repoLocalDeclRE matches `TypeName ident =` / `TypeName ident;`. The mandatory whitespace
	// between type and identifier keeps generic (`List<Owner>`), array (`Owner[]`) and qualified
	// (`Owner.Pet`) forms from matching.
	repoLocalDeclRE = regexp.MustCompile(`(?m)(?:^|[\s(])([A-Z]\w*)\s+([a-z_]\w*)\s*(?:=[^=]|;)`)
	// repoVarNewRE matches `var ident = new TypeName(`.
	repoVarNewRE = regexp.MustCompile(`\bvar\s+([a-z_]\w*)\s*=\s*new\s+([A-Z]\w*)\s*[(<]`)
	// repoExtendsRE / repoImplementsRE read the supertype edges of a class/interface declaration.
	repoExtendsRE    = regexp.MustCompile(`\b(?:class|interface)\s+\w+(?:\s*<[^{]*?>)?\s+extends\s+([\w.]+)`)
	repoImplementsRE = regexp.MustCompile(`\bclass\s+\w+(?:\s*<[^{]*?>)?(?:\s+extends\s+[\w.<>,\s]+?)?\s+implements\s+([\w.<>,\s]+?)\s*\{`)
	repoRecordRE     = regexp.MustCompile(`\brecord\s+\w+\s*\(([^)]*)\)`)
	repoEnumRE       = regexp.MustCompile(`\benum\s+\w+`)
	lombokImportRE   = regexp.MustCompile(`(?m)^\s*import\s+lombok\.`)
)

// javaSourceRoots mirrors dropRepoOwnedTargets' roots: where repo-owned Java sources live.
var javaSourceRoots = []string{"src/main/java", "src/test/java", "src/it/java"}

// objectMemberNames are callable on every Java object.
var objectMemberNames = map[string]bool{
	"toString": true, "equals": true, "hashCode": true, "getClass": true,
	"wait": true, "notify": true, "notifyAll": true, "clone": true, "finalize": true,
}

// markerInterfaces carry no members; an implements edge to one never hides a member.
var markerInterfaces = map[string]bool{
	"Serializable": true, "java.io.Serializable": true,
	"Cloneable": true, "java.lang.Cloneable": true,
}

// RepoInventedMemberReason reports EVERY call to a member that provably does not exist on a
// repo-owned type, or "" when nothing can be proven.
//
// All of them, not the first, for the reason UnresolvedDependencyReason gives: there is exactly one
// regeneration retry, so a violation left unnamed is a violation that reaches disk. The ordering is
// also load-bearing — the locals map was ranged over directly, which made "the first violation"
// whichever one Go's randomised map iteration reached first, so two runs on identical content could
// report different findings.
func RepoInventedMemberReason(repoRoot, testContent string) string {
	if strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(testContent) == "" {
		return ""
	}
	stripped := stripJavaStringsAndLineComments(testContent)
	testPkg := ""
	if m := repoJavaPackageRE.FindStringSubmatch(stripped); m != nil {
		testPkg = m[1]
	}
	imports := javaImportsBySimpleName(stripped)

	// ident -> declared simple type name. A name declared twice with different types is dropped:
	// re-attribution would need scope analysis.
	locals := map[string]string{}
	drop := map[string]bool{}
	record := func(ident, typ string) {
		if prev, ok := locals[ident]; ok && prev != typ {
			drop[ident] = true
			return
		}
		locals[ident] = typ
	}
	for _, m := range repoLocalDeclRE.FindAllStringSubmatch(stripped, -1) {
		record(m[2], m[1])
	}
	for _, m := range repoVarNewRE.FindAllStringSubmatch(stripped, -1) {
		record(m[1], m[2])
	}

	idents := make([]string, 0, len(locals))
	for ident := range locals {
		idents = append(idents, ident)
	}
	sort.Strings(idents)

	var reasons []string
	seen := map[string]bool{}
	memberCache := map[string]map[string]bool{} // resolved repo file -> member set; nil value = unprovable
	for _, ident := range idents {
		typ := locals[ident]
		if drop[ident] {
			continue
		}
		// A type the test file declares itself shadows any repo neighbour.
		if regexp.MustCompile(`\b(?:class|interface|record|enum)\s+` + typ + `\b`).MatchString(stripped) {
			continue
		}
		src := resolveRepoTypeSource(repoRoot, testPkg, imports, typ)
		if src == "" {
			continue
		}
		members, ok := memberCache[src], false
		if members == nil {
			members, ok = collectRepoTypeMembers(repoRoot, src, 0)
			if !ok {
				members = nil
			}
			memberCache[src] = members
		}
		if members == nil {
			continue
		}
		callRE := regexp.MustCompile(`(^|[^\w.])` + ident + `\s*\.\s*(\w+)\s*\(`)
		for _, m := range callRE.FindAllStringSubmatch(stripped, -1) {
			name := m[2]
			if members[name] || objectMemberNames[name] {
				continue
			}
			// One member invented on one type is one finding however many call sites it has:
			// repeating it per site would crowd the other violations out of the retry prompt.
			key := typ + "#" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			reasons = append(reasons, "call to "+name+"() on "+typ+" ("+src+"), which declares no such member anywhere in its repository type hierarchy")
		}
	}
	return strings.Join(reasons, "; also ")
}

// resolveRepoTypeSource maps a simple type name to a repo-relative source path, or "" when the
// resolution is not deterministic. Import-declared repo types win; then same-package neighbours.
func resolveRepoTypeSource(repoRoot, pkg string, imports map[string]string, typ string) string {
	if fq, ok := imports[typ]; ok {
		rel := strings.ReplaceAll(fq, ".", "/") + ".java"
		for _, root := range javaSourceRoots {
			if fileExistsUnder(repoRoot, root+"/"+rel) {
				return root + "/" + rel
			}
		}
		return "" // imported from outside the repo: not ours to judge.
	}
	if pkg == "" {
		return ""
	}
	rel := strings.ReplaceAll(pkg, ".", "/") + "/" + typ + ".java"
	for _, root := range javaSourceRoots {
		if fileExistsUnder(repoRoot, root+"/"+rel) {
			return root + "/" + rel
		}
	}
	return ""
}

// collectRepoTypeMembers gathers every member name reachable on a repo type, walking extends and
// implements edges. ok=false means the hierarchy is unprovable (an edge leaves the repo, Lombok is
// in play, or the chain is unreasonably deep) and the caller must stay silent.
func collectRepoTypeMembers(repoRoot, rel string, depth int) (map[string]bool, bool) {
	if depth > 8 {
		return nil, false
	}
	body, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		return nil, false
	}
	src := stripJavaStringsAndLineComments(string(body))
	if lombokImportRE.MatchString(src) {
		return nil, false // annotations synthesize members the source never declares.
	}
	members := classBodyMemberNames(src)
	if m := repoRecordRE.FindStringSubmatch(src); m != nil {
		for _, comp := range strings.Split(m[1], ",") {
			fields := strings.Fields(strings.TrimSpace(comp))
			if len(fields) >= 2 {
				members[fields[len(fields)-1]] = true
			}
		}
	}
	if repoEnumRE.MatchString(src) {
		for _, n := range []string{"name", "ordinal", "values", "valueOf", "compareTo"} {
			members[n] = true
		}
	}
	pkg := ""
	if m := repoJavaPackageRE.FindStringSubmatch(src); m != nil {
		pkg = m[1]
	}
	imports := javaImportsBySimpleName(src)
	var supers []string
	if m := repoExtendsRE.FindStringSubmatch(src); m != nil {
		supers = append(supers, m[1])
	}
	if m := repoImplementsRE.FindStringSubmatch(src); m != nil {
		for _, iface := range strings.Split(m[1], ",") {
			iface = strings.TrimSpace(iface)
			if i := strings.Index(iface, "<"); i >= 0 {
				iface = strings.TrimSpace(iface[:i])
			}
			if iface != "" {
				supers = append(supers, iface)
			}
		}
	}
	for _, super := range supers {
		if markerInterfaces[super] {
			continue
		}
		simple := super[strings.LastIndex(super, ".")+1:]
		parentRel := resolveRepoTypeSource(repoRoot, pkg, imports, simple)
		if parentRel == "" {
			return nil, false // the chain leaves the repo; a member could live on the unseen parent.
		}
		parentMembers, ok := collectRepoTypeMembers(repoRoot, parentRel, depth+1)
		if !ok {
			return nil, false
		}
		for n := range parentMembers {
			members[n] = true
		}
	}
	return members, true
}

// classBodyMemberNames collects `name(` tokens found at class-body brace depth (methods and
// constructors; interface members end with `;` and are found the same way). Statements inside
// method bodies sit one level deeper and are skipped, so a call site cannot masquerade as a
// declaration — and any slop here only widens the allowed set.
func classBodyMemberNames(src string) map[string]bool {
	out := map[string]bool{}
	depth := 0
	nameRE := regexp.MustCompile(`([A-Za-z_]\w*)\s*\($`)
	token := strings.Builder{}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch c {
		case '{':
			depth++
			token.Reset()
		case '}':
			depth--
			token.Reset()
		case '(':
			if depth == 1 {
				if m := nameRE.FindStringSubmatch(strings.TrimRight(token.String(), " \t") + "("); m != nil {
					out[m[1]] = true
				}
			}
			token.Reset()
		case '\n', ';':
			token.Reset()
		default:
			token.WriteByte(c)
		}
	}
	return out
}

// stripJavaStringsAndLineComments blanks string/char literals and comments so their contents can
// neither declare members nor look like calls. Replacement preserves byte offsets with spaces.
func stripJavaStringsAndLineComments(src string) string {
	b := []byte(src)
	i := 0
	for i < len(b) {
		switch {
		case b[i] == '"' || b[i] == '\'':
			quote := b[i]
			i++
			for i < len(b) && b[i] != quote {
				if b[i] == '\\' {
					b[i] = ' '
					i++
					if i < len(b) {
						b[i] = ' '
					}
				} else {
					if b[i] != '\n' {
						b[i] = ' '
					}
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
			i += 2
		default:
			i++
		}
	}
	return string(b)
}

func fileExistsUnder(repoRoot, rel string) bool {
	st, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	return err == nil && !st.IsDir()
}
