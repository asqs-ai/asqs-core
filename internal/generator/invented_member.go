package generator

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
)

// Generation-time check for APIs that do not exist, ported from asqs-go's orchestrator. The
// classpath and repo-source authorities were already in this package's apisurface dependency;
// nothing here was calling them, so an invented member reached disk and the fix loop every time.

// extendRedirectPathRE reads the target path out of ExtendExistingRedirectPrefix.
var extendRedirectPathRE = regexp.MustCompile(`^The repository already has a test file at "([^"]+)"`)

// IsExtendExistingContext reports whether contextStr asks for methods to append to an existing
// test file rather than a whole file.
func IsExtendExistingContext(contextStr string) bool {
	return strings.Contains(contextStr, ExtendExistingTestContextPrefix)
}

// ExistingTestFileFromContext returns the existing test file body embedded in an extend context
// (between ExtendExistingTestContextPrefix and ExtendExistingTestContextSuffix), or "".
func ExistingTestFileFromContext(contextStr string) string {
	i := strings.Index(contextStr, ExtendExistingTestContextPrefix)
	if i < 0 {
		return ""
	}
	body := contextStr[i+len(ExtendExistingTestContextPrefix):]
	if j := strings.Index(body, ExtendExistingTestContextSuffix); j >= 0 {
		body = body[:j]
	}
	return strings.TrimSpace(body)
}

// ExtendTargetImportHeader returns the import/using lines of the extend target embedded in
// contextStr, so a check run on a methods-only payload sees the bindings the merged file will have.
func ExtendTargetImportHeader(contextStr string) string {
	existing := ExistingTestFileFromContext(contextStr)
	if existing == "" {
		return ""
	}
	var lines []string
	for _, ln := range strings.Split(existing, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "using ") || strings.HasPrefix(t, "global using ") {
			lines = append(lines, t)
		}
	}
	return strings.Join(lines, "\n")
}

// extendTargetPathFromContext returns the repository test file a redirect-extend context names,
// or "" for a plain extend (whose target is the generator's own suggested path).
func extendTargetPathFromContext(contextStr string) string {
	if m := extendRedirectPathRE.FindStringSubmatch(strings.TrimSpace(contextStr)); m != nil {
		return m[1]
	}
	return ""
}

// inventedMemberReason checks the generated body against every API authority in hand: the
// classpath member lists this item's prompt carried (Playwright assertion chains), the
// repository's own sources (members invented on repo types), and the compile classpath itself
// (references to packages no dependency provides). Empty when nothing is provable. checkPath is
// the test file the content is written to, which the TypeScript check needs to resolve relative
// imports.
func (g *LLMGenerator) inventedMemberReason(ctx context.Context, content, itemLang string, isE2E bool, extendHeader, checkPath string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	// A methods-only extend payload carries no imports; the merged file will have the target's.
	assertionContent := content
	if extendHeader != "" {
		assertionContent = extendHeader + "\n" + content
	}
	var reasons []string
	add := func(reason string) {
		if strings.TrimSpace(reason) != "" {
			reasons = append(reasons, reason)
		}
	}
	if g.APISurface != nil && isE2E {
		add(apisurface.InventedAssertionMemberReason(assertionContent, g.pregenerateAPISurfaceEntry(ctx, itemLang, isE2E).surfaces))
	}
	switch apisurface.NormalizeLang(itemLang) {
	case apisurface.LangJava:
		add(apisurface.RepoInventedMemberReason(g.RepoPath, content))
		if g.APISurface != nil {
			add(apisurface.UnresolvedDependencyReason(ctx, g.APISurface, g.RepoPath, content))
		}
	case apisurface.LangNode:
		// Repo classes reached through relative imports: typed calls, jest.spyOn names and the
		// keys of mocks shaped against the class (`useValue: { create: jest.fn() }`), which tsc
		// cannot check and which fail at run time — asqs-go run api-7425be21b83f608e66318fdd99528522.
		add(apisurface.RepoInventedMemberReasonTS(g.RepoPath, checkPath, content))
	}
	return strings.Join(reasons, "; also ")
}

// retryInventedMembers gives the model one turn to correct the violations named in reason, then
// keeps whatever it returns: one invented member is a compile error the fix loop repairs with the
// member list in front of it, and failing the gap would trade that for no test at all.
func (g *LLMGenerator) retryInventedMembers(
	ctx context.Context,
	item *retrieval.TestPlanItem,
	contextStr, content, path, itemLang string,
	isE2E bool,
	runSinglePass func(string) (string, string, error),
) (string, string, error) {
	extendHeader := ""
	checkPath := path
	if IsExtendExistingContext(contextStr) {
		extendHeader = ExtendTargetImportHeader(contextStr)
		if target := extendTargetPathFromContext(contextStr); target != "" {
			checkPath = target
		}
	}
	reason := g.inventedMemberReason(ctx, content, itemLang, isE2E, extendHeader, checkPath)
	if reason == "" {
		return content, path, nil
	}
	g.auditInventedMember(ctx, item, reason, false)
	retryUser := contextStr + "\n\n---\nAPI retry: your previous output was rejected because it used APIs that do not exist. " +
		"EVERY one of the following must be fixed — a reply that corrects some of them will be written to disk with the rest still broken:\n" + reason + "\n" +
		"Where a message names the package a type actually lives in, use that import verbatim; that is the fully-qualified name read from this project's own compile classpath, and it overrides any import you would otherwise reach for. " +
		"Otherwise use only members that the API SURFACE block or the shown source files actually declare, and only packages the dependency manifest provides. " +
		"If the check you want has no member there, assert it a different way; if a capability needs a missing dependency, test without it — do not invent members or dependencies."
	retried, retriedPath, err := runSinglePass(retryUser)
	if err != nil {
		return "", "", err
	}
	retried = g.repairMemberCase(ctx, retried, item, itemLang, isE2E)
	if !IsExtendExistingContext(contextStr) && retriedPath != "" {
		checkPath = retriedPath
	}
	if reason2 := g.inventedMemberReason(ctx, retried, itemLang, isE2E, extendHeader, checkPath); reason2 != "" {
		g.auditInventedMember(ctx, item, reason2, true)
	}
	return retried, retriedPath, nil
}

// auditInventedMember records a rejection, on both the retry and the give-up, so the trail
// distinguishes "the model corrected itself" from "the model repeated it".
func (g *LLMGenerator) auditInventedMember(ctx context.Context, item *retrieval.TestPlanItem, reason string, final bool) {
	if g.Audit == nil {
		return
	}
	fq := ""
	if item != nil && item.Gap != nil && item.Gap.Symbol != nil {
		fq = item.Gap.Symbol.FQName
	}
	msg := fmt.Sprintf("Generated test rejected before write: %s. Regenerating with the violation named.", reason)
	if final {
		msg = fmt.Sprintf("Generated test still uses a non-existent API after one retry: %s. Writing it anyway — a compile error the fixer can repair beats no artifact.", reason)
	}
	g.Audit.Log(ctx, "generate.invented_member_rejected", map[string]interface{}{
		"message": msg,
		"reason":  reason,
		"fq_name": fq,
		"final":   final,
	})
}
