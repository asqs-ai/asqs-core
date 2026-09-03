package generator

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

type fakeSelectorLister struct {
	syms []*metadata.Symbol
	err  error
	got  struct{ repoID, lang, kind string }
	// calls counts queries so the once-per-generator contract is testable.
	calls int
}

func (f *fakeSelectorLister) ListSymbolsByLang(_ context.Context, repoID, lang, kind string) ([]*metadata.Symbol, error) {
	f.calls++
	f.got.repoID, f.got.lang, f.got.kind = repoID, lang, kind
	return f.syms, f.err
}

func hook(path, kind, value string) *metadata.Symbol {
	return &metadata.Symbol{
		Kind:          "ui_test_hook",
		Lang:          "html",
		File:          path,
		SignatureJSON: []byte(`{"selector_kind":"` + kind + `","value":"` + value + `","framework":"html","template_path":"` + path + `"}`),
	}
}

func angularHooks() []*metadata.Symbol {
	return []*metadata.Symbol{
		hook("src/app/features/catalog/catalog.component.html", "data-testid", "catalog-root"),
		hook("src/app/features/catalog/catalog.component.html", "data-testid", "catalog-search-input"),
		hook("src/app/features/catalog/catalog.component.html", "data-testid", "catalog-results"),
		// Same value twice: the model needs the vocabulary, not the multiplicity.
		hook("src/app/features/catalog/catalog.component.html", "data-testid", "catalog-results"),
		hook("src/app/features/checkout/checkout.component.html", "data-testid", "checkout-title"),
	}
}

// The failure this closes: run api-c180f4bbc26b841b969a8f787737694d generated
// getByPlaceholder('Search items...') for a page with no placeholder at all, while
// data-testid="catalog-search-input" sat extracted in the index and UI_TEST_HOOK appeared zero
// times in the whole audit log.
func TestUISelectorInventory_rendersIndexedSelectorsForE2E(t *testing.T) {
	lister := &fakeSelectorLister{syms: angularHooks()}
	g := &LLMGenerator{UISelectors: lister, RepoID: "org/repo"}

	block := g.uiSelectorInventory(context.Background(), "typescript", true)
	if block == "" {
		t.Fatal("no block rendered")
	}
	for _, want := range []string{
		"catalog-search-input",
		"catalog-results",
		"checkout-title",
		"src/app/features/catalog/catalog.component.html",
		"src/app/features/checkout/checkout.component.html",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q:\n%s", want, block)
		}
	}
	if strings.Count(block, "catalog-results") != 1 {
		t.Errorf("duplicate selector was not deduped:\n%s", block)
	}
	if lister.got.lang != "html" || lister.got.kind != uiSelectorHookKind || lister.got.repoID != "org/repo" {
		t.Errorf("queried (%q,%q,%q), want (org/repo, html, %s)", lister.got.repoID, lister.got.lang, lister.got.kind, uiSelectorHookKind)
	}
}

// Repo-wide and identical for every gap, so it must resolve once — the system prompt's cache
// breakpoint depends on the block being stable, and the store should not be re-queried per gap.
func TestUISelectorInventory_resolvesOncePerGenerator(t *testing.T) {
	lister := &fakeSelectorLister{syms: angularHooks()}
	g := &LLMGenerator{UISelectors: lister, RepoID: "org/repo"}

	first := g.uiSelectorInventory(context.Background(), "typescript", true)
	second := g.uiSelectorInventory(context.Background(), "typescript", true)
	if first != second {
		t.Error("block is not stable across gaps; the system-prompt cache breakpoint depends on it")
	}
	if lister.calls != 1 {
		t.Errorf("store queried %d times, want 1", lister.calls)
	}
}

// Everything that must produce no block and no query.
func TestUISelectorInventory_silentWhenNotApplicable(t *testing.T) {
	cases := map[string]struct {
		gen   *LLMGenerator
		lang  string
		isE2E bool
	}{
		"unit item":      {&LLMGenerator{UISelectors: &fakeSelectorLister{syms: angularHooks()}, RepoID: "r"}, "typescript", false},
		"non js/ts":      {&LLMGenerator{UISelectors: &fakeSelectorLister{syms: angularHooks()}, RepoID: "r"}, "java", true},
		"no store":       {&LLMGenerator{RepoID: "r"}, "typescript", true},
		"no repo id":     {&LLMGenerator{UISelectors: &fakeSelectorLister{syms: angularHooks()}}, "typescript", true},
		"empty index":    {&LLMGenerator{UISelectors: &fakeSelectorLister{}, RepoID: "r"}, "typescript", true},
		"store errors":   {&LLMGenerator{UISelectors: &fakeSelectorLister{err: context.DeadlineExceeded}, RepoID: "r"}, "typescript", true},
		"junk signature": {&LLMGenerator{UISelectors: &fakeSelectorLister{syms: []*metadata.Symbol{{Kind: "ui_test_hook", SignatureJSON: []byte(`{"nope":1}`)}}}, RepoID: "r"}, "typescript", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.gen.uiSelectorInventory(context.Background(), tc.lang, tc.isE2E); got != "" {
				t.Errorf("rendered a block when it should not have:\n%s", got)
			}
		})
	}
}

