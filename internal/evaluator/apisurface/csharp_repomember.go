package apisurface

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// RepoInventedMemberReasonCS refuses a generated C# test that calls a member the repository's own
// type does not declare.
//
// This is the generation-time twin of the fixer's missing-member facts: the same claim, made before
// the file reaches disk instead of after the compiler rejects it. Java and TypeScript have refused
// this since the Mockito work; C# had no arm, so a test calling svc.CalculateTotal() on a type whose
// only method is Total() was written out and spent a fix round being discovered.
//
// The claim is "this type declares no such member", which is a negative, so every path that cannot
// prove it returns "". Four things make it unprovable, and all four are ordinary C#:
//
//   - a base type outside the repository, whose members cannot be read here;
//   - an extension method, which is declared on some other static class entirely;
//   - a partial class, whose members are spread over files that do not share its name; and
//   - a type the repository does not declare at all, which is the package surface's business.
//
// The extension-method bound was a fixed list of BCL and library names, which is not a bound at
// all: adding behaviour to a type through an extension method of one's own is ordinary C#, and a
// repository that does it had its own correct calls refused with the receiver's member list offered
// as the alternative. The repository's own `this Foo` declarations are read alongside its types.
//
// A false rejection costs a correct test its regeneration budget, so the bar for speaking is that
// the whole member set is in view.
func RepoInventedMemberReasonCS(repoRoot, testContent string) string {
	if strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(testContent) == "" {
		return ""
	}
	index, repoExtensions, ok := csharpRepoTypeIndex(repoRoot)
	if !ok || len(index) == 0 {
		return ""
	}
	stripped := dotnetproj.StripCSharpCommentsAndStrings(testContent)
	locals := csharpLocalTypesByIdent(stripped)
	if len(locals) == 0 {
		return ""
	}
	// A type the test declares itself shadows its repository namesake.
	declaredHere := map[string]bool{}
	for _, re := range []*regexp.Regexp{reCSharpTypeDeclaration, reCSharpDelegateDeclaration} {
		for _, m := range re.FindAllStringSubmatch(stripped, -1) {
			declaredHere[strings.TrimSpace(m[1])] = true
		}
	}

	idents := make([]string, 0, len(locals))
	for ident := range locals {
		idents = append(idents, ident)
	}
	sort.Strings(idents)

	var reasons []string
	seen := map[string]bool{}
	for _, ident := range idents {
		typ := locals[ident]
		if typ == "" || declaredHere[typ] {
			continue
		}
		decl, known := index[typ]
		if !known {
			continue // third-party: not ours to judge
		}
		members, provable := csharpVisibleMembers(index, typ, 0)
		if !provable {
			continue
		}
		callRE := regexp.MustCompile(`(^|[^\w.])` + regexp.QuoteMeta(ident) + `\s*\.\s*(\w+)\s*\(`)
		for _, m := range callRE.FindAllStringSubmatch(stripped, -1) {
			name := m[2]
			if members[name] || csharpObjectMemberNames[name] ||
				csharpExtensionMethodNames[name] || repoExtensions[name] {
				continue
			}
			key := typ + "#" + name
			if seen[key] {
				continue // one invented member is one finding, however many call sites it has
			}
			seen[key] = true
			reasons = append(reasons, csharpInventedMemberReason(typ, decl.primaryPath(), name, members))
		}
	}
	return strings.Join(reasons, "; also ")
}

func csharpInventedMemberReason(typ, path, name string, members map[string]bool) string {
	var b strings.Builder
	b.WriteString("call to " + name + "() on " + typ + " (" + path + "), which declares no such member")
	offer := make([]string, 0, len(members))
	for m := range members {
		offer = append(offer, m)
	}
	sort.Strings(offer)
	if len(offer) > maxOfferedCSharpMembers {
		offer = offer[:maxOfferedCSharpMembers]
	}
	if len(offer) > 0 {
		b.WriteString(" — it declares: " + strings.Join(offer, ", "))
	}
	return b.String()
}

// maxOfferedCSharpMembers bounds the alternatives named in one reason. The retry prompt has to hold
// every violation in the file; a hundred-member dump would crowd the others out.
const maxOfferedCSharpMembers = 25

// maxCSharpBaseDepth bounds base-type resolution. A hierarchy deeper than this in a repository's own
// code is unusual enough that stopping is cheaper than following it.
const maxCSharpBaseDepth = 6

