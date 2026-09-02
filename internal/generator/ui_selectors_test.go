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
