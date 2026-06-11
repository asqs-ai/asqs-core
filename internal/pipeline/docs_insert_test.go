package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsWellFormedDocComment(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/** ok */", true},
		{"/**\n * ok\n */", true},
		{"/** missing closer", false},      // no */
		{"/** a */\n */", false},           // doubled */
		{"/** a */ trailing", false},       // trailing code after the block
		{"/** Doc. */ // note", true},      // trailing // line comment is fine
		{"/** a /* b */", true},            // /* inside prose does not nest (valid)
		{"/// <summary>x</summary>", true}, // C# XML doc
		{"/// a\n/// b", true},
		{"/// a\npublic void X() {}", false}, // a non-/// line sneaks in
		{"not a comment", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isWellFormedDocComment(c.in); got != c.want {
			t.Errorf("isWellFormedDocComment(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestApplyCollectedDocInserts proves: multiple inserts in one file land at the correct lines (offset),
// a symbol that already has a doc is skipped (no duplicate), and a malformed block is skipped.
func TestApplyCollectedDocInserts(t *testing.T) {
	repo := t.TempDir()
	src := `package p;

public class Foo {
    public void a() {}

    /** existing */
    public void b() {}

    public void c() {}

    public void d() {}
}
`
	if err := os.WriteFile(filepath.Join(repo, "Foo.java"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	byFile := map[string][]docInsert{
		"Foo.java": {
			{line: 4, content: "/** doc a */", symbol: "a"},         // valid
			{line: 7, content: "/** doc b */", symbol: "b"},         // b already documented → skip
			{line: 9, content: "/** doc c\n * x\n */", symbol: "c"}, // valid, multi-line
			{line: 11, content: "/** broken", symbol: "d"},          // malformed → skip
		},
	}

	applied := applyCollectedDocInserts(repo, byFile)
	if applied != 2 {
		t.Fatalf("applied = %d, want 2 (a and c only)", applied)
	}

	out, err := os.ReadFile(filepath.Join(repo, "Foo.java"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	// a's doc sits directly above a() (and c's offset stayed correct after a's insert shifted lines).
	if !strings.Contains(got, "/** doc a */\n    public void a() {}") {
		t.Fatalf("doc a not placed directly above a():\n%s", got)
	}
	if !strings.Contains(got, "/** doc c\n * x\n */\n    public void c() {}") {
		t.Fatalf("doc c not placed directly above c() (offset wrong):\n%s", got)
	}
	// b was already documented → no second doc; d was malformed → never inserted.
	if strings.Contains(got, "doc b") {
		t.Fatalf("doc b must be skipped (b already documented):\n%s", got)
	}
	if strings.Contains(got, "broken") {
		t.Fatalf("malformed doc d must be skipped:\n%s", got)
	}
	// The pre-existing doc is untouched and not duplicated.
	if strings.Count(got, "/** existing */") != 1 {
		t.Fatalf("pre-existing doc should remain exactly once:\n%s", got)
	}
}
