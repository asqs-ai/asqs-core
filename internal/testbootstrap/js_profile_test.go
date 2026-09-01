package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePkgJSON(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSemverMajor(t *testing.T) {
	for in, want := range map[string]int{
		"^19.2.0": 19, "~5.0.1": 5, "18.3.1": 18, ">=6.0.0": 6, "v7": 7,
		"workspace:*": 0, "": 0, "latest": 0,
	} {
		if got := semverMajor(in); got != want {
			t.Errorf("semverMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestDetectJSFramework(t *testing.T) {
	cases := []struct {
		name      string
		pkg       string
		files     []string
		wantFW    JSFramework
		wantMajor int
		wantVite  int
	}{
		{
			name:      "angular",
			pkg:       `{"dependencies":{"@angular/core":"^19.2.0"}}`,
			wantFW:    JSFrameworkAngular,
			wantMajor: 19,
		},
		{
			name:      "nestjs wins over a transitive react",
			pkg:       `{"dependencies":{"@nestjs/core":"^11.0.0","react":"^18.0.0"}}`,
			wantFW:    JSFrameworkNest,
			wantMajor: 11,
		},
		{
			name:      "react with vite",
			pkg:       `{"type":"module","dependencies":{"react":"^18.3.1"},"devDependencies":{"vite":"^6.0.7"}}`,
			files:     []string{"vite.config.ts"},
			wantFW:    JSFrameworkReact,
			wantMajor: 18,
			wantVite:  6,
		},
		{
			name:   "react without vite",
			pkg:    `{"dependencies":{"react":"^18.3.1"}}`,
			wantFW: JSFrameworkReact,
		},
		{
			name:   "esm node",
			pkg:    `{"type":"module","dependencies":{"express":"^4.21.2"}}`,
			wantFW: JSFrameworkNodeESM,
		},
		{
			name:   "plain commonjs",
			pkg:    `{"dependencies":{"express":"^4.21.2"}}`,
			wantFW: JSFrameworkPlain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePkgJSON(t, dir, tc.pkg)
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("export default {}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			det, err := detectJSFramework(dir, "")
			if err != nil {
				t.Fatal(err)
			}
			if det.Framework != tc.wantFW {
				t.Fatalf("framework = %q, want %q (evidence %s)", det.Framework, tc.wantFW, det.Evidence)
			}
			if tc.wantMajor != 0 && det.FrameworkMajor != tc.wantMajor {
				t.Errorf("major = %d, want %d", det.FrameworkMajor, tc.wantMajor)
			}
			if tc.wantVite != 0 && det.ViteMajor != tc.wantVite {
				t.Errorf("vite major = %d, want %d", det.ViteMajor, tc.wantVite)
			}
		})
	}
}

// TestJestPresetAngularForMajor_avoidsTheBuildAngularJestConflict is a regression test for a real
// ERESOLVE failure: @angular-devkit/build-angular declares peerOptional jest ^29.5.0 through Angular
// 19, so pulling the newest preset (which needs Jest 30) makes `npm install` fail outright.
func TestJestPresetAngularForMajor_avoidsTheBuildAngularJestConflict(t *testing.T) {
	for _, ng := range []int{15, 16, 17, 18, 19, 20} {
		preset, jestMajor := jestPresetAngularForMajor(ng)
		if jestMajor != 29 {
			t.Errorf("Angular %d → jest %d; build-angular pins peerOptional jest ^29.5.0 through Angular 19", ng, jestMajor)
		}
		if preset == "" {
			t.Errorf("Angular %d → no preset", ng)
		}
	}
	if preset, jestMajor := jestPresetAngularForMajor(21); jestMajor != 30 || preset == "" {
		t.Errorf("Angular 21 → %q/%d; build-angular 21 requires jest ^30.2.0", preset, jestMajor)
	}
	if preset, _ := jestPresetAngularForMajor(14); preset != "" {
		t.Errorf("Angular 14 is outside every current preset peer range, got %q", preset)
	}
}

func TestVitestVersionForVite(t *testing.T) {
	// Vitest 4 declares vite ^6 || ^7 || ^8 as a peer; installing it next to Vite 5 fails resolution.
	if got := vitestVersionForVite(6); got != VersionVitest4 {
		t.Errorf("vite 6 → %s, want %s", got, VersionVitest4)
	}
	if got := vitestVersionForVite(5); got != VersionVitest3 {
		t.Errorf("vite 5 → %s, want %s", got, VersionVitest3)
	}
	if got := vitestVersionForVite(4); got != VersionVitest1 {
		t.Errorf("vite 4 → %s, want %s", got, VersionVitest1)
	}
	// No Vite at all: Vitest 3 carries its own, so nothing else must be installed.
	if got := vitestVersionForVite(0); got != VersionVitest3 {
		t.Errorf("no vite → %s, want %s", got, VersionVitest3)
	}
}

func TestBuildJSTestProfile_runnerAndEnvironment(t *testing.T) {
	cases := []struct {
		name    string
		det     jsFrameworkDetection
		runner  JSRunner
		env     string
		wantDep []string
	}{
		{
			name:    "react+vite uses vitest and jsdom",
			det:     jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, IsTS: true, ViteMajor: 6},
			runner:  JSRunnerVitest,
			env:     "jsdom",
			wantDep: []string{"vitest", "jsdom", "@testing-library/react", "@testing-library/jest-dom"},
		},
		{
			name:    "react without vite uses jest with a jsdom environment",
			det:     jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, IsTS: true},
			runner:  JSRunnerJest,
			env:     "jsdom",
			wantDep: []string{"jest", "jest-environment-jsdom", "@testing-library/react", "ts-jest"},
		},
		{
			name:    "angular",
			det:     jsFrameworkDetection{Framework: JSFrameworkAngular, FrameworkMajor: 19, IsTS: true},
			runner:  JSRunnerJest,
			env:     "jsdom",
			wantDep: []string{"jest", "jest-preset-angular", "@types/jest"},
		},
		{
			name:    "nestjs",
			det:     jsFrameworkDetection{Framework: JSFrameworkNest, FrameworkMajor: 11, IsTS: true},
			runner:  JSRunnerJest,
			env:     "node",
			wantDep: []string{"jest", "ts-jest", "@nestjs/testing"},
		},
		{
			name:    "esm node uses vitest",
			det:     jsFrameworkDetection{Framework: JSFrameworkNodeESM, IsESM: true},
			runner:  JSRunnerVitest,
			env:     "node",
			wantDep: []string{"vitest"},
		},
		{
			name:    "plain node uses jest",
			det:     jsFrameworkDetection{Framework: JSFrameworkPlain},
			runner:  JSRunnerJest,
			env:     "node",
			wantDep: []string{"jest"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := buildJSTestProfile(tc.det, testNodeVersion)
			if p.Runner != tc.runner {
				t.Errorf("runner = %q, want %q", p.Runner, tc.runner)
			}
			if p.TestEnvironment != tc.env {
				t.Errorf("testEnvironment = %q, want %q", p.TestEnvironment, tc.env)
			}
			got := strings.Join(describeJSDeps(p.Deps), " ")
			for _, want := range tc.wantDep {
				if !strings.Contains(got, want+"@") {
					t.Errorf("missing %s; got %s", want, got)
				}
			}
			if p.TestScript != jsAsqsTestScript {
				t.Errorf("script = %q; bootstrap must not claim `test`", p.TestScript)
			}
		})
	}
}

func TestBuildJSTestProfile_reactTestingLibraryTracksReactMajor(t *testing.T) {
	// RTL 16 declares react ^18 || ^19; the 12 line declares react <18. A mismatch fails install.
	p18 := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, ViteMajor: 6}, testNodeVersion)
	p17 := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 17, ViteMajor: 6}, testNodeVersion)
	find := func(p jsTestProfile) string {
		for _, d := range p.Deps {
			if d.Name == "@testing-library/react" {
				return d.Version
			}
		}
		return ""
	}
	if find(p18) != VersionTestingLibraryReact {
		t.Errorf("react 18 → RTL %s", find(p18))
	}
	if find(p17) != VersionTestingLibraryReactLegacy {
		t.Errorf("react 17 → RTL %s, want the <18 line", find(p17))
	}
}

