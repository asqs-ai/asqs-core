package evaluator

import (
	"strings"
	"testing"
)

const strayCharShell = "package p;\n\nimport org.junit.jupiter.api.Test;\n\nclass FooTest {\n    @Test void t() {\n%s\n    }\n}\n"

func javaSrc(stmt string) string {
	return strings.Replace(strayCharShell, "%s", stmt, 1)
}

// javac rejects these outright as `illegal character`, and nothing between the model and the
// compiler was looking. asqs-go run api-c7682cc0710205f1bed996c3095eaf8f's own audit carries a U+2026 —
// its refused fix anchor was "…returnsFormatted…" — and fix_quality_gate.go records asqs-go run
// api-f1d4227cb6db875a2e51c3100b3e1be8 shipping a `\ ` where `\n` belonged, straight out of the
// structured-JSON envelope. Both reached disk and cost a compile cycle the gate could have saved.
func TestSyntacticShellReason_javaStrayCharacters(t *testing.T) {
	for name, stmt := range map[string]string{
		"ellipsis in an argument list": `        assertEquals(order("ABC", …), result);`,
		"stray backslash in code":      `        String r = f("ABC");\      assertNotNull(r);`,
		"non-breaking space":           "        int x = 1;",
		"smart quotes in code":         "        assertEquals(“ABC”, result);",
	} {
		t.Run(name, func(t *testing.T) {
			reason := SyntacticShellReason("src/test/java/p/FooTest.java", javaSrc(stmt))
			if reason == "" {
				t.Fatal("javac rejects this as an illegal character; the gate must refuse it before the compiler does")
			}
			if !strings.Contains(reason, "line") {
				t.Errorf("the reason must locate the character so the reader can find it, got %q", reason)
			}
		})
	}
}

// The cost of the two error directions is not symmetric: a missed stray character costs one compile
// cycle, a false positive silently destroys a correct artifact. Every row here is Java javac
// accepts — verified against javac 25 before this test was written.
func TestSyntacticShellReason_javaStrayCharacterDoesNotFalsePositive(t *testing.T) {
	for name, stmt := range map[string]string{
		// Java identifiers may contain Unicode letters; rejecting all non-ASCII would be wrong.
		"unicode identifier":          "        String café = \"x\";\n        assertNotNull(café);",
		"ellipsis inside a string":    `        assertEquals("a … b", result);`,
		"ellipsis inside a comment":   "        // trimmed for brevity …\n        assertNotNull(result);",
		"escapes inside a string":     `        assertEquals("a\tb\n", result);`,
		"unicode escape in code":      `        int A = 1;`,
		"lambda arrow and method ref": `        run(() -> list.forEach(String::trim));`,
		"varargs and generics":        `        List<String> l = java.util.List.of("a", "b");`,
		"numeric literal separators":  `        int x = 1_000; double d = 1.5e-3; long l = 0xFFL;`,
		"annotation and diamond":      `        var m = new java.util.HashMap<String, Integer>();`,
	} {
		t.Run(name, func(t *testing.T) {
			if reason := SyntacticShellReason("src/test/java/p/FooTest.java", javaSrc(stmt)); reason != "" {
				t.Errorf("refused valid Java: %q", reason)
			}
		})
	}
}

// A text block's contents are consumed by stripStringsAndComments, so characters that would be
// illegal in code position are not judged when they sit inside one.
func TestSyntacticShellReason_javaStrayCharacterIgnoresTextBlockContents(t *testing.T) {
	src := "package p;\n\nclass FooTest {\n    String s = \"\"\"\n        a … b\n        \"\"\";\n    @Test void t() { assertNotNull(s); }\n}\n"
	if reason := SyntacticShellReason("src/test/java/p/FooTest.java", src); reason != "" {
		t.Errorf("a character inside a text block is not in code position: %q", reason)
	}
}
