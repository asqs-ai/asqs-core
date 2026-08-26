package metadata

import (
	"context"
	"os"
	"testing"
)

// TestInitSchema_appliesAgainstALiveServer executes the real schema.sql. Nothing else catches a
// statement that only fails at the server — a semicolon inside a comment splits a CREATE TABLE and
// the fragment dies with "syntax error at end of input", which no amount of Go-level testing sees.
//
// Skipped unless ASQS_TEST_METADATA_URL points at a scratch database.
//
// (Upstream additionally tests here that InitSchema UPGRADES an existing pre-repo_id database —
// the idempotent-ALTER discipline of rule 6. That test arrives with CP11, which introduces the
// columns it stages.)
func TestInitSchema_appliesAgainstALiveServer(t *testing.T) {
	url := os.Getenv("ASQS_TEST_METADATA_URL")
	if url == "" {
		t.Skip("set ASQS_TEST_METADATA_URL to a scratch database to run this")
	}
	st, err := Open(url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	// Idempotent: startup runs it every time.
	if err := st.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema (second run): %v", err)
	}
}
