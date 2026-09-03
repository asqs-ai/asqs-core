package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// stubToolPrinting puts a fake `name` on PATH whose test invocation prints `script`'s output and
// exits 1, while every other invocation (dependency restore, compile) exits 0 silently.
func stubToolPrinting(t *testing.T, name, script string) {
	t.Helper()
	bin := t.TempDir()
	body := "#!/bin/sh\ncase \"$*\" in\n  *test*) " + script + "\n  exit 1;;\n  *) exit 0;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The local target captures through runCommand rather than the docker job runner, so it needs its
// own strip: the same coloured vitest output that broke the docker run would otherwise reach the
// parsers untouched here.
func TestLocalTest_outputIsStrippedOfANSI(t *testing.T) {
	stubToolPrinting(t, "mvn", `printf ' \033[32m✓\033[39m src/app/AppLayout.test.tsx\n\033[41m\033[1m FAIL \033[22m\033[49m src/app/router.test.tsx:\033[2m59:24\033[22m\n'`)
	repo := writeRepoTree(t, map[string]string{"pom.xml": "<project/>"}, nil)
	sb := &Sandbox{Type: "local", Timeout: "30s"}

	res := sb.Test(context.Background(), repo, "java")

	if res.OK {
		t.Fatal("non-zero exit must fail the step")
	}
	if strings.Contains(res.Output, "\x1b[") {
		t.Errorf("escape codes survived local capture: %q", res.Output)
	}
	for _, want := range []string{"✓ src/app/AppLayout.test.tsx", "FAIL  src/app/router.test.tsx:59:24"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("stripped output lost %q: %q", want, res.Output)
		}
	}
}

// The local target's counterpart of TestDockerTest_noTestFilesIsOkWithSuffix.
func TestLocalTest_noTestFilesIsOkWithSuffix(t *testing.T) {
	stubToolPrinting(t, "npm", `printf ' RUN  v4.1.11\n\nNo test files found, exiting with code 1\n'`)
	repo := writeRepoTree(t, map[string]string{"package.json": `{"name":"x","scripts":{"test":"vitest run"}}`}, nil)
	sb := &Sandbox{Type: "local", Timeout: "30s"}

	res := sb.Test(context.Background(), repo, "typescript")

	if !res.OK {
		t.Fatalf("an empty vitest run must not fail the step at the runner: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, evaluator.NoTestFilesSuffix) {
		t.Errorf("summary must carry NoTestFilesSuffix; got %q", res.Summary)
	}
}