func TestNestTestingForMajor(t *testing.T) {
	for major, want := range map[int]string{8: "8.4.7", 9: "9.4.3", 10: "10.4.22", 11: "11.2.1"} {
		if got := nestTestingForMajor(major); got != want {
			t.Errorf("nest %d → %s, want %s", major, got, want)
		}
	}
	if got := nestTestingForMajor(12); !strings.HasPrefix(got, "12.") {
		t.Errorf("a newer major should fall back to its own line, got %q", got)
	}
	if got := nestTestingForMajor(7); got != "" {
		t.Errorf("unmapped older major should be empty, got %q", got)
	}
}

func TestJSProfile_missingDeps(t *testing.T) {
	p := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, ViteMajor: 6}, testNodeVersion)
	pkg := jsPackageJSON{DevDependencies: map[string]string{"vitest": "^4.0.0"}}
	missing := p.missingDeps(pkg)
	for _, d := range missing {
		if d.Name == "vitest" {
			t.Error("vitest is present and must not be reported missing")
		}
	}
	if len(missing) != len(p.Deps)-1 {
		t.Errorf("expected %d missing, got %d", len(p.Deps)-1, len(missing))
	}
}

// TestDetectUnitJS_reactWithBareJestNeedsBootstrap is the JS analogue of the Spring Boot regression:
// "jest is a devDependency" used to be enough to skip, leaving a React package with a node
// environment and no Testing Library.
func TestDetectUnitJS_reactWithBareJestNeedsBootstrap(t *testing.T) {
	dir := t.TempDir()
	writePkgJSON(t, dir, `{"dependencies":{"react":"^18.3.1"},"devDependencies":{"jest":"^29.7.0"}}`)
	rep, err := DetectUnit(dir, "javascript")
	if err != nil {
		t.Fatal(err)
	}
	if rep.HasFramework {
		t.Fatalf("a React package with only Jest must still be bootstrapped: %s", rep.Reason)
	}
	for _, want := range []string{"@testing-library/react", "jest-environment-jsdom"} {
		if !strings.Contains(rep.Reason, want) {
			t.Errorf("reason should name missing %s; got %s", want, rep.Reason)
		}
	}
}

