package evaluator

import (
	"strings"
	"testing"
)

// stripStringsAndComments is what every syntactic-shell check reasons from, so a literal form it
// cannot tokenize is not a cosmetic gap: the text after it is read as code. For C# that failure is
// live and dangerous. `@"C:\"` is legal C# whose content is one backslash, but reading `\"` as an
// escape runs past the real terminator and closes on the NEXT quote, which puts genuine string
// content into code position — and the brace check then refused Roslyn-valid source with
// "unbalanced braces ({=2, }=0)".
//
// Each case asserts the two things a caller depends on: the literal's contents must not survive
// into the stripped text, and the code AFTER it must.
func TestStripStringsAndComments_literalForms(t *testing.T) {
	for name, tc := range map[string]struct {
		ext, src string
		// mustNotSurvive are fragments that live INSIDE the literal and nowhere else. Brackets are
		// deliberately absent — they occur in the surrounding code too, and the balance assertions
		// below are what catch a leaked one.
		mustNotSurvive []string
	}{
		"cs verbatim ending in backslash": {
			".cs", "class C { void M() { var a = @\"C:\\\"; var b = \"x … y ) z\"; F(1); } }",
			[]string{"…"},
		},
		"cs verbatim with doubled quotes": {
			".cs", "class C { void M() { var a = @\"say \"\"hi(\"\"\"; F(1); } }",
			[]string{"hi"},
		},
		"cs verbatim with parens": {
			".cs", "class C { void M() { var a = @\"C:\\a)b(\"; F(1); } }",
			[]string{"C:"},
		},
		"cs raw string": {
			".cs", "class C { void M() { var a = \"\"\"\n   a ) … b\n   \"\"\"; F(1); } }",
			[]string{"…"},
		},
		"cs interpolated verbatim @$": {
			".cs", "class C { void M() { var a = @$\"C:{1}\\\"; var b = \"… )\"; F(1); } }",
			[]string{"…"},
		},
		"cs interpolated verbatim $@": {
			".cs", "class C { void M() { var a = $@\"C:{1}\\\"; var b = \"… )\"; F(1); } }",
			[]string{"…"},
		},
		"java text block": {
			".java", "class C { void m() { var a = \"\"\"\n   a ) … b\n   \"\"\"; f(1); } }",
			[]string{"…"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := stripStringsAndComments(tc.src, tc.ext)
			for _, frag := range tc.mustNotSurvive {
				if strings.Contains(got, frag) {
					t.Errorf("literal content %q leaked into code position:\n  %q", frag, got)
				}
			}
			// The trailing call is the canary: if the scanner ran past the literal's terminator it
			// swallows the rest of the file and this disappears.
			if !strings.Contains(got, "(1)") {
				t.Errorf("code after the literal was swallowed:\n  %q", got)
			}
			if op, cl := strings.Count(got, "{"), strings.Count(got, "}"); op != cl {
				t.Errorf("braces unbalanced after stripping ({=%d }=%d):\n  %q", op, cl, got)
			}
			if op, cl := strings.Count(got, "("), strings.Count(got, ")"); op != cl {
				t.Errorf("parens unbalanced after stripping ((=%d )=%d):\n  %q", op, cl, got)
			}
		})
	}
}

// Go has no raw-quote string form: `""` is an empty string and a third quote opens a new one, so
// the text-block branch must not claim a run of three. Adjacent triple quotes are not valid Go, but
// the stripper runs on unvalidated model output and must not swallow the rest of the file over it.
func TestStripStringsAndComments_goDoesNotTakeTheRawStringBranch(t *testing.T) {
	got := stripStringsAndComments("package p\nfunc f() { a := \"\"\"x\"; g(1) }\n", ".go")
	if !strings.Contains(got, "(1)") {
		t.Errorf("code after the quotes was swallowed: %q", got)
	}
}

// `@` without a quote after it is a keyword-identifier (`@class`, `@event`), not a string.
func TestStripStringsAndComments_csharpAtIdentifierIsNotAString(t *testing.T) {
	got := stripStringsAndComments(`class C { void M() { var @class = 1; F(@class); } }`, ".cs")
	if !strings.Contains(got, "@class") {
		t.Errorf("a keyword-identifier must survive stripping: %q", got)
	}
}
