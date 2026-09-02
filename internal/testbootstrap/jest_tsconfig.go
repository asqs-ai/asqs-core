package testbootstrap

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// tsTestGlobals describes the ambient type declarations one runner's globals need.
//
// This used to be a single hard-coded Jest constant applied to every TypeScript profile, Vitest
// included. That is wrong in both directions: @types/jest is not installed by the Vitest stack, and
// Vitest's globals are different names. A generated React test calling `vi.mock(...)` therefore
// failed the compile step with `TS2304: Cannot find name 'vi'` — the runner had the global at
// RUN time (the generated vitest.config.ts sets `globals: true`) but nothing declared it to tsc,
// and tsconfig.app.json type-checks src/** where the generated tests live.
type tsTestGlobals struct {
	// DTSFile is the ambient declaration file written beside the package manifest.
	DTSFile string
	// ReferenceTypes are the packages named in `/// <reference types="…" />` directives, and the
	// entries added to compilerOptions.types. For Vitest the first is the `vitest/globals`
	// sub-path, which only declares anything when the config sets `globals: true` — which the
	// config this bootstrap generates does.
	//
	// A list rather than one string because the runner's globals are not the only declarations a
	// generated test needs: a stack that installs @testing-library/jest-dom also needs that
	// package's MATCHER augmentation, and for Vitest it lives behind a different entry point than
	// the one the setup file imports. See tsTestGlobalsForProfile.
	ReferenceTypes []string
	// Runner names the runner in the file header, so a reader knows which stack wrote it.
	Runner string
}

var (
	jestTSGlobals   = tsTestGlobals{DTSFile: "asqs-jest-globals.d.ts", ReferenceTypes: []string{"jest"}, Runner: "Jest"}
	vitestTSGlobals = tsTestGlobals{DTSFile: "asqs-vitest-globals.d.ts", ReferenceTypes: []string{"vitest/globals"}, Runner: "Vitest"}
)

// jestDomVitestTypes is @testing-library/jest-dom's Vitest matcher augmentation.
//
// The setup file imports the package's ROOT entry point (see jsSetupFileContent), which extends
// `expect` at run time but whose TYPES are `types/index.d.ts` -> `types/jest.d.ts` — an augmentation
// of Jest's `jest.Matchers`. Vitest's `Assertion` interface is augmented only by
// `types/vitest.d.ts`, reachable through this sub-path export. Nothing else declares it, and the
// setup file is not in the tsconfig program anyway (a repo's `include` is typically ["src"]), so
// without this entry `tsc` rejects every generated component test with
// `TS2339: Property 'toBeInTheDocument' does not exist on type 'Assertion<HTMLElement>'`.
//
// Measured on the run of 2026-09-01 (react/vitest, 20 gaps): 63 of 112 compile errors were this one
// missing declaration; adding it took the compile from 112 errors to 49.
const jestDomVitestTypes = "@testing-library/jest-dom/vitest"

// jestDomPackage is the dependency whose presence in the profile gates jestDomVitestTypes.
const jestDomPackage = "@testing-library/jest-dom"

// typesNodePackage is Node's own type declarations, installed by js_profile.go for
// Node-environment TypeScript stacks (see ensureNodeTypesForNodeEnvironment). Named here because
// this file must not add type references for a package the profile does not install.
const typesNodePackage = "@types/node"

// asqsManagedTypeEntries is every compilerOptions.types entry this bootstrap may write.
//
// patchRootTSConfigForTestGlobals retires the managed entries that are NOT in the current
// profile's set, which is what lets a repository move between runners AND between framework
// profiles without accumulating entries for packages its stack no longer installs. An unresolvable
// entry in compilerOptions.types is TS2688 on every compile, regardless of skipLibCheck.
var asqsManagedTypeEntries = []string{"jest", "vitest/globals", jestDomVitestTypes}

// tsTestGlobalsForRunner maps the profile's runner to the declarations its globals need.
func tsTestGlobalsForRunner(r JSRunner) tsTestGlobals {
	if r == JSRunnerVitest {
		return vitestTSGlobals
	}
	return jestTSGlobals
}

// tsTestGlobalsForProfile is tsTestGlobalsForRunner plus the declarations this profile's OWN
// dependencies require.
//
// The jest-dom entry is conditional on the profile actually installing the package, and it must
// stay that way: vitest-vite, vitest-vite-jsdom and vitest-node-esm select Vitest without any
// Testing Library. Declaring types for a package that is not installed is TS2688 — unconditionally
// when the entry lands in compilerOptions.types, and from the .d.ts too on a repository that does
// not set skipLibCheck. Both were verified against @testing-library/jest-dom@7.0.1.
//
// Jest profiles are deliberately left alone. jest-dom's root types augment `jest.Matchers`, which
// is the shape @types/jest already provides, so the Jest arm needs a different entry and different
// evidence than the Vitest one measured here.
func tsTestGlobalsForProfile(p jsTestProfile) tsTestGlobals {
	g := tsTestGlobalsForRunner(p.Runner)
	// Copy before mutating: the package-level vars are shared by every call.
	g.ReferenceTypes = append([]string(nil), g.ReferenceTypes...)
	if p.Runner == JSRunnerVitest && profileHasDep(p, jestDomPackage) {
		g.ReferenceTypes = append(g.ReferenceTypes, jestDomVitestTypes)
	}
	return g
}

