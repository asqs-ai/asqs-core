package apisurface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func csSourceRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// The generation-time twin of the fix loop's member facts. A model that calls a method the repo's
// own type does not declare produces a compile error the loop then spends a round discovering; the
// Java and TypeScript arms have refused this before the write since the Mockito work, and C# had
// no arm at all.
func TestRepoInventedMemberReasonCS_namesAMemberTheTypeDoesNotDeclare(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{
		"src/OrderService.cs": `namespace Shop.Core;
public class OrderService
{
    public decimal Total(int qty, decimal unit) => qty * unit;
    public string Name { get; set; } = "";
}`,
	})
	const test = `using Xunit;
namespace Shop.Tests;
public class OrderServiceTests
{
    [Fact]
    public void Totals()
    {
        var svc = new OrderService();
        Assert.Equal(10m, svc.CalculateTotal(2, 5m));
    }
}`
	got := RepoInventedMemberReasonCS(repo, test)
	if !strings.Contains(got, "CalculateTotal") || !strings.Contains(got, "OrderService") {
		t.Fatalf("reason = %q, want it to name CalculateTotal on OrderService", got)
	}
	if !strings.Contains(got, "Total") {
		t.Errorf("reason = %q, want it to offer the members that DO exist", got)
	}
}

// A partial class is one type spread over several files, and generated code uses them constantly.
// Judging it from the file that happens to match the name would call every member declared in the
// other half invented — a false rejection of a correct test.
func TestRepoInventedMemberReasonCS_silentOnAPartialClassSecondHalf(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{
		"src/OrderService.cs": `namespace Shop.Core;
public partial class OrderService
{
    public decimal Total() => 0m;
}`,
		"src/OrderService.Validation.cs": `namespace Shop.Core;
public partial class OrderService
{
    public bool Validate() => true;
}`,
	})
	const test = `public class T { public void M() { var svc = new OrderService(); svc.Validate(); } }`
	if got := RepoInventedMemberReasonCS(repo, test); got != "" {
		t.Fatalf("reason = %q for a member declared in the other half of a partial class", got)
	}
}

// An inherited member is declared on the base, not on the type the test names.
func TestRepoInventedMemberReasonCS_silentOnAnInheritedMember(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{
		"src/BaseService.cs": `namespace Shop.Core;
public abstract class BaseService
{
    public void Save() { }
}`,
		"src/OrderService.cs": `namespace Shop.Core;
public class OrderService : BaseService
{
    public decimal Total() => 0m;
}`,
	})
	const test = `public class T { public void M() { var svc = new OrderService(); svc.Save(); } }`
	if got := RepoInventedMemberReasonCS(repo, test); got != "" {
		t.Fatalf("reason = %q for a member the base class declares", got)
	}
}

// The negative claim is only safe when the whole hierarchy is in view. A base type that comes from
// a package cannot be read here, so nothing about the derived type's member set is provable and the
// gate must say nothing at all — including about members that look invented.
func TestRepoInventedMemberReasonCS_silentWhenABaseTypeIsOutsideTheRepo(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{
		"src/OrderController.cs": `namespace Shop.Api;
public class OrderController : ControllerBase
{
    public int Count() => 0;
}`,
	})
	const test = `public class T { public void M() { var c = new OrderController(); c.Ok(); } }`
	if got := RepoInventedMemberReasonCS(repo, test); got != "" {
		t.Fatalf("reason = %q when the base type is not in the repository", got)
	}
}

// Extension methods are declared on a static class somewhere else entirely and are the single
// biggest source of false positives in C#: LINQ, EF Core's async operators and FluentAssertions all
// look exactly like an invented instance member.
func TestRepoInventedMemberReasonCS_silentOnExtensionMethods(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{
		"src/OrderService.cs": `namespace Shop.Core;
public class OrderService
{
    public decimal Total() => 0m;
}`,
	})
	for _, call := range []string{"Should", "ToListAsync", "Select", "Where", "Any", "FirstOrDefault", "ConfigureAwait"} {
		const shell = `public class T { public void M() { var svc = new OrderService(); svc.%s(); } }`
		if got := RepoInventedMemberReasonCS(repo, strings.Replace(shell, "%s", call, 1)); got != "" {
			t.Errorf("reason = %q for %s(), which is an extension method", got, call)
		}
	}
}

// A type the repository does not declare is the package surface's business. Claiming a member is
// absent from one, on the strength of a source scan that never saw it, would be a false statement
// attached to an instruction to rewrite the test.
func TestRepoInventedMemberReasonCS_silentOnAThirdPartyType(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{"src/OrderService.cs": "namespace Shop.Core;\npublic class OrderService { }"})
	const test = `public class T { public void M() { var c = new HttpClient(); c.SendItPlease(); } }`
	if got := RepoInventedMemberReasonCS(repo, test); got != "" {
		t.Fatalf("reason = %q for a type this repository does not declare", got)
	}
}

// Every object has these, and a type that overrides none of them still answers them.
func TestRepoInventedMemberReasonCS_silentOnObjectMembers(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{"src/OrderService.cs": "namespace Shop.Core;\npublic class OrderService { public decimal Total() => 0m; }"})
	const test = `public class T { public void M() { var svc = new OrderService(); svc.ToString(); svc.GetHashCode(); svc.Equals(null); } }`
	if got := RepoInventedMemberReasonCS(repo, test); got != "" {
		t.Fatalf("reason = %q for members every object has", got)
	}
}

// A test file that declares its own stub of the same name is talking about its own type.
func TestRepoInventedMemberReasonCS_silentWhenTheTestDeclaresTheTypeItself(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{"src/OrderService.cs": "namespace Shop.Core;\npublic class OrderService { public decimal Total() => 0m; }"})
	const test = `public class FakeOrderService { }
public class OrderService { public decimal Anything() => 0m; }
public class T { public void M() { var svc = new OrderService(); svc.Anything(); } }`
	if got := RepoInventedMemberReasonCS(repo, test); got != "" {
		t.Fatalf("reason = %q when the test declares the type itself", got)
	}
}

// A record's positional parameters are properties, and a model reading the declaration will use
// them. Missing that would reject every test against a record in the repository.
func TestRepoInventedMemberReasonCS_silentOnRecordPositionalProperties(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{
		"src/Order.cs": "namespace Shop.Core;\npublic record Order(int Id, string Sku, decimal Price);",
	})
	const test = `public class T { public void M() { var o = new Order(1, "a", 2m); var x = o.Sku; o.Price.ToString(); } }`
	if got := RepoInventedMemberReasonCS(repo, test); got != "" {
		t.Fatalf("reason = %q for a record's positional properties", got)
	}
}

// The whole mechanism rests on knowing which type an identifier holds. With no repository there is
// nothing to check against and the answer is silence, not a guess.
func TestRepoInventedMemberReasonCS_silentWithoutEvidence(t *testing.T) {
	if got := RepoInventedMemberReasonCS("", "public class T { }"); got != "" {
		t.Errorf("reason = %q with no repository", got)
	}
	if got := RepoInventedMemberReasonCS(t.TempDir(), ""); got != "" {
		t.Errorf("reason = %q with no content", got)
	}
}
