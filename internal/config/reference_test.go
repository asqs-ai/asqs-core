package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The checked-in reference must match what the current schema renders.
//
// This is the whole reason the document is generated rather than written: a hand-maintained mirror of
// a 200-key struct drifts, and it drifts silently, because nothing compares the prose to the code. A
// failure here is not a nuisance — it means the document currently in the repository is lying about
// some key.
func TestConfigReferenceIsNotStale(t *testing.T) {
	want, err := RenderConfigReferenceMarkdown()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	path := filepath.Join(repoRootFromConfigPkg(t), "docs", "CONFIG-REFERENCE.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\n\nGenerate it with:\n  go run ./cmd/asqs-core config reference -o docs/CONFIG-REFERENCE.md", path, err)
	}
	if string(got) != want {
		t.Errorf("docs/CONFIG-REFERENCE.md is stale: it no longer matches the schema it claims to "+
			"describe.\n\n%s\n\nRegenerate with:\n  go run ./cmd/asqs-core config reference -o docs/CONFIG-REFERENCE.md",
			firstDiff(string(got), want))
	}
}

// Every key in the schema must appear in the rendered reference. The drift test above compares whole
// documents, which catches this too — but only once someone has regenerated. This states the
// property directly, so the failure message says "key missing" rather than "documents differ".
func TestConfigReferenceCoversEveryKey(t *testing.T) {
	entries, err := BuildConfigReference()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Path] = true
	}
	var missing []string
	forEachV2Leaf(t, func(path string, _ func(*SchemaV2, bool)) {
		if !have[path] {
			missing = append(missing, path)
		}
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("these schema keys are absent from the generated reference:\n  %s", strings.Join(missing, "\n  "))
	}
}

// A key with no description is worse than an undocumented one: it looks complete. CP38's
// TestEveryV2FieldIsDocumented checks the SOURCE has a comment; this checks the comment survived the
// render, including the inheritance that gives a wrapper's single `enabled` the sentence from the
// field one level up.
func TestConfigReferenceHasNoEmptyDescriptions(t *testing.T) {
	entries, err := BuildConfigReference()
	if err != nil {
		t.Fatal(err)
	}
	var blank []string
	for _, e := range entries {
		if strings.TrimSpace(e.Doc) == "" {
			blank = append(blank, e.Path)
		}
	}
	if len(blank) > 0 {
		sort.Strings(blank)
		t.Errorf("these reference rows render with no description:\n  %s", strings.Join(blank, "\n  "))
	}
}

// Defaults in the reference come from an actual defaults pass, so they cannot claim a value the
// loader does not apply. Spot-check both tri-state outcomes, because "unset" versus "true" is the
// distinction an operator most needs the document to get right.
func TestConfigReferenceDefaultsComeFromTheLoader(t *testing.T) {
	entries, err := BuildConfigReference()
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]ReferenceEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	cases := map[string]string{
		// Defaulted true by ApplyV2Defaults.
		"retrieval.policy.abstention.enabled": "`true`",
		"generation.docs.overview.enabled":    "`true`",
		// Deliberately NOT defaulted: absent must stay distinguishable from false.
		"generation.policy.tools.enabled": "unset",
	}
	for path, want := range cases {
		e, ok := byPath[path]
		if !ok {
			t.Errorf("%s is missing from the reference", path)
			continue
		}
		if e.Default != want {
			t.Errorf("%s default renders as %s, want %s", path, e.Default, want)
		}
	}
}

// reGetenv finds direct environment reads, and reEnvConst finds the other form the tree uses: a
// named constant holding the variable name, read through it. Matching only the literal call would
// have missed ASQS_ALLOW_EMBEDDING_DIM_RESET, which is exactly the sort of gap that makes a
// mechanical sweep worth writing — the variable that most needs documenting is the one someone took
// the trouble to name.
var (
	reGetenv   = regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("([A-Za-z_][A-Za-z0-9_]*)"\)`)
	reEnvConst = regexp.MustCompile(`(?m)^\s*(?:const\s+|var\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=\s*"(ASQS_[A-Z0-9_]+)"`)
)

// The env-only registry must list every variable read directly from the environment, and nothing it
// does not.
//
// A hand-written list of this kind is exactly what goes stale — upstream's equivalent was drafted by
// grepping one file and missed most of the real set. Sweeping the tree is what makes the appendix in
// the generated reference trustworthy rather than decorative.
//
// Schema-derived variables are excluded by construction: they are read through the derived-env
// walker, not through a literal os.Getenv("ASQS_…"), so they never appear in the sweep.
func TestEnvOnlySwitchesAreComplete(t *testing.T) {
	root := repoRootFromConfigPkg(t)
	found := map[string]string{} // variable -> first package that reads it

	for _, sub := range []string{"cmd", "internal", "tools"} {
		base := filepath.Join(root, sub)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			matches := reGetenv.FindAllStringSubmatch(string(b), -1)
			matches = append(matches, reEnvConst.FindAllStringSubmatch(string(b), -1)...)
			for _, m := range matches {
				name := m[1]
				// The loader's own plumbing for finding a config file and a tenant, plus the derived
				// prefix itself. These are documented in the reference's prose, not as env-only rows.
				switch name {
				case "ASQS_CONFIG_PATH", "ASQS_CLIENT_ID":
					continue
				}
				if _, seen := found[name]; !seen {
					found[name] = filepath.ToSlash(filepath.Dir(rel))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}

	listed := map[string]bool{}
	for _, sw := range EnvOnlySwitches {
		listed[sw.Name] = true
	}

	var missing, stale []string
	for name, pkg := range found {
		if !listed[name] {
			missing = append(missing, name+" (read by "+pkg+")")
		}
	}
	for name := range listed {
		if _, ok := found[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Errorf("these variables are read from the environment but are not in EnvOnlySwitches, so "+
			"nothing documents them:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("these EnvOnlySwitches entries are no longer read anywhere; the appendix documents "+
			"settings that do nothing:\n  %s", strings.Join(stale, "\n  "))
	}
}

// Every entry needs a description and a valid kind, or the appendix renders a blank cell that reads
// as "we do not know what this does".
func TestEnvOnlySwitchesAreDescribed(t *testing.T) {
	valid := map[string]bool{"asqs": true, "inherited": true, "test": true}
	for _, sw := range EnvOnlySwitches {
		if strings.TrimSpace(sw.Doc) == "" {
			t.Errorf("%s has no description", sw.Name)
		}
		if strings.TrimSpace(sw.Component) == "" {
			t.Errorf("%s does not say what reads it", sw.Name)
		}
		if !valid[sw.Kind] {
			t.Errorf("%s has kind %q; want asqs, inherited or test", sw.Name, sw.Kind)
		}
	}
}