// csharpExtensionMethodNames are method names that a type does not have to declare to answer.
//
// This is the C# hazard with no Java equivalent. An extension method is declared as a static method
// on an unrelated class and called as though it were an instance member, so `orders.ToListAsync()`
// and `result.Should()` are indistinguishable from an invented call by any scan of the receiver's
// own declaration. Every name here is one that a real generated test uses routinely: LINQ's
// operators, EF Core's async terminals, FluentAssertions' entry point and the Task combinators.
//
// The list is an ALLOWANCE, not a claim — a name on it is one this gate declines to judge.
var csharpExtensionMethodNames = map[string]bool{
	// LINQ.
	"Select": true, "SelectMany": true, "Where": true, "OrderBy": true, "OrderByDescending": true,
	"ThenBy": true, "ThenByDescending": true, "GroupBy": true, "Join": true, "Any": true, "All": true,
	"First": true, "FirstOrDefault": true, "Single": true, "SingleOrDefault": true, "Last": true,
	"LastOrDefault": true, "Count": true, "LongCount": true, "Sum": true, "Min": true, "Max": true,
	"Average": true, "ToList": true, "ToArray": true, "ToDictionary": true, "ToHashSet": true,
	"Contains": true, "Distinct": true, "Skip": true, "Take": true, "Concat": true, "Reverse": true,
	"Cast": true, "OfType": true, "Zip": true, "Aggregate": true, "ElementAt": true, "SequenceEqual": true,
	"Append": true, "Prepend": true, "Chunk": true, "DefaultIfEmpty": true,
	// EF Core's async terminals and query operators.
	"ToListAsync": true, "ToArrayAsync": true, "FirstAsync": true, "FirstOrDefaultAsync": true,
	"SingleAsync": true, "SingleOrDefaultAsync": true, "AnyAsync": true, "AllAsync": true,
	"CountAsync": true, "SumAsync": true, "MinAsync": true, "MaxAsync": true, "AverageAsync": true,
	"ToDictionaryAsync": true, "Include": true, "ThenInclude": true, "AsNoTracking": true,
	"AsTracking": true, "AsQueryable": true, "AsEnumerable": true, "FindAsync": true,
	// Assertion libraries.
	"Should": true, "ShouldBe": true, "ShouldSatisfyAllConditions": true,
	// Task and awaitable plumbing.
	"ConfigureAwait": true, "GetAwaiter": true, "AsTask": true, "WaitAsync": true,
	// Dependency-injection and host builder extensions, which are extension methods to a fault.
	"AddScoped": true, "AddSingleton": true, "AddTransient": true, "AddDbContext": true,
	"BuildServiceProvider": true, "GetRequiredService": true, "GetService": true,
	"UseInMemoryDatabase": true, "UseSqlite": true, "UseNpgsql": true, "UseSqlServer": true,
	// HttpClient's JSON extensions live in System.Net.Http.Json, not on HttpClient.
	"GetFromJsonAsync": true, "PostAsJsonAsync": true, "PutAsJsonAsync": true, "ReadFromJsonAsync": true,
}

// csharpObjectMemberNames are the members every C# type has from System.Object. The Java set is
// spelled in Java's casing (toString, hashCode), so reusing it would have let ToString() through as
// an invented member on every type in the repository.
var csharpObjectMemberNames = map[string]bool{
	"ToString": true, "Equals": true, "GetHashCode": true, "GetType": true,
	"ReferenceEquals": true, "MemberwiseClone": true, "Finalize": true,
	// Every type is disposable or awaitable often enough, and both are usually implemented through
	// an interface whose declaration this scan may not have reached.
	"Dispose": true, "DisposeAsync": true,
}

// csharpTypeDecl is where a type is declared and what its declarations say. A type has more than one
// path when it is partial, which is why members are collected across all of them.
type csharpTypeDecl struct {
	paths  []string
	bodies []string
}

func (d csharpTypeDecl) primaryPath() string {
	if len(d.paths) == 0 {
		return ""
	}
	return d.paths[0]
}

// reCSharpExtensionMethod matches an extension method DECLARATION: a static method whose first
// parameter is marked `this`. Only the method name is captured.
//
// The receiver type is deliberately not keyed on. An extension declared on an interface answers for
// every implementation of it, one declared on a generic parameter answers for everything, and
// getting either wrong reintroduces the false refusal this replaces. A name the repository declares
// as an extension is a name this gate declines to judge — the same allowance, and for the same
// reason, as csharpExtensionMethodNames.
var reCSharpExtensionMethod = regexp.MustCompile(
	`(?:public|internal)\s+(?:static\s+|unsafe\s+|partial\s+|extern\s+)*static\s+` +
		`(?:\([^()\n]*\)|[\w.<>\[\],?]+)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:<[^<>()\n]*>\s*)?` +
		`\(\s*(?:\[[^\]\n]*\]\s*)*this\s`)

