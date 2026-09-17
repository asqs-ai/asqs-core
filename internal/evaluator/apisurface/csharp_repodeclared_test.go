package apisurface

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCS(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// RepoDeclaredSimpleNames bounds a NEGATIVE claim: the prompt block that consumes it says "no
// import makes these resolve … delete the code that uses them". Java was the only language with an
// answer, so for C# the block never rendered — which is safe but silent, and the fixer kept
// spending lookup slots on names the repository declares itself.
//
// C# source lives anywhere under the repo, so unlike Java there is no source root to key on: the
// walk is the whole tree minus build output, and supported=false unless it completes.
func TestRepoDeclaredSimpleNames_csharp(t *testing.T) {
	repo := t.TempDir()
	writeCS(t, repo, "src/Core/Models/Order.cs", `namespace Shop.Core.Models;

public class Order { public string Id { get; set; } = ""; }

public record BasketLine(string Sku, int Quantity);

public enum OrderState { Open, Closed }

public interface IOrderSink { void Accept(Order o); }

public struct Money { public decimal Amount; }

public delegate void OrderHandler(Order o);
`)
	writeCS(t, repo, "src/Core/Services/PricingService.cs", "namespace Shop.Core.Services;\n\npublic sealed class PricingService { }\n")
	// Build output must not contribute: a stale generated type would license the opposite claim.
	writeCS(t, repo, "src/Core/obj/Debug/net8.0/Generated.cs", "namespace Shop; public class StaleGenerated {}")
	writeCS(t, repo, "src/Core/bin/Release/net8.0/AlsoStale.cs", "namespace Shop; public class AlsoStale {}")

	names, ok := RepoDeclaredSimpleNames(LangCSharp, repo)
	if !ok {
		t.Fatal("supported=false for a C# repository the walk could read")
	}
	for _, want := range []string{"Order", "BasketLine", "OrderState", "IOrderSink", "Money", "OrderHandler", "PricingService"} {
		if !names[want] {
			t.Errorf("%q was not recorded as declared; got %v", want, names)
		}
	}
	for _, unwanted := range []string{"StaleGenerated", "AlsoStale"} {
		if names[unwanted] {
			t.Errorf("%q came from build output and must not count", unwanted)
		}
	}
}

// A name inside a comment or a string is not a declaration. Getting this wrong makes the absence
// claim wrong in the safe direction (fewer absences), but it also wastes the lookup budget the
// filter exists to protect.
func TestRepoDeclaredSimpleNames_csharpIgnoresCommentsAndStrings(t *testing.T) {
	repo := t.TempDir()
	writeCS(t, repo, "src/Notes.cs", `namespace Shop;

// public class CommentedOut { }
public static class Templates
{
    public const string Snippet = @"public class InsideAString { }";
}
`)
	names, ok := RepoDeclaredSimpleNames(LangCSharp, repo)
	if !ok {
		t.Fatal("supported=false")
	}
	if !names["Templates"] {
		t.Error("the real declaration was missed")
	}
	for _, unwanted := range []string{"CommentedOut", "InsideAString"} {
		if names[unwanted] {
			t.Errorf("%q is not a declaration", unwanted)
		}
	}
}

// Java is unchanged, and a language with no answer still says so rather than returning an empty set
// that would read as "nothing is declared here".
func TestRepoDeclaredSimpleNames_unsupportedLanguagesUnchanged(t *testing.T) {
	repo := t.TempDir()
	writeCS(t, repo, "src/app/thing.ts", "export class Thing {}")
	if _, ok := RepoDeclaredSimpleNames(LangNode, repo); ok {
		t.Error("TS/JS claims support; module names are not filenames there")
	}
}

// A walk that cannot complete must report supported=false: an incomplete set would make every name
// it failed to see read as proof of absence.
func TestRepoDeclaredSimpleNames_csharpEmptyRepo(t *testing.T) {
	repo := t.TempDir()
	names, ok := RepoDeclaredSimpleNames(LangCSharp, repo)
	if !ok {
		t.Fatal("an empty but readable repository is still an answerable question")
	}
	if len(names) != 0 {
		t.Errorf("names = %v, want none", names)
	}
	if _, ok := RepoDeclaredSimpleNames(LangCSharp, ""); ok {
		t.Error("an empty repo path claims support")
	}
}