func TestDetectUnitJS_completeStackIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writePkgJSON(t, dir, `{"dependencies":{"express":"^4.21.2"},"devDependencies":{"jest":"^29.7.0"}}`)
	rep, err := DetectUnit(dir, "javascript")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasFramework {
		t.Fatalf("a plain CommonJS package with Jest needs nothing: %s", rep.Reason)
	}
}

// TestDetectUnitJS_monorepoWithoutRootPackageJSON guards a hard failure: detection used to stat only
// the repo root and return the os error, aborting bootstrap for the whole run.
func TestDetectUnitJS_monorepoWithoutRootPackageJSON(t *testing.T) {
	repo := t.TempDir()
	writePkgJSON(t, filepath.Join(repo, "packages", "api"), `{"name":"api","dependencies":{"express":"^4.21.2"}}`)

	rep, err := DetectUnit(repo, "javascript")
	if err != nil {
		t.Fatalf("a monorepo with no root package.json must not fail detection: %v", err)
	}
	if rep.HasFramework {
		t.Errorf("the nested package has no runner: %s", rep.Reason)
	}
}

// TestBuildJSTestProfile_vitePicksVitestRegardlessOfFramework locks in the rule that Vite — not
// React — decides the runner. Keying it on React left a plain Vite + TypeScript app on Jest, where
// the JSX/alias/import.meta.env settings from vite.config are simply not applied.
func TestBuildJSTestProfile_vitePicksVitestRegardlessOfFramework(t *testing.T) {
	cases := []struct {
		name       string
		det        jsFrameworkDetection
		wantRunner JSRunner
		wantEnv    string
		wantJsdom  bool
	}{
		{
			name:       "vite + react",
			det:        jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, ViteMajor: 6, BrowserLike: true, IsTS: true},
			wantRunner: JSRunnerVitest, wantEnv: "jsdom", wantJsdom: true,
		},
		{
			name:       "vite + vue detected as esm",
			det:        jsFrameworkDetection{Framework: JSFrameworkNodeESM, ViteMajor: 6, BrowserLike: true, IsESM: true, IsTS: true},
			wantRunner: JSRunnerVitest, wantEnv: "jsdom", wantJsdom: true,
		},
		{
			name:       "vite vanilla TS, commonjs package",
			det:        jsFrameworkDetection{Framework: JSFrameworkPlain, ViteMajor: 6, IsTS: true},
			wantRunner: JSRunnerVitest, wantEnv: "node",
		},
		{
			name:       "vite library that renders to a DOM",
			det:        jsFrameworkDetection{Framework: JSFrameworkPlain, ViteMajor: 5, BrowserLike: true},
			wantRunner: JSRunnerVitest, wantEnv: "jsdom", wantJsdom: true,
		},
		{
			name:       "no vite, commonjs, stays on jest",
			det:        jsFrameworkDetection{Framework: JSFrameworkPlain},
			wantRunner: JSRunnerJest, wantEnv: "node",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := buildJSTestProfile(tc.det, testNodeVersion)
			if p.Runner != tc.wantRunner {
				t.Fatalf("runner = %q, want %q", p.Runner, tc.wantRunner)
			}
			if p.TestEnvironment != tc.wantEnv {
				t.Errorf("environment = %q, want %q", p.TestEnvironment, tc.wantEnv)
			}
			deps := strings.Join(describeJSDeps(p.Deps), " ")
			if tc.wantJsdom && !strings.Contains(deps, "jsdom@") && !strings.Contains(deps, "@testing-library/react@") {
				t.Errorf("a jsdom environment needs jsdom installed; got %s", deps)
			}
			if p.Runner == JSRunnerVitest && !strings.HasPrefix(p.ConfigFile, "vitest.config.") {
				t.Errorf("config = %q", p.ConfigFile)
			}
		})
	}
}

