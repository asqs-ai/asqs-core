package evaluator

import (
	"strings"
	"testing"
)

func csharpSrc(stmt string) string {
	return "namespace N;\n\npublic class FooTests {\n    [Fact]\n    public void T() {\n" + stmt + "\n    }\n}\n"
}

// The C# arm of the bracket and character checks. Roslyn rejects each of these; nothing between the
// model and the compiler was looking, exactly as on the Java side.
func TestSyntacticShellReason_csharpLexicalDefects(t *testing.T) {
	for name, tc := range map[string]struct{ stmt, want string }{
		"dropped closing paren": {`        if (result.Contains("ABC (NEW)") {` + "\n            Assert.NotNull(result);\n        }", "paren"},
		"smart quotes in code":  {"        Assert.Equal(\u201cABC\u201d, result);", "illegal character"},
		"ellipsis in code":      {`        Assert.Equal(Order("ABC", …), result);`, "illegal character"},
		"stray backslash":       {`        var r = F("ABC");\      Assert.NotNull(r);`, "stray backslash"},
	} {
		t.Run(name, func(t *testing.T) {
			reason := SyntacticShellReason("tests/FooTests.cs", csharpSrc(tc.stmt))
			if reason == "" {
				t.Fatal("Roslyn rejects this; the gate must refuse it before the compiler does")
			}
			if !strings.Contains(reason, tc.want) {
				t.Errorf("reason = %q, want it to name %q", reason, tc.want)
			}
		})
	}
}

// Every row is C# Roslyn accepts, verified against dotnet 10 before this test was written. The
// non-breaking space is the one that makes this NOT a copy of the Java arm: javac rejects U+00A0,
// Roslyn treats it as whitespace, so the Java rule would destroy a correct C# artifact here.
func TestSyntacticShellReason_csharpLexicalDoesNotFalsePositive(t *testing.T) {
	for name, stmt := range map[string]string{
		"unicode identifier":        "        string caf\u00e9 = \"x\";\n        Assert.NotNull(caf\u00e9);",
		"non-breaking space":        "        int x =\u00a01;\n        Assert.Equal(1, x);",
		"ellipsis inside a string":  `        Assert.Equal("a … b", result);`,
		"ellipsis inside a comment": "        // trimmed …\n        Assert.NotNull(result);",
		"unicode escape identifier": `        int \u0041 = 1;`,
		"range index null coalesce": `        var s = arr[1..^1]; int? q = null; q ??= 3;`,
		"lambda and generics":       `        var l = new List<string>(); l.ForEach(x => Assert.NotNull(x));`,
		"parens inside a string":    `        Assert.Equal("a ( b ) ) (", result);`,
	} {
		t.Run(name, func(t *testing.T) {
			if reason := SyntacticShellReason("tests/FooTests.cs", csharpSrc(stmt)); reason != "" {
				t.Errorf("refused valid C#: %q", reason)
			}
		})
	}
}

// Preprocessor directives live outside the class body, so they get their own case.
func TestSyntacticShellReason_csharpAllowsPreprocessorDirectives(t *testing.T) {
	src := "#nullable enable\n#region Tests\nnamespace N;\npublic class FooTests {\n    [Fact] public void T() { Assert.True(true); }\n}\n#endregion\n"
	if reason := SyntacticShellReason("tests/FooTests.cs", src); reason != "" {
		t.Errorf("refused valid C#: %q", reason)
	}
}

// The literal forms that used to defeat the scanner. stripStringsAndComments understands verbatim,
// interpolated-verbatim and raw strings now, so these are JUDGED rather than skipped: valid source
// passes, and a defect sitting beside one of those literals is caught like any other.
//
// The first row is the regression that motivated the stripper fix. It is legal C# — a verbatim
// string whose content is one backslash — and the BRACE check refused it with
// "unbalanced braces ({=2, }=0)", destroying a correct artifact, because reading `\"` as an escape
// ran the scanner past the terminator and turned the rest of the file into string.
func TestSyntacticShellReason_csharpJudgesUntokenizableStringForms(t *testing.T) {
	for name, src := range map[string]string{
		"verbatim ending in a backslash": "namespace N;\n\npublic class FooTests {\n    void M() {\n        var a = @\"C:\\\";\n        var b = \"x … y ) z\";\n        F(1);\n    }\n    void F(int i) {}\n}\n",
		"verbatim with parens inside":    "namespace N;\n\npublic class FooTests {\n    void M() {\n        var p = @\"C:\\a)b(\";\n        F(1);\n    }\n    void F(int i) {}\n}\n",
		"interpolated verbatim @$":       "namespace N;\n\npublic class FooTests {\n    void M() {\n        var a = @$\"C:{1}\\\";\n        var b = \"… )\";\n    }\n}\n",
		"interpolated verbatim $@":       "namespace N;\n\npublic class FooTests {\n    void M() {\n        var a = $@\"C:{1}\\\";\n        var b = \"… )\";\n    }\n}\n",
		"raw string literal":             "namespace N;\n\npublic class FooTests {\n    void M() {\n        var p = \"\"\"\n            a ) … b\n            \"\"\";\n        F(1);\n    }\n    void F(int i) {}\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if reason := SyntacticShellReason("tests/FooTests.cs", src); reason != "" {
				t.Errorf("refused valid C#: %q", reason)
			}
		})
	}
}

// The coverage the decline used to cost: a defect NEXT TO one of those literals is now caught
// instead of waved through.
func TestSyntacticShellReason_csharpCatchesDefectsBesideVerbatimStrings(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"dropped paren after a verbatim string": {
			"namespace N;\n\npublic class FooTests {\n    void M() {\n        var p = @\"C:\\\";\n        if (p.Contains(\"C\") {\n            F(1);\n        }\n    }\n    void F(int i) {}\n}\n",
			"paren",
		},
		"stray character after a verbatim string": {
			"namespace N;\n\npublic class FooTests {\n    void M() {\n        var p = @\"C:\\\";\n        F(1, …);\n    }\n    void F(int i, int j) {}\n}\n",
			"illegal character",
		},
	} {
		t.Run(name, func(t *testing.T) {
			reason := SyntacticShellReason("tests/FooTests.cs", tc.src)
			if !strings.Contains(reason, tc.want) {
				t.Errorf("reason = %q, want it to name %q", reason, tc.want)
			}
		})
	}
}
