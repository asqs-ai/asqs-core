package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// maxExampleTemplateLines caps the shipped starting template.
//
// The cap is the mechanism, not a style preference. v1's template grew by accretion until it was an
// exhaustive mirror maintained by hand — which is how it drifted about twenty fields from the struct
// while still being what operators copied. Exhaustiveness now belongs to the generated reference,
// which cannot drift; this file's job is to get someone to a working run, and a file that cannot grow
// past this line count cannot quietly take the other job back.
const maxExampleTemplateLines = 200

func shippedTemplates(t *testing.T) map[string][]byte {
	t.Helper()
	root := repoRootFromConfigPkg(t)
	out := map[string][]byte{}
	paths := []string{filepath.Join(root, "config.example.yaml")}
	extra, err := filepath.Glob(filepath.Join(root, "examples", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, extra...)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = b
	}
	if len(out) == 0 {
		t.Fatal("no shipped templates found; this test would pass vacuously")
	}
	return out
}

func TestExampleTemplateStaysShort(t *testing.T) {
	root := repoRootFromConfigPkg(t)
	b, err := os.ReadFile(filepath.Join(root, "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(strings.TrimRight(string(b), "\n"), "\n")); n > maxExampleTemplateLines {
		t.Errorf("config.example.yaml is %d lines, over the %d-line cap. Exhaustiveness belongs to "+
			"docs/CONFIG-REFERENCE.md, which is generated and cannot drift; the template's job is a "+
			"working run.", n, maxExampleTemplateLines)
	}
}

// reExplicitNull matches a key set to an explicit null.
var reExplicitNull = regexp.MustCompile(`(?mi):\s*(null|~)\s*(#.*)?$`)

// No explicit `null` in a file operators copy.
//
// Null and absent are equivalent to the loader, so this changes nothing mechanically — which is
// exactly why it needs a test rather than a habit. It is about the READER: `enabled: null` looks like
// a third state beside true and false, and an operator reasonably concludes there is a distinction to
// understand. There is not. Omit the key.
func TestNoExplicitNullsInShippedTemplates(t *testing.T) {
	for path, body := range shippedTemplates(t) {
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if reExplicitNull.MatchString(line) {
				t.Errorf("%s:%d writes an explicit null: %q. Null and absent are the same to the "+
					"loader, but null reads as a third state — delete the line instead.", path, i+1, trimmed)
			}
		}
	}
}

// GUARD: every shipped template must LOAD under the real strict decoder.
//
// A template that does not parse is worse than no template: it is the first thing a new user copies,
// and CP38's strict loader means one stale key now fails their whole run. This is executable
// documentation — the file is checked by the same code path an operator's config goes through.
func TestShippedTemplatesParse(t *testing.T) {
	for path, body := range shippedTemplates(t) {
		if _, err := UnmarshalSchemaV2(body); err != nil {
			t.Errorf("%s does not load under the strict decoder:\n%v", path, err)
		}
	}
}

// GUARD: every v2 YAML block in living documentation must load under the real strict decoder.
//
// Documentation is where config examples go stale first, because nothing executes them. After a
// schema change the prose still looks plausible while every block in it has become uncopyable. This
// makes those blocks executable.
//
// Only blocks that look like a v2 CONFIG are checked — a fenced yaml block might be a Kubernetes
// manifest, a compose file or a fragment showing one nested key. The heuristic is a top-level v2
// section name at column zero, which is what a copyable config has and a fragment does not.
func TestDocumentedYAMLBlocksParse(t *testing.T) {
	root := repoRootFromConfigPkg(t)
	var checked int
	for _, path := range livingDocs(t, root) {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		for i, block := range yamlBlocksIn(string(b)) {
			if !looksLikeV2Config(block) {
				continue
			}
			checked++
			if _, err := UnmarshalSchemaV2([]byte(block)); err != nil {
				t.Errorf("%s: yaml block %d does not load under the strict decoder — an operator "+
					"copying it gets a failed run:\n%v", filepath.ToSlash(rel), i+1, err)
			}
		}
	}
	if checked == 0 {
		t.Error("no documented config blocks were checked; the guard is passing vacuously")
	}
	t.Logf("checked %d documented config block(s)", checked)
}

// reFence captures fenced yaml blocks.
var reFence = regexp.MustCompile("(?s)```ya?ml\\n(.*?)```")

func yamlBlocksIn(md string) []string {
	var out []string
	for _, m := range reFence.FindAllStringSubmatch(md, -1) {
		out = append(out, m[1])
	}
	return out
}

// v2TopLevelSections are the section names a copyable v2 config starts with.
var v2TopLevelSections = []string{"general:", "bootstrap:", "indexer:", "retrieval:", "generation:", "fixer:", "schema_version:"}

func looksLikeV2Config(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		for _, s := range v2TopLevelSections {
			if strings.HasPrefix(line, s) {
				return true
			}
		}
	}
	return false
}

// livingDocs returns the markdown a reader is expected to act on. The implementation plan is
// excluded: it records what keys USED to be called, deliberately, and holding a history to the
// current schema would be wrong.
func livingDocs(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "testdata", "target", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "docs/IMPLEMENTATION-PLAN") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(out)
	return out
}