// csharpRepoTypeIndex maps every simple type name the repository declares to the files declaring it,
// and collects the names of every extension method the repository declares.
//
// It reports false rather than a partial index for an unreadable subtree, for the same reason
// csharpDeclaredSimpleNames does: a name the walk failed to see would read as proof of absence, and
// the caller's whole output is an absence claim.
func csharpRepoTypeIndex(repoRoot string) (map[string]csharpTypeDecl, map[string]bool, bool) {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil, nil, false
	}
	out := map[string]csharpTypeDecl{}
	extensions := map[string]bool{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if dotnetproj.WalkSkipDir(d.Name()) || dotnetproj.WalkDepth(root, path) > maxCSharpDeclaredWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".cs") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := dotnetproj.StripCSharpCommentsAndStrings(string(b))
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		for _, m := range reCSharpExtensionMethod.FindAllStringSubmatch(src, -1) {
			if name := strings.TrimSpace(m[1]); name != "" {
				extensions[name] = true
			}
		}
		for _, m := range reCSharpTypeDeclaration.FindAllStringSubmatch(src, -1) {
			name := strings.TrimSpace(m[1])
			if name == "" {
				continue
			}
			decl := out[name]
			decl.paths = append(decl.paths, rel)
			decl.bodies = append(decl.bodies, src)
			out[name] = decl
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, false
	}
	return out, extensions, true
}

// csharpVisibleMembers collects the members a caller may reach on a type: its own declarations
// across every partial file, plus everything its base types declare.
//
// It returns false the moment a base type cannot be resolved inside the repository. That is the
// bound that makes the whole gate safe — a type deriving from ControllerBase or DbContext inherits
// a member set this package cannot see, and every unresolved call on it would otherwise be reported
// as invented.
func csharpVisibleMembers(index map[string]csharpTypeDecl, typ string, depth int) (map[string]bool, bool) {
	if depth > maxCSharpBaseDepth {
		return nil, false
	}
	decl, ok := index[typ]
	if !ok {
		return nil, false
	}
	out := map[string]bool{}
	for _, body := range decl.bodies {
		for _, name := range dotnetproj.DeclaredMemberNames(typ, body) {
			out[name] = true
		}
		for _, base := range dotnetproj.BaseTypeNames(typ, body) {
			// An interface the repository does not declare contributes no members a CLASS does not
			// already have to implement, but there is no way to tell an interface from a class by
			// name — so an unresolvable base of either kind ends the claim.
			baseMembers, provable := csharpVisibleMembers(index, base, depth+1)
			if !provable {
				return nil, false
			}
			for name := range baseMembers {
				out[name] = true
			}
		}
	}
	return out, true
}

var (
	// reCSharpVarNew matches `var x = new Foo(` and `var x = new Foo<T>(`.
	reCSharpVarNew = regexp.MustCompile(`\bvar\s+([A-Za-z_]\w*)\s*=\s*new\s+([A-Z]\w*)\s*[(<]`)

	// reCSharpTypedDecl matches an explicitly typed declaration, including the target-typed form
	// `Foo x = new();` that C# 9 added and that generated tests use constantly.
	reCSharpTypedDecl = regexp.MustCompile(`(?m)(?:^|[\s(])([A-Z]\w*)(?:<[^>;=]*>)?\s+([A-Za-z_]\w*)\s*=\s*new\b`)
)

// csharpLocalTypesByIdent maps an identifier to the type it holds. An identifier declared twice with
// different types is dropped: telling them apart needs scope analysis, and attributing a call to the
// wrong type is how a correct test gets rejected.
func csharpLocalTypesByIdent(stripped string) map[string]string {
	out := map[string]string{}
	drop := map[string]bool{}
	record := func(ident, typ string) {
		if ident == "" || typ == "" || csharpDeclarationKeywords[typ] {
			return
		}
		if prev, ok := out[ident]; ok && prev != typ {
			drop[ident] = true
			return
		}
		out[ident] = typ
	}
	for _, m := range reCSharpVarNew.FindAllStringSubmatch(stripped, -1) {
		record(m[1], m[2])
	}
	for _, m := range reCSharpTypedDecl.FindAllStringSubmatch(stripped, -1) {
		record(m[2], m[1])
	}
	for ident := range drop {
		delete(out, ident)
	}
	return out
}

// csharpDeclarationKeywords are words the type patterns can capture that are not type names.
var csharpDeclarationKeywords = map[string]bool{
	"Task": true, "ValueTask": true,
}
