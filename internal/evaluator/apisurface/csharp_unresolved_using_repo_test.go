package apisurface

import (
	"path/filepath"
	"strings"
	"testing"
)

const repairTargetCS = `namespace Clean.Architecture.FunctionalTests;

using Xunit;

public class CreateContributorHandlerTests
{
    [Fact]
    public void A() { }
}
`

// The regression from run api-bdf7539b296a0df65a7cf1bf2bf2739b.
//
// The fixer proposed `using Ardalis.Result;` for a handler whose Handle returns Result<int>. The
// namespace is used by seven files in that repository — and none of them were in the fix prompt,
// which had been clamped to 48,000 runes. The gate read its evidence from the prompt, concluded the
// namespace exists nowhere, and refused the repair three rounds running; the third refusal ended
// the run at iteration 4 of a 20-iteration budget.
//
// Evidence a file carries does not stop existing because the prompt could not afford to carry it.
func TestCSharpIntroducedUnresolvedUsing_repoEvidenceSurvivesPromptClamping(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		// The file that proves the namespace resolves. Deliberately NOT in the prompt.
		"src/Clean.Architecture.UseCases/Contributors/Create/CreateContributorHandler.cs": `using Ardalis.Result;

namespace Clean.Architecture.UseCases.Contributors.Create;

public class CreateContributorHandler { }
`,
	})
	// The prompt holds only the file under repair, as a clamped round does.
	prompt := map[string]string{
		"tests/Clean.Architecture.FunctionalTests/CreateContributorHandlerTests.cs": repairTargetCS,
	}
	after := strings.Replace(repairTargetCS, "using Xunit;", "using Xunit;\nusing Ardalis.Result;", 1)

	if reason := CSharpIntroducedUnresolvedUsingReason(repairTargetCS, after, root, prompt); reason != "" {
		t.Fatalf("refused a using the repository itself provides (the prompt just did not carry the file):\n%s", reason)
	}
}

// The gate's claim is that the namespace exists in neither the repository's sources NOR the
// packages it references — and it never once looked at the packages. A namespace no source has
// imported yet is exactly what a NEW test needs.
func TestCSharpIntroducedUnresolvedUsing_packageReferenceIsEvidence(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Ardalis.Result" Version="8.0.0" />
  </ItemGroup>
</Project>
`,
		"src/App/Thing.cs": "namespace App;\n\npublic class Thing { }\n",
	})
	prompt := map[string]string{"tests/AppTests/ThingTests.cs": repairTargetCS}
	after := strings.Replace(repairTargetCS, "using Xunit;", "using Xunit;\nusing Ardalis.Result;", 1)

	if reason := CSharpIntroducedUnresolvedUsingReason(repairTargetCS, after, root, prompt); reason != "" {
		t.Fatalf("refused a using provided by a referenced package:\n%s", reason)
	}
}

// A NuGet package id and the namespace it ships differ in case often enough that a case-sensitive
// comparison would reinstate the false refusal for the most common test packages of all.
func TestCSharpIntroducedUnresolvedUsing_packageIDCaseIsNotEvidenceOfAbsence(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		"tests/AppTests/AppTests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="xunit" Version="2.9.0" />
  </ItemGroup>
</Project>
`,
		"src/App/Thing.cs": "namespace App;\n\npublic class Thing { }\n",
	})
	prompt := map[string]string{"tests/AppTests/ThingTests.cs": repairTargetCS}
	after := strings.Replace(repairTargetCS, "using Xunit;", "using Xunit;\nusing Xunit.Abstractions;", 1)

	if reason := CSharpIntroducedUnresolvedUsingReason(repairTargetCS, after, root, prompt); reason != "" {
		t.Fatalf("refused Xunit.Abstractions against a repo referencing package \"xunit\":\n%s", reason)
	}
}

// The control. Widening the evidence must not disarm the gate: a namespace that is genuinely in
// neither the sources nor the packages is still refused. In the same run the gate correctly refused
// `using Mediator;` and `using Mediator.Abstractions;`, which that repository really does not have.
func TestCSharpIntroducedUnresolvedUsing_stillRefusesGenuinelyAbsent(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Ardalis.Result" Version="8.0.0" />
  </ItemGroup>
</Project>
`,
		"src/App/Thing.cs": "using Ardalis.Result;\n\nnamespace App;\n\npublic class Thing { }\n",
	})
	prompt := map[string]string{"tests/AppTests/ThingTests.cs": repairTargetCS}
	after := strings.Replace(repairTargetCS, "using Xunit;", "using Xunit;\nusing Mediator.Abstractions;", 1)

	reason := CSharpIntroducedUnresolvedUsingReason(repairTargetCS, after, root, prompt)
	if reason == "" {
		t.Fatal("accepted a namespace present in neither the repository nor its packages")
	}
	if !strings.Contains(reason, "Mediator.Abstractions") {
		t.Errorf("the reason does not name the namespace:\n%s", reason)
	}
}

// With no readable repository there is no evidence, and a negative claim with nothing behind it
// refuses correct repairs — which is the whole failure this file exists to prevent. The prompt is
// NOT a fallback: it is precisely the partial view that produced the false refusal.
func TestCSharpIntroducedUnresolvedUsing_silentWithoutRepoEvidence(t *testing.T) {
	prompt := map[string]string{
		"tests/AppTests/ThingTests.cs": repairTargetCS,
		"src/App/Thing.cs":             "namespace App;\n\npublic class Thing { }\n",
	}
	after := strings.Replace(repairTargetCS, "using Xunit;", "using Xunit;\nusing Totally.Invented;", 1)

	if reason := CSharpIntroducedUnresolvedUsingReason(repairTargetCS, after, filepath.Join(t.TempDir(), "absent"), prompt); reason != "" {
		t.Fatalf("claimed absence from an unreadable repository:\n%s", reason)
	}
}
