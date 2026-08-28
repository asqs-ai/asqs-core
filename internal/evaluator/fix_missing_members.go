package evaluator

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
)

// Missing-member facts: deterministic statements for "cannot find symbol: method M" diagnostics
// that blame REPO-OWNED types, which the API-surface machinery deliberately cannot answer.
//
// lookupAPISurface drops owned types before the classpath lookup (FilterOwnedTypes) because javap
// over the compile classpath can only miss for the tree that just failed to compile — and nothing
// replaced the dropped target with the answer that IS available from source. Run
// api-7549a0ea57f8950449087ff85f1c4ce6 stalled exactly there: `setVets` invented on Vets (a repo
// type whose source sat in the prompt), `notNull`/`equalTo` invented on the test class itself,
// api_surface_types=0 on every stalled round, and the fixer re-emitted variants for four rounds.
//
// The facts stated here are NEGATIVE ("this method does not exist") plus enumerated alternatives,
// which converts an open-ended generation problem into a selection problem — the level a small
// fixer model can execute. Everything is derived mechanically from the compiler's own diagnostic
// and the sources already loaded for the prompt; no LLM judgment is involved.

// maxMissingMemberFacts bounds the block: the first few misses are the primary failure; a long
// cascade repeats the same root cause.
const maxMissingMemberFacts = 4

// maxDeclaredMethodsListed bounds the alternatives list per fact.
const maxDeclaredMethodsListed = 25

// staticImportCandidateTypes are the types a bare unresolved call in a Java TEST class most
// plausibly meant. Checked against the classpath via the API-surface provider, so only candidates
// that really declare the missing method are suggested — a wrong import suggestion is worse than
// none. Ordered: JUnit first, then mocking/matching, then Spring's Assert (the utility the
// motivating run's generator was reaching for).
var staticImportCandidateTypes = []string{
	"org.junit.jupiter.api.Assertions",
	"org.mockito.Mockito",
	"org.mockito.ArgumentMatchers",
	"org.hamcrest.Matchers",
	"org.hamcrest.MatcherAssert",
	"org.assertj.core.api.Assertions",
	"org.springframework.util.Assert",
}

// javaMethodDeclRE matches a Java method declaration line: at least one modifier, a return type
// token, then the name and an opening parenthesis. Requiring whitespace between the return type
// and the name excludes constructors (`public Vets() {`) naturally. Package-private methods
// (no modifier) are missed on purpose — a false negative only shortens the alternatives list,
// while loosening the anchor would let call sites inside method bodies through.
var javaMethodDeclRE = regexp.MustCompile(`(?m)^\s*(?:(?:public|protected|private|static|final|abstract|synchronized|native|strictfp|default)\s+)+[\w$.<>\[\], ?&]+\s+([a-zA-Z_$][\w$]*)\s*\(`)

// Provenance for the negative claim. apisurface.ParseTargetsWithSources merges (type, member)
// targets from SEVERAL diagnostic shapes — missing-method triples, ambiguous references,
// not-applicable overloads — because its consumer only renders the type's real members, which is
// correct for all of them. A NEGATIVE claim ("T has NO method M") is only true for the
// missing-method triples, so those are re-matched here rather than trusting the merged targets.
// Run api-7b38aac91623c962b588a0e0a9fbb2f6 is what trusting them produced: `reference to getPet
// is ambiguous` (both overloads EXIST) became the fact "Owner has NO method getPet — do not call
// it again", stated as ground truth on all seven stuck rounds, while the only correct repair was
// to keep calling getPet with a disambiguated argument.
//
// These two regexes mirror apisurface's unexported javacMethodOnType / javacMethodOnVariable;
// TestMissingMemberFacts_ambiguityIsNotAMissingMethod pins the behavioural contract.
var (
	javacMissingMethodOnTypeRE     = regexp.MustCompile(`symbol:\s+method\s+(\w+)[^\n]*\n\s*location:\s+(?:class|interface|enum|record)\s+([\w.$]+)`)
	javacMissingMethodOnVariableRE = regexp.MustCompile(`symbol:\s+method\s+(\w+)[^\n]*\n\s*location:\s+variable\s+\w+\s+of\s+type\s+([\w.$]+)`)
	// javacAmbiguousOverloadsRE captures the full ambiguity detail line so the TRUE fact can name
	// both overloads: `reference to M is ambiguous\n both method M(sig1) in T and method M(sig2)
	// in T match`.
	javacAmbiguousOverloadsRE = regexp.MustCompile(`reference to (\w+) is ambiguous[^\n]*\n\s*both method\s+\w+\(([^)]*)\)\s+in\s+([\w.$]+)\s+and\s+method\s+\w+\(([^)]*)\)\s+in\s+[\w.$]+\s+match`)
)

