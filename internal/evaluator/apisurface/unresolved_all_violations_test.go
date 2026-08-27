package apisurface

import (
	"context"
	"strings"
	"testing"
)

// The shape that ended run api-0c344e6bc0658e0db06506efb9d964f5: one generated file with four
// unresolvable package references, of which the checker named exactly one per pass.
//
// Ascending (pkg, cls) order put org.mockito.MockBean first and AutoConfigureMockMvc second, which
// is precisely the pair the audit recorded across the two generation passes — the other three
// reached disk unmentioned and outlived the run.
const bootFourStyleTest = `package org.springframework.samples.petclinic.vet;

import org.springframework.boot.test.autoconfigure.web.servlet.AutoConfigureMockMvc;
import org.springframework.boot.test.mock.mockito.MockBean;
import org.springframework.boot.web.server.LocalServerPort;
import org.springframework.test.web.client.MockMvcRestServiceServer;

class VetControllerE2EIT {
}
`

func bootFourProvider() *stubDepProvider {
	// What a Spring Boot 4.0.1 classpath actually answers: the simple names resolve, at packages
	// the model did not write. MockMvcRestServiceServer is a hallucination and resolves nowhere.
	return &stubDepProvider{byName: map[string][]string{
		"AutoConfigureMockMvc":     {"org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc"},
		"MockBean":                 {},
		"LocalServerPort":          {"org.springframework.boot.test.web.server.LocalServerPort"},
		"MockMvcRestServiceServer": {},
	}}
}

func TestUnresolvedDependency_reportsEveryViolation(t *testing.T) {
	repo := depRepo(t)
	got := UnresolvedDependencyReason(context.Background(), bootFourProvider(), repo, bootFourStyleTest)

	for _, want := range []string{
		"org.springframework.boot.test.autoconfigure.web.servlet.AutoConfigureMockMvc",
		"org.springframework.boot.test.mock.mockito.MockBean",
		"org.springframework.boot.web.server.LocalServerPort",
		"org.springframework.test.web.client.MockMvcRestServiceServer",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reason omits %s — one retry cannot fix a violation it was never told about.\ngot: %s", want, got)
		}
	}
}

// The defect that made the message actively misleading: the resolver computes the FQCNs that DO
// provide the simple name in order to decide the reference misses, then threw them away and said
// "the project has no such dependency" — which points at deleting the annotation, not at fixing the
// import.
func TestUnresolvedDependency_namesTheRealPackage(t *testing.T) {
	repo := depRepo(t)
	got := UnresolvedDependencyReason(context.Background(), bootFourProvider(), repo, bootFourStyleTest)

	if !strings.Contains(got, "org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc") {
		t.Errorf("the correct package was resolved and discarded; the model cannot guess it.\ngot: %s", got)
	}
	if !strings.Contains(got, "org.springframework.boot.test.web.server.LocalServerPort") {
		t.Errorf("LocalServerPort's real package is missing from the reason.\ngot: %s", got)
	}
	// A name nothing on the classpath provides keeps the original wording: there, "no such
	// dependency" is the true statement.
	if !strings.Contains(got, "MockMvcRestServiceServer, which is neither on the project compile classpath") {
		t.Errorf("a genuinely absent class must still be reported as absent.\ngot: %s", got)
	}
}

func TestUnresolvedDependencyRefs_carriesCandidates(t *testing.T) {
	repo := depRepo(t)
	refs := UnresolvedDependencyRefs(context.Background(), bootFourProvider(), repo, bootFourStyleTest)
	if len(refs) != 4 {
		t.Fatalf("got %d refs, want 4: %+v", len(refs), refs)
	}
	byKey := map[string]UnresolvedRef{}
	for _, r := range refs {
		byKey[r.Key()] = r
	}
	got := byKey["org.springframework.boot.test.autoconfigure.web.servlet.AutoConfigureMockMvc"]
	if len(got.Candidates) != 1 || got.Candidates[0] != "org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc" {
		t.Errorf("Candidates = %v, want the Boot 4 package", got.Candidates)
	}
	if c := byKey["org.springframework.test.web.client.MockMvcRestServiceServer"].Candidates; len(c) != 0 {
		t.Errorf("a hallucinated class must carry no candidates, got %v", c)
	}
}

