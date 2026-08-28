package sqlsplit

import (
	"strings"
	"testing"
)

func TestStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "plain statements",
			in:   "CREATE TABLE a (x int); CREATE TABLE b (y int);",
			want: []string{"CREATE TABLE a (x int)", "CREATE TABLE b (y int)"},
		},
		{
			// The failure that took down startup: a semicolon inside a prose comment split the
			// statement in half, and the error named the statement's opening comment rather than
			// the comment that actually broke it.
			name: "semicolon inside a line comment",
			in:   "-- note: do this; then that\nCREATE TABLE a (x int);",
			want: []string{"-- note: do this; then that\nCREATE TABLE a (x int)"},
		},
		{
			name: "semicolon inside a string literal",
			in:   "INSERT INTO t VALUES ('a;b');\nSELECT 1;",
			want: []string{"INSERT INTO t VALUES ('a;b')", "SELECT 1"},
		},
		{
			name: "escaped quote inside a string literal",
			in:   "INSERT INTO t VALUES ('it''s; fine');\nSELECT 2;",
			want: []string{"INSERT INTO t VALUES ('it''s; fine')", "SELECT 2"},
		},
		{
			name: "semicolon inside a block comment",
			in:   "/* one; two */ SELECT 3;",
			want: []string{"/* one; two */ SELECT 3"},
		},
		{
			name: "nested block comment",
			in:   "/* outer /* inner; */ still outer; */ SELECT 4;",
			want: []string{"/* outer /* inner; */ still outer; */ SELECT 4"},
		},
		{
			// A function body is where semicolons are expected, and the naive splitter destroyed it.
			name: "dollar-quoted function body",
			in:   "CREATE FUNCTION f() RETURNS int AS $$\nBEGIN\n  PERFORM 1;\n  RETURN 2;\nEND;\n$$ LANGUAGE plpgsql;\nSELECT 5;",
			want: []string{
				"CREATE FUNCTION f() RETURNS int AS $$\nBEGIN\n  PERFORM 1;\n  RETURN 2;\nEND;\n$$ LANGUAGE plpgsql",
				"SELECT 5",
			},
		},
		{
			name: "tagged dollar quote",
			in:   "DO $body$ BEGIN RAISE NOTICE 'a;b'; END $body$;\nSELECT 6;",
			want: []string{"DO $body$ BEGIN RAISE NOTICE 'a;b'; END $body$", "SELECT 6"},
		},
		{
			name: "positional parameters are not dollar quotes",
			in:   "SELECT * FROM t WHERE a = $1 AND b = $2;\nSELECT 7;",
			want: []string{"SELECT * FROM t WHERE a = $1 AND b = $2", "SELECT 7"},
		},
		{
			name: "quoted identifier containing a semicolon",
			in:   `CREATE TABLE "weird;name" (x int);`,
			want: []string{`CREATE TABLE "weird;name" (x int)`},
		},
		{
			name: "trailing semicolon and blank statements",
			in:   "SELECT 1;;\n\n;",
			want: []string{"SELECT 1"},
		},
		{
			name: "no trailing semicolon",
			in:   "SELECT 1",
			want: []string{"SELECT 1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Statements(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d statement(s), want %d:\ngot:  %q\nwant: %q", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if strings.TrimSpace(got[i]) != strings.TrimSpace(tc.want[i]) {
					t.Errorf("statement %d:\n got: %q\nwant: %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The naive implementation this replaces, kept as an executable statement of what was wrong.
func TestStatements_beatsANaiveSplit(t *testing.T) {
	const script = "INSERT INTO t VALUES ('a;b');"
	naive := strings.Split(script, ";")
	if len(naive) <= 2 {
		t.Fatalf("the naive split was expected to produce fragments; got %d", len(naive))
	}
	if got := Statements(script); len(got) != 1 {
		t.Errorf("Statements produced %d statements for one INSERT: %q", len(got), got)
	}
}
