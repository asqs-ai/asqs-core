package uitesthooks

import (
	"strings"
	"testing"
)

// Razor markup is HTML with C# woven through it, and the C# is invisible to an HTML scanner.
// Editing inside it produces a file that does not compile — and unlike a JSX edit nothing
// type-checks a template, so the damage surfaces as a build failure in a production project the run
// was never asked to change.
func TestApplyRazor_neverEditsCSharp(t *testing.T) {
	const src = `@page
@model Shop.Pages.Orders.IndexModel

@* <button>this is a comment, not markup</button> *@

<h1>Orders</h1>
<button type="submit">Place order</button>

@code {
    private string Markup = "<button>inside a C# string</button>";
    private void Nested() { if (true) { Console.WriteLine("<a href=\"x\">y</a>"); } }
}
`
	got := ApplyRazor(src, "orders", 0)
	if !got.Changed {
		t.Fatal("nothing was hooked in a page that has a button")
	}
	// The real button gained a hook.
	// The attribute is inserted after the tag NAME, which is where the shared scanner puts it.
	if !strings.Contains(got.Source, `<button data-testid=`) {
		t.Errorf("the real button was not hooked:\n%s", got.Source)
	}
	// Nothing inside the comment or the C# block moved.
	for _, untouched := range []string{
		`@* <button>this is a comment, not markup</button> *@`,
		`private string Markup = "<button>inside a C# string</button>";`,
		`Console.WriteLine("<a href=\"x\">y</a>");`,
	} {
		if !strings.Contains(got.Source, untouched) {
			t.Errorf("a protected region was edited; expected to still contain:\n%s\n\ngot:\n%s", untouched, got.Source)
		}
	}
}

// A @code body routinely contains nested braces. Matching to the first `}` would leave the rest of
// the C# exposed to the element scan.
func TestApplyRazor_codeBlockBracesAreBalanced(t *testing.T) {
	const src = `<h1>Counter</h1>
@code {
    private void A() { }
    private string Html = "<button>x</button>";
}
<button>Real</button>
`
	got := ApplyRazor(src, "counter", 0)
	if strings.Contains(got.Source, `"<button data-testid=`) {
		t.Errorf("hooked inside a C# string after a nested brace:\n%s", got.Source)
	}
	if !strings.Contains(got.Source, `<button data-testid="counter-button-real">Real</button>`) &&
		!strings.Contains(got.Source, `<button data-testid=`) {
		t.Errorf("the real button after the code block was not hooked:\n%s", got.Source)
	}
}

// This edits a file in the APPLICATION, not a test. A pass that kept appending attributes would
// corrupt it a little more each run.
func TestApplyRazor_isIdempotent(t *testing.T) {
	const src = `<h1>Orders</h1>
<button type="submit">Place order</button>
<a href="/orders/new">New</a>
`
	first := ApplyRazor(src, "orders", 0)
	if !first.Changed {
		t.Fatal("the first pass changed nothing")
	}
	second := ApplyRazor(first.Source, "orders", 0)
	if second.Changed {
		t.Errorf("the second pass changed the file again:\n%s", second.Source)
	}
	if second.Source != first.Source {
		t.Errorf("the second pass rewrote the file:\n%s", second.Source)
	}
	if second.Skipped == 0 {
		t.Error("the second pass reported nothing skipped, so it did not recognise its own hooks")
	}
}

// A code-behind is C#, not markup, however much its name resembles one.
func TestRazorFileIsHookable(t *testing.T) {
	for _, p := range []string{
		"src/Shop/Pages/Orders/Index.cshtml",
		"src/Shop/Views/Home/Index.cshtml",
		"src/Shop/Components/Counter.razor",
		"SRC/SHOP/PAGES/INDEX.CSHTML",
	} {
		if !RazorFileIsHookable(p) {
			t.Errorf("RazorFileIsHookable(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"src/Shop/Pages/Orders/Index.cshtml.cs",
		"src/Shop/Program.cs",
		"src/app/page.tsx",
		"",
	} {
		if RazorFileIsHookable(p) {
			t.Errorf("RazorFileIsHookable(%q) = true, want false", p)
		}
	}
}

// An unbalanced brace means a template nothing should be editing. Protecting to the end of the file
// is the conservative answer; guessing where the block stops is not.
func TestApplyRazor_unbalancedCodeBlockProtectsTheRest(t *testing.T) {
	const src = `<button>Before</button>
@code {
    private void Broken() {
<button>After</button>
`
	got := ApplyRazor(src, "x", 0)
	if strings.Contains(got.Source, `<button data-testid="x-button-after"`) {
		t.Errorf("edited after an unbalanced code block:\n%s", got.Source)
	}
	// The element before the block is still fair game.
	if !strings.Contains(got.Source, `data-testid=`) {
		t.Errorf("nothing before the broken block was hooked:\n%s", got.Source)
	}
}

// The classifier is what routes a file to the Razor inserter at all. Gated on the same Templates
// option the Angular templates use: both edit a file in the APPLICATION rather than a test.
func TestClassify_razor(t *testing.T) {
	on := Options{Templates: true}
	off := Options{}
	for _, rel := range []string{
		"src/Shop/Pages/Orders/Index.cshtml",
		"src/Shop/Components/Counter.razor",
	} {
		if got := targetKind(rel, on); got != "razor" {
			t.Errorf("targetKind(%q, templates on) = %q, want razor", rel, got)
		}
		if got := targetKind(rel, off); got != "" {
			t.Errorf("targetKind(%q, templates off) = %q, want nothing", rel, got)
		}
	}
	// A code-behind is C#, and the JSX/HTML shapes are unaffected.
	if got := targetKind("src/Shop/Pages/Orders/Index.cshtml.cs", on); got != "" {
		t.Errorf("targetKind(code-behind) = %q, want nothing", got)
	}
	if got := targetKind("src/app/hero.component.html", on); got != "html" {
		t.Errorf("targetKind(angular template) = %q, want html", got)
	}
	if got := targetKind("src/app/Page.tsx", on); got != "jsx" {
		t.Errorf("targetKind(tsx) = %q, want jsx", got)
	}
}
