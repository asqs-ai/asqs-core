package evaluator

import (
	"strings"
	"testing"
)

// The Java gate counted braces and never parens, so a dropped `)` reached disk and the project did
// not compile. asqs-go run api-c7682cc0710205f1bed996c3095eaf8f wrote LegacyOrderFormatterTest.java with
// `[37,45] ')' expected` and `[52,47] ')' expected`; the fixer rewrote the file four times without
// touching either site, the loop stopped, and the run shipped nothing.
//
// Braces alone cannot see it: `if (x.contains("a") {` opens and closes its block correctly and is
// short exactly one paren.
func TestSyntacticShellReason_javaUnbalancedParens(t *testing.T) {
	const droppedParen = `package p;

import org.junit.jupiter.api.Test;

class FooTest {
    @Test
    void t() {
        String result = f(g("ABC", "NEW"));
        if (result.contains("ABC (NEW)") {
            assertNotNull(result);
        }
    }
}
`
	reason := SyntacticShellReason("src/test/java/p/FooTest.java", droppedParen)
	if reason == "" {
		t.Fatal("a Java body missing a closing paren must be refused before it reaches the compiler")
	}
	if !strings.Contains(reason, "paren") {
		t.Errorf("the reason must name the parens so the reader knows what to look for, got %q", reason)
	}
	if !strings.Contains(reason, "(=") || !strings.Contains(reason, ")=") {
		t.Errorf("the reason must carry both counts, as the brace check does, got %q", reason)
	}
}

// The counts are taken after stripStringsAndComments, which is what makes the check safe. Each of
// these carries deliberately lopsided parens somewhere the compiler never sees them.
func TestSyntacticShellReason_javaParenCheckDoesNotFalsePositive(t *testing.T) {
	for name, src := range map[string]string{
		"parens in a string literal": `package p;
class FooTest {
    @Test void t() { String s = "a ( b ) ) ) ("; assertNotNull(s); }
}
`,
		"parens in a line comment": `package p;
class FooTest {
    // note: unbalanced ( ( ( here
    @Test void t() { assertNotNull("x"); }
}
`,
		"parens in a block comment": `package p;
class FooTest {
    /* and ) ) ) here */
    @Test void t() { assertNotNull("x"); }
}
`,
		"parens in a text block": `package p;
class FooTest {
    String s = """
        unbalanced ( ( (
        """;
    @Test void t() { assertNotNull(s); }
}
`,
		"paren as a char literal": `package p;
class FooTest {
    char c = '(';
    @Test void t() { assertNotNull("x"); }
}
`,
		"lambda and nested calls": `package p;
class FooTest {
    @Test void t() { run(() -> assertEquals(f(g(1)), h(2))); }
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			if reason := SyntacticShellReason("src/test/java/p/FooTest.java", src); strings.Contains(reason, "paren") {
				t.Errorf("refused a well-formed file: %q", reason)
			}
		})
	}
}

// Braces stay the first thing reported: a truncated file is short both, and "truncated" is the more
// useful diagnosis than "unbalanced parens".
func TestSyntacticShellReason_javaTruncationStillReportsBraces(t *testing.T) {
	const truncated = `package p;
class FooTest {
    @Test void t() { assertEquals(f(1
`
	reason := SyntacticShellReason("src/test/java/p/FooTest.java", truncated)
	if !strings.Contains(reason, "braces") {
		t.Errorf("a truncated body should still be diagnosed by the brace check, got %q", reason)
	}
}
