package evaluator

import (
	"strings"
	"testing"
)

// javaFile wraps a body in a minimal compilable-looking Java shell.
func javaFile(body string) string {
	return "package p;\n\nclass OwnerControllerE2EIT {\n\t@Test\n\tvoid t() {\n\t\t" + body + "\n\t}\n}\n"
}

func csFile(body string) string {
	return "namespace P;\n\npublic class OwnerControllerE2ETests\n{\n\tpublic void T()\n\t{\n\t\t" + body + "\n\t}\n}\n"
}

// TestIllegalEscapeReason_RejectsJava covers the escape shapes a model actually emits when it
// forgets that a Java string needs the backslash doubled.
func TestIllegalEscapeReason_RejectsJava(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"regex digit class", `assertThat(s).matches("\d+");`},
		{"regex word class", `Pattern.compile("^\w+$");`},
		{"escaped forward slash", `page.locator("a[href=\/owners]");`},
		{"windows path", `Path.of("C:\temp\out.txt");`},
		{"char literal", `char c = '\d';`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := IllegalEscapeReason("src/test/java/p/OwnerControllerE2EIT.java", javaFile(tc.body))
			if reason == "" {
				t.Fatalf("expected rejection for %s", tc.body)
			}
			if !strings.Contains(reason, "illegal escape character") {
				t.Errorf("reason = %q, want it to name the illegal escape", reason)
			}
		})
	}
}

// TestIllegalEscapeReason_AcceptsLegalJava is the false-positive guard. Every one of these compiles,
// and rejecting any of them would silently discard a correct repair — strictly worse than the bug
// this gate exists to catch.
func TestIllegalEscapeReason_AcceptsLegalJava(t *testing.T) {
	bodies := []string{
		`String s = "line\nnext";`,
		`String s = "tab\there";`,
		`String s = "quote\"inside";`,
		`String s = "back\\slash";`,
		`Pattern.compile("\\d+");`,
		`String s = "unicode\u0041";`,
		`String s = "octal\101 and \0 and \377";`,
		`String s = "space\sescape";`,
		`String s = "bell\b form\f cr\r";`,
		`char c = '\'';`,
		`char c = '\\';`,
		`String empty = "";`,
		`String s = "no escapes at all";`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			if reason := IllegalEscapeReason("src/test/java/p/OwnerControllerE2EIT.java", javaFile(body)); reason != "" {
				t.Errorf("false positive on legal Java %q: %s", body, reason)
			}
		})
	}
}

// TestIllegalEscapeReason_BailsOnUntokenizable pins the conservative direction: constructs this
// scanner does not model must yield "" rather than a guess.
func TestIllegalEscapeReason_BailsOnUntokenizable(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
	}{
		{
			name:    "java text block",
			path:    "src/test/java/p/OwnerControllerE2EIT.java",
			content: javaFile("String s = \"\"\"\n\t\t\tC:\\dir raw text\n\t\t\t\"\"\";"),
		},
		{
			name:    "csharp verbatim string",
			path:    "tests/P/OwnerControllerE2ETests.cs",
			content: csFile(`var p = @"C:\dir\out.txt";`),
		},
		{
			name:    "csharp verbatim interpolated at-dollar",
			path:    "tests/P/OwnerControllerE2ETests.cs",
			content: csFile(`var p = @$"C:\dir\{name}.txt";`),
		},
		{
			name:    "csharp verbatim interpolated dollar-at",
			path:    "tests/P/OwnerControllerE2ETests.cs",
			content: csFile(`var p = $@"C:\dir\{name}.txt";`),
		},
		{
			name:    "trailing backslash before newline",
			path:    "src/test/java/p/OwnerControllerE2EIT.java",
			content: javaFile("String s = \"dangling\\"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if reason := illegalEscapeReason(tc.content, extOf(tc.path)); reason != "" {
				t.Errorf("expected a bail-out (no finding), got %q", reason)
			}
		})
	}
}

// TestIllegalEscapeReason_SkipsComments pins that a backslash in prose is not source.
func TestIllegalEscapeReason_SkipsComments(t *testing.T) {
	content := javaFile("// a windows path C:\\dir and a regex \\d+\n\t\t/* block \\d also */\n\t\tint x = 1;")
	if reason := IllegalEscapeReason("src/test/java/p/OwnerControllerE2EIT.java", content); reason != "" {
		t.Errorf("false positive from comment text: %s", reason)
	}
}

