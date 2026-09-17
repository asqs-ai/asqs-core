package evaluator

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// csharpMissingMemberFacts turns a C# compile diagnostic that rejects a member into declared-member
// truth about the repository's own type.
//
// The fixer's only evidence for "this member does not exist" was the compiler's one-line
// diagnostic, which says what is wrong and nothing about what is right. A round would then invent a
// different member of the same type and fail again on the next diagnostic. Java has had this block
// since the Mockito work; missingMemberFacts returned nil for every other language.
//
// Only types the REPOSITORY declares are described. A third-party type is the classpath surface's
// job, and claiming a member is absent from one — from a source scan that never saw it — would be a
// false statement attached to an instruction to delete code.
//
// artifactPaths are the files this run generated. When the rejected type is one of them, the fact
// is the opposite one: the model called a helper it never defined, and must define it.
func csharpMissingMemberFacts(errorOutput string, files map[string]string, artifactPaths []string) []string {
	if strings.TrimSpace(errorOutput) == "" || len(files) == 0 {
		return nil
	}
	declared := csharpTypeSourcesByName(files)
	if len(declared) == 0 {
		return nil
	}
	artifacts := make(map[string]bool, len(artifactPaths))
	for _, p := range artifactPaths {
		artifacts[normalizePathForFix(p)] = true
	}

	var facts []string
	seen := map[string]bool{}
	emit := func(key, fact string) {
		if seen[key] || len(facts) >= maxMissingMemberFacts {
			return
		}
		seen[key] = true
		facts = append(facts, fact)
	}

	// CS1061 / CS0117: 'T' does not contain a definition for 'M'.
	for _, m := range reCSharpMissingMember.FindAllStringSubmatch(errorOutput, -1) {
		typeName, member := m[1], m[2]
		src, ok := declared[typeName]
		if !ok {
			continue // third-party: the classpath surface answers for it
		}
		if generated := firstGeneratedArtifactPath(src.paths, artifacts); generated != "" {
			emit(typeName+"#"+member, fmt.Sprintf(
				"%s (%s) is a file THIS run generated and it has no member %q — the call was written before the member. "+
					"Define it in that file, or call something the type under test actually declares.",
				typeName, generated, member))
			continue
		}
		emit(typeName+"#"+member, csharpOwnedTypeMissFact(typeName, src.pathLabel(), member, src.bodies))
	}

	// CS1729 / CS7036: the type exists, the constructor call matches no declared signature.
	for _, m := range reCSharpConstructorArity.FindAllStringSubmatch(errorOutput, -1) {
		typeName := m[1]
		src, ok := declared[typeName]
		if !ok {
			continue
		}
		emit(typeName+"#.ctor", csharpConstructorFact(typeName, src.pathLabel(), src.bodies))
	}
	return facts
}

var (
	// reCSharpMissingMember covers CS1061 and CS0117, which share the sentence. The type name is
	// taken as a simple name because that is how the compiler prints it, even for a nested or
	// namespaced type.
	reCSharpMissingMember = regexp.MustCompile(
		`'([A-Za-z_][A-Za-z0-9_.<>]*)' does not contain a definition for '([A-Za-z_][A-Za-z0-9_]*)'`)

	// reCSharpConstructorArity covers CS1729 and CS7036.
	reCSharpConstructorArity = regexp.MustCompile(
		`'([A-Za-z_][A-Za-z0-9_.<>]*)' does not contain a constructor that takes`)

	// A constructor declaration: a public member whose name equals the type's and which is followed
	// by a parameter list with no return type of its own.
	reCSharpCtorParams = `(?m)^\s*(?:public|internal|protected internal|protected)\s+%s\s*\(([^)]*)\)`
)

// csharpTypeSource is every file in this prompt that declares one type. A type has more than one
// when it is partial.
type csharpTypeSource struct {
	paths  []string
	bodies []string
}

func (s csharpTypeSource) primaryPath() string {
	if len(s.paths) == 0 {
		return ""
	}
	return s.paths[0]
}

// pathLabel names every file the members come from, because the fact that follows it says "shown in
// this prompt" — and for a partial type a single path would attribute half the members to a file
// that does not contain them.
func (s csharpTypeSource) pathLabel() string {
	return strings.Join(s.paths, ", ")
}

// csharpTypeSourcesByName maps a simple type name to the files that declare it. Comments and string
// literals are stripped first: a type named only inside a template string is not declared.
//
// Every declaring file is kept, not the first. A partial class is one type spread over several
// files, and keeping one of them made the fixer offer half a type's members while the generator's
// invented-member gate, which accumulates across files, saw all of them — the divergence the header
// of dotnetproj/csharpdecl.go says must not exist. Which half survived depended on map iteration
// order, so the prompt was not even stable between runs.
func csharpTypeSourcesByName(files map[string]string) map[string]csharpTypeSource {
	out := map[string]csharpTypeSource{}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		body := files[path]
		if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".cs") {
			continue
		}
		src := dotnetproj.StripCSharpCommentsAndStrings(body)
		declaredInThisFile := map[string]bool{}
		for _, m := range reCSharpDeclaredType.FindAllStringSubmatch(src, -1) {
			name := strings.TrimSpace(m[1])
			if name == "" || declaredInThisFile[name] {
				continue
			}
			declaredInThisFile[name] = true
			decl := out[name]
			decl.paths = append(decl.paths, path)
			decl.bodies = append(decl.bodies, body)
			out[name] = decl
		}
	}
	return out
}

// firstGeneratedArtifactPath returns the declaring file this run generated, if any. A partial type
// can be spread over a generated artifact and repo-owned source at once, and the artifact is the
// file the fixer can write to.
func firstGeneratedArtifactPath(paths []string, artifacts map[string]bool) string {
	for _, p := range paths {
		if artifacts[normalizePathForFix(p)] {
			return p
		}
	}
	return ""
}

