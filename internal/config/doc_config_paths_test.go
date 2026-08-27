package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// reDocConfigPath finds a config-shaped dotted path inside backticks in markdown: at least two
// segments of lowercase words joined by dots, optionally followed by `: value`.
//
// Backticks are the filter that makes this workable. Prose is full of dotted things — file names,
// package paths, sentence-ending abbreviations — and matching bare text would drown the signal. A
// path someone wrote as code is a path they meant as a key.
var reDocConfigPath = regexp.MustCompile("`([a-z][a-z0-9_]*(?:\\.[a-z][a-z0-9_]*)+)(?::[^`]*)?`")

// historicalMarkers let a line mention a key that no longer exists.
//
// Documentation legitimately says "this used to be runner.type" — a guard that forbade it would
// force the removal of exactly the sentences that help someone migrating. The marker has to be on
// the same line, so it is a deliberate act rather than a section-wide amnesty.
var historicalMarkers = []string{
	"pre-v2", "v1", "legacy", "formerly", "used to", "removed", "no longer", "renamed",
	"went with", "was ", "old ", "deleted", "frozen", "constant",
}

// GUARD: every config-shaped path in living documentation must resolve against schema v2.
//
// Derived from the schema and from the audit-event names the code emits — never from a
// hand-maintained needle list, which is the thing that goes stale. Upstream learned this expensively:
// its hand-maintained list of key spellings missed 124 stale paths.
//
// What this catches is the failure mode a schema change creates and nothing else notices: prose that
// still reads plausibly while naming a key that no longer parses. After CP38 that is not a cosmetic
// problem — the strict loader turns a stale key an operator copies into a failed run.
func TestNoUnresolvableConfigPathsInDocs(t *testing.T) {
	root := repoRootFromConfigPkg(t)
	known := knownDocPaths(t)

	type hit struct{ file, line, path string }
	var bad []hit

	for _, path := range livingDocs(t, root) {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// The generated reference is the schema rendered; checking it against the schema is circular.
		if rel == "docs/CONFIG-REFERENCE.md" {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if hasHistoricalMarker(line) {
				continue
			}
			for _, m := range reDocConfigPath.FindAllStringSubmatch(line, -1) {
				p := m[1]
				if !looksLikeConfigPath(p) || known[p] {
					continue
				}
				bad = append(bad, hit{file: rel, line: itoa(i + 1), path: p})
			}
		}
	}

	if len(bad) > 0 {
		var lines []string
		for _, h := range bad {
			lines = append(lines, h.file+":"+h.line+" cites `"+h.path+"`")
		}
		sort.Strings(lines)
		t.Errorf("these documented config paths do not resolve against schema v2. After CP38 the "+
			"loader is strict, so a reader who copies one gets a failed run:\n  %s\n\n"+
			"Repoint it, or add a marker such as \"pre-v2\" / \"formerly\" / \"removed\" to the same "+
			"line when the mention is deliberately historical.", strings.Join(unique(lines), "\n  "))
	}
}

// knownDocPaths is every path a document may legitimately name: schema keys and their ancestors,
// plus the audit-event names the code emits, which share the dotted shape and appear in prose for
// the same reason.
func knownDocPaths(t *testing.T) map[string]bool {
	t.Helper()
	known := map[string]bool{}

	var walk func(rt reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for i := 0; i < rt.NumField(); i++ {
			ft := rt.Field(i)
			if ft.PkgPath != "" {
				continue
			}
			name := yamlFieldName(ft)
			if name == "" {
				continue
			}
			p := name
			if prefix != "" {
				p = prefix + "." + name
			}
			// Ancestors count: prose says "the general.sandbox block" as often as it names a leaf.
			known[p] = true
			et := ft.Type
			for et.Kind() == reflect.Ptr || et.Kind() == reflect.Slice || et.Kind() == reflect.Map {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				walk(et, p)
			}
		}
	}
	walk(reflect.TypeOf(SchemaV2{}), "")

	// profile_budgets is a map of user-chosen profile names to blocks, so its children are spelled
	// with a profile in the middle. Register the shape both ways round.
	for _, profile := range []string{"java_unit", "http_api", "e2e_playwright", "react_feature", "nest_module", "full_stack"} {
		base := "retrieval.profile_budgets." + profile
		known[base] = true
		for _, leaf := range []string{"max_similar_tests", "max_dependency_chunks", "max_fixtures"} {
			known[base+"."+leaf] = true
		}
	}

	for _, ev := range auditEventNames(t) {
		known[ev] = true
	}
	return known
}

