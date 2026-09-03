package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The read side of the fixer context used to resolve `/workspace/node_modules/...` stack frames
// onto the real files under the repo and read them: asqs-go run api-72dad6bb281cacee338f43c48432a780
// loaded react-dom.development.js and react-router-dom.development.js (1.13M runes together) on
// every round, only for the clamp to shed them again. Worse, the first of them alone exceeded the
// dependency read budget, so every repo path cited AFTER it in the output was never read at all.
// asqs-core reads the context inline in applyLLMFix, so the assertion is on the request the
// fixer receives.
func TestApplyLLMFix_skipsVendoredCitedPaths(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/pages/HomePage.test.tsx", "import { HomePage } from './HomePage';\n")
	// Larger than the whole dependency read budget: reading it would exhaust the budget before
	// the repo file cited after it gets its turn.
	write("node_modules/react-dom/cjs/react-dom.development.js", strings.Repeat("x", maxFixerDependencyContextRunes+1))
	write("src/pages/HomePage.tsx", "export function HomePage() { return null; }\n")

	errorOutput := "TypeError: Cannot destructure property 'basename'\n" +
		"    at LinkWithRef (/workspace/node_modules/react-dom/cjs/react-dom.development.js:826:7)\n" +
		"    at HomePage (/workspace/src/pages/HomePage.tsx:1:1)\n"
	opts := DefaultEvalOptions(dir, "typescript")
	opts.ArtifactPaths = []string{"src/pages/HomePage.test.tsx"}
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{}}}
	opts.Fixer = fixer
	audit := &recordingAuditor{}
	attempt := 0

	applyLLMFix(context.Background(), opts, StepTest, errorOutput, audit, &attempt, 5, &FixLoopState{}, "")

	if fixer.req.Step == "" {
		t.Fatal("the fixer must have been asked (the failure names a repo location)")
	}
	for k := range fixer.req.Files {
		if strings.Contains(k, "node_modules") {
			t.Errorf("vendored file read into fixer context: %s", k)
		}
	}
	if _, ok := fixer.req.Files["src/pages/HomePage.tsx"]; !ok {
		t.Errorf("the repo file cited after the vendored frame must still be read; got %v", fileKeys(fixer.req.Files))
	}
	if _, ok := fixer.req.Files["src/pages/HomePage.test.tsx"]; !ok {
		t.Errorf("the artifact itself must be read; got %v", fileKeys(fixer.req.Files))
	}
	p := audit.lastPayload("evaluator.fix_context_vendored_skipped")
	if p == nil {
		t.Fatal("skipping a vendored citation must be audited")
	}
	skipped, _ := p["paths"].([]string)
	if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "node_modules/react-dom/") {
		t.Errorf("audited paths = %v, want the node_modules frame", p["paths"])
	}
}
