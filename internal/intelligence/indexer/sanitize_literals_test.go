package indexer

import (
	"strings"
	"testing"
)

// A "/*" inside a string literal must not open a comment.
//
// The old scanner matched "/*" unconditionally, so any occurrence inside a string or regex started
// a comment that ran to the next "*/" — or, when there was none, to the end of the chunk. The
// failure is silent and doubly damaging: the corrupted text is both what gets embedded (retrieval
// then matches source that does not exist) and what gets shown to the model as context.
func TestStripBlockComments_leavesStringLiteralsAlone(t *testing.T) {
	opts := SanitizeOptions{StripBlockComments: true}

	cases := []struct {
		name        string
		in          string
		mustContain []string
		mustNotHave []string
	}{
		{
			name: "java string containing a comment opener",
			in: "String glob = \"/*\";\n" +
				"int keepMe = 42;\n" +
				"String close = \"*/\";\n",
			mustContain: []string{"keepMe", "42", `"/*"`, `"*/"`},
		},
		{
			name: "unterminated opener inside a literal must not eat the rest of the file",
			in: "String s = \"/*\";\n" +
				"public void survivesToTheEnd() {}\n",
			mustContain: []string{"survivesToTheEnd"},
		},
		{
			name: "single quotes",
			in: "char c = '/';\n" +
				"String t = 'a/*b';\n" +
				"int alsoKeep = 7;\n",
			mustContain: []string{"alsoKeep", "7"},
		},
		{
			name: "csharp verbatim string with doubled quotes",
			in: "var path = @\"C:\\dir\\/*not a comment*/\";\n" +
				"var q = @\"say \"\"hi\"\" /*still literal*/\";\n" +
				"int keepVerbatim = 1;\n",
			mustContain: []string{"keepVerbatim", "not a comment", "still literal"},
		},
		{
			name: "template literal spanning lines",
			in: "const q = `select /* not a comment */\n" +
				"  from t`;\n" +
				"const keepTemplate = 3;\n",
			mustContain: []string{"keepTemplate", "not a comment"},
		},
		{
			name: "opener inside a line comment must not pair with a later closer",
			in: "// a /* here\n" +
				"int betweenTheTwo = 5;\n" +
				"// and */ there\n",
			mustContain: []string{"betweenTheTwo", "5"},
		},
		{
			name: "a real block comment is still removed",
			in: "int before = 1;\n" +
				"/* remove me entirely */\n" +
				"int after = 2;\n",
			mustContain: []string{"before", "after"},
			mustNotHave: []string{"remove me entirely"},
		},
		{
			name: "escaped quote does not end the literal early",
			in: "String s = \"he said \\\"/*\\\" and stopped\";\n" +
				"int keepEscaped = 9;\n",
			mustContain: []string{"keepEscaped"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in, opts)
			for _, want := range tc.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("sanitized output lost %q — source was swallowed by a false comment.\ngot:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.mustNotHave {
				if strings.Contains(got, unwanted) {
					t.Errorf("sanitized output still contains %q; the block comment was not removed.\ngot:\n%s", unwanted, got)
				}
			}
		})
	}
}

// Multi-line block comments spanning code must still be removed entirely, which is the behaviour the
// literal awareness must not regress.
func TestStripBlockComments_stillRemovesRealComments(t *testing.T) {
	in := "int a = 1;\n/*\n * javadoc-ish\n * lines\n */\nint b = 2;\n"
	got := Sanitize(in, SanitizeOptions{StripBlockComments: true})
	for _, want := range []string{"int a = 1;", "int b = 2;"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"javadoc-ish", "lines"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("comment body %q survived:\n%s", unwanted, got)
		}
	}
}
