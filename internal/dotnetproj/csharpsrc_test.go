package dotnetproj

import (
	"strings"
	"testing"
)

func TestStripCSharpCommentsAndStrings(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		gone    []string
		present []string
	}{
		{
			name:    "line comment",
			src:     "app.MapRazorPages();\n// TODO: app.MapGet(\"/healthz\", () => \"ok\");\napp.Run();",
			gone:    []string{"MapGet", "healthz"},
			present: []string{"MapRazorPages", "app.Run"},
		},
		{
			name:    "block comment",
			src:     "class A {\n/* builder.Services.AddControllers();\n   app.MapControllers(); */\n  void M() {}\n}",
			gone:    []string{"AddControllers", "MapControllers"},
			present: []string{"class A", "void M"},
		},
		{
			name:    "verbatim string holding a controller",
			src:     "const string T = @\"public class FooController : ControllerBase { }\";\nvoid Keep() {}",
			gone:    []string{"ControllerBase", "FooController"},
			present: []string{"const string T", "void Keep"},
		},
		{
			name:    "doubled quote inside a verbatim string does not end it",
			src:     "const string S = @\"he said \"\"AddRazorPages()\"\" once\";\nvoid Keep() {}",
			gone:    []string{"AddRazorPages"},
			present: []string{"void Keep"},
		},
		{
			name:    "interpolated verbatim string",
			src:     "var s = $@\"{x} app.MapGet(/y)\";\nvoid Keep() {}",
			gone:    []string{"MapGet"},
			present: []string{"void Keep"},
		},
		{
			name:    "raw string literal",
			src:     "const string S = \"\"\"\n  builder.Services.AddRazorPages();\n  \"\"\";\nvoid Keep() {}",
			gone:    []string{"AddRazorPages"},
			present: []string{"void Keep"},
		},
		{
			name:    "escaped quote in a regular string",
			src:     "var s = \"a \\\" AddControllers() b\";\nvoid Keep() {}",
			gone:    []string{"AddControllers"},
			present: []string{"void Keep"},
		},
		{
			name:    "a division is not a comment",
			src:     "var r = total / count; app.MapRazorPages();",
			present: []string{"total", "count", "MapRazorPages"},
		},
		{
			name:    "a char literal holding a quote",
			src:     "var q = '\"'; app.MapRazorPages();",
			present: []string{"MapRazorPages"},
		},
		{
			name:    "unterminated block comment swallows the rest, and does not hang",
			src:     "app.MapRazorPages();\n/* unterminated\nAddControllers();",
			gone:    []string{"AddControllers"},
			present: []string{"MapRazorPages"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripCSharpCommentsAndStrings(tc.src)
			for _, g := range tc.gone {
				if strings.Contains(got, g) {
					t.Errorf("%q survived:\n%s", g, got)
				}
			}
			for _, p := range tc.present {
				if !strings.Contains(got, p) {
					t.Errorf("%q was removed:\n%s", p, got)
				}
			}
		})
	}
}

// Line numbers are preserved so a caller can still report a location.
func TestStripCSharpCommentsAndStrings_preservesLineCount(t *testing.T) {
	src := "line1\n// comment\n/* two\n   lines */\nvar s = @\"a\nb\";\nlast\n"
	if got, want := strings.Count(StripCSharpCommentsAndStrings(src), "\n"), strings.Count(src, "\n"); got != want {
		t.Fatalf("newline count = %d, want %d", got, want)
	}
}