// TestIllegalEscapeReason_CSharp covers the C#-specific escape set.
func TestIllegalEscapeReason_CSharp(t *testing.T) {
	legal := []string{
		`var s = "vert\vtab";`,
		`var s = "alert\a";`,
		`var s = "hex\x41";`,
		`var s = "wide\U0001F600";`,
		`var s = $"interpolated\nvalue {x}";`,
	}
	for _, body := range legal {
		if reason := IllegalEscapeReason("tests/P/OwnerControllerE2ETests.cs", csFile(body)); reason != "" {
			t.Errorf("false positive on legal C# %q: %s", body, reason)
		}
	}
	if reason := IllegalEscapeReason("tests/P/OwnerControllerE2ETests.cs", csFile(`var s = $"bad\d escape";`)); reason == "" {
		t.Error("expected rejection of \\d in a non-verbatim interpolated C# string")
	}
	// \s is legal in Java but NOT in C#; the two tables must not be shared.
	if reason := IllegalEscapeReason("tests/P/OwnerControllerE2ETests.cs", csFile(`var s = "space\sescape";`)); reason == "" {
		t.Error("expected rejection of \\s in C#, which has no such escape")
	}
}

// TestIllegalEscapeReason_ReportsLine pins that the operator is told where to look.
func TestIllegalEscapeReason_ReportsLine(t *testing.T) {
	content := "package p;\n\n\n\nclass C {\n\tvoid t() {\n\t\tString s = \"\\d\";\n\t}\n}\n"
	reason := illegalEscapeReason(content, ".java")
	if !strings.Contains(reason, "line 7") {
		t.Errorf("reason = %q, want it to report line 7", reason)
	}
}

// TestIllegalEscapeReason_OtherLanguagesUnaffected pins that the gate is Java/C# only, so Go and
// TS artifacts keep their existing behaviour exactly.
func TestIllegalEscapeReason_OtherLanguagesUnaffected(t *testing.T) {
	for _, ext := range []string{".go", ".ts", ".js", ""} {
		if reason := illegalEscapeReason(`x := "\d"`, ext); reason != "" {
			t.Errorf("ext %q must be unchecked, got %q", ext, reason)
		}
	}
}

// extOf mirrors SyntacticShellReason's dispatch so the bail-out cases can call the validator
// directly without duplicating the switch.
func extOf(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return strings.ToLower(path[i:])
	}
	return ""
}

// TestSyntacticShellReason_doesNotGateOnEscapes is the regression guard for run
// api-c3e4a6ea003d0f9b1aeb487b4a8faec6.
//
// SyntacticShellReason runs in BOTH write paths, including writeGeneratedFiles. Folding the escape
// check into it meant a generated artifact carrying one bad escape was never written at all: the
// path stayed in ArtifactPaths, the file did not exist, and the evaluator then reported
// fix_missing_required_context for an artifact it could neither read nor repair. Three of twelve
// artifacts were lost that way in a single run.
//
// An illegal escape is a one-line repair and exactly what the fix loop is for. The gate belongs
// only where a rejection preserves the previous version of the file.
func TestSyntacticShellReason_doesNotGateOnEscapes(t *testing.T) {
	cases := []struct{ name, path, content string }{
		{"java", "src/test/java/p/OwnerControllerE2EIT.java", javaFile(`Pattern.compile("\d+");`)},
		{"csharp", "tests/P/OwnerControllerE2ETests.cs", csFile(`var s = "bad\d escape";`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if reason := SyntacticShellReason(tc.path, tc.content); reason != "" {
				t.Errorf("generate-path gate must not reject on escapes (that destroys the artifact); got %q", reason)
			}
			// The same content must still be refused on the fix path, where the previous file survives.
			if reason := IllegalEscapeReason(tc.path, tc.content); reason == "" {
				t.Error("fix-path gate must still reject the illegal escape")
			}
		})
	}
}

// The structural checks SyntacticShellReason owns detect content that is unusable rather than
// broken, so they must keep gating BOTH paths.
func TestSyntacticShellReason_stillGatesStructuralDamage(t *testing.T) {
	cases := []struct{ name, content string }{
		{"markdown fence", "```java\npackage p; class C {}\n```"},
		{"unbalanced braces", "package p;\n\nclass C {\n\tvoid t() {\n}\n"},
		{"no type declaration", "package p;\n\nint x = 1;\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if reason := SyntacticShellReason("src/test/java/p/FooE2EIT.java", tc.content); reason == "" {
				t.Error("structural damage must still be refused in both write paths")
			}
		})
	}
}
