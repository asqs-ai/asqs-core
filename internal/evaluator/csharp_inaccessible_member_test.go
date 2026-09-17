package evaluator

import (
	"strings"
	"testing"
)

const legacyReaderSrc = `namespace Asqs.CsharpTest.Core.Legacy;

public sealed class LegacyXmlCatalogReader
{
    public IReadOnlyList<string> ReadSkuList(string xml) { return null; }

    internal static string DescribeBlockingLegacy(Task<string> t) { return ""; }
}
`

// Run api-2555a79ee2660a8a5cd95c8c860090f5 planned a gap against
// LegacyXmlCatalogReader#DescribeBlockingLegacy, which is declared `internal`. From the separate
// test assembly the compiler says CS0117 "does not contain a definition for", and the fact built
// from that said the type "has NO member" — while listing that same member under "Members declared
// on LegacyXmlCatalogReader include", because DeclaredMemberNames does not filter by visibility.
//
// The model was handed a contradiction. Its only move across three rounds was to delete tests
// (9 → 6, then 9 → 7); the coverage gate refused both, the loop ended on no_accepted_writes, and
// 50 minutes went on a member that was never reachable.
func TestCSharpMissingMemberFacts_namesInaccessibilityNotAbsence(t *testing.T) {
	errOut := `/workspace/tests/Legacy/LegacyXmlCatalogReaderTests.cs(116,52): error CS0117: 'LegacyXmlCatalogReader' does not contain a definition for 'DescribeBlockingLegacy' [/workspace/tests/App.Tests.csproj]`
	files := map[string]string{"src/Core/Legacy/LegacyXmlCatalogReader.cs": legacyReaderSrc}

	facts := csharpMissingMemberFacts(errOut, files, nil)
	if len(facts) == 0 {
		t.Fatal("no fact produced for a repo-owned type")
	}
	joined := strings.Join(facts, "\n")

	if strings.Contains(joined, "has NO member") {
		t.Errorf("a member the type DOES declare was reported as absent:\n%s", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "internal") {
		t.Errorf("the fact does not say the member is internal:\n%s", joined)
	}
	if !strings.Contains(joined, "InternalsVisibleTo") {
		t.Errorf("the fact does not name the remedy (an InternalsVisibleTo grant):\n%s", joined)
	}
}

// Whatever the fact says, the member the compiler just rejected must never appear in the list of
// members offered as alternatives. That is the contradiction itself.
func TestCSharpMissingMemberFacts_neverSuggestsTheRejectedMember(t *testing.T) {
	errOut := `error CS0117: 'LegacyXmlCatalogReader' does not contain a definition for 'DescribeBlockingLegacy'`
	files := map[string]string{"src/Core/Legacy/LegacyXmlCatalogReader.cs": legacyReaderSrc}

	joined := strings.Join(csharpMissingMemberFacts(errOut, files, nil), "\n")
	if i := strings.Index(joined, "include"); i >= 0 {
		if strings.Contains(joined[i:], "DescribeBlockingLegacy") {
			t.Errorf("the rejected member is offered as an alternative to itself:\n%s", joined)
		}
	}
}

// A member the type genuinely does not declare keeps the original wording — that fact was right,
// and this change must not soften it into "maybe it is just not visible".
func TestCSharpMissingMemberFacts_genuineAbsenceIsStillAbsence(t *testing.T) {
	errOut := `error CS0117: 'LegacyXmlCatalogReader' does not contain a definition for 'TotallyInvented'`
	files := map[string]string{"src/Core/Legacy/LegacyXmlCatalogReader.cs": legacyReaderSrc}

	joined := strings.Join(csharpMissingMemberFacts(errOut, files, nil), "\n")
	if !strings.Contains(joined, "has NO member") {
		t.Errorf("a member that really is absent must still be reported as absent:\n%s", joined)
	}
	if strings.Contains(joined, "InternalsVisibleTo") {
		t.Errorf("an absent member must not be described as an accessibility problem:\n%s", joined)
	}
}

// private members are inaccessible for a different reason and no project-file grant fixes them, so
// the remedy must not be offered.
func TestCSharpMissingMemberFacts_privateMemberIsNotAnInternalsVisibleToProblem(t *testing.T) {
	src := "public sealed class Widget\n{\n    private void Hidden() { }\n    public void Run() { }\n}\n"
	errOut := `error CS0122: 'Widget.Hidden()' is inaccessible due to its protection level`
	errOut += "\n" + `error CS0117: 'Widget' does not contain a definition for 'Hidden'`
	files := map[string]string{"src/Widget.cs": src}

	joined := strings.Join(csharpMissingMemberFacts(errOut, files, nil), "\n")
	if strings.Contains(joined, "InternalsVisibleTo") {
		t.Errorf("a private member cannot be reached by any grant:\n%s", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "private") {
		t.Errorf("the fact does not say the member is private:\n%s", joined)
	}
}
