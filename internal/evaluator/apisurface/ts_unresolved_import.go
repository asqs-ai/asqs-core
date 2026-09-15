package apisurface

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Fix-time check for TS/JS imports of packages the repository does not have.
//
// The C# arm has refused an invented `using` since the namespace-evidence work and the Java arm has
// refused an invented import since the classpath work; TS/JS had no such check at all, so a round
// that swapped an unresolvable import for another unresolvable import was written to disk and cost
// a whole compile round to discover — the same failure, one ecosystem over.
//
// The evidence is the repository's manifests: every dependency block of every package.json it
// contains, every workspace package's own name, the tsconfig path aliases, and the Node builtins.
// Provability bounds, matching the other two arms:
//
//   - Only specifiers the round ADDED are judged. One already present is the state the round
//     inherited, and refusing it blocks every repair on a file that already fails.
//   - Only BARE specifiers are judged. `./x`, `../x` and `/x` name a file, and a path alias names
//     one too; none can be answered from a manifest.
//   - With no readable manifest the check stays silent. A negative claim with no evidence behind it
//     refuses correct repairs, which is worse than the problem it solves.
//   - A subpath export (`msw/node`) is judged on its package, never on the subpath: `exports` maps
//     are arbitrary and absence of a subpath proves nothing.

var (
	// tsImportFromRE covers `import ... from 'x'` and `export ... from 'x'`. RE2 has no
	// backreference, so the closing quote is not pinned to the opening one — harmless here, because
	// a module specifier cannot contain a quote of either kind.
	tsImportFromRE = regexp.MustCompile(`(?m)\b(?:import|export)\b[^;\n]*?\bfrom\s*['"]([^'"]+)['"]`)
	// tsImportBareRE covers the side-effect form `import 'x'`.
	tsImportBareRE = regexp.MustCompile(`(?m)^\s*import\s*['"]([^'"]+)['"]\s*;?`)
	// tsCallSpecifierRE covers CommonJS `require('x')` and dynamic `import('x')`.
	tsCallSpecifierRE = regexp.MustCompile(`\b(?:require|import)\s*\(\s*['"]([^'"]+)['"]\s*\)`)
)

// tsNodeBuiltins are provided by the runtime and appear in no manifest. The `node:` prefix is
// handled by the caller, which strips it before lookup.
var tsNodeBuiltins = map[string]bool{
	"assert": true, "async_hooks": true, "buffer": true, "child_process": true, "cluster": true,
	"console": true, "constants": true, "crypto": true, "dgram": true, "diagnostics_channel": true,
	"dns": true, "domain": true, "events": true, "fs": true, "http": true, "http2": true,
	"https": true, "inspector": true, "module": true, "net": true, "os": true, "path": true,
	"perf_hooks": true, "process": true, "punycode": true, "querystring": true, "readline": true,
	"repl": true, "stream": true, "string_decoder": true, "test": true, "timers": true, "tls": true,
	"trace_events": true, "tty": true, "url": true, "util": true, "v8": true, "vm": true,
	"wasi": true, "worker_threads": true, "zlib": true,
}

// maxTSManifestWalkDepth bounds the manifest walk. A package.json ten directories deep is vendored.
const maxTSManifestWalkDepth = 10

// TSIntroducedUnresolvedImportReason refuses a repair round that imported a package the repository
// does not depend on. Empty means the round is clean, or that there was nothing to judge it with.
func TSIntroducedUnresolvedImportReason(before, after, repoRoot string) string {
	known, aliases, ok := tsRepoModuleEvidence(repoRoot)
	if !ok {
		return ""
	}
	had := tsImportedPackages(before, aliases)
	var unresolved []string
	seen := map[string]bool{}
	for pkg := range tsImportedPackages(after, aliases) {
		if had[pkg] || seen[pkg] || known[pkg] {
			continue
		}
		seen[pkg] = true
		unresolved = append(unresolved, pkg)
	}
	if len(unresolved) == 0 {
		return ""
	}
	sort.Strings(unresolved)
	plural := "package"
	if len(unresolved) > 1 {
		plural = "packages"
	}
	return fmt.Sprintf(
		"this round added an import of %s %s, which the repository does not depend on and does not "+
			"contain — the module would not resolve. Use a package the repository already declares.",
		plural, strings.Join(quoteAll(unresolved), ", "))
}

