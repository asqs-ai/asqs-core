package metadata

import (
	"fmt"
	"os"
	"strings"
)

// ScratchDBForTests returns the connection string for a database that tests may WRITE to, or "" to
// skip. It refuses anything that looks like a real corpus.
//
// Tests that insert symbols need a live server, and the obvious way to give them one is to point
// ASQS_TEST_METADATA_URL at whatever database is already running. That is also how test fixtures end
// up inside somebody's indexed corpus — which happened here, against a freshly re-indexed PetClinic.
// Read-only checks are harmless; writes are not, and the difference is invisible at the call site.
//
// The gate is the database NAME: it must contain "test" or "scratch". That is deliberate friction —
// nobody names a corpus database that by accident, and it is what would have stopped the mistake
// above. An emptiness check was tried too and removed: write-tests share one scratch database, so
// the first test's own fixtures locked out every later one.
func ScratchDBForTests() (string, string) {
	url := strings.TrimSpace(os.Getenv("ASQS_TEST_METADATA_URL"))
	if url == "" {
		return "", "set ASQS_TEST_METADATA_URL to a scratch database to run this"
	}
	name := databaseNameOf(url)
	if !strings.Contains(strings.ToLower(name), "test") && !strings.Contains(strings.ToLower(name), "scratch") {
		return "", fmt.Sprintf("refusing to write to database %q: name it *test* or *scratch* so a "+
			"real corpus cannot be used by accident", name)
	}
	return url, ""
}

func databaseNameOf(url string) string {
	s := url
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return s
}
