package metadata

import (
	"strings"
	"testing"
)

// The guard's whole job is captured by three cases: unset skips with instructions, a
// production-looking name is refused BY NAME, and a scratch-looking name is allowed. The name gate
// is deliberate friction — it is what would have stopped test fixtures landing in a freshly
// indexed corpus upstream.
func TestScratchDBForTests_gatesOnTheDatabaseName(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		wantURL bool
		wantWhy string
	}{
		{"unset skips with instructions", "", false, "set ASQS_TEST_METADATA_URL"},
		{"a corpus-looking name is refused", "postgres://u:p@localhost:5432/asqs?sslmode=disable", false, `refusing to write to database "asqs"`},
		{"a scratch name is allowed", "postgres://u:p@localhost:5432/asqs_scratch?sslmode=disable", true, ""},
		{"a test name is allowed", "postgres://u:p@localhost:5432/ci_test", true, ""},
		{"the gate is case-insensitive", "postgres://u:p@localhost:5432/MyScratch", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ASQS_TEST_METADATA_URL", tc.env)
			url, why := ScratchDBForTests()
			if tc.wantURL && url == "" {
				t.Fatalf("expected the URL to pass the gate, got skip reason %q", why)
			}
			if !tc.wantURL {
				if url != "" {
					t.Fatalf("expected a refusal, got URL %q", url)
				}
				if !strings.Contains(why, tc.wantWhy) {
					t.Errorf("skip reason %q missing %q", why, tc.wantWhy)
				}
			}
		})
	}
}
