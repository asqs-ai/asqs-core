package evaluator

import (
	"strings"
	"testing"
)

const orderSource = `namespace Shop.Core.Models;

public class Order
{
    public string Id { get; set; } = "";
    public string CustomerId { get; set; } = "";
    public decimal Total { get; private set; }

    public Order(string id) { Id = id; }

    public void AddLine(string sku, int quantity) { }
    public decimal Recalculate() => Total;
    private void Normalise() { }
}
`

// The fixer's only evidence for "this member does not exist" was the compiler's one-line
// diagnostic. missingMemberFacts turns that into declared-member truth — "Order has NO member
// SkuList; declared: Id, CustomerId, Total, AddLine, Recalculate" — which is what stops the next
// round inventing a different member of the same type. Java has had it since the Mockito work;
// the function returned nil for every other language.
func TestMissingMemberFacts_csharpMissingMember(t *testing.T) {
	const out = `/workspace/tests/App.Tests/OrderTests.cs(12,27): error CS1061: 'Order' does not contain a definition for 'SkuList' and no accessible extension method 'SkuList' accepting a first argument of type 'Order' could be found (are you missing a using directive or an assembly reference?) [/workspace/tests/App.Tests/App.Tests.csproj]`

	facts := csharpMissingMemberFacts(out, map[string]string{
		"src/Core/Models/Order.cs": orderSource,
	}, nil)
	if len(facts) == 0 {
		t.Fatal("no facts produced for a CS1061 against a repo-owned type")
	}
	joined := strings.Join(facts, "\n")
	if !strings.Contains(joined, "SkuList") {
		t.Errorf("the rejected member is not named:\n%s", joined)
	}
	for _, want := range []string{"Id", "CustomerId", "Total", "AddLine", "Recalculate"} {
		if !strings.Contains(joined, want) {
			t.Errorf("declared member %q is not listed:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Normalise") {
		t.Errorf("a private member was offered as a call target:\n%s", joined)
	}
	if !strings.Contains(joined, "src/Core/Models/Order.cs") {
		t.Errorf("the fact does not say where the type is declared:\n%s", joined)
	}
}

// CS0117 is the same claim about a static/enum member and must read the same way.
func TestMissingMemberFacts_csharpCS0117(t *testing.T) {
	const out = `/workspace/tests/App.Tests/OrderTests.cs(9,31): error CS0117: 'OrderState' does not contain a definition for 'Cancelled'`
	facts := csharpMissingMemberFacts(out, map[string]string{
		"src/Core/Models/OrderState.cs": "namespace Shop.Core.Models;\n\npublic enum OrderState { Open, Locked, CheckedOut }\n",
	}, nil)
	joined := strings.Join(facts, "\n")
	if !strings.Contains(joined, "Cancelled") {
		t.Fatalf("the rejected member is not named:\n%s", joined)
	}
	for _, want := range []string{"Open", "Locked", "CheckedOut"} {
		if !strings.Contains(joined, want) {
			t.Errorf("enum member %q is not listed:\n%s", want, joined)
		}
	}
}

// CS1729 and CS7036 are about CONSTRUCTORS: the type exists, the call does not match any of its
// signatures. Listing the arities the source actually declares is the fact that resolves it.
func TestMissingMemberFacts_csharpConstructorArity(t *testing.T) {
	const out = `/workspace/tests/App.Tests/OrderTests.cs(7,23): error CS1729: 'Order' does not contain a constructor that takes 0 arguments`
	facts := csharpMissingMemberFacts(out, map[string]string{
		"src/Core/Models/Order.cs": orderSource,
	}, nil)
	joined := strings.Join(facts, "\n")
	if !strings.Contains(joined, "constructor") {
		t.Fatalf("no constructor fact:\n%s", joined)
	}
	if !strings.Contains(joined, "Order(string id)") && !strings.Contains(joined, "1 argument") {
		t.Errorf("the declared constructor is not described:\n%s", joined)
	}
}

// A type the repository does not declare is the classpath surface's job. Claiming a member is
// absent from a third-party type, from a source scan that never saw it, would be a false statement
// attached to a destructive instruction.
func TestMissingMemberFacts_csharpIgnoresThirdPartyTypes(t *testing.T) {
	const out = `/workspace/tests/App.Tests/T.cs(5,10): error CS1061: 'ILogger' does not contain a definition for 'LogSomething'`
	if facts := csharpMissingMemberFacts(out, map[string]string{
		"src/Core/Models/Order.cs": orderSource,
	}, nil); len(facts) != 0 {
		t.Fatalf("claimed a fact about a type the repo does not declare:\n%v", facts)
	}
}

// The generated test class itself: the fixer wrote a call to a helper it never defined. The fact
// has to say that rather than list the SUT's members.
func TestMissingMemberFacts_csharpTestClassItself(t *testing.T) {
	const out = `/workspace/tests/App.Tests/OrderTests.cs(14,14): error CS1061: 'OrderTests' does not contain a definition for 'BuildOrder'`
	facts := csharpMissingMemberFacts(out, map[string]string{
		"tests/App.Tests/OrderTests.cs": "namespace Shop.Tests;\n\npublic class OrderTests\n{\n    [Fact] public void A() { }\n}\n",
	}, []string{"tests/App.Tests/OrderTests.cs"})
	joined := strings.Join(facts, "\n")
	if !strings.Contains(joined, "BuildOrder") {
		t.Fatalf("the missing helper is not named:\n%s", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "define") && !strings.Contains(strings.ToLower(joined), "declare") {
		t.Errorf("the fact does not tell the model it must define the helper:\n%s", joined)
	}
}

// Nothing to say is said as nothing.
func TestMissingMemberFacts_csharpQuietWhenIrrelevant(t *testing.T) {
	for _, out := range []string{
		"",
		`/workspace/T.cs(8,17): error CS1503: Argument 1: cannot convert from 'string' to 'int'`,
		"Passed!  - Failed: 0, Passed: 3",
	} {
		if facts := csharpMissingMemberFacts(out, map[string]string{"src/Core/Models/Order.cs": orderSource}, nil); len(facts) != 0 {
			t.Errorf("facts produced for %q:\n%v", out, facts)
		}
	}
}
