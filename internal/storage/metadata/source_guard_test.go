package metadata

import (
	"os"
	"strings"
	"testing"
)

// readMetadataSourceWithoutComments concatenates the named files in this package with line comments
// stripped, so source-level guards match on code rather than on prose describing the bug they guard.
//
// Several guards in this package assert invariants about the SQL the store issues. They read source
// because the alternative — asserting against a live database — makes them skip in CI, which is
// exactly when a regression needs catching. Taking a file list rather than a fixed name matters:
// when the symbol INSERT was split between store.go and batch.go, guards hardcoded to store.go
// would have started passing vacuously.
func readMetadataSourceWithoutComments(t *testing.T, files ...string) string {
	t.Helper()
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, ln := range strings.Split(string(b), "\n") {
			if i := strings.Index(ln, "//"); i >= 0 {
				ln = ln[:i]
			}
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}
