package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// UISelectorLister is the narrow slice of the metadata store the selector inventory needs.
//
// An interface rather than *metadata.Store for the same reason APISurface is one: the generator
// should be constructible in a test without a database, and the inventory is a fallback — a store
// that cannot answer costs the block, never the gap.
type UISelectorLister interface {
	ListSymbolsByLang(ctx context.Context, repoID, lang, kind string) ([]*metadata.Symbol, error)
}

// uiSelectorHookKind / uiSelectorHookLang are what the JS/TS and Java HTML indexers store for a
// selector found in a template (see tools/js-ts-indexer enrichers-html-hooks.ts, which tags
// lang:"html" and kind:"UI_TEST_HOOK"). Kinds are stored lowercased; ListSymbolsByLang normalizes
// its argument the same way, so the SCREAMING_CASE spelling here is safe.
const (
	uiSelectorHookKind = "UI_TEST_HOOK"
	uiSelectorHookLang = "html"
)

// Caps on the rendered block. A repository with hundreds of data-testid attributes must not push
// the retrieved context out of the prompt; the inventory is an aid, not the payload.
const (
	uiSelectorMaxTemplates       = 12
	uiSelectorMaxPerTemplate     = 40
	uiSelectorMaxBlockRunes      = 4000
	uiSelectorMaxValueRunesShown = 80
)

// uiSelectorInventory renders the repository's indexed UI test selectors for an E2E generation
// prompt, or "" when there is nothing to say.
//
// The problem it solves: run api-c180f4bbc26b841b969a8f787737694d generated
// `page.getByPlaceholder('Search items...')` for a catalog page whose search input carries no
// placeholder at all, while `data-testid="catalog-search-input"` sat extracted and stored in the
// index — UI_TEST_HOOK appeared zero times in the whole audit log. The selectors were indexed and
// simply never shown to the model.
//
// Repository-scoped, not route-scoped, and deliberately so: a PAGE_ROUTE symbol records only
// {framework, path_pattern} (enrichers-page-route-common.pushPageRoute), never the component it
// loads, so there is no route → component → template path to walk. Scoping to the repo sidesteps
// that entirely and stays small — a template's literal selectors are a few dozen bytes each.
//
// Placed in the SYSTEM message by the caller: the block is identical for every gap in a run, which
// is the invariant the system prompt's cache breakpoint depends on.
func (g *LLMGenerator) uiSelectorInventory(ctx context.Context, lang string, isE2E bool) string {
	if !isE2E || g.UISelectors == nil || strings.TrimSpace(g.RepoID) == "" {
		return ""
	}
	if !isJSTSLangForSelectors(lang) {
		return ""
	}
	g.uiSelectorOnce.Do(func() {
		g.uiSelectorBlock = g.buildUISelectorInventory(ctx)
	})
	return g.uiSelectorBlock
}

func isJSTSLangForSelectors(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
		return true
	}
	return false
}

// uiSelector is one indexed selector, flattened from a UI_TEST_HOOK symbol's signature.
type uiSelector struct {
	Kind         string
	Value        string
	TemplatePath string
}

