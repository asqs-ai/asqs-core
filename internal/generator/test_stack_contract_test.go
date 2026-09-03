package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/teststack"
)

func writeContract(t *testing.T, root string, c teststack.Contract) {
	t.Helper()
	if err := teststack.Write(root, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(teststack.RelPath))); err != nil {
		t.Fatalf("contract not written: %v", err)
	}
}

// Bootstrap is off by default, so the overwhelmingly common case is no contract — and in that case
// this bundle must be undetectable in the prompt. Anything else is a regression shipped to every
// user who never turned bootstrap on.
func TestTestStackBlock_absentContractRendersNothing(t *testing.T) {
	if got := testStackLLMBlock(t.TempDir()); got != "" {
		t.Errorf("no contract must render nothing, got %q", got)
	}
	if got := testStackLLMBlock(""); got != "" {
		t.Errorf("empty repo path must render nothing, got %q", got)
	}
	g := &LLMGenerator{RepoPath: t.TempDir()}
	if got := g.testStackSystemBlock(); got != "" {
		t.Errorf("system block must be empty without a contract, got %q", got)
	}
}

// An unreadable or wrong-schema file is the same answer as no file: this is a best-effort
// enhancement, and a malformed contract must never take generation down with it.
func TestTestStackBlock_malformedContractRendersNothing(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, filepath.FromSlash(teststack.RelPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := testStackLLMBlock(root); got != "" {
		t.Errorf("malformed contract must render nothing, got %q", got)
	}
}

// The point of the whole mechanism: the model is told which libraries exist, and told in terms that
// forbid inventing others. The run this came from had twenty candidates rejected for importing
// libraries the module did not carry.
func TestTestStackBlock_statesTheAllowListAsClosed(t *testing.T) {
	root := t.TempDir()
	writeContract(t, root, teststack.Contract{
		Version:          teststack.SchemaVersion,
		Framework:        "spring-boot",
		FrameworkVersion: "3.2.0",
		Runner:           "junit5",
		AvailableImports: []string{"org.junit.jupiter", "org.mockito"},
	})
	got := testStackLLMBlock(root)

	for _, want := range []string{"org.junit.jupiter", "org.mockito", "spring-boot 3.2.0", "junit5"} {
		if !strings.Contains(got, want) {
			t.Errorf("block omits %q:\n%s", want, got)
		}
	}
	// A list the model may extend is not an allow-list. The closure has to be stated.
	if !strings.Contains(got, "Import from no other library") {
		t.Error("the allow-list is never stated as closed, so it reads as a suggestion")
	}
	// assertj is NOT in the contract; nothing may imply it is available.
	if strings.Contains(got, "assertj") {
		t.Error("block names a library outside the allow-list")
	}
}

// Roots come from build coordinates, which carry no version, so they cannot prove a sub-package
// exists. Presenting them as if they could is what licensed a Spring Boot 3 import on a Boot 4
// classpath — the canonical imports are the half that IS version-resolved, and must win.
func TestTestStackBlock_canonicalImportsOverrideRoots(t *testing.T) {
	root := t.TempDir()
	writeContract(t, root, teststack.Contract{
		Version:          teststack.SchemaVersion,
		Framework:        "spring-boot",
		AvailableImports: []string{"org.springframework.boot"},
		CanonicalImports: map[string]string{
			"MockBean":       "org.springframework.boot.test.mock.mockito.MockBean",
			"SpringBootTest": "org.springframework.boot.test.context.SpringBootTest",
		},
	})
	got := testStackLLMBlock(root)

	if !strings.Contains(got, "import org.springframework.boot.test.mock.mockito.MockBean;") {
		t.Errorf("canonical import not rendered as an import statement:\n%s", got)
	}
	if !strings.Contains(got, "this list wins") {
		t.Error("canonical imports do not override the version-blind roots, so a wrong recollection still wins")
	}
	// Deterministic order: a map iterated raw would reorder the prompt between runs, defeating
	// prompt caching and making two identical runs produce different inputs.
	if strings.Index(got, "MockBean;") > strings.Index(got, "SpringBootTest;") {
		t.Error("canonical imports are not sorted; prompt bytes would vary run to run")
	}
}

// "Verified" was being read as "every package under every listed root was exercised". It means one
// smoke test compiled and ran, and the block has to say which imports that actually covered.
func TestTestStackBlock_scopesWhatVerifiedMeans(t *testing.T) {
	root := t.TempDir()
	writeContract(t, root, teststack.Contract{
		Version:         teststack.SchemaVersion,
		Language:        "java",
		Framework:       "spring-boot",
		Verified:        true,
		VerifiedImports: []string{"org.springframework.boot.test.context.SpringBootTest"},
	})
	got := testStackLLMBlock(root)
	if !strings.Contains(got, "What bootstrap actually compiled and ran") {
		t.Errorf("verified scope not stated:\n%s", got)
	}

	root2 := t.TempDir()
	writeContract(t, root2, teststack.Contract{Version: teststack.SchemaVersion, Language: "java", Framework: "spring-boot"})
	got2 := testStackLLMBlock(root2)
	if !strings.Contains(got2, "did not compile or run anything") {
		t.Errorf("unverified contract does not admit it is unverified:\n%s", got2)
	}
}

// A failed or skipped smoke test is information the model can act on — prefer unit tests over the
// integration style bootstrap could not get running here.
func TestTestStackBlock_smokeStatusSteersTestStyle(t *testing.T) {
	cases := map[teststack.SmokeStatus]string{
		teststack.SmokePassed:  "integration tests work here",
		teststack.SmokeFailed:  "Avoid",
		teststack.SmokeSkipped: "Avoid",
	}
	for status, want := range cases {
		root := t.TempDir()
		writeContract(t, root, teststack.Contract{
			Version:   teststack.SchemaVersion,
			Language:  "java",
			Framework: "spring-boot",
			Smoke:     teststack.Smoke{Status: status, Kind: "Spring"},
		})
		if got := testStackLLMBlock(root); !strings.Contains(got, want) {
			t.Errorf("smoke=%s: block missing %q:\n%s", status, want, got)
		}
	}
}

// A node test environment has no DOM. Saying so once in the contract block is cheaper than the
// compile failure that teaches it.
func TestTestStackBlock_nodeEnvironmentWarnsAboutTheDOM(t *testing.T) {
	root := t.TempDir()
	writeContract(t, root, teststack.Contract{
		Version:         teststack.SchemaVersion,
		Language:        "typescript",
		Framework:       "express",
		TestEnvironment: "node",
	})
	if got := testStackLLMBlock(root); !strings.Contains(got, "no DOM here") {
		t.Errorf("node environment does not warn about the DOM:\n%s", got)
	}
}

// F8. An ESM package gets an explicit no-require rule from the contract; a CommonJS one does not.
func TestTestStackBlock_esmModuleTypeRendersTheNoRequireRule(t *testing.T) {
	for moduleType, want := range map[string]bool{"esm": true, "commonjs": false, "": false} {
		root := t.TempDir()
		writeContract(t, root, teststack.Contract{
			Language: "typescript", Framework: "react", Runner: "vitest", TestEnvironment: "jsdom",
			ModuleType: moduleType, AvailableImports: []string{"vitest"},
		})
		got := testStackLLMBlock(root)
		if strings.Contains(got, "use `import` only") != want {
			t.Errorf("module_type=%q: no-require rule present=%v, want %v:\n%s", moduleType, !want, want, got)
		}
	}
}
