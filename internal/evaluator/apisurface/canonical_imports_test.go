package apisurface

import (
	"context"
	"errors"
	"testing"
)

// canonicalStub answers symbol lookups from a simple-name table, the way a classpath scan does.
type canonicalStub struct {
	byName map[string][]string
	err    error
}

func (s *canonicalStub) Lookup(_ context.Context, _ string, targets []Target) ([]TypeSurface, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []TypeSurface
	for _, t := range targets {
		for _, fq := range s.byName[t.Name] {
			out = append(out, TypeSurface{FQCN: fq})
		}
	}
	return out, nil
}

// The whole point: a Spring Boot 4 classpath answers differently from a Boot 3 one, and the
// coordinate table that produces AvailableImports cannot see the difference. Asking the classpath
// removes the question — the contract carries whatever THIS project resolves.
func TestResolveCanonicalImports_readsTheProjectsOwnClasspath(t *testing.T) {
	bootFour := &canonicalStub{byName: map[string][]string{
		"WebMvcTest":           {"org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest"},
		"SpringBootTest":       {"org.springframework.boot.test.context.SpringBootTest"},
		"LocalServerPort":      {"org.springframework.boot.test.web.server.LocalServerPort"},
		"AutoConfigureMockMvc": {"org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc"},
		"MockitoBean":          {"org.springframework.test.context.bean.override.mockito.MockitoBean"},
	}}
	got := ResolveCanonicalImports(context.Background(), bootFour, "/repo", "java")

	want := map[string]string{
		"WebMvcTest":           "org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest",
		"SpringBootTest":       "org.springframework.boot.test.context.SpringBootTest",
		"LocalServerPort":      "org.springframework.boot.test.web.server.LocalServerPort",
		"AutoConfigureMockMvc": "org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc",
		"MockitoBean":          "org.springframework.test.context.bean.override.mockito.MockitoBean",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d imports, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// A name that resolves nowhere is simply absent: the contract is rendered as authoritative, so it
// must never state something it cannot support. MockBean was removed in Spring Boot 4.
func TestResolveCanonicalImports_omitsUnresolvableAndAmbiguousNames(t *testing.T) {
	provider := &canonicalStub{byName: map[string][]string{
		"SpringBootTest": {"org.springframework.boot.test.context.SpringBootTest"},
		// Two classes share the simple name: picking one would be a guess.
		"LocalServerPort": {
			"org.springframework.boot.test.web.server.LocalServerPort",
			"com.vendor.shaded.LocalServerPort",
		},
	}}
	got := ResolveCanonicalImports(context.Background(), provider, "/repo", "java")

	if got["SpringBootTest"] != "org.springframework.boot.test.context.SpringBootTest" {
		t.Errorf("an unambiguous resolution must be kept, got %v", got)
	}
	if _, ok := got["LocalServerPort"]; ok {
		t.Errorf("an ambiguous name must be omitted, got %v", got)
	}
	if _, ok := got["MockBean"]; ok {
		t.Errorf("a name absent from the classpath must be omitted, got %v", got)
	}
}

// Every degradation path leaves the contract exactly as bootstrap wrote it.
func TestResolveCanonicalImports_silentOnEveryDegradation(t *testing.T) {
	ok := &canonicalStub{byName: map[string][]string{"SpringBootTest": {"org.springframework.boot.test.context.SpringBootTest"}}}

	if got := ResolveCanonicalImports(context.Background(), nil, "/repo", "java"); got != nil {
		t.Errorf("no provider must resolve nothing, got %v", got)
	}
	if got := ResolveCanonicalImports(context.Background(), ok, "", "java"); got != nil {
		t.Errorf("no repo path must resolve nothing, got %v", got)
	}
	if got := ResolveCanonicalImports(context.Background(), ok, "/repo", "csharp"); got != nil {
		t.Errorf("the framework list is Java-only, got %v", got)
	}
	bad := &canonicalStub{err: errors.New("dependency:build-classpath failed")}
	if got := ResolveCanonicalImports(context.Background(), bad, "/repo", "java"); got != nil {
		t.Errorf("an unresolvable classpath must resolve nothing, got %v", got)
	}
	empty := &canonicalStub{byName: map[string][]string{}}
	if got := ResolveCanonicalImports(context.Background(), empty, "/repo", "java"); got != nil {
		t.Errorf("zero resolutions must produce no map at all, got %v", got)
	}
}
