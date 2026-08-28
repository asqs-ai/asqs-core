package evaluator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
)

// lookupAPISurface resolves real member signatures for the third-party types named in a diagnostic.
//
// Every failure mode here is non-fatal and audited. A missing provider, an unresolvable classpath,
// or a lookup error all yield no block — the fixer then behaves exactly as it did before this
// existed. What must never happen is a PARTIAL surface presented as complete: the model reads a
// member list as authoritative, so an unannounced truncation would make it conclude a real method
// does not exist. Truncation is therefore carried per type and rendered explicitly.
func lookupAPISurface(ctx context.Context, opts EvalOptions, errorOutput string, files map[string]string, step SandboxStep, audit Auditor) ([]apisurface.TypeSurface, []string) {
	if opts.APISurfaceProvider == nil || strings.TrimSpace(errorOutput) == "" {
		return nil, nil
	}
	// Sources are passed so a failed import can be resolved to the simple name it was binding;
	// javac's "package X does not exist" never names it. See apisurface.missingPackageSymbol.
	targets := apisurface.ParseTargetsWithSources(errorOutput, files)
	// javac's `location:` line names the ENCLOSING class for an unresolved-symbol error, which is
	// the file under repair. Asking the classpath about the repo's own test class is a lookup that
	// can only miss, and it burns one of the bounded target slots.
	targets = apisurface.FilterOwnedTypes(targets, repoDeclaredTypeNames(files))
	// Drop JDK/BCL types: their member lists are budget the prompt cannot spare and knowledge the
	// model already has. Unresolved simple names survive — resolving one to its import line is the
	// whole point. The language is what decides whether an unqualified name counts as unresolvable
	// (it does in Java, and does not in C# or TypeScript, where every reported type name is bare).
	targets = apisurface.FilterUninterestingTypes(apisurface.NormalizeLang(opts.Lang), targets)
	// Ownership is settled by here, so a bare-symbol search that a surviving type target already
	// explains is pure noise — and worse than noise, because every candidate is rendered as an
	// import suggestion.
	targets = apisurface.DropSymbolsCoveredByType(targets)
	// The lookup budget is spent HERE, on survivors, not during the parse. Truncating before the
	// filters ran meant slots went to candidates the filters were about to discard.
	targets = apisurface.CapTargets(targets)
	if len(targets) == 0 {
		return nil, nil
	}
	surfaces, err := opts.APISurfaceProvider.Lookup(ctx, opts.RepoPath, targets)
	if err != nil || len(surfaces) == 0 {
		if audit != nil {
			reason := "no surfaces resolved"
			if err != nil {
				reason = err.Error()
			}
			audit.Log(ctx, "evaluator.fix_api_surface_unavailable", map[string]interface{}{
				"message": fmt.Sprintf("No API surface available for step %s; the fixer will run without it. Reason: %s", step, reason),
				"step":    step,
				"reason":  reason,
				"targets": describeTargets(targets),
			})
		}
		return nil, nil
	}
	// The absence claim is bounded by what the REPOSITORY declares, not only by what the classpath
	// holds. See absentTargetNames.
	repoDeclared, repoKnown := apisurface.RepoDeclaredSimpleNames(apisurface.NormalizeLang(opts.Lang), opts.RepoPath)
	absent := absentTargetNames(targets, surfaces, repoDeclared, repoKnown)
	if audit != nil {
		resolved := make([]string, 0, len(surfaces))
		for _, s := range surfaces {
			resolved = append(resolved, fmt.Sprintf("%s (%d member(s), truncated=%v)", s.FQCN, len(s.Members), s.Truncated))
		}
		audit.Log(ctx, "evaluator.fix_api_surface", map[string]interface{}{
			"message":  fmt.Sprintf("Resolved %d API surface(s) from the compile classpath for step %s; %d target(s) are absent from it.", len(surfaces), step, len(absent)),
			"step":     step,
			"targets":  describeTargets(targets),
			"resolved": resolved,
			"absent":   absent,
		})
	}
	return surfaces, absent
}