// tsImportedPackages returns the set of PACKAGE names a source imports. Path-like and aliased
// specifiers, and Node builtins, are dropped rather than returned unresolvable: they name a file or
// the runtime, and no manifest can answer for them.
func tsImportedPackages(src string, aliases []string) map[string]bool {
	out := map[string]bool{}
	stripped := stripJSComments(src)
	for _, re := range []*regexp.Regexp{tsImportFromRE, tsImportBareRE, tsCallSpecifierRE} {
		for _, m := range re.FindAllStringSubmatch(stripped, -1) {
			if pkg := tsPackageNameOf(m[1], aliases); pkg != "" {
				out[pkg] = true
			}
		}
	}
	return out
}

// tsPackageNameOf reduces a specifier to the package it names, or "" when it names no package.
//
// A scoped package is two segments (`@scope/name`); everything else is one. The rest is a subpath
// export, which is never judged.
func tsPackageNameOf(spec string, aliases []string) string {
	s := strings.TrimSpace(spec)
	if s == "" || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") {
		return ""
	}
	for _, a := range aliases {
		if s == a || strings.HasPrefix(s, a) {
			return ""
		}
	}
	s = strings.TrimPrefix(s, "node:")
	segs := strings.Split(s, "/")
	name := segs[0]
	if strings.HasPrefix(s, "@") && len(segs) >= 2 {
		name = segs[0] + "/" + segs[1]
	}
	if name == "" || tsNodeBuiltins[name] {
		return ""
	}
	return name
}

// tsRepoModuleEvidence walks the repository for the module names its manifests make resolvable.
//
// ok is false when no package.json was read at all — not when the walk is merely incomplete, since
// a monorepo whose nested manifest is unreadable still proves the names the root one declares, and
// the returned set is only ever used to ACCEPT.
func tsRepoModuleEvidence(repoRoot string) (known map[string]bool, aliases []string, ok bool) {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	if root == "" || root == "." {
		return nil, nil, false
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil, nil, false
	}

	known = map[string]bool{}
	manifests := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if tsWalkSkipDir(d.Name()) || tsWalkDepth(root, path) > maxTSManifestWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "package.json":
			if tsReadManifest(path, known) {
				manifests++
			}
		case "tsconfig.json", "jsconfig.json":
			aliases = append(aliases, tsReadPathAliases(path)...)
		}
		return nil
	})
	if manifests == 0 {
		return nil, nil, false
	}
	sort.Strings(aliases)
	return known, aliases, true
}

// tsReadManifest folds one package.json into known: every dependency block, plus the package's own
// name so a workspace sibling resolves without being listed as a dependency of itself.
func tsReadManifest(path string, known map[string]bool) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m struct {
		Name         string            `json:"name"`
		Dependencies map[string]string `json:"dependencies"`
		Dev          map[string]string `json:"devDependencies"`
		Peer         map[string]string `json:"peerDependencies"`
		Optional     map[string]string `json:"optionalDependencies"`
	}
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	if n := strings.TrimSpace(m.Name); n != "" {
		known[n] = true
	}
	for _, block := range []map[string]string{m.Dependencies, m.Dev, m.Peer, m.Optional} {
		for name := range block {
			if n := strings.TrimSpace(name); n != "" {
				known[n] = true
			}
		}
	}
	return true
}

// tsReadPathAliases returns the compilerOptions.paths keys with any trailing wildcard removed, so
// `@/*` matches every specifier under `@/`.
func tsReadPathAliases(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		CompilerOptions struct {
			Paths map[string]interface{} `json:"paths"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return nil
	}
	var out []string
	for k := range cfg.CompilerOptions.Paths {
		if k = strings.TrimSpace(strings.TrimSuffix(k, "*")); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// tsWalkSkipDir names the directories a manifest walk must not descend into: installed packages
// (whose own manifests say nothing about what THIS repository depends on), build output, and VCS.
func tsWalkSkipDir(name string) bool {
	switch name {
	case "node_modules", ".git", "dist", "build", "out", "coverage", ".next", ".nuxt", ".turbo",
		".cache", ".yarn", "vendor":
		return true
	}
	return strings.HasPrefix(name, ".")
}

func tsWalkDepth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}