// A repository with a large selector surface must not push the retrieved context out of the prompt.
func TestUISelectorInventory_capsTheBlock(t *testing.T) {
	var syms []*metadata.Symbol
	for i := 0; i < 40; i++ {
		path := "src/app/t" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".component.html"
		for j := 0; j < 60; j++ {
			syms = append(syms, hook(path, "data-testid", "sel-"+string(rune('a'+j%26))+"-"+string(rune('0'+j/26))))
		}
	}
	g := &LLMGenerator{UISelectors: &fakeSelectorLister{syms: syms}, RepoID: "r"}

	block := g.uiSelectorInventory(context.Background(), "typescript", true)
	if n := len([]rune(block)); n > uiSelectorMaxBlockRunes+512 {
		t.Errorf("block is %d runes, want it capped near %d", n, uiSelectorMaxBlockRunes)
	}
	if block == "" {
		t.Error("cap must trim the block, not suppress it")
	}
}

// Listing a bare attribute name invites the model to invent a query method for it. Run
// api-7e4930f7306db0a480d4ced6c4107ede took the real data-cy value this block had listed and wrote
// `getByTestId('checkout-line-total').getByCy('checkout-total-amount')` — getByCy is not a
// Playwright API. The block must name the CALL, not just the value.
func TestUISelectorInventory_rendersTheQueryFormNotTheAttribute(t *testing.T) {
	syms := []*metadata.Symbol{
		hook("src/app/features/checkout/checkout.component.html", "data-testid", "checkout-title"),
		hook("src/app/features/checkout/checkout.component.html", "data-cy", "checkout-total-amount"),
		// A Thymeleaf th:testid is recorded with selector_kind "testid"; it has no dedicated
		// Playwright getter either.
		hook("src/app/features/checkout/checkout.component.html", "testid", "legacy-total"),
	}

	t.Run("playwright", func(t *testing.T) {
		g := &LLMGenerator{UISelectors: &fakeSelectorLister{syms: syms}, RepoID: "r", E2EFramework: "playwright"}
		block := g.uiSelectorInventory(context.Background(), "typescript", true)
		// data-testid is Playwright's DEFAULT testIdAttribute, so the idiomatic getter is correct.
		if !strings.Contains(block, `getByTestId('checkout-title')`) {
			t.Errorf("want getByTestId for data-testid:\n%s", block)
		}
		// Everything else must use the attribute-selector form, which is correct whatever the
		// repository configured.
		for _, want := range []string{
			`locator('[data-cy="checkout-total-amount"]')`,
			`locator('[testid="legacy-total"]')`,
		} {
			if !strings.Contains(block, want) {
				t.Errorf("want %s:\n%s", want, block)
			}
		}
		if strings.Contains(block, "getByCy") {
			t.Errorf("block suggests a getter that does not exist:\n%s", block)
		}
		// The bare attribute name must not appear as a standalone label any more.
		if strings.Contains(block, "\n  data-cy: ") || strings.Contains(block, "\n  data-testid: ") {
			t.Errorf("block still lists raw attribute labels:\n%s", block)
		}
	})

	t.Run("cypress", func(t *testing.T) {
		g := &LLMGenerator{UISelectors: &fakeSelectorLister{syms: syms}, RepoID: "r", E2EFramework: "cypress"}
		block := g.uiSelectorInventory(context.Background(), "typescript", true)
		for _, want := range []string{
			`cy.get('[data-testid="checkout-title"]')`,
			`cy.get('[data-cy="checkout-total-amount"]')`,
		} {
			if !strings.Contains(block, want) {
				t.Errorf("want %s:\n%s", want, block)
			}
		}
		if strings.Contains(block, "getByTestId") {
			t.Errorf("Playwright getter leaked into a Cypress block:\n%s", block)
		}
	})
}

