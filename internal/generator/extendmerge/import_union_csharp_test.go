package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A realistic xUnit test file: file-scoped namespace, a `using static` for Assert (the idiomatic
// xUnit/FluentAssertions style), and no System.Linq.
const xunitOwnerTests = `// Copyright (c) The ASQS Authors.

using System;
using static Xunit.Assert;

namespace Petclinic.Owners.Tests;

public class OwnerControllerTests
{
    [Fact]
    public void Existing()
    {
        True(true);
    }
}
`

func extendCSharp(t *testing.T, repo, rel, payload string) string {
	t.Helper()
	n, _, _ := Write(repo, []Item{{
		Path:             rel,
		Content:          payload,
		ExtendExisting:   true,
		SourceSymbolFile: "src/Petclinic/Owners/OwnerController.cs",
	}})
	if n != 1 {
		t.Fatalf("expected the extend write to land; wrote %d file(s)", n)
	}
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Defect 1: `using static X;` matched neither the hoist regex nor the payload classifier, because
// both required `using` to be followed directly by a dotted path. The directive stayed in the
// payload, was classified members-only, and got spliced INSIDE the class body — which roslyn
// rejects with "A using clause must precede all other elements defined in the namespace".
//
// This is the same failure class F04 fixed for Java, and it was live for every C# repo.
func TestExtendExistingCSharp_hoistsUsingStaticOutOfTheClassBody(t *testing.T) {
	rel := "tests/Petclinic.Tests/OwnerControllerTests.cs"
	repo := writeTemp(t, rel, xunitOwnerTests)

	payload := `using static FluentAssertions.AssertionExtensions;
using System.Linq;

[Fact]
public void FiltersOwners()
{
    var names = new[] { "a" }.Where(x => x != null);
    Equal(1, names.Count());
}`

	got := extendCSharp(t, repo, rel, payload)

	classAt := strings.Index(got, "public class OwnerControllerTests")
	for _, want := range []string{"using static FluentAssertions.AssertionExtensions;", "using System.Linq;"} {
		at := strings.Index(got, want)
		if at < 0 {
			t.Errorf("merged file is missing %q:\n%s", want, got)
			continue
		}
		if at > classAt {
			t.Errorf("%q was spliced at/after the class declaration — roslyn rejects a using clause there:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "FiltersOwners") {
		t.Errorf("the new test method did not survive the merge:\n%s", got)
	}
	if strings.Count(got, "using static Xunit.Assert;") != 1 {
		t.Errorf("existing using duplicated:\n%s", got)
	}
}

// Defect 2: `global using` and `using static` shared one bool, so neither could be represented
// faithfully. Each of the four C# forms must round-trip through parse -> render unchanged.
func TestParseImportsCSharp_roundTripsEveryDirectiveForm(t *testing.T) {
	src := strings.Join([]string{
		"using System;",
		"using static Xunit.Assert;",
		"global using System.Linq;",
		"global using static System.Math;",
		"using Sb = System.Text.StringBuilder;",
		"using IntList = System.Collections.Generic.List<int>;",
	}, "\n")

	got := parseImports(src, ".cs")
	if len(got) != 6 {
		t.Fatalf("parsed %d directive(s), want 6: %+v", len(got), got)
	}
	for _, want := range strings.Split(src, "\n") {
		found := false
		for _, d := range got {
			if d.render(".cs") == want {
				found = true
				break
			}
		}
		if !found {
			var rendered []string
			for _, d := range got {
				rendered = append(rendered, d.render(".cs"))
			}
			t.Errorf("%q did not round-trip; got %v", want, rendered)
		}
	}
}

// A using STATEMENT / DECLARATION inside a method body is not a directive. Stripping one would
// delete live code from the payload.
func TestParseImportsCSharp_ignoresUsingStatements(t *testing.T) {
	body := "[Fact]\npublic void A()\n{\n    using var conn = new SqlConnection();\n    using (var s = Open()) { }\n}\n"
	if got := parseImports(body, ".cs"); len(got) != 0 {
		t.Errorf("using statements were parsed as directives: %+v", got)
	}
	imports, rest := hoistTopLevelImports("A.cs", body)
	if len(imports) != 0 {
		t.Errorf("hoisted a using statement: %+v", imports)
	}
	if !strings.Contains(rest, "using var conn") || !strings.Contains(rest, "using (var s = Open())") {
		t.Errorf("hoisting deleted live statements from the payload:\n%s", rest)
	}
}

// Defect 3: with no existing usings the merge inserted at byte 0, landing before the licence header
// and concatenating onto it — `using System.Linq;// Copyright header`. Java fails closed in the
// equivalent case; C# reported success while corrupting the file.
func TestMergeImportsIntoFileCSharp_anchorChain(t *testing.T) {
	linq := parseImports("using System.Linq;", ".cs")

	t.Run("after the last existing using", func(t *testing.T) {
		src := "// header\n\nusing System;\n\nnamespace N;\n\npublic class T { }\n"
		got, ok := mergeImportsIntoFile(src, linq, ".cs")
		if !ok {
			t.Fatal("expected an anchor")
		}
		lines := strings.Split(got, "\n")
		if lines[2] != "using System;" || lines[3] != "using System.Linq;" {
			t.Errorf("new using should follow the existing block:\n%s", got)
		}
	})

	t.Run("after a file-scoped namespace when there are no usings", func(t *testing.T) {
		src := "// Copyright header\n\nnamespace N;\n\npublic class T { }\n"
		got, ok := mergeImportsIntoFile(src, linq, ".cs")
		if !ok {
			t.Fatal("expected an anchor")
		}
		if strings.Contains(got, "using System.Linq;// Copyright header") {
			t.Fatalf("using was concatenated onto the header line:\n%s", got)
		}
		nsAt := strings.Index(got, "namespace N;")
		usingAt := strings.Index(got, "using System.Linq;")
		if usingAt < nsAt {
			t.Errorf("a file-scoped namespace may be followed by usings; got them before it:\n%s", got)
		}
		if !strings.HasPrefix(got, "// Copyright header") {
			t.Errorf("header must stay first:\n%s", got)
		}
	})

	t.Run("before a block namespace", func(t *testing.T) {
		src := "// header\n\nnamespace N\n{\n    public class T { }\n}\n"
		got, ok := mergeImportsIntoFile(src, linq, ".cs")
		if !ok {
			t.Fatal("expected an anchor")
		}
		if strings.Index(got, "using System.Linq;") > strings.Index(got, "namespace N") {
			t.Errorf("a block namespace may not be preceded by its usings inside the file scope:\n%s", got)
		}
	})

	t.Run("after the header when there is no namespace", func(t *testing.T) {
		src := "// Copyright\n// second line\n#nullable enable\n\npublic class T { }\n"
		got, ok := mergeImportsIntoFile(src, linq, ".cs")
		if !ok {
			t.Fatal("expected an anchor")
		}
		if !strings.HasPrefix(got, "// Copyright\n// second line\n#nullable enable") {
			t.Errorf("comments and preprocessor directives must stay above the usings:\n%s", got)
		}
		if strings.Index(got, "using System.Linq;") > strings.Index(got, "public class T") {
			t.Errorf("using landed after the type declaration:\n%s", got)
		}
	})

	t.Run("fails closed on a header-only file", func(t *testing.T) {
		if _, ok := mergeImportsIntoFile("// just a comment\n\n", linq, ".cs"); ok {
			t.Error("a file with nothing to extend has no correct using position; expected fail-closed")
		}
	})
}

// A repeated alias name is CS1537 — a hard error — regardless of whether the target matches.
func TestUnionImportsCSharp_refusesDuplicateAlias(t *testing.T) {
	existing := parseImports("using Sb = System.Text.StringBuilder;", ".cs")
	incoming := parseImports("using Sb = System.Collections.Generic.List<int>;", ".cs")

	add, skipped := unionImports(existing, incoming, ".cs")
	if len(add) != 0 {
		t.Errorf("added a colliding alias (CS1537): %+v", add)
	}
	if len(skipped) != 1 {
		t.Fatalf("collision not reported: %v", skipped)
	}
	for _, reason := range skipped {
		if !strings.Contains(reason, "CS1537") {
			t.Errorf("skip reason should name the error; got %q", reason)
		}
	}
}

// A local using duplicating a global one is CS8933 (a warning) — redundant, so skip it, but the
// skip message must be rendered in C# syntax, not Java's.
func TestUnionImportsCSharp_skipsRedundantAndRendersCSharpSyntax(t *testing.T) {
	existing := parseImports("global using System.Linq;", ".cs")
	incoming := parseImports("using System.Linq;\nusing System.Text;", ".cs")

	add, skipped := unionImports(existing, incoming, ".cs")
	if len(add) != 1 || add[0].path != "System.Text" {
		t.Errorf("add = %+v, want only System.Text", add)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want the redundant local using", skipped)
	}
	for key := range skipped {
		if strings.HasPrefix(key, "import ") {
			t.Errorf("C# skip rendered with Java syntax: %q", key)
		}
		if key != "using System.Linq;" {
			t.Errorf("skip key = %q, want the C# directive", key)
		}
	}
}

// `using static A;` and `using A;` are different directives and must not dedupe against each other.
func TestUnionImportsCSharp_staticAndPlainAreDistinct(t *testing.T) {
	existing := parseImports("using System.Math;", ".cs")
	incoming := parseImports("using static System.Math;", ".cs")

	add, _ := unionImports(existing, incoming, ".cs")
	if len(add) != 1 || add[0].kind != importStatic {
		t.Errorf("a using static must not be swallowed by a plain using of the same path: %+v", add)
	}
}
