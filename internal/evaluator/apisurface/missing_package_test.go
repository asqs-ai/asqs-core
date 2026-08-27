package apisurface

import "testing"

// The round-0 diagnostic from run api-d4895d20922fd19a9a35fab4ec5dea88. javac names the package it
// could not find and never names WebMvcTest, the simple name the import was binding.
const failedImportDiagnostic = `[ERROR] COMPILATION ERROR :
[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/owner/PetControllerTests.java:[41,63] package org.springframework.boot.test.autoconfigure.web.servlet does not exist
`

const petControllerTestsSource = `package org.springframework.samples.petclinic.owner;

import org.junit.jupiter.api.Test;
import org.springframework.boot.test.autoconfigure.web.servlet.WebMvcTest;

@WebMvcTest(PetController.class)
class PetControllerTests {
}
`

// TestParseTargets_failedImportNoLongerEmitsFirstSegment is the regression this bug deserves.
// Round 0 produced the target "symbol:org", which burned a bounded slot on a search that cannot
// succeed and left WebMvcTest unresolved for two fixer rounds.
func TestParseTargets_failedImportNoLongerEmitsFirstSegment(t *testing.T) {
	for _, tgt := range ParseTargets(failedImportDiagnostic) {
		if tgt.Kind == KindSymbol && tgt.Name == "org" {
			t.Fatalf("emitted the useless target symbol:org; targets = %+v", ParseTargets(failedImportDiagnostic))
		}
	}
}

// With the sources available the real simple name is recoverable, because the import line is the
// only place it is written down.
func TestParseTargetsWithSources_recoversSimpleNameFromImport(t *testing.T) {
	sources := map[string]string{
		"src/test/java/org/springframework/samples/petclinic/owner/PetControllerTests.java": petControllerTestsSource,
	}
	targets := ParseTargetsWithSources(failedImportDiagnostic, sources)

	found := false
	for _, tgt := range targets {
		if tgt.Kind == KindSymbol && tgt.Name == "WebMvcTest" {
			found = true
		}
		if tgt.Kind == KindSymbol && tgt.Name == "org" {
			t.Errorf("still emitting symbol:org alongside the real name")
		}
	}
	if !found {
		t.Fatalf("did not recover WebMvcTest from the import line; targets = %+v", targets)
	}
}

// TestMissingPackageSymbol_shapes pins the resolution order directly.
func TestMissingPackageSymbol_shapes(t *testing.T) {
	sources := map[string]string{
		"A.java": "import org.springframework.boot.test.autoconfigure.web.servlet.WebMvcTest;\n",
	}
	cases := []struct {
		name    string
		pkg     string
		sources map[string]string
		want    string
		wantOK  bool
	}{
		{
			name: "dotless nested-type shape is the name itself",
			pkg:  "RuntimeHints", want: "RuntimeHints", wantOK: true,
		},
		{
			name: "failed import resolves via the import line",
			pkg:  "org.springframework.boot.test.autoconfigure.web.servlet", sources: sources,
			want: "WebMvcTest", wantOK: true,
		},
		{
			name: "qualified nested-type use falls back to the capitalised tail",
			pkg:  "org.springframework.aot.hint.RuntimeHints",
			want: "RuntimeHints", wantOK: true,
		},
		{
			name: "unresolvable package yields nothing rather than a junk target",
			pkg:  "org.springframework.boot.test.autoconfigure.web.servlet",
			want: "", wantOK: false,
		},
		{
			name: "empty input",
			pkg:  "   ", want: "", wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := missingPackageSymbol(tc.pkg, tc.sources)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("missingPackageSymbol(%q) = (%q, %v), want (%q, %v)", tc.pkg, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestImportedSimpleName_staticImportAndDeterminism covers the two ways this lookup could go wrong.
func TestImportedSimpleName_staticImportAndDeterminism(t *testing.T) {
	if got := importedSimpleName("org.assertj.core.api.Assertions", map[string]string{
		"A.java": "import static org.assertj.core.api.Assertions.assertThatThrownBy;\n",
	}); got != "assertThatThrownBy" {
		t.Errorf("static import: got %q, want assertThatThrownBy", got)
	}
	// Two files importing different types from the same package must resolve identically on every
	// run, or the audit flips between runs for no reason.
	sources := map[string]string{
		"z.java": "import a.b.Zeta;\n",
		"a.java": "import a.b.Alpha;\n",
	}
	first := importedSimpleName("a.b", sources)
	for i := 0; i < 50; i++ {
		if got := importedSimpleName("a.b", sources); got != first {
			t.Fatalf("non-deterministic across map iteration order: %q then %q", first, got)
		}
	}
	if first != "Alpha" {
		t.Errorf("expected the lexicographically first path to win, got %q", first)
	}
}

// TestParseTargets_nilSourcesDegradesCleanly pins that the diagnostic-only path is unchanged for
// every caller that has no sources to offer.
func TestParseTargets_nilSourcesDegradesCleanly(t *testing.T) {
	a := ParseTargets(realJavacOutput)
	b := ParseTargetsWithSources(realJavacOutput, nil)
	if len(a) != len(b) {
		t.Fatalf("ParseTargets and ParseTargetsWithSources(nil) disagree: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("target %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}
