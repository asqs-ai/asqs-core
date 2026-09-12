package evaluator

import (
	"context"
	"strings"
	"testing"
)

// The mock-misuse facts were Mockito-only. A C# run got nothing from a Moq or NSubstitute failure,
// so the fixer argued with a stack trace instead of being told what was actually wrong — the same
// gap the Java arm closed after the Mockito work.
func TestTestFailureFacts_csharpMockFrameworks(t *testing.T) {
	files := map[string]string{
		"tests/Shop.Tests/BasketTests.cs": `using NSubstitute;
using Moq;
using NUnit.Framework;

public class BasketTests
{
    [Test]
    public void NotASubstitute()
    {
        var policy = new FlatDiscountPolicy();
        policy.Rate().Returns(5);
    }

    [Test]
    public void StrictMock()
    {
        var repo = new Mock<IBasketRepository>(MockBehavior.Strict);
        repo.Object.Load("id");
    }
}`,
	}
	artifacts := []string{"tests/Shop.Tests/BasketTests.cs"}

	cases := []struct {
		name   string
		output string
		want   []string
	}{
		{
			name: "NSubstitute told to stub a real object",
			output: `  Failed NotASubstitute [12 ms]
  Error Message:
   NSubstitute.Exceptions.CouldNotSetReturnDueToNoLastCallException : Could not find a call to return from.
  Stack Trace:
     at Shop.Tests.BasketTests.NotASubstitute() in /workspace/tests/Shop.Tests/BasketTests.cs:line 11`,
			want: []string{"policy", "Substitute.For"},
		},
		{
			name: "Moq strict mock with no matching setup",
			output: `  Failed StrictMock [4 ms]
  Error Message:
   Moq.MockException : IBasketRepository.Load("id") invocation failed with mock behavior Strict.
All invocations on the mock must have a corresponding setup.
  Stack Trace:
     at Shop.Tests.BasketTests.StrictMock() in /workspace/tests/Shop.Tests/BasketTests.cs:line 18`,
			want: []string{"Strict", "Setup"},
		},
		{
			name: "Moq verification that never happened",
			output: `  Failed Verifies [4 ms]
  Error Message:
   Moq.MockException : Expected invocation on the mock at least once, but was never performed: r => r.Load("id")
  Stack Trace:
     at Shop.Tests.BasketTests.Verifies() in /workspace/tests/Shop.Tests/BasketTests.cs:line 18`,
			want: []string{"never"},
		},
		{
			name: "NSubstitute received-call assertion",
			output: `  Failed Receives [4 ms]
  Error Message:
   NSubstitute.Exceptions.ReceivedCallsException : Expected to receive exactly 1 call matching: Load("id")
  Stack Trace:
     at Shop.Tests.BasketTests.Receives() in /workspace/tests/Shop.Tests/BasketTests.cs:line 18`,
			want: []string{"Received"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testFailureFacts(context.Background(), StepTest, tc.output, files, artifacts, nil)
			if len(got) == 0 {
				t.Fatalf("no facts derived from a %s failure", tc.name)
			}
			joined := strings.Join(got, " | ")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("facts = %q, missing %q", joined, want)
				}
			}
		})
	}
}

// Output with none of the exceptions must produce nothing: a fact stated from no evidence is worse
// than silence.
func TestTestFailureFacts_csharpSilentWithoutAMockException(t *testing.T) {
	files := map[string]string{"tests/T.cs": "public class T { }"}
	const out = `  Failed M [1 ms]
  Error Message:
   Assert.That(1, Is.EqualTo(2))
  Stack Trace:
     at T.M() in /workspace/tests/T.cs:line 3`
	if got := testFailureFacts(context.Background(), StepTest, out, files, []string{"tests/T.cs"}, nil); len(got) != 0 {
		t.Fatalf("facts = %v for an ordinary assertion failure", got)
	}
}

// The `using` residue advisory catches a deletion-shaped fix: the round adds imports, then removes
// the code that needed them. It ran for .java only.
//
// Only the ALIAS form is judged, because it is the only C# directive that names an identifier the
// body is obliged to write. `using Moq;` imports a namespace — a test writing `new Mock<IRepo>()`
// never contains "Moq" — and `using static Xunit.Assert;` imports members, so the body writes
// `True(...)` rather than `Assert`. Java's rule applied to either would report a correct file's
// imports as dead residue, and this advisory goes straight into the fixer's prompt.
func TestUnusedImportResidueReason_csharp(t *testing.T) {
	const before = `using Xunit;

public class T { }`
	after := `using Xunit;
using Sut = Shop.Core.Basket;

public class T { }`
	if got := unusedImportResidueReason("tests/T.cs", before, after); !strings.Contains(got, "Sut") {
		t.Errorf("reason = %q, want it to name the unused alias", got)
	}

	used := `using Xunit;
using Sut = Shop.Core.Basket;

public class T { public void M() { var b = new Sut("id"); } }`
	if got := unusedImportResidueReason("tests/T.cs", before, used); got != "" {
		t.Errorf("reason = %q, but the alias is referenced", got)
	}

	// The two forms this cannot judge must stay silent.
	unjudgeable := `using Xunit;
using Moq;
using static Xunit.Assert;

public class T { public void M() { True(new Mock<int>().Object == 0); } }`
	if got := unusedImportResidueReason("tests/T.cs", before, unjudgeable); got != "" {
		t.Errorf("reason = %q for directives whose use cannot be proved from the body", got)
	}

	// A directive the round inherited is not its doing.
	if got := unusedImportResidueReason("tests/T.cs", after, after); got != "" {
		t.Errorf("reason = %q for directives the round inherited", got)
	}
}