// TestBuildJSTestProfile_angularAndNestKeepJestEvenWithVite: Angular ships its own builder and
// preset, and Nest's convention is Jest. A stray vite.config must not switch either.
func TestBuildJSTestProfile_angularAndNestKeepJestEvenWithVite(t *testing.T) {
	ng := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkAngular, FrameworkMajor: 19, ViteMajor: 6, IsTS: true}, testNodeVersion)
	if ng.Runner != JSRunnerJest || ng.Stack != "jest-preset-angular" {
		t.Errorf("angular → %s/%s", ng.Runner, ng.Stack)
	}
	nest := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkNest, FrameworkMajor: 11, ViteMajor: 6, IsTS: true}, testNodeVersion)
	if nest.Runner != JSRunnerJest || nest.Stack != "jest-nestjs" {
		t.Errorf("nestjs → %s/%s", nest.Runner, nest.Stack)
	}
}

func TestDetectJSFramework_browserLikeFromIndexHTMLOrUILibrary(t *testing.T) {
	// index.html is the Vite app entry point, so a package with one renders to a DOM even when its
	// UI library is not one bootstrap knows by name.
	dir := t.TempDir()
	writePkgJSON(t, dir, `{"devDependencies":{"vite":"^6.0.7"}}`)
	if err := os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte("export default {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	det, err := detectJSFramework(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if det.BrowserLike {
		t.Error("no UI library and no index.html should not be browser-like")
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	det, err = detectJSFramework(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !det.BrowserLike {
		t.Error("index.html marks a Vite app that renders to a DOM")
	}
}

func TestRenderJSUnitSmoke_assertsTheDOMWhenEnvironmentIsJsdom(t *testing.T) {
	jsdomProf := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkPlain, ViteMajor: 6, BrowserLike: true, IsTS: true}, testNodeVersion)
	src, _ := renderJSUnitSmoke(jsdomProf)
	if !strings.Contains(src, "document.createElement") {
		t.Errorf("a jsdom profile's mandatory smoke must prove the DOM environment:\n%s", src)
	}
	nodeProf := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkPlain}, testNodeVersion)
	src2, _ := renderJSUnitSmoke(nodeProf)
	if strings.Contains(src2, "document.createElement") {
		t.Errorf("a node profile must not reference document:\n%s", src2)
	}
}

func TestDetectJSFramework_vueAndSvelte(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pkg       string
		wantFW    JSFramework
		wantMajor int
	}{
		{"vue 3", `{"type":"module","dependencies":{"vue":"^3.5.13"},"devDependencies":{"vite":"^6.0.7"}}`, JSFrameworkVue, 3},
		{"vue 2", `{"dependencies":{"vue":"^2.7.16"}}`, JSFrameworkVue, 2},
		{"svelte 5", `{"type":"module","dependencies":{"svelte":"^5.15.0"},"devDependencies":{"vite":"^6.0.7"}}`, JSFrameworkSvelte, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePkgJSON(t, dir, tc.pkg)
			det, err := detectJSFramework(dir, "")
			if err != nil {
				t.Fatal(err)
			}
			if det.Framework != tc.wantFW || det.FrameworkMajor != tc.wantMajor {
				t.Fatalf("got %s/%d, want %s/%d", det.Framework, det.FrameworkMajor, tc.wantFW, tc.wantMajor)
			}
			if !det.BrowserLike {
				t.Error("a Vue/Svelte package renders to a DOM")
			}
		})
	}
}

func TestBuildJSTestProfile_vueTestUtilsTracksVueMajor(t *testing.T) {
	// @vue/test-utils is hard-split: the 2 line declares peer vue 3.x, the 1 line declares 2.x.
	find := func(p jsTestProfile) string {
		for _, d := range p.Deps {
			if d.Name == "@vue/test-utils" {
				return d.Version
			}
		}
		return ""
	}
	v3 := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkVue, FrameworkMajor: 3, ViteMajor: 6, IsTS: true}, testNodeVersion)
	if find(v3) != VersionVueTestUtils {
		t.Errorf("vue 3 → @vue/test-utils %s", find(v3))
	}
	v2 := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkVue, FrameworkMajor: 2, IsTS: true}, testNodeVersion)
	if find(v2) != VersionVueTestUtilsLegacy {
		t.Errorf("vue 2 → @vue/test-utils %s, want the 1.x line", find(v2))
	}
	if v3.Runner != JSRunnerVitest || v3.TestEnvironment != "jsdom" {
		t.Errorf("vue → %s/%s", v3.Runner, v3.TestEnvironment)
	}
}