// profileHasDep reports whether the resolved profile installs a package by name.
func profileHasDep(p jsTestProfile, name string) bool {
	for _, d := range p.Deps {
		if d.Name == name {
			return true
		}
	}
	return false
}

// other returns the declarations for the runner this one is NOT, so a repository re-bootstrapped
// onto a different runner does not keep an ambient file pointing at types it no longer installs.
func (g tsTestGlobals) other() tsTestGlobals {
	if g.DTSFile == vitestTSGlobals.DTSFile {
		return jestTSGlobals
	}
	return vitestTSGlobals
}

func (g tsTestGlobals) content() string {
	b := "// " + asqsGeneratedHeader + " — " + g.Runner + " globals for TypeScript / ESLint. Safe to edit or delete.\n"
	for _, t := range g.ReferenceTypes {
		b += `/// <reference types="` + t + `" />` + "\n"
	}
	return b
}

// detectJestBootstrapIsTS is true when the repo should get the TypeScript Jest stack (@types/jest, ts-jest, tsconfig tweaks).
func detectJestBootstrapIsTS(repo, lang string) bool {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "typescript" || lang == "ts" {
		return true
	}
	pkgDir := repo
	if d, err := resolveJSPackageDirForBootstrap(repo); err == nil {
		pkgDir = d
	}
	for _, base := range []string{pkgDir, repo} {
		if _, err := os.Stat(filepath.Join(base, "tsconfig.json")); err == nil {
			return true
		}
		matches, err := filepath.Glob(filepath.Join(base, "tsconfig*.json"))
		if err == nil && len(matches) > 0 {
			return true
		}
	}
	data, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return false
	}
	var root map[string]interface{}
	if json.Unmarshal(data, &root) != nil {
		return false
	}
	return packageJSONDeclaresTypeScript(root)
}

func packageJSONDeclaresTypeScript(root map[string]interface{}) bool {
	for _, key := range []string{"devDependencies", "dependencies", "peerDependencies", "optionalDependencies"} {
		m, _ := root[key].(map[string]interface{})
		if m == nil {
			continue
		}
		for _, name := range []string{"typescript", "tsx"} {
			if _, ok := m[name]; ok {
				return true
			}
		}
	}
	return false
}

// tsconfigFilesToPatch lists common repo-root configs that may type-check app/test sources (Angular/Nest/Vite/ESLint).
var tsconfigFilesToPatch = []string{
	"tsconfig.json",
	"tsconfig.app.json",
	"tsconfig.lib.json",
	"tsconfig.eslint.json",
}

// ensureTestTypeScriptTooling writes the profile's globals .d.ts and patches tsconfig(s) when needed
// so editors and tsc see describe/it/expect/vi AND the matchers the profile's own test libraries
// add (explicit compilerOptions.types and restrictive include/files). Returns repo-relative paths
// of tsconfig files that were modified.
//
// It takes the whole profile, not just the runner, because the declarations a generated test needs
// depend on which libraries the stack installs — see tsTestGlobalsForProfile.
func ensureTestTypeScriptTooling(repoRoot, pkgDir string, prof jsTestProfile) (patchedTSConfigFiles []string, err error) {
	g := tsTestGlobalsForProfile(prof)
	if err := writeTestGlobalsDTS(pkgDir, g); err != nil {
		return nil, err
	}
	// A file left by the other runner references types this stack does not install, which tsc
	// reports as TS2688 on every compile. Removed only when ASQS wrote it.
	removeStaleTestGlobalsDTS(pkgDir, g)
	seen := map[string]bool{}
	for _, base := range []string{pkgDir, repoRoot} {
		for _, name := range tsconfigFilesToPatch {
			path := filepath.Join(base, name)
			if seen[path] {
				continue
			}
			seen[path] = true
			if _, err := os.Stat(path); err != nil {
				continue
			}
			ok, err := patchRootTSConfigForTestGlobals(path, g)
			if err != nil {
				return patchedTSConfigFiles, err
			}
			if ok {
				if rel, e := filepath.Rel(repoRoot, path); e == nil {
					patchedTSConfigFiles = append(patchedTSConfigFiles, filepath.ToSlash(rel))
				} else {
					patchedTSConfigFiles = append(patchedTSConfigFiles, name)
				}
			}
		}
	}
	return patchedTSConfigFiles, nil
}

