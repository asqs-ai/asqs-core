package apisurface

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubDepProvider answers symbol lookups from a fixed name -> FQCNs table.
type stubDepProvider struct {
	byName map[string][]string
	err    error
	calls  []string
}

func (s *stubDepProvider) Lookup(_ context.Context, _ string, targets []Target) ([]TypeSurface, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []TypeSurface
	for _, t := range targets {
		s.calls = append(s.calls, t.Name)
		for _, fq := range s.byName[t.Name] {
			out = append(out, TypeSurface{FQCN: fq})
		}
	}
	return out, nil
}

func depRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	full := filepath.Join(repo, "src/main/java/org/springframework/samples/petclinic/vet/Vet.java")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package org.springframework.samples.petclinic.vet;\npublic class Vet {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

// The run's shape: five inline org.mozilla.javascript usages in a project with no Rhino
// dependency. The classpath scan finds no Context in that package.
func TestUnresolvedDependency_rhinoInlineUsage(t *testing.T) {
	repo := depRepo(t)
	provider := &stubDepProvider{byName: map[string][]string{
		"Context": {"io.undertow.Context"}, // a same-named class elsewhere must not satisfy it
	}}
	test := `package org.springframework.samples.petclinic.vet;
class VetControllerE2EIT {
	void t() {
		Object result = org.mozilla.javascript.Context.enter();
	}
}
`
	got := UnresolvedDependencyReason(context.Background(), provider, repo, test)
	if got == "" || !strings.Contains(got, "org.mozilla.javascript.Context") {
		t.Fatalf("reason = %q, want the Rhino reference reported", got)
	}
}

// An import of a class the classpath really has resolves and stays silent; jakarta included.
func TestUnresolvedDependency_classpathHitsAreSilent(t *testing.T) {
	repo := depRepo(t)
	provider := &stubDepProvider{byName: map[string][]string{
		"WebMvcTest": {"org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest"},
		"Valid":      {"jakarta.validation.Valid"},
		"Mockito":    {"org.mockito.Mockito"},
	}}
	test := `package org.springframework.samples.petclinic.vet;
import org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest;
import jakarta.validation.Valid;
import static org.mockito.Mockito.when;
import java.util.List;
class T {}
`
	if got := UnresolvedDependencyReason(context.Background(), provider, repo, test); got != "" {
		t.Errorf("false rejection: %q", got)
	}
}

// Repo-domain packages are never judged by file presence: package-private classes live anywhere.
func TestUnresolvedDependency_repoPackageIsSilent(t *testing.T) {
	repo := depRepo(t)
	provider := &stubDepProvider{byName: map[string][]string{}}
	test := `package x;
import org.springframework.samples.petclinic.vet.SomethingUnseen;
class T {}
`
	if got := UnresolvedDependencyReason(context.Background(), provider, repo, test); got != "" {
		t.Errorf("repo-domain package must be silent, got %q", got)
	}
	if len(provider.calls) != 0 {
		t.Errorf("repo-domain reference must not reach the classpath, looked up %v", provider.calls)
	}
}

// No provider, a lookup error, or a candidate list at the resolver's cap: silence, not rejection.
func TestUnresolvedDependency_unprovableStatesAreSilent(t *testing.T) {
	repo := depRepo(t)
	test := "package x;\nimport com.nowhere.gone.Thing;\nclass T {}\n"
	if got := UnresolvedDependencyReason(context.Background(), nil, repo, test); got != "" {
		t.Errorf("nil provider must be silent, got %q", got)
	}
	if got := UnresolvedDependencyReason(context.Background(), &stubDepProvider{err: errors.New("mvn failed")}, repo, test); got != "" {
		t.Errorf("classpath failure must be silent, got %q", got)
	}
	capped := make([]string, resolveSymbolCandidateCap)
	for i := range capped {
		capped[i] = "p" + strings.Repeat("x", i) + ".Thing"
	}
	if got := UnresolvedDependencyReason(context.Background(), &stubDepProvider{byName: map[string][]string{"Thing": capped}}, repo, test); got != "" {
		t.Errorf("a capped candidate list is unprovable, got %q", got)
	}
}

// Wildcard imports and single-segment qualifiers (locals) are out of scope.
func TestUnresolvedDependency_outOfScopeShapes(t *testing.T) {
	repo := depRepo(t)
	provider := &stubDepProvider{byName: map[string][]string{}}
	test := `package x;
import com.somewhere.util.*;
class T {
	void t(Object owner) {
		owner.toString();
	}
}
`
	if got := UnresolvedDependencyReason(context.Background(), provider, repo, test); got != "" {
		t.Errorf("wildcards and locals are unprovable, got %q", got)
	}
}
