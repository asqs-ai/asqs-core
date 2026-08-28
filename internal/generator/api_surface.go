// Generate-side API surface: real member signatures for the third-party types a test is about to
// call into, resolved from the project's own build inputs before the model writes anything.
//
// This is the counterpart of the block the fixer gets. Without it the only thing that ever
// contradicts an invented API is a containerised compile — one that costs a full repair round per
// wrong member, and that a small local model may not act on even once told.
package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
)

// apiSurfaceEntry is one resolved lookup: the prompt block, plus the surfaces behind it so a
// post-generation check can be made against the same facts the model was shown.
type apiSurfaceEntry struct {
	block    string
	surfaces []apisurface.TypeSurface
}

// pregenerateAPISurface returns the framework API-surface block for this layer.
//
// Stable per (language, e2e framework, layer) by construction, which is what lets the caller keep
// it in the SYSTEM prompt: the Anthropic client puts its cache_control breakpoint on the whole
// system message, so anything gap-specific there would drop the cache hit rate to zero. Per-symbol
// surfaces go through signatureAPISurface into the user message instead.
//
// Every failure path yields "" — a missing block costs the old behaviour, while a failed
// generation costs the gap.
func (g *LLMGenerator) pregenerateAPISurface(ctx context.Context, lang string, isE2E bool) string {
	return g.pregenerateAPISurfaceEntry(ctx, lang, isE2E).block
}

func (g *LLMGenerator) pregenerateAPISurfaceEntry(ctx context.Context, lang string, isE2E bool) apiSurfaceEntry {
	if g.APISurface == nil {
		return apiSurfaceEntry{}
	}
	targets := apisurface.PregenerateTargets(lang, g.E2EFramework, isE2E)
	key := fmt.Sprintf("framework\x00%s\x00%s\x00%t", apisurface.NormalizeLang(lang), strings.TrimSpace(g.E2EFramework), isE2E)
	return g.resolveAPISurfaceBlock(ctx, key, targets, apiSurfaceScopeFramework, lang, "")
}

// signatureAPISurface returns the API-surface block for the third-party types named in THIS
// symbol's signature, for placement in the user message.
//
// The framework list cannot cover these: it is fixed, and the type a given method takes is not.
// See apisurface.SignatureTargets for the run this closes.
func (g *LLMGenerator) signatureAPISurface(ctx context.Context, item *retrieval.TestPlanItem, lang string) string {
	return g.signatureAPISurfaceEntry(ctx, item, lang).block
}

func (g *LLMGenerator) signatureAPISurfaceEntry(ctx context.Context, item *retrieval.TestPlanItem, lang string) apiSurfaceEntry {
	if g.APISurface == nil || item == nil || item.Gap == nil || item.Gap.Symbol == nil {
		return apiSurfaceEntry{}
	}
	sym := item.Gap.Symbol
	signature := javaSignatureText(sym.SignatureJSON)
	if signature == "" {
		return apiSurfaceEntry{}
	}
	source, err := os.ReadFile(filepath.Join(g.RepoPath, filepath.FromSlash(strings.TrimSpace(sym.File))))
	if err != nil {
		return apiSurfaceEntry{}
	}
	targets := apisurface.SignatureTargets(lang, signature, string(source))
	targets = dropRepoOwnedTargets(g.RepoPath, targets)
	if len(targets) == 0 {
		return apiSurfaceEntry{}
	}
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
	}
	key := "signature\x00" + strings.Join(names, ",")
	return g.resolveAPISurfaceBlock(ctx, key, targets, apiSurfaceScopeSignature, lang, sym.FQName)
}

