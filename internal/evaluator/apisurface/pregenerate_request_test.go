package apisurface

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The three API errors of run api-e08817ff5df431f6bb8f1fb92e7659a2, and the type whose member list
// answers each. None of them is an assertion, which is why e2eAssertionTargets alone did not help.
var inventedRequestAPIs = []struct {
	wrote, answeredBy, because string
}{
	{"playwright.request", "com.microsoft.playwright.Playwright", "JS exposes request as a property; Java declares APIRequest request()"},
	{"APIRequestArgs.create()", "com.microsoft.playwright.options.RequestOptions", "APIRequestArgs exists in no binding; RequestOptions is the builder, and it lives under .options"},
	{"response.jsonBody(Class)", "com.microsoft.playwright.APIResponse", "JS has response.json(); the Java interface has neither"},
}

// Every type that answers one of those errors must be in the pre-generation list for a Java
// Playwright E2E gap. Before this the list was assertion-only and all three reached the compiler.
func TestPregenerateTargets_coversTheInventedRequestAPIs(t *testing.T) {
	got := PregenerateTargets("java", "playwright-java", true)
	have := make(map[string]bool, len(got))
	for _, tgt := range got {
		have[tgt.Name] = true
	}
	for _, c := range inventedRequestAPIs {
		if !have[c.answeredBy] {
			t.Errorf("%s is not requested, so `%s` reaches the compiler again (%s)", c.answeredBy, c.wrote, c.because)
		}
	}
}

// The request types belong to the E2E layer only: a unit gap must not pay for them.
func TestPregenerateTargets_requestTypesAreE2EOnly(t *testing.T) {
	for _, tgt := range PregenerateTargets("java", "playwright-java", false) {
		if strings.HasPrefix(tgt.Name, "com.microsoft.playwright.") {
			t.Errorf("unit gap carries a Playwright type: %+v", tgt)
		}
	}
}

// TypeScript's default E2E profile is browser-driven (orchestrator.DefaultRetrievalProfileE2E), so
// it pays nothing for the REQUEST half — issuing HTTP calls is Java's and .NET's layer, not its.
// Pinned because the natural instinct on reading e2eRequestTargets is to add Node "for symmetry".
//
// Routing is the exception, and deliberately so: interception is how a BROWSER test controls what
// the application receives, so it belongs to precisely the profile that has no request half.
func TestPregenerateTargets_nodeGetsAssertionsAndRoutingButNotRequests(t *testing.T) {
	got := PregenerateTargets("typescript", "playwright", true)
	names := map[string]bool{}
	for _, tgt := range got {
		names[tgt.Name] = true
	}
	for _, want := range []string{"LocatorAssertions", "PageAssertions", "APIResponseAssertions", "Route", "Page"} {
		if !names[want] {
			t.Errorf("TypeScript E2E is missing %s: %+v", want, got)
		}
	}
	// The request-issuing types stay out.
	for _, unwanted := range []string{"APIRequest", "APIRequestContext", "RequestOptions", "APIResponse"} {
		if names[unwanted] {
			t.Errorf("TypeScript E2E carries the request type %s, which its profile does not use: %+v", unwanted, got)
		}
	}
	if len(got) != 5 {
		t.Errorf("got %d targets, want 3 assertions + 2 routing: %+v", len(got), got)
	}
}

// playwrightJarOrSkip locates the Playwright jar in the local Maven repository, and skips the test
// when it is not there.
//
// The path used to be a hard-coded /Users/<name>/.m2/... literal, which is to say the test asserted
// against one developer's laptop and could not pass anywhere else — CI included. It did carry a
// skip, but on the wrong condition: Lookup drops names it cannot resolve WITHOUT returning an
// error, so a missing jar produced an empty result rather than a failure to skip on, and every
// assertion below then failed. The existence check has to happen here, before Lookup is asked
// anything.
func playwrightJarOrSkip(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory to resolve the local Maven repository from: %v", err)
	}
	repo := os.Getenv("MAVEN_REPO_LOCAL")
	if repo == "" {
		repo = filepath.Join(home, ".m2", "repository")
	}
	jar := filepath.Join(repo, "com", "microsoft", "playwright", "playwright", "1.49.0", "playwright-1.49.0.jar")
	if _, err := os.Stat(jar); err != nil {
		t.Skipf("playwright 1.49.0 is not in the local Maven repository (%s) — run `mvn dependency:get -Dartifact=com.microsoft.playwright:playwright:1.49.0` to include this check", jar)
	}
	return jar
}

// The list is only worth anything if the types actually resolve, and a typo would be silent —
// Lookup drops an unresolvable name without complaint. This runs against the real Playwright jar
// when it is in the local Maven repository, and skips when it is not.
func TestPregenerateTargets_javaRequestTypesResolveAgainstTheRealJar(t *testing.T) {
	jar := playwrightJarOrSkip(t)
	p := NewJavaProvider()
	p.cpCache["/repo"] = classpathEntry{classpath: jar, fingerprint: "pregenerate-request", at: time.Now()}

	var requested []Target
	for _, tgt := range PregenerateTargets("java", "playwright-java", true) {
		if strings.HasPrefix(tgt.Name, "com.microsoft.playwright.") {
			requested = append(requested, tgt)
		}
	}
	surfaces, err := p.Lookup(context.Background(), "/repo", requested)
	if err != nil {
		t.Skipf("playwright jar not resolvable here: %v", err)
	}
	resolved := make(map[string]bool, len(surfaces))
	for _, s := range surfaces {
		resolved[s.FQCN] = true
	}
	for _, tgt := range requested {
		if !resolved[tgt.Name] {
			t.Errorf("%s does not resolve on the classpath — a typo here costs the whole point of the list", tgt.Name)
		}
	}

	// The block must state the two facts the run got wrong, verbatim from the jar.
	block := RenderSurfaces(surfaces)
	if !strings.Contains(block, "APIRequest request();") {
		t.Error("the block does not show that request() is a method")
	}
	if strings.Contains(block, "jsonBody") || strings.Contains(block, " json()") {
		t.Error("APIResponse must be shown without a JSON accessor; its absence is the fact that matters")
	}
}
