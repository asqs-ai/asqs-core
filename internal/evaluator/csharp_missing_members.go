package evaluator

import (
	"context"
	"fmt"
	"regexp"
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
		if artifacts[normalizePathForFix(src.path)] {
			emit(typeName+"#"+member, fmt.Sprintf(
				"%s (%s) is a file THIS run generated and it has no member %q — the call was written before the member. "+
					"Define it in that file, or call something the type under test actually declares.",
				typeName, src.path, member))
			continue
		}
		emit(typeName+"#"+member, csharpOwnedTypeMissFact(typeName, src.path, member, src.body))
	}

	// CS1729 / CS7036: the type exists, the constructor call matches no declared signature.
	for _, m := range reCSharpConstructorArity.FindAllStringSubmatch(errorOutput, -1) {
		typeName := m[1]
		src, ok := declared[typeName]
		if !ok {
			continue
		}
		emit(typeName+"#.ctor", csharpConstructorFact(typeName, src.path, src.body))
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

type csharpTypeSource struct {
	path string
	body string
}

// csharpTypeSourcesByName maps a simple type name to the file that declares it. Comments and string
// literals are stripped first: a type named only inside a template string is not declared.
func csharpTypeSourcesByName(files map[string]string) map[string]csharpTypeSource {
	out := map[string]csharpTypeSource{}
	for path, body := range files {
		if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".cs") {
			continue
		}
		src := dotnetproj.StripCSharpCommentsAndStrings(body)
		for _, m := range reCSharpDeclaredType.FindAllStringSubmatch(src, -1) {
			if name := strings.TrimSpace(m[1]); name != "" {
				if _, dup := out[name]; !dup {
					out[name] = csharpTypeSource{path: path, body: body}
				}
			}
		}
	}
	return out
}

func csharpOwnedTypeMissFact(typeName, path, member, body string) string {
	declared := csharpDeclaredMemberNames(typeName, body)
	partial := false
	if len(declared) > maxDeclaredMethodsListed {
		declared = declared[:maxDeclaredMethodsListed]
		partial = true
	}
	var b strings.Builder
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

func csharpConstructorFact(typeName, path, body string) string {
	src := dotnetproj.StripCSharpCommentsAndStrings(body)
	re := regexp.MustCompile(fmt.Sprintf(reCSharpCtorParams, regexp.QuoteMeta(typeName)))
	var sigs []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		params := strings.TrimSpace(m[1])
		if params == "" {
			sigs = append(sigs, typeName+"()")
			continue
		}
		sigs = append(sigs, typeName+"("+params+")")
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
func csharpDeclaredMemberNames(typeName, body string) []string {
	return dotnetproj.DeclaredMemberNames(typeName, body)
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