// resolveAPISurfaceBlock renders targets once per distinct key and caches the result.
func (g *LLMGenerator) resolveAPISurfaceBlock(ctx context.Context, key string, targets []apisurface.Target, scope, lang, fqName string) apiSurfaceEntry {
	if len(targets) == 0 {
		return apiSurfaceEntry{}
	}
	if entry, ok := g.cachedAPISurfaceBlock(key); ok {
		return entry
	}
	v, _, _ := g.apiSurfaceGroup.Do(key, func() (interface{}, error) {
		if entry, ok := g.cachedAPISurfaceBlock(key); ok {
			return entry, nil
		}
		block := ""
		surfaces, err := g.APISurface.Lookup(ctx, g.RepoPath, targets)
		if err != nil || len(surfaces) == 0 {
			surfaces = nil
			reason := "no surfaces resolved"
			if err != nil {
				reason = err.Error()
			}
			g.auditCapabilityDegraded(ctx, "api_surface_pregenerate", reason)
			g.auditPregenerateAPISurface(ctx, lang, scope, fqName, targets, nil, "", reason)
		} else {
			block = apisurface.RenderSurfaces(surfaces)
			g.auditPregenerateAPISurface(ctx, lang, scope, fqName, targets, surfaces, block, "")
		}

		entry := apiSurfaceEntry{block: block, surfaces: surfaces}
		g.apiSurfaceMu.Lock()
		if g.apiSurfaceBlocks == nil {
			g.apiSurfaceBlocks = map[string]apiSurfaceEntry{}
		}
		// A failed resolution is cached too: it is almost always a missing classpath, and retrying
		// it once per gap would add a Maven timeout per gap to a run that is already degraded.
		g.apiSurfaceBlocks[key] = entry
		g.apiSurfaceMu.Unlock()
		return entry, nil
	})
	entry, _ := v.(apiSurfaceEntry)
	return entry
}

// cachedAPISurfaceBlock returns a previously resolved entry for key.
func (g *LLMGenerator) cachedAPISurfaceBlock(key string) (apiSurfaceEntry, bool) {
	g.apiSurfaceMu.Lock()
	defer g.apiSurfaceMu.Unlock()
	entry, ok := g.apiSurfaceBlocks[key]
	return entry, ok
}

const (
	apiSurfaceScopeFramework = "framework"
	apiSurfaceScopeSignature = "signature"
)

// repairMemberCase corrects generated calls that differ from a real classpath member only in
// letter case, using the surfaces this item was actually shown.
//
// Run api-3fdd28e8f16a37247fa6494315ff6176 is the case: the E2E prompt carried
// `APIResponseAssertions (2 member(s), truncated=false)` under "these are the ONLY members that
// exist", and the model wrote `isOk()` for `isOK()` — chai's spelling, in a Java file. Delivering
// the member list was necessary and not sufficient; this settles the residue deterministically
// rather than spending a ~28s compile and an LLM repair round on one character.
//
// Only surfaces from THIS item's prompt are used. A unit gap never sees the Playwright block, so
// its output is never measured against Playwright's members.
func (g *LLMGenerator) repairMemberCase(ctx context.Context, content string, item *retrieval.TestPlanItem, itemLang string, isE2E bool) string {
	if g.APISurface == nil || strings.TrimSpace(content) == "" {
		return content
	}
	var surfaces []apisurface.TypeSurface
	surfaces = append(surfaces, g.pregenerateAPISurfaceEntry(ctx, itemLang, isE2E).surfaces...)
	surfaces = append(surfaces, g.signatureAPISurfaceEntry(ctx, item, itemLang).surfaces...)
	repaired, repairs := apisurface.RepairMemberCase(content, surfaces)
	if len(repairs) == 0 {
		return content
	}
	if g.Audit != nil {
		rendered := make([]string, 0, len(repairs))
		for _, r := range repairs {
			rendered = append(rendered, fmt.Sprintf("%s -> %s on %s (%d site(s))", r.From, r.To, r.FQCN, r.Count))
		}
		fqName := ""
		if item != nil && item.Gap != nil && item.Gap.Symbol != nil {
			fqName = item.Gap.Symbol.FQName
		}
		g.Audit.Log(ctx, "generate.member_case_repaired", map[string]interface{}{
			"message": fmt.Sprintf("Corrected %d generated call(s) whose name differed from a resolved classpath member only in case; "+
				"the complete member list settled it without a compile round.", len(repairs)),
			"fq_name": fqName,
			"repairs": rendered,
		})
	}
	return repaired
}

