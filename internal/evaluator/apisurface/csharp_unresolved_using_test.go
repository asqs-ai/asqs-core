package apisurface

import (
	"strings"
	"testing"
)

const beforeCS = `namespace Shop.Tests;

using Xunit;
using Shop.Core.Models;

public class OrderTests
{
    [Fact]
    public void A() { }
}
`

// A repair round that adds a `using` for a namespace that exists nowhere trades one compile error
// for another, and the loop spends a round discovering that. The Java gate has refused such a round
// since the classpath work; the C# arm returned "" unconditionally, so a round that invented
// `using Shop.Core.Repositories;` for a repo with no such namespace was accepted and written.
func TestCSharpIntroducedUnresolvedUsing(t *testing.T) {
	declared := map[string]bool{
		"Shop.Core.Models":   true,
		"Shop.Core.Services": true,
		"Shop.Tests":         true,
	}
	known := map[string]bool{"Xunit": true, "System": true, "Moq": true}

	after := strings.Replace(beforeCS, "using Shop.Core.Models;",
		"using Shop.Core.Models;\nusing Shop.Core.Repositories;", 1)

	reason := csharpIntroducedUnresolvedUsingReason(beforeCS, after, declared, known)
	if reason == "" {
		t.Fatal("a using for a namespace that exists in neither the repo nor the package closure was accepted")
	}
	if !strings.Contains(reason, "Shop.Core.Repositories") {
		t.Errorf("the reason does not name the namespace:\n%s", reason)
	}
}

// A namespace the repository declares is fine, even if no package provides it.
func TestCSharpIntroducedUnresolvedUsing_repoNamespaceIsFine(t *testing.T) {
	declared := map[string]bool{"Shop.Core.Models": true, "Shop.Core.Services": true}
	after := strings.Replace(beforeCS, "using Shop.Core.Models;",
		"using Shop.Core.Models;\nusing Shop.Core.Services;", 1)

	if reason := csharpIntroducedUnresolvedUsingReason(beforeCS, after, declared, nil); reason != "" {
		t.Fatalf("a repo-declared namespace was refused: %s", reason)
	}
}

// So is one a referenced package provides.
func TestCSharpIntroducedUnresolvedUsing_packageNamespaceIsFine(t *testing.T) {
	known := map[string]bool{"FluentAssertions": true, "Xunit": true}
	after := strings.Replace(beforeCS, "using Xunit;", "using Xunit;\nusing FluentAssertions;", 1)

	if reason := csharpIntroducedUnresolvedUsingReason(beforeCS, after, nil, known); reason != "" {
		t.Fatalf("a package-provided namespace was refused: %s", reason)
	}
}

// Only usings the round ADDED are judged. One that was already there is the state the round
// inherited, and refusing it would block every repair on a file that already fails to compile.
func TestCSharpIntroducedUnresolvedUsing_preexistingIsNotTheRoundsFault(t *testing.T) {
	before := strings.Replace(beforeCS, "using Xunit;", "using Xunit;\nusing Totally.Absent;", 1)
	after := strings.Replace(before, "public void A() { }", "public void A() { Assert.True(true); }", 1)

	if reason := csharpIntroducedUnresolvedUsingReason(before, after, nil, nil); reason != "" {
		t.Fatalf("a pre-existing using was blamed on the round: %s", reason)
	}
}

// With no evidence at all — neither a declared-namespace set nor a package closure — the gate must
// stay silent. A negative claim with nothing behind it would reject correct repairs.
func TestCSharpIntroducedUnresolvedUsing_silentWithoutEvidence(t *testing.T) {
	after := strings.Replace(beforeCS, "using Xunit;", "using Xunit;\nusing Shop.Anything;", 1)
	if reason := csharpIntroducedUnresolvedUsingReason(beforeCS, after, nil, nil); reason != "" {
		t.Fatalf("the gate fired with no evidence to fire on: %s", reason)
	}
}

// A using with an alias, a static using, and a global using are all still usings.
func TestCSharpIntroducedUnresolvedUsing_usingForms(t *testing.T) {
	declared := map[string]bool{"Shop.Core.Models": true}
	for _, form := range []string{
		"using Abs = Shop.Absent.Thing;",
		"using static Shop.Absent.Helpers;",
		"global using Shop.Absent.Globals;",
	} {
		after := strings.Replace(beforeCS, "using Xunit;", "using Xunit;\n"+form, 1)
		if reason := csharpIntroducedUnresolvedUsingReason(beforeCS, after, declared, map[string]bool{"Xunit": true}); reason == "" {
			t.Errorf("form not recognised as a using: %s", form)
		}
	}
}
