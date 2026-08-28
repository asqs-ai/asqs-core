package testbootstrap

import (
	"slices"
	"strings"
	"testing"
)

// Contract.Verified means "a smoke test compiled and ran". These are the imports that claim covers,
// read from the templates so the list cannot drift from the files it describes.
func TestSmokeVerifiedImports_readFromTheTemplates(t *testing.T) {
	got := smokeVerifiedImports(javaUnitSmokeClass, javaSpringBootSmokeClass)

	for _, want := range []string{
		"org.junit.jupiter.api.Test",
		"org.assertj.core.api.Assertions.assertThat",
		"org.mockito.Mockito.mock",
		"org.springframework.boot.test.context.SpringBootTest",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("%q is imported by a smoke template but missing from the verified list: %v", want, got)
		}
	}
	// The JDK is not what "verified" is claiming.
	for _, g := range got {
		if strings.HasPrefix(g, "java.") {
			t.Errorf("java.* must not appear in the verified list: %v", got)
		}
	}
}

// The exact gap that made "verified" an overclaim: the Spring smoke touches ONE package under
// org.springframework.boot.test.*, while the contract advertises the whole root.
func TestSmokeVerifiedImports_coverOneSpringTestPackageOnly(t *testing.T) {
	got := smokeVerifiedImports(javaUnitSmokeClass, javaSpringBootSmokeClass)

	n := 0
	for _, g := range got {
		if strings.HasPrefix(g, "org.springframework.boot.test.") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one org.springframework.boot.test.* import in the smoke templates, got %d: %v", n, got)
	}
	for _, moved := range []string{
		"org.springframework.boot.test.autoconfigure",
		"org.springframework.boot.test.mock.mockito",
		"org.springframework.boot.test.web.client",
	} {
		for _, g := range got {
			if strings.HasPrefix(g, moved) {
				t.Errorf("the smoke never compiled %s, so verified must not imply it: %v", moved, got)
			}
		}
	}
}

func TestSmokeVerifiedImports_deduplicatesAcrossSources(t *testing.T) {
	got := smokeVerifiedImports(javaUnitSmokeClass, javaUnitSmokeClass)
	seen := map[string]bool{}
	for _, g := range got {
		if seen[g] {
			t.Fatalf("%q appears twice: %v", g, got)
		}
		seen[g] = true
	}
}
