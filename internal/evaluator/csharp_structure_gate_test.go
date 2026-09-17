package evaluator

import (
	"strings"
	"testing"
)

// The C# half of the pre-write structural gates. Java has refused both shapes since
// run api-4f92fec6985aee5e4ce48de0041747d2; C# had neither, so a file with a stray fragment above
// the type, or a statement the compiler reads as two, reached disk and spent a round being found.
//
// Every case below that expects NO reason is the reason this gate is bounded the way it is: a false
// rejection destroys a correct artifact, and C# has far more legal `)`-then-identifier shapes than
// Java does.
func TestCSharpStatementStructureReason(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "two statements run together",
			src:  "public class T { public void M() { Assert.True(true) Assert.False(false); } }",
			want: true,
		},
		{
			name: "a tuple return type is followed by the method name",
			src:  "public class T { public (int Total, string Label) Describe() => (1, \"a\"); }",
			want: false,
		},
		{
			name: "a generic constraint follows the parameter list",
			src:  "public class T { public void M<TItem>(TItem x) where TItem : class { } }",
			want: false,
		},
		{
			name: "an exception filter follows the catch header",
			src:  "public class T { public void M() { try { } catch (Exception ex) when (ex.Message.Length > 0) { } } }",
			want: false,
		},
		{
			name: "an expression-bodied member",
			src:  "public class T { public int M(int a) => a * 2; }",
			want: false,
		},
		{
			name: "a cast",
			src:  "public class T { public void M(object o) { var n = (int)o; var s = (string)o; } }",
			want: false,
		},
		{
			name: "a foreach header",
			src:  "public class T { public void M(int[] xs) { foreach (var x in xs) Console.WriteLine(x); } }",
			want: false,
		},
		{
			name: "a using statement header",
			src:  "public class T { public void M() { using (var s = Open()) s.Read(); } }",
			want: false,
		},
		{
			name: "an attribute argument list before a declaration",
			src:  "public class T { [Theory] [InlineData(1, 2)] public void M(int a, int b) { } }",
			want: false,
		},
		{
			name: "a primary constructor before a base list",
			src:  "public class Shop(int id) : Base(id) { }",
			want: false,
		},
		{
			name: "a switch expression arm",
			src:  "public class T { public string M(int x) => x switch { 1 => \"one\", _ => \"other\" }; }",
			want: false,
		},
		{
			name: "a pattern with a designation",
			src:  "public class T { public void M(object o) { if (o is string s) Use(s); } }",
			want: false,
		},
		{
			name: "a null-conditional call chain",
			src:  "public class T { public void M(string s) { var n = s?.Trim()?.Length ?? 0; } }",
			want: false,
		},
		{
			name: "a lambda passed to a call",
			src:  "public class T { public void M() { Items.Where(x => x.Ok()).ToList(); } }",
			want: false,
		},
		{
			name: "text inside a string is not code",
			src:  "public class T { const string S = \"Assert.True(true) Assert.False(false);\"; }",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CSharpStatementStructureReason(tc.src)
			if (got != "") != tc.want {
				t.Fatalf("CSharpStatementStructureReason = %q, want violation = %v", got, tc.want)
			}
		})
	}
}

// The stray-token gate: content between the file header and the first type declaration is what
// roslyn rejects as "type or namespace definition, or end-of-file expected".
func TestCSharpStrayTokenBeforeTypeReason(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "a stray fragment above the type",
			src:  "using Xunit;\n\nAssert.True(true);\n\npublic class T { }",
			want: true,
		},
		{
			name: "an ordinary header",
			src:  "using System;\nusing Xunit;\n\nnamespace Shop.Tests;\n\npublic class T { }",
			want: false,
		},
		{
			name: "a global using and an alias",
			src:  "global using System;\nusing Sut = Shop.Core.OrderService;\n\npublic class T { }",
			want: false,
		},
		{
			name: "an attribute list wrapping across lines",
			src:  "using Xunit;\n\n[Collection(\"db\")]\n[Trait(\"kind\",\n    \"integration\")]\npublic class T { }",
			want: false,
		},
		{
			name: "an assembly-level attribute",
			src:  "using Xunit;\n\n[assembly: CollectionBehavior(DisableTestParallelization = true)]\n\npublic class T { }",
			want: false,
		},
		{
			name: "a preprocessor directive",
			src:  "using Xunit;\n#if NET8_0_OR_GREATER\n#region setup\n\npublic class T { }\n#endregion\n#endif",
			want: false,
		},
		{
			name: "a block-scoped namespace",
			src:  "using Xunit;\n\nnamespace Shop.Tests\n{\n    public class T { }\n}",
			want: false,
		},
		{
			name: "a comment above the type",
			src:  "using Xunit;\n\n// Tests the basket.\n/// <summary>x</summary>\npublic class T { }",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := csharpStrayTokenBeforeTypeReason(stripStringsAndComments(tc.src, ".cs"))
			if (got != "") != tc.want {
				t.Fatalf("csharpStrayTokenBeforeTypeReason = %q, want violation = %v", got, tc.want)
			}
			if tc.want && !strings.Contains(got, "stray token") {
				t.Errorf("reason = %q, want it to name the stray token", got)
			}
		})
	}
}

// A skipped test with an empty body reports as a pass and asserts nothing. xUnit's spelling was
// covered; NUnit's [Ignore] and [Explicit] — which D2 makes first-class — were not, so the same
// empty shell was refused for one runner and accepted for the other two.
func TestCSharpSkipShell_coversEveryRunnersSpelling(t *testing.T) {
	shells := []string{
		`[Fact(Skip = "flaky")]` + "\npublic void M() { }",
		`[Ignore]` + "\npublic void M() { }",
		`[Ignore("needs a database")]` + "\npublic void M() { }",
		`[Explicit]` + "\npublic void M() { }",
		`[Explicit("run by name")]` + "\npublic void M() { }",
	}
	for _, sh := range shells {
		if !reFixLowValueCSharpSkip.MatchString(sh) {
			t.Errorf("skip shell not matched:\n%s", sh)
		}
	}
	// A test that is skipped but still has a body is a real test somebody turned off on purpose,
	// and an [Ignore] on a whole class is not a shell either.
	for _, ok := range []string{
		`[Ignore("x")]` + "\npublic void M() { Assert.True(true); }",
		`[Ignore]` + "\npublic class Suite { public void M() { Assert.True(true); } }",
	} {
		if reFixLowValueCSharpSkip.MatchString(ok) {
			t.Errorf("matched a test that has a body:\n%s", ok)
		}
	}
}

// Both new gates have to be reachable from the pre-write check itself, or they are inert.
func TestCSharpSyntacticShellReason_reachesTheStructuralGates(t *testing.T) {
	stray := "using Xunit;\n\nAssert.True(true);\n\npublic class T { }"
	if got := csharpSyntacticShellReason(stray); !strings.Contains(got, "stray token") {
		t.Errorf("csharpSyntacticShellReason = %q, want the stray-token reason", got)
	}
	runTogether := "public class T { public void M() { Assert.True(true) Assert.False(false); } }"
	if got := csharpSyntacticShellReason(runTogether); !strings.Contains(got, "malformed statement") {
		t.Errorf("csharpSyntacticShellReason = %q, want the statement-structure reason", got)
	}
	ok := "using Xunit;\n\npublic class T { [Fact] public void M() { Assert.True(true); } }"
	if got := csharpSyntacticShellReason(ok); got != "" {
		t.Errorf("csharpSyntacticShellReason = %q for a correct file", got)
	}
}