// TestBuildJSTestProfile_svelteNeedsBrowserResolveCondition guards a failure that reproduces exactly:
// without resolve.conditions=['browser'], Svelte resolves to its server build and render() throws
// "mount(...) is not available on the server" even though the jsdom environment is correct.
func TestBuildJSTestProfile_svelteNeedsBrowserResolveCondition(t *testing.T) {
	p := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkSvelte, FrameworkMajor: 5, ViteMajor: 6, IsTS: true}, testNodeVersion)
	if len(p.ResolveConditions) == 0 || p.ResolveConditions[0] != "browser" {
		t.Fatalf("svelte profile must set resolve.conditions=['browser'], got %v", p.ResolveConditions)
	}
	cfg := renderVitestConfig(p, "vite.config.ts")
	if !strings.Contains(cfg, "conditions: ['browser']") {
		t.Errorf("rendered config missing the browser condition:\n%s", cfg)
	}
	// Vue must not get it — nothing there needs it, and narrowing resolution has side effects.
	vue := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkVue, FrameworkMajor: 3, ViteMajor: 6, IsTS: true}, testNodeVersion)
	if len(vue.ResolveConditions) != 0 {
		t.Errorf("vue should not narrow resolve conditions, got %v", vue.ResolveConditions)
	}
}

// TestRenderJSFrameworkSmokeSpec_vueAndSvelteShipACompanionComponent: a .vue / .svelte file compiles
// only through that framework's Vite plugin, so importing a real one is what proves the plugin
// reaches the test run through the merged config.
func TestRenderJSFrameworkSmokeSpec_vueAndSvelteShipACompanionComponent(t *testing.T) {
	vue := renderJSFrameworkSmokeSpec(buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkVue, FrameworkMajor: 3, ViteMajor: 6, IsTS: true}, testNodeVersion))
	if vue.CompanionName != "AsqsSmoke.vue" || !strings.Contains(vue.TestSource, "./AsqsSmoke.vue") {
		t.Errorf("vue smoke must import a real SFC: %+v", vue.CompanionName)
	}
	if !strings.Contains(vue.CompanionSource, "<template>") {
		t.Errorf("companion is not an SFC:\n%s", vue.CompanionSource)
	}
	sv := renderJSFrameworkSmokeSpec(buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkSvelte, FrameworkMajor: 5, ViteMajor: 6, IsTS: true}, testNodeVersion))
	if sv.CompanionName != "AsqsSmoke.svelte" || !strings.Contains(sv.TestSource, "./AsqsSmoke.svelte") {
		t.Errorf("svelte smoke must import a real component: %+v", sv.CompanionName)
	}
	// React needs no companion: JSX lives in the test file itself.
	react := renderJSFrameworkSmokeSpec(buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, ViteMajor: 6, IsTS: true}, testNodeVersion))
	if react.CompanionName != "" {
		t.Errorf("react should need no companion, got %q", react.CompanionName)
	}
}

func TestWriteJSFrameworkSmokeTest_writesAndReportsTheCompanion(t *testing.T) {
	dir := t.TempDir()
	p := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkSvelte, FrameworkMajor: 5, ViteMajor: 6, IsTS: true}, testNodeVersion)
	test, extra, staged, err := writeJSFrameworkSmokeTest(dir, p)
	if err != nil || !staged {
		t.Fatalf("staged = %v err = %v", staged, err)
	}
	if len(extra) != 1 || !extra[0].Wrote {
		t.Fatalf("companion not reported: %+v", extra)
	}
	for _, f := range append([]jsSmokeFile{test}, extra...) {
		if !fileExists(f.Abs) {
			t.Errorf("%s not written", f.Rel)
		}
	}
	// A failed smoke must take its companion with it.
	removeJSSmokeFile(test)
	for _, f := range extra {
		removeJSSmokeFile(f)
	}
	for _, f := range append([]jsSmokeFile{test}, extra...) {
		if fileExists(f.Abs) {
			t.Errorf("%s should have been removed", f.Rel)
		}
	}
}