// reAuditEvent finds the step names passed to the audit sink. They are dotted lowercase strings, so
// they match the config-path shape and would otherwise be reported as unresolvable keys.
//
// The call shape varies — Log, LogError, and package-local helpers like logAudit that take the sink
// first — so this matches any call whose argument list contains ctx followed by a dotted literal,
// rather than pinning one spelling. Pinning one is how the first draft missed every
// test_bootstrap.* event in the bootstrap package.
var (
	reAuditEvent = regexp.MustCompile(`ctx,\s*"([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+)"`)
	// Some steps name themselves in a struct field rather than at a call site
	// (auditStartStep: "retrieve.plan_start"), so a call-shape regex alone misses them.
	reAuditStepField = regexp.MustCompile(`(?i)(?:step|event)[A-Za-z]*:\s*"([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+)"`)
)

func auditEventNames(t *testing.T) []string {
	t.Helper()
	root := repoRootFromConfigPkg(t)
	var out []string
	for _, sub := range []string{"internal", "cmd"} {
		_ = filepath.Walk(filepath.Join(root, sub), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			for _, m := range reAuditEvent.FindAllStringSubmatch(string(b), -1) {
				out = append(out, m[1])
			}
			for _, m := range reAuditStepField.FindAllStringSubmatch(string(b), -1) {
				out = append(out, m[1])
			}
			return nil
		})
	}
	return out
}

// looksLikeConfigPath filters out the dotted things that are not keys — file names, package paths,
// commands. Anything with a known file extension, a slash, or an uppercase letter is not a v2 key.
func looksLikeConfigPath(p string) bool {
	if strings.Contains(p, "/") {
		return false
	}
	for _, ext := range []string{".go", ".md", ".yaml", ".yml", ".json", ".sql", ".xml", ".jar",
		".dll", ".cs", ".java", ".ts", ".js", ".tsx", ".jsx", ".log", ".txt", ".props", ".csproj",
		".sln", ".lock", ".toml", ".sh", ".cmd", ".exe", ".jsonl", ".gradle", ".properties",
		".mod", ".sum", ".cjs", ".mjs", ".kts", ".config", ".env", ".npmrc", ".gitignore",
		".html", ".css", ".scss", ".vue", ".svelte", ".png", ".svg"} {
		if strings.HasSuffix(p, ext) {
			return false
		}
	}
	// A leading segment that belongs to something other than asqs-core's own schema: another tool's
	// configuration file (package.json's `scripts.test`, a Vite config's `resolve.conditions`), a
	// package namespace, or a directory. Those are legitimately documented and are not v2 keys.
	switch strings.SplitN(p, ".", 2)[0] {
	case "github", "gitlab", "bitbucket", "npm", "pnpm", "yarn", "mvn", "gradle", "dotnet",
		"org", "com", "io", "net", "www", "docs", "node_modules", "src", "tools", "internal", "cmd",
		"scripts", "resolve", "test", "compilerOptions", "dependencies", "devdependencies",
		"jest", "vite", "vitest", "cypress", "playwright", "tsconfig", "package",
		// Database identifiers. `metadata.symbols` and `files.is_test` are a schema and a column,
		// and they are dotted for the same reason config keys are.
		"metadata", "embeddings", "files", "symbols", "edges", "chunks",
		// Runtime APIs of other languages, which appear in prose about the indexers.
		"process", "window", "document", "console":
		return false
	}
	return true
}

func hasHistoricalMarker(line string) bool {
	low := strings.ToLower(line)
	for _, m := range historicalMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}
