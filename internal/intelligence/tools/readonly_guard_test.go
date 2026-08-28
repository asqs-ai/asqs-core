package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The package doc's core claim — "Everything here is READ-ONLY … and no tool in this package
// shells out" — is a security boundary, not a style preference: these handlers execute
// model-chosen arguments. This scans the package's production sources for write and exec
// identifiers so a future tool cannot cross the boundary silently. (Writes stay with the
// generator's artifact path; the only os call a handler may make is a read.)
func TestPackage_handlersNeverWriteOrExec(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"os.WriteFile", "os.Create", "os.Mkdir", "os.Remove", "os.Rename", "os.Chmod",
		"os.OpenFile", "exec.Command", "os/exec", "syscall.Exec",
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range forbidden {
			if strings.Contains(string(body), f) {
				t.Errorf("%s contains %q — tools must stay read-only and never exec", name, f)
			}
		}
	}
}
