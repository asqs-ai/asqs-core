package errout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func csRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for rel, body := range map[string]string{
		"src/Core/Models/Order.cs":            "namespace Shop.Core.Models;\n\npublic class Order { }\n",
		"src/Core/Services/PricingService.cs": "namespace Shop.Core.Services;\n\npublic sealed class PricingService { }\n",
		"src/Core/Models/OrderState.cs":       "namespace Shop.Core.Models;\n\npublic enum OrderState { Open }\n",
		// Build output must never be offered as the type's source.
		"src/Core/obj/Debug/net8.0/Order.g.cs": "namespace Shop.Core.Models; public class Order { }",
	} {
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

// CS0246 says a type could not be found. When the repository declares it, the fixer's prompt should
// carry that file — the repair is almost always a using, and the source states the namespace.
// ResolveMissingTypeFiles answered for Java and Kotlin only, so a C# round had to guess.
func TestResolveMissingTypeFiles_csharp(t *testing.T) {
	repo := csRepo(t)
	const out = `/workspace/tests/App.Tests/T.cs(6,9): error CS0246: The type or namespace name 'PricingService' could not be found (are you missing a using directive or an assembly reference?)`

	got := ResolveMissingTypeFiles(out, repo, "csharp", 5)
	if len(got) == 0 {
		t.Fatal("the declaring file was not offered")
	}
	if got[0] != "src/Core/Services/PricingService.cs" {
		t.Errorf("got %v, want src/Core/Services/PricingService.cs first", got)
	}
	for _, p := range got {
		if strings.Contains(p, "/obj/") || strings.Contains(p, "/bin/") {
			t.Errorf("build output offered as source: %s", p)
		}
	}
}

// CS0234 names a namespace member, CS0103 a bare name in scope. Both resolve the same way.
func TestResolveMissingTypeFiles_csharpOtherDiagnostics(t *testing.T) {
	repo := csRepo(t)
	cases := []struct{ name, out, want string }{
		{
			name: "CS0234",
			out:  `/workspace/T.cs(2,20): error CS0234: The type or namespace name 'OrderState' does not exist in the namespace 'Shop.Core' (are you missing an assembly reference?)`,
			want: "src/Core/Models/OrderState.cs",
		},
		{
			name: "CS0103",
			out:  `/workspace/T.cs(9,9): error CS0103: The name 'Order' does not exist in the current context`,
			want: "src/Core/Models/Order.cs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveMissingTypeFiles(tc.out, repo, "csharp", 5)
			found := false
			for _, p := range got {
				if p == tc.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("got %v, want it to include %s", got, tc.want)
			}
		})
	}
}

// A type the repository does not declare has no file to offer, and inventing one would fill the
// prompt's file budget with something irrelevant.
func TestResolveMissingTypeFiles_csharpUnknownTypeOffersNothing(t *testing.T) {
	repo := csRepo(t)
	const out = `/workspace/T.cs(1,7): error CS0246: The type or namespace name 'Xunit' could not be found`
	if got := ResolveMissingTypeFiles(out, repo, "csharp", 5); len(got) != 0 {
		t.Fatalf("offered %v for a type the repo does not declare", got)
	}
}

// The limit is the prompt's file budget and must be respected.
func TestResolveMissingTypeFiles_csharpRespectsTheLimit(t *testing.T) {
	repo := csRepo(t)
	const out = `error CS0246: The type or namespace name 'Order' could not be found
error CS0246: The type or namespace name 'PricingService' could not be found
error CS0246: The type or namespace name 'OrderState' could not be found`
	if got := ResolveMissingTypeFiles(out, repo, "csharp", 2); len(got) > 2 {
		t.Fatalf("returned %d files for a limit of 2: %v", len(got), got)
	}
}

// Java is untouched.
func TestResolveMissingTypeFiles_javaUnchanged(t *testing.T) {
	repo := t.TempDir()
	p := filepath.Join(repo, "src", "main", "java", "org", "example", "Vet.java")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("package org.example; public class Vet {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	const out = `T.java:5: error: cannot find symbol
  symbol:   class Vet
  location: class org.example.VetTest`
	if got := ResolveMissingTypeFiles(out, repo, "java", 5); len(got) == 0 {
		t.Fatal("the Java path stopped resolving")
	}
}
