package config

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the resolved-config goldens")

// The goldens are recorded at the CP36 boundary, deliberately BEFORE the constants freeze (CP37) and
// the v2 schema (CP38). Both of those bundles change how a value is spelled or where it comes from
// while promising to change no EFFECTIVE value, and that promise is otherwise untestable: the only
// evidence would be reading two versions of the loader side by side.
//
// This is also the half of the inert-key defence that does not depend on identifiers. The field-usage
// lint is name-based and has a documented blind spot; a golden diff is type-blind by construction, so
// a key that silently stops being populated shows up here even when the lint cannot see it.
//
// Regenerate deliberately, never reflexively:
//
//	go test ./internal/config/ -run TestResolvedConfigGolden -update-golden
//
// A diff in these files is a behaviour change. Read it before accepting it.
func TestResolvedConfigGolden(t *testing.T) {
	cases := []struct {
		name   string
		yaml   string // fixture file under testdata/
		golden string
	}{
		// Only the required keys set: what an operator gets with nothing tuned, which is the single
		// most important resolution to hold still.
		{name: "defaults", yaml: "golden_minimal.yaml", golden: "golden_defaults.json"},
		{name: "full", yaml: "golden_full.yaml", golden: "golden_full.json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearASQSEnv(t)

			path := ""
			if tc.yaml != "" {
				path = filepath.Join("testdata", tc.yaml)
			}
			cfg, err := Load(LoadOptions{ConfigPath: path, ValidateMode: "audit"})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			// SourcePath records where the file came from, so it is machine-specific and would make
			// the golden unstable across checkouts. Everything else is resolution.
			cfg.SourcePath = ""

			got, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')

			goldenPath := filepath.Join("testdata", tc.golden)
			if *updateGolden {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s (%d bytes)", goldenPath, len(got))
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden: %v\n\nRecord it with:\n  go test ./internal/config/ -run TestResolvedConfigGolden -update-golden", err)
			}
			if string(got) != string(want) {
				t.Errorf("resolved config changed for %q.\n\n%s\n\nIf the change is intended, re-record with:\n"+
					"  go test ./internal/config/ -run TestResolvedConfigGolden -update-golden",
					tc.name, firstDiff(string(want), string(got)))
			}
		})
	}
}

// firstDiff reports the first differing line with a little context, because a whole-config dump is
// hundreds of lines and a raw got/want pair is unreadable in test output.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	n := len(wl)
	if len(gl) < n {
		n = len(gl)
	}
	for i := 0; i < n; i++ {
		if wl[i] != gl[i] {
			var b strings.Builder
			b.WriteString("first difference at line ")
			b.WriteString(itoa(i + 1))
			b.WriteString(":\n")
			for j := i - 2; j <= i+2; j++ {
				if j < 0 || j >= n {
					continue
				}
				marker := "  "
				if j == i {
					marker = "! "
				}
				b.WriteString(marker + "want: " + wl[j] + "\n")
				b.WriteString(marker + "got:  " + gl[j] + "\n")
			}
			return b.String()
		}
	}
	if len(wl) != len(gl) {
		return "same prefix, different length: want " + itoa(len(wl)) + " lines, got " + itoa(len(gl))
	}
	return "(no line difference found)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// clearASQSEnv removes every ASQS_* variable for the duration of the test.
//
// Load reads the ambient environment, so a developer with ASQS_LLM_PROVIDER exported would otherwise
// see the golden fail on their machine and pass in CI — the worst possible failure mode for a
// fixture whose whole job is to be authoritative.
func clearASQSEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if k := kv[:eq]; strings.HasPrefix(k, "ASQS_") {
			t.Setenv(k, "")
			if err := os.Unsetenv(k); err != nil {
				t.Fatalf("unset %s: %v", k, err)
			}
		}
	}
}
