package uitesthooks

import (
	"strings"
	"testing"
)

// This inserter edits a file in the application, not a test, and nothing type-checks a template —
// so a bad edit surfaces as a build failure in production code the run was not asked to change.
// Each case below was verified against the Razor compiler before it was pinned here.
func TestApplyRazor_leavesMarkupInsideCSharpAlone(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		wantChanged bool
	}{
		{
			// Index.cshtml(3,29): error CS1003: Syntax error, ',' expected — the inserted quote
			// terminates the C# string it was written into. @Html.Raw is ordinary MVC markup and is
			// none of the three things the protected ranges covered.
			name: "markup inside a C# string argument",
			src:  "<div>\n@Html.Raw(\"<li>first</li>\")\n</div>\n",
		},
		{
			name: "an explicit expression's string argument",
			src:  "<div>\n@(BuildMarkup(\"<button>Buy</button>\"))\n</div>\n",
		},
		{
			// The brace counter was string-blind, so the `}` inside the first literal closed the
			// block early and the second literal was rewritten.
			name: "a C# string holding a closing brace does not end the code block",
			src: "@code {\n    private string Close = \"}\";\n" +
				"    private string Tpl = \"<button>Buy</button>\";\n}\n",
		},
		{
			name: "a Razor comment still protects what it wraps",
			src:  "@* <button>old</button> *@\n",
		},
		{
			name:        "ordinary markup outside all of them is still hooked",
			src:         "<div>\n  <button>Go</button>\n</div>\n",
			wantChanged: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := ApplyRazor(tc.src, "p", 10)
			if res.Changed != tc.wantChanged {
				t.Fatalf("Changed = %v, want %v; result:\n%s", res.Changed, tc.wantChanged, res.Source)
			}
			if !tc.wantChanged && res.Source != tc.src {
				t.Fatalf("source was edited:\n--- before ---\n%s\n--- after ---\n%s", tc.src, res.Source)
			}
		})
	}
}

// An attribute value can contain '>' — a Blazor lambda handler is the everyday case. Reading the
// attribute text only as far as that '>' hid the hook that was already there, and the element was
// given a second one: Counter.razor(1,8): error RZ10007, the attribute is used two or more times.
func TestApplyRazor_seesExistingHookAfterAnAttributeContainingAngleBracket(t *testing.T) {
	src := "<button @onclick=\"() => Go()\" data-testid=\"already\">Go</button>\n"
	res := ApplyRazor(src, "p", 10)
	if res.Changed {
		t.Fatalf("re-hooked an element that already carries data-testid:\n%s", res.Source)
	}
	if strings.Count(res.Source, "data-testid") != 1 {
		t.Fatalf("element carries a duplicate attribute:\n%s", res.Source)
	}
}

// The same shape in a plain HTML template: an Angular binding whose expression contains '>'.
func TestApplyHTML_seesExistingHookAfterAnAttributeContainingAngleBracket(t *testing.T) {
	src := "<button (click)=\"count > 1 ? more() : less()\" data-testid=\"already\">Go</button>\n"
	if res := ApplyHTML(src, "p", 10); res.Changed {
		t.Fatalf("re-hooked an element that already carries data-testid:\n%s", res.Source)
	}
}