// absentTargetNames returns the names the classpath scan looked up and did not find.
//
// A partially-resolved lookup used to drop its misses silently, so the prompt said nothing at all
// about them — and silence is the one answer a model cannot act on. In run
// api-0c344e6bc0658e0db06506efb9d964f5 `MockBean` and `MockMvcRestServiceServer` were targets on all
// ten rounds and resolved on none; the model reintroduced both every round, which put them back in
// the diagnostic, which put them back in the targets. Naming them as VERIFIED ABSENT is what breaks
// that loop.
//
// Two conditions license the negative, and the claim is only as good as the weaker of them.
//
// First, at least one surface must have resolved: a Lookup that returned nothing at all is
// indistinguishable from a classpath that could not be read, and the caller reports that as
// fix_api_surface_unavailable instead.
//
// Second, the repository must be able to testify about itself (repoKnown). A classpath answers "is
// this name in a compiled artifact", which is NOT the question — the fixer runs because compile
// failed, so a repo class that the failing build never produced resolves to nothing and would be
// reported absent. FilterOwnedTypes does not cover it: it drops only KindType targets, and only
// those whose FULLY-QUALIFIED name is among the files already in the prompt, while these are bare
// simple names that may belong to a file the prompt never loaded. Without that testimony the
// function says nothing at all, because the block built from it tells the model to delete the code
// that uses these names.
func absentTargetNames(targets []apisurface.Target, surfaces []apisurface.TypeSurface, repoDeclared map[string]bool, repoKnown bool) []string {
	if len(targets) == 0 || len(surfaces) == 0 || !repoKnown {
		return nil
	}
	resolved := make(map[string]bool, len(surfaces)*2)
	for _, s := range surfaces {
		fq := strings.TrimSpace(s.FQCN)
		if fq == "" {
			continue
		}
		resolved[fq] = true
		resolved[fq[strings.LastIndex(fq, ".")+1:]] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range targets {
		name := strings.TrimSpace(t.Name)
		if name == "" || seen[name] || resolved[name] {
			continue
		}
		if repoDeclared[simpleTypeName(name)] {
			continue // the repository declares it in source; the build simply has not produced it.
		}
		// A dotted target that resolved under its own name is covered by the map above; a bare
		// symbol is covered by the simple-name entries. Anything left was looked up and missed.
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// introducedUnresolvedDependencyReason is the fixer-side wrapper over
// apisurface.IntroducedUnresolvedDependencyReason: it applies the same provability bounds the
// generator's call has, plus the two the fixer needs.
//
// Java only, because the underlying check reads Java imports and Java source roots — the generator
// gates on exactly this before calling it. And silent without a provider, because "no classpath to
// ask" must never read as "the reference is bad": the whole gate is a negative claim, and a
// negative claim with no evidence behind it would reject correct repairs.
func introducedUnresolvedDependencyReason(ctx context.Context, opts EvalOptions, before, after string) string {
	if opts.APISurfaceProvider == nil || strings.TrimSpace(opts.RepoPath) == "" {
		return ""
	}
	if apisurface.NormalizeLang(opts.Lang) != apisurface.LangJava {
		return ""
	}
	return apisurface.IntroducedUnresolvedDependencyReason(ctx, opts.APISurfaceProvider, opts.RepoPath, before, after)
}

// simpleTypeName reduces a target name to the last dotted segment, so a fully-qualified type and a
// bare symbol are compared against repo source file names on the same terms.
func simpleTypeName(name string) string {
	return name[strings.LastIndex(name, ".")+1:]
}

func describeTargets(targets []apisurface.Target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.Member != "" {
			out = append(out, fmt.Sprintf("%s:%s#%s", t.Kind, t.Name, t.Member))
			continue
		}
		out = append(out, fmt.Sprintf("%s:%s", t.Kind, t.Name))
	}
	return out
}

// repoDeclaredTypeNames derives the fully-qualified type names the repository itself declares, from
// the paths already loaded into the fix prompt. Java only: the path-to-FQCN mapping is exact there
// (src/{main,test}/java mirrors the package), which is what makes the filter safe to apply.
func repoDeclaredTypeNames(files map[string]string) map[string]bool {
	if len(files) == 0 {
		return nil
	}
	out := make(map[string]bool, len(files))
	for p := range files {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if !strings.HasSuffix(p, ".java") {
			continue
		}
		idx := -1
		for _, root := range []string{"src/test/java/", "src/main/java/", "src/it/java/"} {
			if i := strings.Index(p, root); i >= 0 {
				idx = i + len(root)
				break
			}
		}
		if idx < 0 {
			continue
		}
		fq := strings.TrimSuffix(p[idx:], ".java")
		out[strings.ReplaceAll(fq, "/", ".")] = true
	}
	return out
}