// writeTestGlobalsDTS writes the profile's ambient declaration file.
//
// The content must match g EXACTLY, not merely contain it: the declaration set can shrink as well
// as grow (a repository re-bootstrapped onto a profile that no longer installs jest-dom), and a
// leftover reference to an uninstalled package is TS2688 on every compile once it also reaches
// compilerOptions.types. writeIfAbsentOrOwned gives precisely that contract, and the same
// ownership rule the rest of this package uses: a file carrying no ASQS header belongs to the
// repository and is never clobbered.
func writeTestGlobalsDTS(dir string, g tsTestGlobals) error {
	_, err := writeIfAbsentOrOwned(filepath.Join(dir, g.DTSFile), g.content())
	return err
}

// removeStaleTestGlobalsDTS deletes the other runner's ambient file when this bootstrap wrote it.
// Best effort: a file without the ASQS header belongs to the repository and is left alone.
func removeStaleTestGlobalsDTS(dir string, g tsTestGlobals) {
	stale := filepath.Join(dir, g.other().DTSFile)
	b, err := os.ReadFile(stale)
	if err != nil || !strings.Contains(string(b), asqsGeneratedHeader) {
		return
	}
	_ = os.Remove(stale)
}

func patchRootTSConfigForTestGlobals(tsconfigPath string, g tsTestGlobals) (bool, error) {
	raw, err := os.ReadFile(tsconfigPath)
	if err != nil {
		return false, err
	}
	stripped := stripTSConfigJSONComments(raw)
	stripped = stripTrailingJSONCommas(stripped)
	var root map[string]interface{}
	if err := json.Unmarshal(stripped, &root); err != nil {
		return false, nil
	}
	changed := false
	if co, ok := root["compilerOptions"].(map[string]interface{}); ok {
		if types, ok := co["types"].([]interface{}); ok {
			// Retire every entry this bootstrap manages that the CURRENT profile does not need —
			// the other runner's, and any jest-dom entry left by a profile that used to install it.
			// An entry naming a package the stack no longer installs is TS2688 on every compile.
			want := make(map[string]bool, len(g.ReferenceTypes))
			for _, t := range g.ReferenceTypes {
				want[t] = true
			}
			for _, managed := range asqsManagedTypeEntries {
				if want[managed] {
					continue
				}
				var dropped bool
				types, dropped = dropStringFromSlice(types, managed)
				changed = changed || dropped
			}
			for _, t := range g.ReferenceTypes {
				if !stringSliceContains(types, t) {
					types = append(types, t)
					changed = true
				}
			}
			co["types"] = types
		}
	}
	for _, key := range []string{"include", "files"} {
		list, ok := root[key].([]interface{})
		if !ok {
			continue
		}
		list, dropped := dropStringFromSlice(list, g.other().DTSFile)
		changed = changed || dropped
		if !pathListReferencesGlobalsDTS(list, g.DTSFile) {
			list = append(list, g.DTSFile)
			changed = true
		}
		root[key] = list
	}
	if !changed {
		return false, nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	if err := atomicWrite(tsconfigPath, out); err != nil {
		return false, err
	}
	return true, nil
}

func stringSliceContains(slice []interface{}, want string) bool {
	for _, v := range slice {
		s, _ := v.(string)
		if s == want {
			return true
		}
	}
	return false
}

// dropStringFromSlice removes every entry equal to want (by base name for paths), reporting whether
// anything was removed. Used to retire the other runner's entries rather than leave both.
func dropStringFromSlice(list []interface{}, want string) ([]interface{}, bool) {
	out := make([]interface{}, 0, len(list))
	removed := false
	for _, v := range list {
		s, _ := v.(string)
		t := strings.TrimSpace(s)
		if t == want || strings.TrimPrefix(filepath.Base(t), "./") == want {
			removed = true
			continue
		}
		out = append(out, v)
	}
	return out, removed
}

func pathListReferencesGlobalsDTS(inc []interface{}, dtsFile string) bool {
	for _, v := range inc {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.TrimPrefix(filepath.Base(s), "./") == dtsFile {
			return true
		}
		if strings.Contains(s, dtsFile) {
			return true
		}
		// Broad globs that already pull in repo-root .d.ts
		if s == "**/*" || s == "**/*.ts" || s == "**/*.tsx" {
			return true
		}
	}
	return false
}

func stripTSConfigJSONComments(b []byte) []byte {
	lines := bytes.Split(b, []byte("\n"))
	var out [][]byte
	for _, line := range lines {
		trim := bytes.TrimSpace(line)
		if len(trim) >= 2 && bytes.HasPrefix(trim, []byte("//")) {
			continue
		}
		out = append(out, line)
	}
	return bytes.Join(out, []byte("\n"))
}

var trailingCommaJSON = regexp.MustCompile(`,(\s*[}\]])`)

func stripTrailingJSONCommas(b []byte) []byte {
	return trailingCommaJSON.ReplaceAll(b, []byte("$1"))
}
