package uitesthooks

import (
	"strings"
	"testing"
)

const catalogTemplate = `<!-- <button>commented out</button> -->
<section class="catalog">
  <h1>Catalog</h1>
  @if (loading) {
    <p>Loading…</p>
  }
  <input placeholder="Search items..." [(ngModel)]="query" />
  <button (click)="search()">Search</button>
  <button data-testid="catalog-reset">Reset</button>
  <ul>
    @for (item of results; track item.id) {
      <li>{{ item.name }}</li>
    }
  </ul>
  <a href="/checkout">{{ 'go' | translate }}</a>
</section>
`

func TestApplyHTML_addsPredictableIdsAndSkipsExistingOnes(t *testing.T) {
	res := ApplyHTML(catalogTemplate, "catalog", 0)
	if !res.Changed {
		t.Fatal("template must change")
	}
	names := map[string]bool{}
	for _, h := range res.Added {
		names[h.Name] = true
	}
	for _, want := range []string{
		"catalog-root",               // the first hookable element
		"catalog-heading-catalog",    // literal heading text
		"catalog-input-search-items", // placeholder attribute
		"catalog-button-search",      // literal button text
		"catalog-list",               // ul
		"catalog-item",               // li inside @for; text is an interpolation, so no slug
		"catalog-link-checkout",      // href attribute because the text is an interpolation
	} {
		if !names[want] {
			t.Errorf("missing %q in %v", want, names)
		}
	}
	if names["catalog-button-reset"] {
		t.Error("an element that already carries data-testid must not get another")
	}
	if res.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the pre-hooked button)", res.Skipped)
	}
	if strings.Contains(res.Source, `<!-- <button data-testid`) {
		t.Error("a commented-out element must not be edited")
	}
	if !strings.Contains(res.Source, `<section data-testid="catalog-root" class="catalog">`) {
		t.Errorf("root attribute placed wrongly:\n%s", res.Source)
	}
	if !strings.Contains(res.Source, `<input data-testid="catalog-input-search-items" placeholder=`) {
		t.Errorf("self-closing input not hooked:\n%s", res.Source)
	}
	// Idempotent: a second pass finds every element already hooked.
	again := ApplyHTML(res.Source, "catalog", 0)
	if again.Changed || len(again.Added) != 0 {
		t.Fatalf("second pass must change nothing; added %v", again.Added)
	}
}

func TestApplyHTML_respectsCapAndAngularBindings(t *testing.T) {
	src := `<div><button>A</button><button>B</button><button [attr.data-testid]="dyn">C</button></div>`
	res := ApplyHTML(src, "x", 2)
	if len(res.Added) != 2 {
		t.Fatalf("cap ignored: %v", res.Added)
	}
	res = ApplyHTML(src, "x", 0)
	if res.Skipped != 1 {
		t.Fatalf("an Angular [attr.data-testid] binding counts as an existing hook; skipped=%d", res.Skipped)
	}
}
