package dotnetproj

import (
	"strings"
	"testing"
)

// The member scan feeds two claims made to the model: the generator's refusal of a call to a member
// a type does not declare, and the fixer's "members declared on X include: …" offer appended to a
// compiler-backed CS1061. A member form the pattern cannot see is a member the generator calls
// invented and the fixer steers away from — so every ordinary way of writing one is pinned here.
func TestDeclaredMemberNames_ordinaryMemberForms(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		src      string
		want     []string
		absent   []string
	}{
		{
			name:     "generic methods are members; the name is followed by < not (",
			typeName: "Repo",
			src: "public class Repo\n{\n" +
				"    public T Get<T>(int id) => default;\n" +
				"    public Task<List<T>> FindAllAsync<T>(int x) => null;\n" +
				"    public int Plain(int id) { return 1; }\n}\n",
			want: []string{"FindAllAsync", "Get", "Plain"},
		},
		{
			name:     "an attribute on the member's own line does not hide it",
			typeName: "Dto",
			src: "public class Dto\n{\n" +
				"    [JsonPropertyName(\"id\")] public string Id { get; set; }\n" +
				"    public string Name { get; set; }\n}\n",
			want: []string{"Id", "Name"},
		},
		{
			name:     "const fields and tuple return types",
			typeName: "K",
			src: "public class K\n{\n" +
				"    public const int MaxItems = 10;\n" +
				"    public (int, string) Summary() => (1, \"a\");\n" +
				"    public int Ok() => 1;\n}\n",
			want: []string{"MaxItems", "Ok", "Summary"},
		},
		{
			name:     "interface members carry no access modifier at all",
			typeName: "IOrderService",
			src: "public interface IOrderService\n{\n" +
				"    Task<Order> GetAsync(int id);\n" +
				"    void Cancel(int id);\n" +
				"    int Count { get; }\n}\n",
			want: []string{"Cancel", "Count", "GetAsync"},
		},
		{
			name:     "modifiers in either order, and private stays out",
			typeName: "M",
			src: "public class M\n{\n" +
				"    internal protected void Hidden() { }\n" +
				"    protected internal void AlsoHidden() { }\n" +
				"    private void Priv() { }\n" +
				"    public void Shown() { }\n}\n",
			want:   []string{"AlsoHidden", "Hidden", "Shown"},
			absent: []string{"Priv"},
		},
		{
			name:     "a sibling type declaration is not a member of this one",
			typeName: "Order",
			src: "public class Order\n{\n    public void AddLine(string sku) { }\n}\n\n" +
				"public class OrderDto\n{\n    public int Id { get; set; }\n}\n",
			want:   []string{"AddLine"},
			absent: []string{"OrderDto"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeclaredMemberNames(tc.typeName, tc.src)
			have := map[string]bool{}
			for _, g := range got {
				have[g] = true
			}
			for _, w := range tc.want {
				if !have[w] {
					t.Errorf("DeclaredMemberNames(%q) = %v; missing %q", tc.typeName, got, w)
				}
			}
			for _, a := range tc.absent {
				if have[a] {
					t.Errorf("DeclaredMemberNames(%q) = %v; must not offer %q", tc.typeName, got, a)
				}
			}
		})
	}
}

// The offer list is only useful if it is a list of members. A name captured from a nested type
// declaration or a keyword would be a member the model is invited to call and cannot.
func TestDeclaredMemberNames_doesNotOfferTypeKeywords(t *testing.T) {
	src := "public class Outer\n{\n    public void Do() { }\n    public enum Mode { A, B }\n}\n"
	for _, got := range DeclaredMemberNames("Outer", src) {
		if typeKeywords[got] || got == "Mode" {
			t.Errorf("DeclaredMemberNames offered %q, which is not a member of Outer (all: %v)",
				got, strings.Join(DeclaredMemberNames("Outer", src), ", "))
		}
	}
}

// A generic type argument list is written with a space after the comma by everyone, and the type
// slot was a character run that a space ends. Same bug class as the generic-method hole and the same
// two claim sites: the member vanishes, the generator calls the call invented, and the fixer offers
// the type's other members as the alternative.
func TestDeclaredMemberNames_genericTypesWithSpacedArguments(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		src      string
		want     []string
	}{
		{
			name:     "a spaced generic return type",
			typeName: "Cache",
			src: "public class Cache\n{\n    public Dictionary<string, int> Snapshot() => null;\n" +
				"    public void Clear() { }\n}\n",
			want: []string{"Clear", "Snapshot"},
		},
		{
			name:     "a spaced generic field and property",
			typeName: "Holder",
			src: "public class Holder\n{\n    public Func<int, string> Format;\n" +
				"    public IDictionary<Guid, Order> ByID { get; set; }\n}\n",
			want: []string{"ByID", "Format"},
		},
		{
			name:     "nested generics, arrays and nullables",
			typeName: "Deep",
			src: "public class Deep\n{\n    public Task<Dictionary<string, List<int>>> LoadAsync() => null;\n" +
				"    public KeyValuePair<string, int>[] Pairs { get; set; }\n" +
				"    public List<int>? Maybe { get; set; }\n}\n",
			want: []string{"LoadAsync", "Maybe", "Pairs"},
		},
		{
			name:     "an interface declaring the same shapes, and an event",
			typeName: "IStore",
			src: "public interface IStore\n{\n    Task<Dictionary<string, int>> LoadAsync();\n" +
				"    event EventHandler Changed;\n    void Clear();\n}\n",
			want: []string{"Changed", "Clear", "LoadAsync"},
		},
		{
			name:     "an attribute whose arguments contain brackets",
			typeName: "Attr",
			src: "public class Attr\n{\n    [Values(new[] { 1, 2 })] public int Threshold { get; set; }\n" +
				"    public void Reset() { }\n}\n",
			want: []string{"Reset", "Threshold"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeclaredMemberNames(tc.typeName, tc.src)
			have := map[string]bool{}
			for _, g := range got {
				have[g] = true
			}
			for _, w := range tc.want {
				if !have[w] {
					t.Errorf("DeclaredMemberNames(%q) = %v; missing %q", tc.typeName, got, w)
				}
			}
		})
	}
}
