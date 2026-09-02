package testbootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// vitestProfile / jestProfile are the minimum a caller needs to select declarations: the runner,
// plus the dependency list the jest-dom entry is gated on.
func vitestProfile(deps ...string) jsTestProfile {
	return testProfile(JSRunnerVitest, deps...)
}

func jestProfile(deps ...string) jsTestProfile {
	return testProfile(JSRunnerJest, deps...)
}

func testProfile(r JSRunner, deps ...string) jsTestProfile {
	p := jsTestProfile{Runner: r}
	for _, d := range deps {
		p.Deps = append(p.Deps, jsDep{Name: d, Version: "0.0.0"})
	}
	return p
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestEnsureTestTypeScriptTooling_vitestDeclaresVitestGlobals is the regression for a React run
// whose compile step failed with `TS2304: Cannot find name 'vi'`.
//
// The patcher was Jest-only and ran for every TypeScript profile. A Vitest project got
// `/// <reference types="jest" />` — types its stack never installs — while `vi`, `describe` and
// `expect` stayed undeclared, so `tsc --noEmit -p tsconfig.app.json` rejected every generated test.
// The globals exist at run time (the generated vitest.config.ts sets `globals: true`); only the
// declaration was missing.
func TestEnsureTestTypeScriptTooling_vitestDeclaresVitestGlobals(t *testing.T) {
	dir := t.TempDir()
	tsconfig := filepath.Join(dir, "tsconfig.app.json")
	if err := os.WriteFile(tsconfig, []byte(`{"compilerOptions":{"types":["node"]},"include":["src"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	patched, err := ensureTestTypeScriptTooling(dir, dir, vitestProfile())
	if err != nil {
		t.Fatal(err)
	}
	if len(patched) != 1 {
		t.Fatalf("patched = %v, want the one tsconfig", patched)
	}

	dts := readFileString(t, filepath.Join(dir, vitestTSGlobals.DTSFile))
	if !strings.Contains(dts, `reference types="vitest/globals"`) {
		t.Errorf("declaration file must reference vitest/globals:\n%s", dts)
	}
	if strings.Contains(dts, `types="jest"`) {
		t.Errorf("a Vitest project must not be given Jest globals:\n%s", dts)
	}
	if _, err := os.Stat(filepath.Join(dir, jestTSGlobals.DTSFile)); !os.IsNotExist(err) {
		t.Error("the Jest declaration file must not be written for a Vitest profile")
	}

	out := readFileString(t, tsconfig)
	if !strings.Contains(out, `"vitest/globals"`) {
		t.Errorf("compilerOptions.types must carry vitest/globals:\n%s", out)
	}
	if !strings.Contains(out, vitestTSGlobals.DTSFile) {
		t.Errorf("include must carry the declaration file:\n%s", out)
	}
	if strings.Contains(out, `"jest"`) {
		t.Errorf("types must not carry jest for a Vitest profile:\n%s", out)
	}
}

// Jest keeps exactly what it had: Angular and NestJS depend on it, and this change must not touch
// them.
func TestEnsureTestTypeScriptTooling_jestUnchanged(t *testing.T) {
	dir := t.TempDir()
	tsconfig := filepath.Join(dir, "tsconfig.json")
	if err := os.WriteFile(tsconfig, []byte(`{"compilerOptions":{"types":["node"]},"include":["src"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTestTypeScriptTooling(dir, dir, jestProfile()); err != nil {
		t.Fatal(err)
	}
	dts := readFileString(t, filepath.Join(dir, jestTSGlobals.DTSFile))
	if !strings.Contains(dts, `reference types="jest"`) {
		t.Errorf("Jest profiles keep the Jest declaration:\n%s", dts)
	}
	out := readFileString(t, tsconfig)
	if !strings.Contains(out, `"jest"`) || !strings.Contains(out, jestTSGlobals.DTSFile) {
		t.Errorf("Jest tsconfig patch regressed:\n%s", out)
	}
}

// Re-bootstrapping a repository onto the other runner must retire the previous declaration rather
// than leave both: the stale file references types the new stack does not install, which tsc
// reports as TS2688 on every compile from then on.
func TestEnsureTestTypeScriptTooling_retiresTheOtherRunnersDeclaration(t *testing.T) {
	dir := t.TempDir()
	tsconfig := filepath.Join(dir, "tsconfig.json")
	if err := os.WriteFile(tsconfig, []byte(`{"compilerOptions":{"types":["node"]},"include":["src"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTestTypeScriptTooling(dir, dir, jestProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTestTypeScriptTooling(dir, dir, vitestProfile()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, jestTSGlobals.DTSFile)); !os.IsNotExist(err) {
		t.Error("the Jest declaration this bootstrap wrote must be removed when switching to Vitest")
	}
	out := readFileString(t, tsconfig)
	if strings.Contains(out, jestTSGlobals.DTSFile) {
		t.Errorf("include still references the retired declaration:\n%s", out)
	}
	if strings.Contains(out, `"jest"`) {
		t.Errorf("compilerOptions.types still carries jest:\n%s", out)
	}
	if !strings.Contains(out, `"vitest/globals"`) || !strings.Contains(out, vitestTSGlobals.DTSFile) {
		t.Errorf("the Vitest declaration is missing:\n%s", out)
	}
}

// A declaration file the repository wrote itself is not ours to delete.
func TestRemoveStaleTestGlobalsDTS_leavesForeignFilesAlone(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, jestTSGlobals.DTSFile)
	const body = "/// <reference types=\"jest\" />\n"
	if err := os.WriteFile(foreign, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	removeStaleTestGlobalsDTS(dir, vitestTSGlobals)
	if got := readFileString(t, foreign); got != body {
		t.Errorf("a file without the ASQS header must be left untouched, got %q", got)
	}
}

// Running twice for the same runner must change nothing the second time.
func TestEnsureTestTypeScriptTooling_idempotent(t *testing.T) {
	dir := t.TempDir()
	tsconfig := filepath.Join(dir, "tsconfig.json")
	if err := os.WriteFile(tsconfig, []byte(`{"compilerOptions":{"types":["node"]},"include":["src"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTestTypeScriptTooling(dir, dir, vitestProfile()); err != nil {
		t.Fatal(err)
	}
	first := readFileString(t, tsconfig)
	patched, err := ensureTestTypeScriptTooling(dir, dir, vitestProfile())
	if err != nil {
		t.Fatal(err)
	}
	if len(patched) != 0 {
		t.Errorf("second run reported %v as patched", patched)
	}
	if second := readFileString(t, tsconfig); second != first {
		t.Errorf("tsconfig changed on the second run:\n%s\n---\n%s", first, second)
	}
	// And the types array must not have grown a duplicate.
	var root map[string]any
	if err := json.Unmarshal([]byte(first), &root); err != nil {
		t.Fatal(err)
	}
	types, _ := root["compilerOptions"].(map[string]any)["types"].([]any)
	seen := 0
	for _, v := range types {
		if s, _ := v.(string); s == "vitest/globals" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("vitest/globals appears %d time(s) in %v", seen, types)
	}
}

func TestTSTestGlobalsForRunner(t *testing.T) {
	if got := tsTestGlobalsForRunner(JSRunnerVitest); !slices.Equal(got.ReferenceTypes, []string{"vitest/globals"}) {
		t.Errorf("vitest → %q", got.ReferenceTypes)
	}
	if got := tsTestGlobalsForRunner(JSRunnerJest); !slices.Equal(got.ReferenceTypes, []string{"jest"}) {
		t.Errorf("jest → %q", got.ReferenceTypes)
	}
	// An unset runner keeps the historical Jest behaviour rather than declaring nothing.
	if got := tsTestGlobalsForRunner(""); !slices.Equal(got.ReferenceTypes, []string{"jest"}) {
		t.Errorf("unknown runner → %q, want the jest default", got.ReferenceTypes)
	}
}

// TestEnsureTestTypeScriptTooling_vitestDeclaresJestDomMatchers is the regression for the run of
// 2026-09-01 (react/vitest, 20 gaps): 63 of 112 compile errors were
// `TS2339: Property 'toBeInTheDocument' does not exist on type 'Assertion<HTMLElement>'`.
//
// The setup file imports @testing-library/jest-dom's ROOT entry point, which extends `expect` at
// run time — the bootstrap's own framework smoke asserts .toBeInTheDocument() and passed — but
// whose types augment Jest's `jest.Matchers`, not Vitest's `Assertion`. Nothing declared the Vitest
// augmentation to tsc, and the setup file is not in the program (`include` was ["src", <dts>]), so
// every generated component test failed the compile step the run's own baseline had just passed.
func TestEnsureTestTypeScriptTooling_vitestDeclaresJestDomMatchers(t *testing.T) {
	dir := t.TempDir()
	tsconfig := filepath.Join(dir, "tsconfig.app.json")
	if err := os.WriteFile(tsconfig, []byte(`{"compilerOptions":{"types":["node"]},"include":["src"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTestTypeScriptTooling(dir, dir, vitestProfile("vitest", "jsdom", jestDomPackage)); err != nil {
		t.Fatal(err)
	}
	dts := readFileString(t, filepath.Join(dir, vitestTSGlobals.DTSFile))
	if !strings.Contains(dts, `reference types="`+jestDomVitestTypes+`"`) {
		t.Errorf("a stack that installs %s must declare its Vitest matchers:\n%s", jestDomPackage, dts)
	}
	if !strings.Contains(dts, `reference types="vitest/globals"`) {
		t.Errorf("the runner's own globals must survive:\n%s", dts)
	}
	if out := readFileString(t, tsconfig); !strings.Contains(out, jestDomVitestTypes) {
		t.Errorf("compilerOptions.types must carry the matcher augmentation:\n%s", out)
	}
}

// A Vitest stack with no Testing Library must NOT get the jest-dom entry: vitest-vite,
// vitest-vite-jsdom and vitest-node-esm select Vitest and install no matchers. Declaring types for
// a package that is not installed is TS2688 — unconditionally from compilerOptions.types, and from
// the .d.ts too on a repository that does not set skipLibCheck. Both verified against
// @testing-library/jest-dom@7.0.1.
func TestEnsureTestTypeScriptTooling_vitestWithoutJestDomDeclaresNoMatchers(t *testing.T) {
	dir := t.TempDir()
	tsconfig := filepath.Join(dir, "tsconfig.json")
	if err := os.WriteFile(tsconfig, []byte(`{"compilerOptions":{"types":["node"]},"include":["src"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTestTypeScriptTooling(dir, dir, vitestProfile("vitest")); err != nil {
		t.Fatal(err)
	}
	dts := readFileString(t, filepath.Join(dir, vitestTSGlobals.DTSFile))
	if strings.Contains(dts, jestDomPackage) {
		t.Errorf("a stack without jest-dom must not reference it (TS2688):\n%s", dts)
	}
	if out := readFileString(t, tsconfig); strings.Contains(out, jestDomPackage) {
		t.Errorf("compilerOptions.types must not name an uninstalled package:\n%s", out)
	}
}

// Re-bootstrapping a repository onto a profile that no longer installs jest-dom must RETIRE the
// entry, not leave it behind. compilerOptions.types is the sharp edge here: an entry naming an
// uninstalled package is TS2688 on every compile whatever skipLibCheck says.
func TestEnsureTestTypeScriptTooling_retiresJestDomWhenProfileDrops(t *testing.T) {
	dir := t.TempDir()
	tsconfig := filepath.Join(dir, "tsconfig.json")
	if err := os.WriteFile(tsconfig, []byte(`{"compilerOptions":{"types":["node"]},"include":["src"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTestTypeScriptTooling(dir, dir, vitestProfile("vitest", jestDomPackage)); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTestTypeScriptTooling(dir, dir, vitestProfile("vitest")); err != nil {
		t.Fatal(err)
	}
	dts := readFileString(t, filepath.Join(dir, vitestTSGlobals.DTSFile))
	if strings.Contains(dts, jestDomPackage) {
		t.Errorf("the declaration file still references the retired package:\n%s", dts)
	}
	out := readFileString(t, tsconfig)
	if strings.Contains(out, jestDomPackage) {
		t.Errorf("compilerOptions.types still carries the retired entry:\n%s", out)
	}
	if !strings.Contains(out, `"vitest/globals"`) {
		t.Errorf("the runner's own entry must survive the retirement:\n%s", out)
	}
}

// Upgrading a repository bootstrapped by an older release — whose .d.ts carries only the runner
// globals — must ADD the matcher declaration rather than treat the file as already correct.
func TestWriteTestGlobalsDTS_upgradesAnIncompleteASQSFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, vitestTSGlobals.DTSFile)
	old := "// " + asqsGeneratedHeader + " — Vitest globals for TypeScript / ESLint. Safe to edit or delete.\n" +
		`/// <reference types="vitest/globals" />` + "\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	g := tsTestGlobalsForProfile(vitestProfile("vitest", jestDomPackage))
	if err := writeTestGlobalsDTS(dir, g); err != nil {
		t.Fatal(err)
	}
	if got := readFileString(t, path); !strings.Contains(got, jestDomVitestTypes) {
		t.Errorf("an ASQS-written file missing a declaration must be upgraded:\n%s", got)
	}
}

// ...but a file the REPOSITORY wrote is not ours to rewrite, which is the rule
// removeStaleTestGlobalsDTS already follows. Before the set could grow this was unreachable.
func TestWriteTestGlobalsDTS_leavesAForeignFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, vitestTSGlobals.DTSFile)
	const body = "/// <reference types=\"vitest/globals\" />\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g := tsTestGlobalsForProfile(vitestProfile("vitest", jestDomPackage))
	if err := writeTestGlobalsDTS(dir, g); err != nil {
		t.Fatal(err)
	}
	if got := readFileString(t, path); got != body {
		t.Errorf("a file without the ASQS header must be left untouched, got:\n%s", got)
	}
}

// The Jest arm is deliberately unchanged: jest-dom's root types augment jest.Matchers, which is a
// different entry point and a different question than the Vitest one this run measured.
func TestTSTestGlobalsForProfile_jestUnchangedByJestDom(t *testing.T) {
	got := tsTestGlobalsForProfile(jestProfile("jest", jestDomPackage))
	if !slices.Equal(got.ReferenceTypes, []string{"jest"}) {
		t.Errorf("jest profile → %q, want just the jest globals", got.ReferenceTypes)
	}
}