// javaSignatureText pulls the raw declaration text out of a symbol's signature JSON. The shape is
// the java-indexer's ({"signature": "...", "visibility": ...}); anything else yields "".
func javaSignatureText(sigJSON []byte) string {
	if len(sigJSON) == 0 {
		return ""
	}
	var parsed struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(sigJSON, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Signature)
}

// dropRepoOwnedTargets removes types the repository declares itself.
//
// Retrieval already ships repo source, so a member dump for one is duplicated prompt budget, and
// javap would miss anyway unless the class happens to be compiled. The path convention matches
// repoDeclaredTypeNames in internal/evaluator, which does the same job for the fixer from the
// files-under-repair map — a map that does not exist at generation time, hence the stat.
func dropRepoOwnedTargets(repoPath string, targets []apisurface.Target) []apisurface.Target {
	if strings.TrimSpace(repoPath) == "" || len(targets) == 0 {
		return targets
	}
	roots := []string{"src/main/java", "src/test/java", "src/it/java"}
	out := make([]apisurface.Target, 0, len(targets))
	for _, t := range targets {
		owned := false
		rel := strings.ReplaceAll(t.Name, ".", "/") + ".java"
		for _, root := range roots {
			if _, err := os.Stat(filepath.Join(repoPath, filepath.FromSlash(root+"/"+rel))); err == nil {
				owned = true
				break
			}
		}
		if !owned {
			out = append(out, t)
		}
	}
	return out
}

// auditPregenerateAPISurface records what the generator was actually told about the classpath.
//
// Only the failure path was audited at first, and that made the mechanism unfalsifiable. Run
// api-c3e4a6ea003d0f9b1aeb487b4a8faec6 produced `PageAssertions#hasTitleContaining` — an invented
// member on a type this block is supposed to cover — and the log could not distinguish "the block
// was rendered and the model ignored it" from "the block never rendered". Those two call for
// opposite responses: the first is a prompt-placement or model-capability problem, the second is a
// wiring bug. Recording the resolved types and their member counts on the SUCCESS path is what
// makes the next occurrence diagnosable.
//
// Member counts rather than member text: the block itself can run to thousands of runes and the
// audit is not the place to duplicate it, but "LocatorAssertions (28 members)" is enough to tell
// whether the model was given the answer it then ignored.
func (g *LLMGenerator) auditPregenerateAPISurface(ctx context.Context, lang, scope, fqName string, targets []apisurface.Target, surfaces []apisurface.TypeSurface, block, degradedReason string) {
	if g.Audit == nil {
		return
	}
	requested := make([]string, 0, len(targets))
	for _, t := range targets {
		requested = append(requested, t.Name)
	}
	resolved := make([]string, 0, len(surfaces))
	for _, s := range surfaces {
		resolved = append(resolved, fmt.Sprintf("%s (%d member(s), truncated=%v)", s.FQCN, len(s.Members), s.Truncated))
	}
	payload := map[string]interface{}{
		"lang":          lang,
		"e2e_framework": g.E2EFramework,
		// scope separates the fixed framework list from the per-symbol signature lookup. Without
		// it the two are indistinguishable in the log, and "which list did the model actually
		// get?" is the question this event exists to answer.
		"scope":       scope,
		"requested":   requested,
		"resolved":    resolved,
		"block_runes": len([]rune(block)),
		"rendered":    strings.TrimSpace(block) != "",
	}
	if strings.TrimSpace(fqName) != "" {
		payload["fq_name"] = fqName
	}
	if degradedReason != "" {
		payload["message"] = fmt.Sprintf(
			"Pre-generation API surface (%s) unavailable for %s/%s; the generator runs without it. Reason: %s", scope, lang, g.E2EFramework, degradedReason)
		payload["reason"] = degradedReason
	} else {
		payload["message"] = fmt.Sprintf(
			"Pre-generation API surface (%s) resolved %d of %d requested type(s) and rendered %d rune(s) into the generator prompt.",
			scope, len(surfaces), len(targets), len([]rune(block)))
	}
	g.Audit.Log(ctx, "generate.api_surface", payload)
}
