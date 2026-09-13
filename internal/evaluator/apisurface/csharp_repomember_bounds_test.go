package apisurface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCSharpRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Adding behaviour to a type through an extension method declared elsewhere is ordinary C#, and a
// repository's own extension methods were judged against the receiver's declaration — so a correct
// test calling one was refused, and the model was told the method does not exist.
func TestRepoInventedMemberReasonCS_allowsRepositoryOwnExtensionMethods(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		"src/Order.cs": "public class Order\n{\n    public decimal Subtotal { get; set; }\n" +
			"    public void AddLine(string sku) { }\n}\n",
		"src/OrderExtensions.cs": "public static class OrderExtensions\n{\n" +
			"    public static decimal TotalWithTax(this Order o) => o.Subtotal * 1.2m;\n}\n",
	})
	test := "public class T\n{\n    public void A()\n    {\n        Order o = new Order();\n" +
		"        var x = o.TotalWithTax();\n    }\n}\n"
	if got := RepoInventedMemberReasonCS(root, test); got != "" {
		t.Fatalf("refused a call to the repository's own extension method: %s", got)
	}
	// The allowance is for names the repository actually declares as extensions, not for anything.
	test = strings.Replace(test, "TotalWithTax", "TotalWithVat", 1)
	if got := RepoInventedMemberReasonCS(root, test); !strings.Contains(got, "TotalWithVat") {
		t.Fatalf("expected an invented-member finding for TotalWithVat; got %q", got)
	}
}

// An interface-typed local is the commonest shape in a generated test with dependency injection.
// Its members come from the interface, which declares them with no access modifier.
func TestRepoInventedMemberReasonCS_resolvesInterfaceMembers(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		"src/IOrderService.cs": "public interface IOrderService\n{\n    Task<Order> GetAsync(int id);\n" +
			"    void Cancel(int id);\n}\n",
		"src/OrderService.cs": "public class OrderService : IOrderService\n{\n" +
			"    public Task<Order> GetAsync(int id) => null;\n    public void Cancel(int id) { }\n}\n",
		"src/Order.cs": "public class Order\n{\n    public int Id { get; set; }\n}\n",
	})
	test := "public class T\n{\n    public void A()\n    {\n" +
		"        IOrderService s = new OrderService();\n        s.GetAsync(1);\n    }\n}\n"
	if got := RepoInventedMemberReasonCS(root, test); got != "" {
		t.Fatalf("refused a call the interface declares one line above: %s", got)
	}
	if got := RepoInventedMemberReasonCS(root, strings.Replace(test, "s.GetAsync(1)", "s.Missing(1)", 1)); !strings.Contains(got, "Missing") {
		t.Fatalf("expected a finding for a member the interface does not declare; got %q", got)
	}
}

// A generic method called with inferred type arguments reads exactly like a non-generic call, which
// is how the receiver's own generic method came to be reported as invented.
func TestRepoInventedMemberReasonCS_allowsGenericMethodWithInferredTypeArguments(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		"src/Repo.cs": "public class Repo\n{\n    public T Get<T>(int id) => default;\n" +
			"    public void Save(object o) { }\n}\n",
	})
	test := "public class T2\n{\n    public void A()\n    {\n        Repo r = new Repo();\n        r.Get(1);\n    }\n}\n"
	if got := RepoInventedMemberReasonCS(root, test); got != "" {
		t.Fatalf("refused a call to a generic method with inferred type arguments: %s", got)
	}
}

// The same hole reaching the gate: a spaced generic return type made the member invisible, so a
// correct call was refused with the type's other members offered as the alternative.
func TestRepoInventedMemberReasonCS_allowsSpacedGenericSignatures(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		"src/Cache.cs": "public class Cache\n{\n    public Dictionary<string, int> Snapshot() => null;\n" +
			"    public void Clear() { }\n}\n",
	})
	test := "public class T\n{\n    public void A()\n    {\n        Cache c = new Cache();\n        c.Snapshot();\n    }\n}\n"
	if got := RepoInventedMemberReasonCS(root, test); got != "" {
		t.Fatalf("refused a call to a method whose return type has a space in its type arguments: %s", got)
	}
}
