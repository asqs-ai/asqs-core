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

// uiSelectorQueryForm renders how to ACTUALLY query one selector in the run's E2E framework.
//
// Listing the bare attribute name was not enough and was actively harmful. Run
// api-7e4930f7306db0a480d4ced6c4107ede took `checkout-total-amount` — a real `data-cy` value this
// block had correctly listed — and reached for `getByCy(...)`, invented by analogy with the
// `getByTestId(...)` it knew, which is not a Playwright API at all. Naming the selector without
// naming the call is an invitation to guess the call.
//
// `getByTestId` is emitted only for `data-testid`, and only because that is Playwright's default
// testIdAttribute — the value the bootstrap's own generated config leaves in place. Every other
// attribute gets the attribute-selector form, which is correct whatever a repository has
// configured.
func uiSelectorQueryForm(e2eFramework, kind, value string) string {
	attr := fmt.Sprintf("[%s=%q]", kind, value)
	if strings.EqualFold(strings.TrimSpace(e2eFramework), "cypress") {
		return fmt.Sprintf("cy.get('%s')", attr)
	}
	if strings.EqualFold(kind, "data-testid") {
		return fmt.Sprintf("getByTestId('%s')", value)
	}
	return fmt.Sprintf("locator('%s')", attr)
}

// uiSelectorLifecycleNote says WHEN the element exists, or "" for one that is simply always there.
//
// A flat list of selectors answers "what exists" and says nothing about "when", and every remaining
// E2E failure in run api-c81d90a22d1460d87b64e483837fdc24 was a real selector asserted at the wrong
// moment: `catalog-loading` sits inside `@if (loading)` and is gone the instant the search resolves,
// so `toBeVisible()` on it can only pass by luck.
//
// The advice names route interception rather than "drive the app into that state", which is what
// this note said first and what run api-620c78444155f43a6afdc9587c097eae showed to be unfollowable.
// A loading flag is set on click and cleared when the request settles; against a dev server with no
// backend the request fails on the next tick, so the state exists for microseconds and no amount of
// driving reaches it. Holding the response open is the only way the assertion can be made to mean
// anything, and it is also how a repeated selector gets a non-empty collection to be repeated over.
func uiSelectorLifecycleNote(s uiSelector) string {
	switch {
	case s.Repeated:
		return "   — one per item in an @for / *ngFor; absent entirely when the collection is empty, " +
			"so populate the collection (intercept the request with page.route(...) and fulfil it) before asserting"
	case s.Conditional:
		return "   — only present while its @if / *ngIf condition holds. If that condition is a pending " +
			"request, hold the request open with page.route(...) rather than racing it; a state that ends when " +
			"the request settles cannot be observed by polling"
	}
	return ""
}

// uiSelector is one indexed selector, flattened from a UI_TEST_HOOK symbol's signature.
type uiSelector struct {
	Kind         string
	Value        string
	TemplatePath string
	// Conditional / Repeated describe WHEN the element is on the page, which the selector alone
	// cannot say. The HTML indexer derives them from the template's Angular control flow.
	Conditional bool
	Repeated    bool
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
	b.WriteString("Each entry below is written in the form you should call — copy it verbatim rather than inventing a query method for the attribute. ")
	b.WriteString("These are the selectors that actually exist: do not assert on a `placeholder`, label or test id that is not listed here, because a selector absent from this list is one the page does not have. ")
	b.WriteString("A template may also contain selectors built at runtime from an expression, which cannot be listed: prefer a listed container selector over guessing an item selector. ")
	b.WriteString("Note that a container whose only contents are repeated is in the DOM but has no size while the collection is empty, so `toBeVisible()` on it fails until something has been rendered into it — assert on its contents, or on the container being attached. ")
	b.WriteString("The headings are template FILE PATHS, not indexed symbols — do not pass them to get_symbol.\n")

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
		line := "\n" + p + "\n"
		perTemplate := 0
		for _, s := range sels {
			if perTemplate >= uiSelectorMaxPerTemplate {
				break
			}
			line += "  " + uiSelectorQueryForm(g.E2EFramework, s.Kind, truncateRunes(s.Value, uiSelectorMaxValueRunesShown)) +
				uiSelectorLifecycleNote(s) + "\n"
			perTemplate++
			renderedSelectors++
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
		Conditional  bool   `json:"conditional"`
		Repeated     bool   `json:"repeated"`
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
	return uiSelector{Kind: kind, Value: value, TemplatePath: path, Conditional: m.Conditional, Repeated: m.Repeated}, true
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
	// The unserved-backend notice is resolved the same way and for the same reason: it is a
	// property of the repository, identical for every gap in the run.
	e2eBackendOnce  sync.Once
	e2eBackendBlock string
}
