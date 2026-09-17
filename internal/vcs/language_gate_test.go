package vcs

import "testing"

// serve.gating.supported_languages is written by an operator ("csharp") and compared against
// whatever the repo inspector answers ("cs", "C#"). The comparison used to be ==, so a C# repo
// could be rejected as "language/framework not supported" by a config that named it.
func TestLanguageSupported(t *testing.T) {
	cases := []struct {
		name      string
		supported []string
		detected  string
		want      bool
	}{
		{"exact match", []string{"java", "csharp"}, "csharp", true},
		{"short form detected, canonical configured", []string{"java", "csharp"}, "cs", true},
		{"canonical detected, short form configured", []string{"java", "cs"}, "csharp", true},
		{"C# spelling from a repo inspector", []string{"csharp"}, "C#", true},
		{"case and space insensitive", []string{" CSharp "}, "cs", true},
		{"ts short form", []string{"typescript"}, "ts", true},
		{"js short form", []string{"javascript"}, "js", true},
		{"genuinely unsupported", []string{"java", "csharp"}, "go", false},
		{"java is not csharp", []string{"csharp"}, "java", false},
		{"empty supported list means no gate here", nil, "go", true},
		{"empty detection is not a rejection", []string{"csharp"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LanguageSupported(tc.supported, tc.detected); got != tc.want {
				t.Fatalf("LanguageSupported(%v, %q) = %v, want %v", tc.supported, tc.detected, got, tc.want)
			}
		})
	}
}
