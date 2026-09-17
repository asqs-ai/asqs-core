package dotnetproj

import "testing"

const accessFixture = `public sealed class LegacyXmlCatalogReader
{
    public IReadOnlyList<string> ReadSkuList(string xml) { return null; }

    [Obsolete("legacy")]
    internal static string DescribeBlockingLegacy(Task<string> t) { return ""; }

    protected internal void Shared() { }
    internal protected void SharedOtherOrder() { }
    private protected void Narrow() { }
    protected virtual void Hook() { }
    private void Hidden() { }
    void Implicit() { }
    public Dictionary<string, List<int>> Complex<T>(T x) { return null; }
}
`

func TestDeclaredMemberAccess(t *testing.T) {
	cases := map[string]string{
		"ReadSkuList":            "public",
		"DescribeBlockingLegacy": "internal",
		"Shared":                 "protected internal",
		"SharedOtherOrder":       "internal protected",
		"Narrow":                 "private protected",
		"Hook":                   "protected",
		"Hidden":                 "private",
		"Implicit":               "private", // C# defaults a class member with no modifier to private
		"Complex":                "public",
	}
	for member, want := range cases {
		got, ok := DeclaredMemberAccess("LegacyXmlCatalogReader", member, accessFixture)
		if !ok {
			t.Errorf("%s: not found", member)
			continue
		}
		if got != want {
			t.Errorf("%s: access = %q, want %q", member, got, want)
		}
	}
}

// A member the type does not declare must report ok=false, so the caller says "absent" rather than
// inventing an access modifier for it.
func TestDeclaredMemberAccess_absentMemberIsNotFound(t *testing.T) {
	if got, ok := DeclaredMemberAccess("LegacyXmlCatalogReader", "TotallyInvented", accessFixture); ok {
		t.Errorf("an undeclared member reported access %q", got)
	}
}

// An interface member carries no modifier and is public; the class default would call it private
// and invent an inaccessibility that does not exist.
func TestDeclaredMemberAccess_interfaceMembersArePublic(t *testing.T) {
	src := "public interface IOrderCatalog\n{\n    Order? Find(string id);\n    Task<int> CountAsync();\n}\n"
	for _, m := range []string{"Find", "CountAsync"} {
		got, ok := DeclaredMemberAccess("IOrderCatalog", m, src)
		if !ok || got != "public" {
			t.Errorf("%s: access = %q ok=%v, want public", m, got, ok)
		}
	}
}

// A member named inside a comment or a string is not a declaration.
func TestDeclaredMemberAccess_ignoresCommentsAndStrings(t *testing.T) {
	src := "public class W\n{\n    // internal void Ghost() { }\n    const string S = \"internal void Ghost() { }\";\n    public void Real() { }\n}\n"
	if got, ok := DeclaredMemberAccess("W", "Ghost", src); ok {
		t.Errorf("a commented-out declaration reported access %q", got)
	}
}

func TestAccessIsAssemblyScoped(t *testing.T) {
	for _, a := range []string{"internal", "protected internal", "internal protected"} {
		if !AccessIsAssemblyScoped(a) {
			t.Errorf("%q must be openable by an InternalsVisibleTo grant", a)
		}
	}
	// private protected is "this assembly AND derived" — a grant alone does not open it.
	for _, a := range []string{"public", "private", "protected", "private protected", ""} {
		if AccessIsAssemblyScoped(a) {
			t.Errorf("%q must not be described as an InternalsVisibleTo problem", a)
		}
	}
}

func TestDescribeAccessRemedy(t *testing.T) {
	if got := DescribeAccessRemedy("T", "M", "public"); got != "" {
		t.Errorf("public is not an accessibility problem, got %q", got)
	}
	if got := DescribeAccessRemedy("T", "M", "internal"); got == "" ||
		!contains(got, "InternalsVisibleTo") || !contains(got, "internal") {
		t.Errorf("internal remedy does not name the grant: %q", got)
	}
	if got := DescribeAccessRemedy("T", "M", "private"); contains(got, "InternalsVisibleTo") {
		t.Errorf("private must not be described as a grant problem: %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
