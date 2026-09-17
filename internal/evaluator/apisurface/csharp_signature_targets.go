package apisurface

import (
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// csharpSignatureTypeRE captures the identifiers that could be type names in a signature: an
// initial capital, which is the C# convention for every type that is not a keyword.
var csharpSignatureTypeRE = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\b`)

// csharpSignatureTargets resolves the third-party types named in a C# member's signature to
// fully-qualified names the API surface can look up.
//
// Java asks the file's IMPORT block, because an import names a TYPE: `import a.b.C;` maps `C` to
// `a.b.C` and there is nothing to guess. C# has no such mapping — a using names a NAMESPACE, so
// `using Microsoft.EntityFrameworkCore;` plus `DbContextOptions` COULD be
// `Microsoft.EntityFrameworkCore.DbContextOptions` and could equally be a type from any other
// namespace the file imports.
//
// So a name is emitted only when exactly ONE imported namespace could supply it. Two candidates
// means silence: the whole point of resolving a name is to state its namespace authoritatively, and
// a coin flip would instruct the generator to import a type that does not exist. In practice a test
// file imports a handful of namespaces and the ambiguity is rare, which is why the rule pays.
//
// Namespaces the repository declares itself are skipped: retrieval already ships repo source, so a
// member dump for one is duplicated prompt budget — the same reason dropRepoOwnedTargets exists on
// the Java path.
func csharpSignatureTargets(signature, source string) []Target {
	signature = strings.TrimSpace(signature)
	if signature == "" || strings.TrimSpace(source) == "" {
		return nil
	}
	stripped := dotnetproj.StripCSharpCommentsAndStrings(source)
	namespaces := csharpImportedNamespaces(stripped)
	if len(namespaces) == 0 {
		return nil
	}
	declaredHere := map[string]bool{}
	for _, re := range []*regexp.Regexp{reCSharpTypeDeclaration, reCSharpDelegateDeclaration} {
		for _, m := range re.FindAllStringSubmatch(stripped, -1) {
			declaredHere[strings.TrimSpace(m[1])] = true
		}
	}
	// The file's own namespace is the repository's, and so is anything under it.
	var ownRoots []string
	for _, m := range reCSharpNamespaceDeclaration.FindAllStringSubmatch(stripped, -1) {
		if ns := strings.TrimSpace(m[1]); ns != "" {
			ownRoots = append(ownRoots, csharpNamespaceRoot(ns))
		}
	}

	// The member's own name is capitalised too — `public List<string> Names(int count)` — and it is
	// the one capitalised word in a signature that is certainly NOT a type.
	memberName := csharpSignatureMemberName(signature)

	var out []Target
	seen := map[string]bool{}
	for _, m := range csharpSignatureTypeRE.FindAllStringSubmatch(signature, -1) {
		simple := m[1]
		switch {
		case simple == memberName, declaredHere[simple], csharpSignatureNoiseNames[simple]:
			continue
		}
		var candidates []string
		for _, ns := range namespaces {
			if csharpNamespaceIsOwn(ns, ownRoots) {
				continue
			}
			candidates = append(candidates, ns+"."+simple)
		}
		if len(candidates) != 1 {
			// Absent, or ambiguous between two imported namespaces: say nothing rather than pick.
			continue
		}
		fq := candidates[0]
		if seen[fq] || IsUninterestingType(fq) {
			continue
		}
		seen[fq] = true
		out = append(out, Target{Kind: KindType, Name: fq})
		if len(out) >= maxSignatureTargets {
			break
		}
	}
	return out
}

// csharpSignatureMemberName returns the identifier immediately before the parameter list, which in
// every C# member declaration is the member's own name.
func csharpSignatureMemberName(signature string) string {
	open := strings.IndexByte(signature, '(')
	if open <= 0 {
		return ""
	}
	head := strings.TrimRight(signature[:open], " \t")
	// A generic member carries its type parameters between the name and the list: `Get<T>(…)`.
	if strings.HasSuffix(head, ">") {
		if lt := strings.LastIndexByte(head, '<'); lt > 0 {
			head = strings.TrimRight(head[:lt], " \t")
		}
	}
	i := len(head)
	for i > 0 && (isCSharpIdentPart(head[i-1])) {
		i--
	}
	return head[i:]
}

func isCSharpIdentPart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func csharpNamespaceIsOwn(ns string, ownRoots []string) bool {
	root := csharpNamespaceRoot(ns)
	for _, own := range ownRoots {
		if root == own {
			return true
		}
	}
	return false
}

// csharpImportedNamespaces lists the plain namespace usings a file brings into scope, excluding the
// System namespaces whose members every model already knows and whose dumps are enormous.
func csharpImportedNamespaces(stripped string) []string {
	var out []string
	seen := map[string]bool{}
	for ns := range csharpUsingNamespaces(stripped) {
		if ns == "System" || strings.HasPrefix(ns, "System.") {
			continue
		}
		if seen[ns] {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	return out
}

// csharpSignatureNoiseNames are the words a signature scan picks up that are not third-party types:
// C# contextual keywords in their capitalised forms, the containers every model knows, and the
// modifiers that appear before a return type.
var csharpSignatureNoiseNames = map[string]bool{
	"Task": true, "ValueTask": true, "List": true, "IList": true, "IEnumerable": true,
	"ICollection": true, "IReadOnlyList": true, "IReadOnlyCollection": true, "Dictionary": true,
	"IDictionary": true, "IReadOnlyDictionary": true, "HashSet": true, "ISet": true, "Array": true,
	"String": true, "Int32": true, "Int64": true, "Boolean": true, "Double": true, "Decimal": true,
	"Object": true, "Guid": true, "DateTime": true, "DateTimeOffset": true, "TimeSpan": true,
	"CancellationToken": true, "Exception": true, "Type": true, "Nullable": true, "Func": true,
	"Action": true, "Span": true, "Memory": true, "Tuple": true, "KeyValuePair": true,
}
