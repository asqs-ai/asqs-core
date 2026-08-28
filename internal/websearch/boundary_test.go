package websearch

import (
	"os/exec"
	"strings"
	"testing"
)

// The containment story for prompt injection is NOT the prompt framing — it is that web content
// has no code path into file writes or command execution. Since B30 the fixer holds the tool
// Registry and the Registry embeds this package, so a blanket "evaluator must not transitively
// import websearch" is unsatisfiable BY DESIGN — web content is supposed to reach the model's
// conversation, and the conversation lives in those packages. What IS enforceable:
//
//  1. The exec machinery (internal/runner, the B02 gate) must have no websearch dependency at
//     all — a fetched page must not be reachable from the code that builds commands.
//  2. Only the tool layer and the wiring may TOUCH this package directly. Everyone else sees web
//     content the one way they are supposed to: as a fenced string in a model conversation.
//  3. This package itself must be inert: no os/exec, no writes outside its own cache.
func TestWebsearchBoundary(t *testing.T) {
	// (1) runner: zero websearch anywhere in its dependency closure.
	out, err := exec.Command("go", "list", "-deps", "github.com/asqs/asqs-core/internal/runner/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	if strings.Contains(string(out), "github.com/asqs/asqs-core/internal/websearch") {
		t.Error("internal/runner transitively imports websearch; web content now has a path toward " +
			"command construction, which the B54 design forbids")
	}

	// (2) direct importers: exactly the tool layer and the wiring.
	allowedDirect := map[string]bool{
		"github.com/asqs/asqs-core/internal/intelligence/tools": true,
		// Core has no workflow package; the pipeline is the orchestration layer here.
		"github.com/asqs/asqs-core/internal/pipeline": true,
	}
	all, err := exec.Command("go", "list", "-f", "{{.ImportPath}}\t{{join .Imports \",\"}}", "github.com/asqs/asqs-core/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(all)), "\n") {
		pkg, imports, ok := strings.Cut(line, "\t")
		if !ok || !strings.Contains(imports, "github.com/asqs/asqs-core/internal/websearch") {
			continue
		}
		if pkg == "github.com/asqs/asqs-core/internal/websearch" || allowedDirect[pkg] {
			continue
		}
		t.Errorf("%s imports internal/websearch directly; web access must flow through the tool "+
			"registry so the ledger, allow-list and framing cannot be bypassed", pkg)
	}

	// (3) this package runs nothing.
	self, err := exec.Command("go", "list", "-f", "{{join .Deps \",\"}}", "github.com/asqs/asqs-core/internal/websearch").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	if strings.Contains(string(self), "os/exec") {
		t.Error("internal/websearch depends on os/exec; a fetch package must be inert")
	}
}