// The fixer gate must judge the ROUND, not the file. A repair that leaves an inherited bad
// reference untouched has to land, or the file can never improve.
func TestIntroducedUnresolvedDependency_onlyFlagsNewReferences(t *testing.T) {
	repo := depRepo(t)
	provider := bootFourProvider()
	before := `package org.springframework.samples.petclinic.vet;

import org.springframework.boot.test.mock.mockito.MockBean;

class VetControllerE2EIT {
}
`
	unchanged := before
	if got := IntroducedUnresolvedDependencyReason(context.Background(), provider, repo, before, unchanged); got != "" {
		t.Errorf("an inherited reference is not this round's fault: %s", got)
	}

	partial := `package org.springframework.samples.petclinic.vet;

import org.springframework.boot.test.mock.mockito.MockBean;

class VetControllerE2EIT {
	void added() {
	}
}
`
	if got := IntroducedUnresolvedDependencyReason(context.Background(), provider, repo, before, partial); got != "" {
		t.Errorf("a partial repair that adds no bad reference must land: %s", got)
	}
}

// Attempt 3 of the motivating run: all four edits applied, and line 8 moved from one non-existent
// package to another. Nothing checked the fixer's output, so that import survived four more
// compile rounds.
func TestIntroducedUnresolvedDependency_catchesASwappedPackage(t *testing.T) {
	repo := depRepo(t)
	provider := &stubDepProvider{byName: map[string][]string{
		"MockBean":         {},
		"TestRestTemplate": {},
	}}
	before := `package org.springframework.samples.petclinic.vet;

import org.springframework.boot.test.mock.mockito.MockBean;

class VetControllerE2EIT {
}
`
	after := `package org.springframework.samples.petclinic.vet;

import org.springframework.boot.test.web.client.TestRestTemplate;

class VetControllerE2EIT {
}
`
	got := IntroducedUnresolvedDependencyReason(context.Background(), provider, repo, before, after)
	if !strings.Contains(got, "org.springframework.boot.test.web.client.TestRestTemplate") {
		t.Errorf("swapping one absent package for another must be refused, got %q", got)
	}
	if strings.Contains(got, "MockBean") {
		t.Errorf("the inherited reference must not be charged to this round: %s", got)
	}
}

// Without a provider the gate has no evidence, and a negative claim with no evidence would reject
// correct repairs.
func TestIntroducedUnresolvedDependency_silentWithoutProvider(t *testing.T) {
	repo := depRepo(t)
	if got := IntroducedUnresolvedDependencyReason(context.Background(), nil, repo, "", bootFourStyleTest); got != "" {
		t.Errorf("no provider must mean no claim, got %q", got)
	}
}

// A same-named class in an unrelated library must never be advertised as the fix. Rhino's Context
// and Undertow's Context share a simple name and nothing else; "import that instead" would swap a
// missing dependency for a wrong type. The fact is still stated — the judgement is left open.
func TestUnresolvedRef_reasonWordingByCandidateKind(t *testing.T) {
	relocated := UnresolvedRef{
		Pkg:        "org.springframework.boot.test.autoconfigure.web.servlet",
		Cls:        "AutoConfigureMockMvc",
		Candidates: []string{"org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc"},
	}
	if got := relocated.Reason(); !strings.Contains(got, "relocated package: use that import") {
		t.Errorf("a same-library relocation must be stated as a directive, got %q", got)
	}

	collision := UnresolvedRef{
		Pkg:        "org.mozilla.javascript",
		Cls:        "Context",
		Candidates: []string{"io.undertow.Context"},
	}
	got := collision.Reason()
	if strings.Contains(got, "use that import") {
		t.Errorf("an unrelated same-named class must not be advertised as the fix, got %q", got)
	}
	if !strings.Contains(got, "io.undertow.Context") {
		t.Errorf("the fact that the name resolves elsewhere is still worth stating, got %q", got)
	}

	absent := UnresolvedRef{Pkg: "org.mozilla.javascript", Cls: "Context"}
	if got := absent.Reason(); !strings.Contains(got, "the project has no such dependency") {
		t.Errorf("with no candidate at all the original wording is the true one, got %q", got)
	}
}