func (g *LLMGenerator) buildUISelectorInventory(ctx context.Context) string {
	syms, err := g.UISelectors.ListSymbolsByLang(ctx, g.RepoID, uiSelectorHookLang, uiSelectorHookKind)
	if err != nil || len(syms) == 0 {
		g.auditUISelectorInventory(ctx, 0, 0, 0, 0, errString(err))
		return ""
	}
	byTemplate := map[string][]uiSelector{}
	seen := map[string]bool{}
	total := 0
	for _, s := range syms {
		if s == nil {
			continue
		}
		sel, ok := uiSelectorFromSignature(s.SignatureJSON)
		if !ok {
			continue
		}
		// The same value can legitimately appear on several elements; the model needs the
		// vocabulary, not the multiplicity.
		dedupe := sel.TemplatePath + "\x00" + sel.Kind + "\x00" + sel.Value
		if seen[dedupe] {
			continue
		}
		seen[dedupe] = true
		byTemplate[sel.TemplatePath] = append(byTemplate[sel.TemplatePath], sel)
		total++
	}
	if len(byTemplate) == 0 {
		g.auditUISelectorInventory(ctx, 0, 0, 0, 0, "no UI_TEST_HOOK signature carried a usable selector")
		return ""
	}
	paths := make([]string, 0, len(byTemplate))
	for p := range byTemplate {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var b strings.Builder
	b.WriteString("\n\n**Indexed UI test selectors (read from this repository's own templates).**\n")
	b.WriteString("These are the selectors that actually exist. Prefer them over any selector you would otherwise infer, ")
	b.WriteString("and do not assert on a `placeholder`, label or test id that is not listed here — a selector absent from this list is one the page does not have. ")
	b.WriteString("A template may also contain selectors built at runtime from an expression, which cannot be listed: prefer a listed container selector over guessing an item selector.\n")

	renderedTemplates, renderedSelectors := 0, 0
	for _, p := range paths {
		if renderedTemplates >= uiSelectorMaxTemplates {
			break
		}
		sels := byTemplate[p]
		sort.Slice(sels, func(i, j int) bool {
			if sels[i].Kind != sels[j].Kind {
				return sels[i].Kind < sels[j].Kind
			}
			return sels[i].Value < sels[j].Value
		})
		byKind := map[string][]string{}
		kinds := []string{}
		for _, s := range sels {
			if len(byKind[s.Kind]) >= uiSelectorMaxPerTemplate {
				continue
			}
			if _, ok := byKind[s.Kind]; !ok {
				kinds = append(kinds, s.Kind)
			}
			byKind[s.Kind] = append(byKind[s.Kind], truncateRunes(s.Value, uiSelectorMaxValueRunesShown))
			renderedSelectors++
		}
		line := "\n" + p + "\n"
		for _, k := range kinds {
			line += "  " + k + ": " + strings.Join(byKind[k], ", ") + "\n"
		}
		if len([]rune(b.String()))+len([]rune(line)) > uiSelectorMaxBlockRunes {
			break
		}
		b.WriteString(line)
		renderedTemplates++
	}
	block := b.String()
	g.auditUISelectorInventory(ctx, len(paths), renderedTemplates, total, renderedSelectors, "")
	return block
}

func uiSelectorFromSignature(sig []byte) (uiSelector, bool) {
	if len(sig) == 0 {
		return uiSelector{}, false
	}
	var m struct {
		SelectorKind string `json:"selector_kind"`
		Value        string `json:"value"`
		TemplatePath string `json:"template_path"`
	}
	if json.Unmarshal(sig, &m) != nil {
		return uiSelector{}, false
	}
	kind := strings.TrimSpace(m.SelectorKind)
	value := strings.TrimSpace(m.Value)
	path := strings.TrimSpace(m.TemplatePath)
	if kind == "" || value == "" || path == "" {
		return uiSelector{}, false
	}
	return uiSelector{Kind: kind, Value: value, TemplatePath: path}, true
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// auditUISelectorInventory records what the model was given, mirroring auditPregenerateAPISurface:
// "did it get the answer and ignore it" must stay answerable from the log alone.
func (g *LLMGenerator) auditUISelectorInventory(ctx context.Context, templates, templatesRendered, found, rendered int, degradedReason string) {
	if g.Audit == nil {
		return
	}
	payload := map[string]interface{}{
		"templates":          templates,
		"templates_rendered": templatesRendered,
		"selectors_found":    found,
		"selectors_rendered": rendered,
		"rendered":           rendered > 0,
	}
	if degradedReason != "" {
		payload["reason"] = degradedReason
		payload["message"] = "UI selector inventory unavailable; the E2E generator runs without it. Reason: " + degradedReason
	} else {
		payload["message"] = fmt.Sprintf(
			"UI selector inventory: %d selector(s) across %d template(s) indexed, %d rendered into the E2E generator prompt.",
			found, templates, rendered)
	}
	g.Audit.Log(ctx, "generate.ui_selector_inventory", payload)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// uiSelectorState is embedded in LLMGenerator; the inventory is repo-wide so one resolve per
// generator instance is enough.
type uiSelectorState struct {
	uiSelectorOnce  sync.Once
	uiSelectorBlock string
}