// The model read the template paths out of this block and passed them to get_symbol, which fails:
// the indexed symbols are named STATIC_TEMPLATE:<path>, not <path>. Three wasted tool calls in run
// api-7e4930f7306db0a480d4ced6c4107ede.
func TestUISelectorInventory_saysHeadingsArePathsNotSymbols(t *testing.T) {
	g := &LLMGenerator{UISelectors: &fakeSelectorLister{syms: angularHooks()}, RepoID: "r"}
	block := g.uiSelectorInventory(context.Background(), "typescript", true)
	if !strings.Contains(block, "get_symbol") {
		t.Errorf("block does not warn that the headings are paths, not symbols:\n%s", block)
	}
}

func lifecycleHook(path, value string, conditional, repeated bool) *metadata.Symbol {
	sig := `{"selector_kind":"data-testid","value":"` + value + `","framework":"html","template_path":"` + path +
		`","conditional":` + boolLit(conditional) + `,"repeated":` + boolLit(repeated) + `}`
	return &metadata.Symbol{Kind: "ui_test_hook", Lang: "html", File: path, SignatureJSON: []byte(sig)}
}

func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// A flat list answers "what exists" and says nothing about "when". Every remaining E2E failure in
// run api-c81d90a22d1460d87b64e483837fdc24 was a REAL selector asserted at the wrong moment:
// `catalog-loading` lives inside `@if (loading)` and is gone once the search resolves.
func TestUISelectorInventory_annotatesConditionalAndRepeatedSelectors(t *testing.T) {
	const tpl = "src/app/features/catalog/catalog.component.html"
	g := &LLMGenerator{RepoID: "r", E2EFramework: "playwright", UISelectors: &fakeSelectorLister{syms: []*metadata.Symbol{
		lifecycleHook(tpl, "catalog-results", false, false),
		lifecycleHook(tpl, "catalog-loading", true, false),
		lifecycleHook(tpl, "catalog-item-price", true, true),
	}}}

	block := g.uiSelectorInventory(context.Background(), "typescript", true)

	for _, line := range strings.Split(block, "\n") {
		switch {
		case strings.Contains(line, "catalog-loading"):
			if !strings.Contains(line, "@if") || strings.Contains(line, "@for") {
				t.Errorf("conditional selector must be marked as @if-gated: %q", line)
			}
			// "drive the app into that state" was the first wording and run
			// api-620c78444155f43a6afdc9587c097eae showed it is unfollowable: a loading flag
			// cleared when the request settles lasts microseconds against a dev server with no
			// backend. Only holding the response open makes the assertion mean anything.
			if !strings.Contains(line, "page.route(") {
				t.Errorf("a conditional selector gated on a request must recommend interception: %q", line)
			}
		case strings.Contains(line, "catalog-item-price"):
			if !strings.Contains(line, "@for") {
				t.Errorf("repeated selector must be marked as @for-repeated: %q", line)
			}
			if !strings.Contains(line, "empty") {
				t.Errorf("a repeated selector must say it is absent when the collection is empty: %q", line)
			}
			if !strings.Contains(line, "page.route(") {
				t.Errorf("a repeated selector needs a populated collection, which means interception: %q", line)
			}
		case strings.Contains(line, "catalog-results"):
			// Always present: no note, or the model is told to wait for a state that never changes.
			if strings.Contains(line, "@if") || strings.Contains(line, "@for") {
				t.Errorf("an unconditional selector must carry no lifecycle note: %q", line)
			}
		}
	}

	// The empty-container case is not expressible per selector, so the header has to carry it.
	if !strings.Contains(block, "no size while the collection is empty") {
		t.Errorf("header must warn that an empty repeated container is attached but not visible:\n%s", block)
	}
}

// Signatures written before the indexer recorded lifecycle simply have no flags, and must render
// exactly as they did.
func TestUISelectorInventory_toleratesSignaturesWithoutLifecycle(t *testing.T) {
	g := &LLMGenerator{RepoID: "r", UISelectors: &fakeSelectorLister{syms: angularHooks()}}
	block := g.uiSelectorInventory(context.Background(), "typescript", true)
	if strings.Contains(block, "@if") || strings.Contains(block, "@for") {
		t.Errorf("absent flags must not produce a lifecycle note:\n%s", block)
	}
	if !strings.Contains(block, "getByTestId('catalog-search-input')") {
		t.Errorf("selectors must still render:\n%s", block)
	}
}
