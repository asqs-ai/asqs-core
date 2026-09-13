package evaluator

import (
	"strings"
	"testing"
)

// The fixer's offer and the generator's gate read the same declarations, and the header of
// dotnetproj/csharpdecl.go says they must not diverge. They did: the generator accumulated every
// file declaring a partial type, the fixer kept the first and dropped the rest — so a member
// declared in the other half was offered by one half of the pipeline and not the other.
func TestCSharpMissingMemberFacts_partialClassOffersMembersFromEveryFile(t *testing.T) {
	files := map[string]string{
		"src/Svc.Part1.cs": "public partial class Svc\n{\n    public void One() { }\n}\n",
		"src/Svc.Part2.cs": "public partial class Svc\n{\n    public void Two() { }\n}\n",
	}
	out := "src/SvcTests.cs(9,20): error CS1061: 'Svc' does not contain a definition for 'Three'"
	facts := csharpMissingMemberFacts(out, files, nil)
	if len(facts) == 0 {
		t.Fatal("expected a missing-member fact")
	}
	joined := strings.Join(facts, "\n")
	for _, want := range []string{"One", "Two"} {
		if !strings.Contains(joined, want) {
			t.Errorf("fact does not offer %q, which the partial type declares:\n%s", want, joined)
		}
	}
}

// A partial type's constructor may be declared in either file, and a fact that reads the first file
// only reports "no constructors" for a type that has one.
func TestCSharpMissingMemberFacts_partialClassFindsConstructorInEitherFile(t *testing.T) {
	files := map[string]string{
		"src/Svc.Part1.cs": "public partial class Svc\n{\n    public void One() { }\n}\n",
		"src/Svc.Part2.cs": "public partial class Svc\n{\n    public Svc(int id) { }\n}\n",
	}
	out := "src/SvcTests.cs(9,20): error CS1729: 'Svc' does not contain a constructor that takes 0 arguments"
	facts := csharpMissingMemberFacts(out, files, nil)
	joined := strings.Join(facts, "\n")
	if !strings.Contains(joined, "Svc(int id)") {
		t.Errorf("constructor fact missed the signature declared in the other partial file:\n%s", joined)
	}
}
