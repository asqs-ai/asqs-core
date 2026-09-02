package generator

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/teststack"
)

// The generated apiClient test of 2026-09-01 wrote `global.fetch = vi.fn()`, which runs (every
// runner here executes on Node) and then fails the compile step with `Cannot find name 'global'` —
// 3 of that run's 112 errors. A browser stack deliberately does not install @types/node, so the
// spelling has to come from the prompt.
func TestTestStackContractBlock_steersDOMStacksToGlobalThis(t *testing.T) {
	root := t.TempDir()
	writeContract(t, root, teststack.Contract{
		Version: teststack.SchemaVersion, Language: "typescript", Framework: "react",
		Runner: "vitest", TestEnvironment: "jsdom", Verified: true,
	})
	block := testStackLLMBlock(root)
	if !strings.Contains(block, "globalThis") {
		t.Fatalf("a DOM stack must be told to use globalThis:\n%s", block)
	}
	if !strings.Contains(block, "Cannot find name 'global'") {
		t.Errorf("the rule should say what goes wrong, not just what to do:\n%s", block)
	}
}

// A Node-environment stack installs @types/node (ensureNodeTypesForNodeEnvironment), so `global` is
// declared there and the rule would be noise.
func TestTestStackContractBlock_nodeStackNotToldAboutGlobalThis(t *testing.T) {
	root := t.TempDir()
	writeContract(t, root, teststack.Contract{
		Version: teststack.SchemaVersion, Language: "typescript",
		Runner: "vitest", TestEnvironment: "node", Verified: true,
	})
	block := testStackLLMBlock(root)
	if strings.Contains(block, "globalThis") {
		t.Errorf("a node-environment stack has @types/node; the rule is noise there:\n%s", block)
	}
	// ...and it keeps its own environment warning.
	if !strings.Contains(block, "there is no DOM here") {
		t.Errorf("the node environment warning regressed:\n%s", block)
	}
}

// Languages with no JS test environment must not see a JS rule.
func TestTestStackContractBlock_nonJSStackUnaffected(t *testing.T) {
	root := t.TempDir()
	writeContract(t, root, teststack.Contract{
		Version: teststack.SchemaVersion, Language: "java", Framework: "spring-boot",
		Runner: "junit5", Verified: true,
	})
	if block := testStackLLMBlock(root); strings.Contains(block, "globalThis") {
		t.Errorf("a Java contract must not carry a JS rule:\n%s", block)
	}
}

func TestIsDOMTestEnvironment(t *testing.T) {
	for env, want := range map[string]bool{
		"jsdom": true, "happy-dom": true, "JSDOM": true,
		"node": false, "Node": false, "": false, "   ": false,
	} {
		if got := isDOMTestEnvironment(env); got != want {
			t.Errorf("isDOMTestEnvironment(%q) = %v, want %v", env, got, want)
		}
	}
}
