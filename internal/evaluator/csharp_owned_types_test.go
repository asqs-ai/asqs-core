package evaluator

import "testing"

// repoDeclaredTypeNames feeds FilterOwnedTypes, which stops the API-surface lookup spending its
// budget on types the repository declares itself — the classpath cannot answer for those, and on
// the fixer's path they are exactly the ones not yet compiled. Java derived them from the
// src/main/java path mirror; C# has no such mirror and got nothing, so every repo type in a
// diagnostic consumed a lookup slot and came back empty.
func TestRepoDeclaredTypeNamesForLang_csharp(t *testing.T) {
	files := map[string]string{
		"src/Core/Models/Order.cs": `namespace Shop.Core.Models;

public class Order { }
public record BasketLine(string Sku);
`,
		"src/Core/Services/PricingService.cs": "namespace Shop.Core.Services;\npublic sealed class PricingService { }\n",
		// A file-scoped namespace and a block-scoped one must both work.
		"src/Api/Controllers/OrdersController.cs": `namespace Shop.Api.Controllers
{
    public class OrdersController { }
}
`,
		// Not C#, and not a declaration.
		"README.md": "# Shop\npublic class NotReal { }\n",
	}

	got := repoDeclaredTypeNamesForLang("csharp", files)
	for _, want := range []string{
		"Shop.Core.Models.Order",
		"Shop.Core.Models.BasketLine",
		"Shop.Core.Services.PricingService",
		"Shop.Api.Controllers.OrdersController",
	} {
		if !got[want] {
			t.Errorf("%q not derived; got %v", want, got)
		}
	}
	if got["NotReal"] {
		t.Error("a markdown file contributed a type name")
	}
	// The simple name is recorded too: a diagnostic often names the type without its namespace.
	if !got["Order"] {
		t.Error("the simple name was not recorded; a bare-name target would still consume a slot")
	}
}

// Java keeps its exact path-to-FQCN mapping, which is what makes its filter safe.
func TestRepoDeclaredTypeNamesForLang_javaUnchanged(t *testing.T) {
	files := map[string]string{
		"src/main/java/org/example/Vet.java":     "package org.example; class Vet {}",
		"src/test/java/org/example/VetTest.java": "package org.example; class VetTest {}",
	}
	got := repoDeclaredTypeNamesForLang("java", files)
	for _, want := range []string{"org.example.Vet", "org.example.VetTest"} {
		if !got[want] {
			t.Errorf("%q not derived; got %v", want, got)
		}
	}
}

// A language with no mapping gets nothing rather than a guess.
func TestRepoDeclaredTypeNamesForLang_otherLanguages(t *testing.T) {
	files := map[string]string{"src/app/thing.ts": "export class Thing {}"}
	if got := repoDeclaredTypeNamesForLang("typescript", files); len(got) != 0 {
		t.Errorf("TS derived %v, want nothing", got)
	}
}