func csharpOwnedTypeMissFact(typeName, path, member string, bodies []string) string {
	declared := csharpDeclaredMemberNames(typeName, bodies...)
	// Never offer the member the compiler just rejected as the alternative to itself.
	//
	// DeclaredMemberNames lists public AND internal members, by design — they are call targets from
	// inside the same assembly. So a rejected internal member appeared in its own suggestion list,
	// and the fact read "has NO member X … Members declared on T include: X". Run
	// api-2555a79ee2660a8a5cd95c8c860090f5 handed the fixer exactly that contradiction; across
	// three rounds its only move was to delete tests (9 → 6, then 9 → 7), the coverage gate refused
	// both, and the loop ended on no_accepted_writes having spent fifty minutes.
	declared = withoutMember(declared, member)
	partial := false
	if len(declared) > maxDeclaredMethodsListed {
		declared = declared[:maxDeclaredMethodsListed]
		partial = true
	}
	var b strings.Builder
	// "Does not contain a definition for" is what the compiler says whether the member is absent or
	// merely out of reach from this assembly, and the two have different remedies. Ask the source
	// which one it is rather than asserting the stronger claim.
	if access, ok := csharpMemberAccess(typeName, member, bodies); ok && access != "public" {
		b.WriteString(dotnetproj.DescribeAccessRemedy(typeName, member, access))
		if len(declared) > 0 {
			suffix := ""
			if partial {
				suffix = " (list shortened)"
			}
			fmt.Fprintf(&b, " Members of %s this test CAN call include%s: %s.", typeName, suffix, strings.Join(declared, ", "))
		}
		return b.String()
	}
	fmt.Fprintf(&b, "%s (%s, shown in this prompt) has NO member %q — the compiler rejected it; do not use it again.",
		typeName, path, member)
	if len(declared) > 0 {
		suffix := ""
		if partial {
			suffix = " (list shortened)"
		}
		fmt.Fprintf(&b, " Members declared on %s include%s: %s.", typeName, suffix, strings.Join(declared, ", "))
		b.WriteString(" Use one of these, or build the state you need through the constructors the source shows.")
	}
	return b.String()
}

// csharpMemberAccess asks every body that declares the type how it declares this member. The first
// answer wins: a partial class splits one type across files, and only one of them declares it.
// csharpMemberAccess asks every body that declares the type how it declares this member. The first
// answer wins: a partial class splits one type across files, and only one of them declares it.
func csharpMemberAccess(typeName, member string, bodies []string) (string, bool) {
	for _, body := range bodies {
		if access, ok := dotnetproj.DeclaredMemberAccess(typeName, member, body); ok {
			return access, true
		}
	}
	return "", false
}

// withoutMember returns names with one removed, preserving order.
func withoutMember(names []string, member string) []string {
	out := names[:0:0]
	for _, n := range names {
		if n != member {
			out = append(out, n)
		}
	}
	return out
}

func csharpConstructorFact(typeName, path string, bodies []string) string {
	re := regexp.MustCompile(fmt.Sprintf(reCSharpCtorParams, regexp.QuoteMeta(typeName)))
	var sigs []string
	seen := map[string]bool{}
	for _, body := range bodies {
		src := dotnetproj.StripCSharpCommentsAndStrings(body)
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			params := strings.TrimSpace(m[1])
			sig := typeName + "(" + params + ")"
			if seen[sig] {
				continue
			}
			seen[sig] = true
			sigs = append(sigs, sig)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, shown in this prompt) has no constructor matching that call.", typeName, path)
	switch len(sigs) {
	case 0:
		// No declared constructor means the implicit parameterless one, which the diagnostic
		// already rejected — so the type is likely a record or has a primary constructor.
		b.WriteString(" Its source declares no explicit constructor: check for a primary constructor or record parameters" +
			" in the declaration itself, and match those.")
	default:
		fmt.Fprintf(&b, " Declared constructor(s): %s. Call one of these exactly.", strings.Join(sigs, "; "))
	}
	return b.String()
}

// csharpDeclaredMemberNames lists the members a caller may actually use on a repo-owned type.
//
// It delegates to dotnetproj so the fixer and the generator's invented-member gate read the same
// declarations. They used to be two scans of the same source: a member the generator allowed and
// the fixer then called absent would have put the loop into an argument with itself.
//
// Variadic over bodies for the same reason the gate accumulates them: one partial type's members
// are spread over several files.
func csharpDeclaredMemberNames(typeName string, bodies ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, body := range bodies {
		for _, name := range dotnetproj.DeclaredMemberNames(typeName, body) {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// auditMissingMemberFacts records the C# facts on the same event the Java path uses, so a
// post-mortem reads one trail rather than two. Silent when there is nothing to state.
func auditMissingMemberFacts(ctx context.Context, audit Auditor, opts EvalOptions, facts []string) {
	if len(facts) == 0 || audit == nil {
		return
	}
	subjects := make([]string, 0, len(facts))
	for _, f := range facts {
		// The fact opens with `<Type> (<path>…` — the subject is everything before the first space.
		if i := strings.IndexByte(f, ' '); i > 0 {
			subjects = append(subjects, f[:i])
			continue
		}
		subjects = append(subjects, f)
	}
	audit.Log(ctx, "evaluator.fix_missing_member_facts", map[string]interface{}{
		"message": fmt.Sprintf("Stated %d compiler-verified missing-member fact(s) for repo-owned types: %s.",
			len(facts), strings.Join(subjects, ", ")),
		"facts": subjects,
		"count": len(facts),
		"lang":  opts.Lang,
	})
}
