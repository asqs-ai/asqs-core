package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileKeyWithSuffix(files map[string]string, suffix string) (string, bool) {
	for k := range files {
		if strings.HasSuffix(filepath.ToSlash(k), suffix) {
			return k, true
		}
	}
	return "", false
}

// RC1 end-to-end: a generated test references PetType (imported from the wrong package) and calls a
// non-existent Pet constructor. PetType/Pet are NOT the artifact, NOT declared ArtifactDependencies,
// and NOT the reverse-mapped source — so before RC1 they were never in the fixer's context, and at
// attempt >= 3 read-scope narrowing guaranteed they never could be. This test runs applyLLMFix at the
// escalation threshold and asserts both real sources are forwarded to the fixer with full bodies.
func TestApplyLLMFix_forwardsMissingTypeSourcesEvenUnderEscalation(t *testing.T) {
	dir := t.TempDir()
	owner := "src/main/java/org/springframework/samples/petclinic/owner/"
	mustWrite(t, dir, owner+"PetType.java", "package org.springframework.samples.petclinic.owner;\npublic class PetType extends NamedEntity {}\n")
	mustWrite(t, dir, owner+"Pet.java", "package org.springframework.samples.petclinic.owner;\npublic class Pet extends NamedEntity {}\n")
	mustWrite(t, dir, owner+"PetValidator.java", "package org.springframework.samples.petclinic.owner;\npublic class PetValidator {}\n")
	artifact := "src/test/java/org/springframework/samples/petclinic/owner/PetValidatorTest.java"
	mustWrite(t, dir, artifact, "class PetValidatorTest {}\n")

	raw := `[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/owner/PetValidatorTest.java:[13,51] cannot find symbol
  symbol:   class PetType
  location: package org.springframework.samples.petclinic.model
[ERROR] constructor Pet in class org.springframework.samples.petclinic.owner.Pet cannot be applied to given types;
  required: no arguments`

	opts := DefaultEvalOptions(dir, "java")
	opts.ArtifactPaths = []string{artifact}
	fixer := &stubFixer{resp: FixResponse{Files: nil}}
	opts.Fixer = fixer

	// attemptCounter = 2 => currentAttempt = 3 => autoEscalate: signature-slicing AND read-scope
	// narrowing are both active. This is precisely the tier that previously evicted PetType/Pet.
	attempt := 2
	applyLLMFix(context.Background(), opts, StepTest, raw, &recordingAuditor{}, &attempt, 30, &FixLoopState{}, "")

	if fixer.req.Files == nil {
		t.Fatal("fixer was not invoked")
	}
	petTypeKey, ok := fileKeyWithSuffix(fixer.req.Files, "owner/PetType.java")
	if !ok {
		t.Fatalf("PetType.java not forwarded to fixer under escalation; files=%v", fileKeys(fixer.req.Files))
	}
	if !strings.Contains(fixer.req.Files[petTypeKey], "class PetType") {
		t.Errorf("PetType.java should be present with full body (symbolKeep prevents slicing); got %q", fixer.req.Files[petTypeKey])
	}
	if _, ok := fileKeyWithSuffix(fixer.req.Files, "owner/Pet.java"); !ok {
		t.Errorf("Pet.java (from `constructor Pet in class …owner.Pet`) not forwarded; files=%v", fileKeys(fixer.req.Files))
	}
}

func fileKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
