package profile

import "testing"

// The runner emits "csharp" but plan options, CLI flags and the C# indexer all carry "cs", and
// ForLang used to fall through to the minimal default for the short form — which handed a C# repo
// the JDK image (eclipse-temurin:21-jdk) and Java's report paths.
func TestForLang_acceptsCSharpAliases(t *testing.T) {
	want := Profiles["csharp"]
	for _, alias := range []string{"csharp", "cs", "CS", "C#", " cs ", "CSharp"} {
		got := ForLang(alias)
		if got.Lang != want.Lang {
			t.Errorf("ForLang(%q).Lang = %q, want %q", alias, got.Lang, want.Lang)
		}
		if got.DefaultImage != want.DefaultImage {
			t.Errorf("ForLang(%q).DefaultImage = %q, want %q", alias, got.DefaultImage, want.DefaultImage)
		}
		if len(got.ReportPaths) != len(want.ReportPaths) {
			t.Errorf("ForLang(%q).ReportPaths = %v, want %v", alias, got.ReportPaths, want.ReportPaths)
		}
	}
}

// The same folding must apply to the other families, and an unknown language must still get the
// minimal default rather than a C# profile.
func TestForLang_foldsOtherAliasesAndKeepsTheDefault(t *testing.T) {
	if got := ForLang("ts").Lang; got != ForLang("typescript").Lang {
		t.Errorf(`ForLang("ts").Lang = %q, want the typescript profile`, got)
	}
	if got := ForLang("js").Lang; got != ForLang("javascript").Lang {
		t.Errorf(`ForLang("js").Lang = %q, want the javascript profile`, got)
	}
	if got := ForLang("rust"); got.DefaultImage != "eclipse-temurin:21-jdk" {
		t.Errorf("ForLang(rust) = %+v, want the minimal default", got)
	}
}

// ImageFor already keyed on the language; the alias must reach the dotnet override, not the Java one.
func TestImageFor_csAlias(t *testing.T) {
	const dotnet = "mirror.local/dotnet/sdk:8.0"
	for _, alias := range []string{"csharp", "cs", "C#"} {
		if got := ImageFor(alias, "javaimg", dotnet, "nodeimg"); got != dotnet {
			t.Errorf("ImageFor(%q) = %q, want %q", alias, got, dotnet)
		}
	}
}
