package indexer

import (
	"strings"
	"testing"
)

// The Roslyn indexer reads .cs and nothing else, so everything a UI test needs to know lived
// outside the index entirely: the route a page answers at, the controls a test can address, and
// the text it can assert on. A generated Razor Pages E2E test had no selector to use and no URL to
// navigate to, and invented both.
func TestCSharpMarkupHooks(t *testing.T) {
	const src = `@page
@model Shop.Pages.Orders.IndexModel

<h1 id="orders-title">Orders</h1>
<form method="post">
    <input asp-for="Order.Sku" class="form-control" />
    <input data-testid="quantity" type="number" />
    <select name="shipping-method"></select>
    <button data-cy="submit-order" type="submit">Place order</button>
    <a asp-page="/Orders/Detail">Detail</a>
</form>
`
	got := CSharpMarkupHooks(src)
	want := map[string]string{
		"orders-title":    "id",
		"Order_Sku":       "asp-for", // asp-for renders id="Order_Sku": the selector is nowhere in the source
		"quantity":        "data-testid",
		"shipping-method": "name",
		"submit-order":    "data-cy",
		"/Orders/Detail":  "asp-page",
	}
	byValue := map[string]string{}
	for _, h := range got {
		byValue[h.Value] = h.Attribute
	}
	for value, attr := range want {
		if byValue[value] != attr {
			t.Errorf("hook %q = attribute %q, want %q (all: %+v)", value, byValue[value], attr, got)
		}
	}
}

// A value computed at render time is not a selector: recording `@Model.Id` would give a test a
// string the page never contains.
func TestCSharpMarkupHooks_skipsComputedValues(t *testing.T) {
	const src = `<input id="@Model.FieldId" />
<input data-testid="@(ctx.Name)" />
<input name="" />
<div class="ok"></div>
`
	if got := CSharpMarkupHooks(src); len(got) != 0 {
		t.Fatalf("CSharpMarkupHooks = %+v, want none", got)
	}
}

// One control yields one hook. A button with both a data-testid and an id must not produce two
// competing answers to "how do I address this".
func TestCSharpMarkupHooks_oneHookPerControl(t *testing.T) {
	const src = `<button id="submit" data-testid="submit-order">Go</button>`
	got := CSharpMarkupHooks(src)
	if len(got) != 1 {
		t.Fatalf("CSharpMarkupHooks = %+v, want exactly one", got)
	}
	if got[0].Attribute != "data-testid" {
		t.Errorf("kept %q, want the more reliable data-testid", got[0].Attribute)
	}
}

// A quoted @page states the route outright; a bare @page defers to the file's location under
// Pages/, which is the same rule the Roslyn side applies to the code-behind — so the two dedupe.
func TestCSharpMarkupRoutes(t *testing.T) {
	cases := []struct {
		name string
		path string
		src  string
		want []string
	}{
		{
			name: "an explicit route with a parameter",
			path: "src/Shop/Pages/Orders/Detail.cshtml",
			src:  "@page \"/orders/{id:int}\"\n<h1>Detail</h1>\n",
			want: []string{"/orders/*"},
		},
		{
			name: "a bare @page falls back to the path",
			path: "src/Shop/Pages/Orders/Detail.cshtml",
			src:  "@page\n<h1>Detail</h1>\n",
			want: []string{"/orders/detail"},
		},
		{
			name: "an Index page answers at its directory too",
			path: "src/Shop/Pages/Orders/Index.cshtml",
			src:  "@page\n<h1>Orders</h1>\n",
			want: []string{"/orders", "/orders/index"},
		},
		{
			name: "a Blazor component route",
			path: "src/Shop/Components/Counter.razor",
			src:  "@page \"/counter\"\n<h1>Counter</h1>\n",
			want: []string{"/counter"},
		},
		{
			name: "a view with no @page declares no route",
			path: "src/Shop/Views/Home/Index.cshtml",
			src:  "<h1>Home</h1>\n",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := csharpMarkupRoutes(tc.path, tc.src)
			var paths []string
			for _, r := range got {
				paths = append(paths, r.path)
			}
			if strings.Join(paths, ",") != strings.Join(tc.want, ",") {
				t.Errorf("routes = %v, want %v", paths, tc.want)
			}
		})
	}
}

// The markup side and the Roslyn side both emit routes; they normalise identically so the shared
// ones collapse rather than competing.
func TestCSharpMarkupRoutes_normaliseLikeTheRoslynSide(t *testing.T) {
	cases := map[string]string{
		"/Orders/{id}":     "/orders/*",
		"/Orders/{id:int}": "/orders/*",
		"Orders":           "/orders",
		"/":                "/",
		"/a//b":            "/a/b",
	}
	for in, want := range cases {
		if got := normalizeCSharpRoutePath(in); got != want {
			t.Errorf("normalizeCSharpRoutePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The enricher adds symbols to a file the Roslyn indexer never saw, without disturbing one it did.
func TestMergeCSharpMarkupIntoMap(t *testing.T) {
	byPath := map[string]*ParsedFile{
		"src/Shop/Pages/Orders/Index.cshtml": {
			Path:   "src/Shop/Pages/Orders/Index.cshtml",
			Source: "@page\n<h1 id=\"orders-title\">Orders</h1>\n<button data-testid=\"add\">Add</button>\n",
		},
		"src/Shop/Pages/Orders/Index.cshtml.cs": {
			Path:    "src/Shop/Pages/Orders/Index.cshtml.cs",
			Lang:    "csharp",
			Source:  "public class IndexModel { }",
			Symbols: []ParsedSymbol{{Kind: "class", FQName: "Shop.Pages.Orders.IndexModel"}},
		},
	}
	if n := MergeCSharpMarkupIntoMap("", byPath); n != 1 {
		t.Fatalf("MergeCSharpMarkupIntoMap enriched %d files, want 1", n)
	}
	markup := byPath["src/Shop/Pages/Orders/Index.cshtml"]
	kinds := map[string]int{}
	for _, s := range markup.Symbols {
		kinds[s.Kind]++
	}
	for kind, want := range map[string]int{"MODULE": 1, "STATIC_TEMPLATE": 1, "UI_TEST_HOOK": 2, "PAGE_ROUTE": 2} {
		if kinds[kind] != want {
			t.Errorf("%s count = %d, want %d (symbols: %+v)", kind, kinds[kind], want, markup.Symbols)
		}
	}
	if markup.Lang != "csharp" {
		t.Errorf("markup lang = %q, want csharp so it joins the run's own chunks", markup.Lang)
	}
	// The code-behind is a different file and is left exactly as the Roslyn indexer produced it.
	code := byPath["src/Shop/Pages/Orders/Index.cshtml.cs"]
	if len(code.Symbols) != 1 {
		t.Errorf("the code-behind was modified: %+v", code.Symbols)
	}
}