// missingMethodTriples returns the (method, type-FQCN) pairs the output genuinely reports as
// "cannot find symbol: method" — the only shape that licenses a non-existence claim.
func missingMethodTriples(errorOutput string) map[[2]string]bool {
	out := map[[2]string]bool{}
	for _, re := range []*regexp.Regexp{javacMissingMethodOnTypeRE, javacMissingMethodOnVariableRE} {
		for _, m := range re.FindAllStringSubmatch(errorOutput, -1) {
			if len(m) >= 3 {
				out[[2]string{m[1], m[2]}] = true
			}
		}
	}
	return out
}

// javaDeclaredMethodNames extracts declared method names from Java source (full or
// signature-sliced — fixslice keeps declaration lines, so both shapes match). Sorted, unique.
func javaDeclaredMethodNames(src string) []string {
	seen := make(map[string]bool)
	for _, m := range javaMethodDeclRE.FindAllStringSubmatch(src, -1) {
		if len(m) < 2 {
			continue
		}
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// repoFQCNToPath derives FQCN → repo path for the .java files in the prompt, mirroring
// repoDeclaredTypeNames' root handling so the two never disagree about ownership.
func repoFQCNToPath(files map[string]string) map[string]string {
	out := make(map[string]string, len(files))
	for p := range files {
		slash := filepath.ToSlash(strings.TrimSpace(p))
		if !strings.HasSuffix(slash, ".java") {
			continue
		}
		idx := -1
		for _, root := range []string{"src/test/java/", "src/main/java/", "src/it/java/"} {
			if i := strings.Index(slash, root); i >= 0 {
				idx = i + len(root)
				break
			}
		}
		if idx < 0 {
			continue
		}
		fq := strings.ReplaceAll(strings.TrimSuffix(slash[idx:], ".java"), "/", ".")
		out[fq] = p
	}
	return out
}

// missingMemberFacts builds the facts block for step compile diagnostics that reject a method on a
// repo-owned type. Returns nil when the language is not Java, nothing matches, or every match
// names a non-owned type (those are the classpath surface's job).
func missingMemberFacts(ctx context.Context, opts EvalOptions, errorOutput string, files map[string]string, artifactPaths []string, audit Auditor) []string {
	if !strings.EqualFold(strings.TrimSpace(opts.Lang), "java") || strings.TrimSpace(errorOutput) == "" || len(files) == 0 {
		return nil
	}
	fqcnToPath := repoFQCNToPath(files)
	if len(fqcnToPath) == 0 {
		return nil
	}
	artifactSet := make(map[string]bool, len(artifactPaths))
	for _, p := range artifactPaths {
		artifactSet[normalizePathForFix(p)] = true
	}

	type miss struct {
		method string
		fqcn   string
	}
	genuineMisses := missingMethodTriples(errorOutput)
	seen := make(map[miss]bool)
	var facts []string
	var auditEntries []string
	for _, t := range apisurface.ParseTargetsWithSources(errorOutput, files) {
		if t.Kind != apisurface.KindType || t.Member == "" {
			continue
		}
		// Non-existence must be licensed by an actual missing-method triple. The merged targets
		// also carry ambiguity / not-applicable shapes, for which the method EXISTS — see the
		// provenance comment on the regexes above.
		if !genuineMisses[[2]string{t.Member, t.Name}] {
			continue
		}
		path, owned := fqcnToPath[t.Name]
		if !owned {
			continue // third-party type: the classpath API surface answers those.
		}
		key := miss{method: t.Member, fqcn: t.Name}
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(facts) >= maxMissingMemberFacts {
			break
		}
		simple := t.Name
		if i := strings.LastIndex(simple, "."); i >= 0 {
			simple = simple[i+1:]
		}
		if artifactSet[normalizePathForFix(path)] {
			facts = append(facts, testClassMissFact(ctx, opts, simple, t.Member))
		} else {
			facts = append(facts, ownedTypeMissFact(simple, t.Name, path, t.Member, files[path]))
		}
		auditEntries = append(auditEntries, fmt.Sprintf("%s#%s", t.Name, t.Member))
	}
	// Ambiguous overloads get the TRUE fact: the method exists more than once and the argument
	// must be disambiguated. Stated for any type — owned or not — because every clause is a
	// restatement of the compiler's own detail line. Without this the block stayed silent (or,
	// before the provenance check above, actively lied) on exactly the diagnostic that stalled
	// run api-7b38aac91623c962b588a0e0a9fbb2f6 for six rounds.
	for _, m := range javacAmbiguousOverloadsRE.FindAllStringSubmatch(errorOutput, -1) {
		if len(m) < 5 || len(facts) >= maxMissingMemberFacts {
			break
		}
		method, sig1, fqcn, sig2 := m[1], m[2], m[3], m[4]
		key := miss{method: method, fqcn: fqcn}
		if seen[key] {
			continue
		}
		seen[key] = true
		facts = append(facts, fmt.Sprintf(
			"The call to %q is AMBIGUOUS, not missing: %s declares both %s(%s) and %s(%s), and the argument at the blamed line matches both (typically a null or untyped value). Keep calling %s and disambiguate the ARGUMENT — cast it to one overload's parameter type (e.g. (%s) value) or pass a concretely typed value. Do not remove the call and do not add imports.",
			method, fqcn, method, sig1, method, sig2, method, sig1))
		auditEntries = append(auditEntries, fmt.Sprintf("%s#%s (ambiguous-overload)", fqcn, method))
	}
	if len(facts) > 0 && audit != nil {
		audit.Log(ctx, "evaluator.fix_missing_member_facts", map[string]interface{}{
			"message": fmt.Sprintf("Stated %d compiler-verified missing-member fact(s) for repo-owned types: %s.",
				len(facts), strings.Join(auditEntries, ", ")),
			"facts": auditEntries,
			"count": len(facts),
		})
	}
	return facts
}

// ownedTypeMissFact covers `location: variable v of type <repo type>` (and `location: class` for a
// non-artifact repo type): the method was invented on a type whose real members are in the prompt.
func ownedTypeMissFact(simple, fqcn, path, method, src string) string {
	declared := javaDeclaredMethodNames(src)
	partial := false
	if len(declared) > maxDeclaredMethodsListed {
		declared = declared[:maxDeclaredMethodsListed]
		partial = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, shown in this prompt) has NO method %q — the compiler rejected it; do not call it again.", fqcn, path, method)
	if len(declared) > 0 {
		suffix := ""
		if partial {
			suffix = " (list shortened)"
		}
		fmt.Fprintf(&b, " Methods declared in %s include%s: %s.", simple, suffix, strings.Join(declared, ", "))
		b.WriteString(" Use one of these, or build the needed state through constructors/fields the source shows.")
	}
	return b.String()
}

// testClassMissFact covers `location: class <the test class itself>`: a bare call with no matching
// static import. When the classpath proves a candidate type declares the method, the exact import
// line is suggested; the JUnit rewrite is always offered as the fallback.
func testClassMissFact(ctx context.Context, opts EvalOptions, simple, method string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "No method %q is defined in %s and none is statically imported — the call cannot compile as written.", method, simple)
	if imports := staticImportCandidatesFor(ctx, opts, method); len(imports) > 0 {
		fmt.Fprintf(&b, " The compile classpath declares it on: %s. Add ONE of these static imports, or qualify the call.", strings.Join(imports, " OR "))
	}
	b.WriteString(" Otherwise rewrite the call with standard JUnit assertions (org.junit.jupiter.api.Assertions: assertNotNull, assertEquals, assertTrue, ...).")
	return b.String()
}

// staticImportCandidatesFor returns `import static <FQCN>.<method>;` lines for the curated
// candidate types that really declare the method, per the classpath. Membership is checked against
// AllMemberNames — the complete name list — never the prompt-budget Members slice.
func staticImportCandidatesFor(ctx context.Context, opts EvalOptions, method string) []string {
	if opts.APISurfaceProvider == nil {
		return nil
	}
	targets := make([]apisurface.Target, 0, len(staticImportCandidateTypes))
	for _, t := range staticImportCandidateTypes {
		targets = append(targets, apisurface.Target{Kind: apisurface.KindType, Name: t, Member: method})
	}
	surfaces, err := opts.APISurfaceProvider.Lookup(ctx, opts.RepoPath, targets)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range surfaces {
		for _, name := range s.AllMemberNames {
			if name == method {
				out = append(out, fmt.Sprintf("import static %s.%s;", s.FQCN, method))
				break
			}
		}
	}
	return out
}
