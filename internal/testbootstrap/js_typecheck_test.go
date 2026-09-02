package testbootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tcPkg(t *testing.T, scripts string, dirs ...string) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"name":"t","private":true`
	if scripts != "" {
		body += `,"scripts":` + scripts
	}
	body += "}\n"
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The cheap half of the compile gate is preferred, but `build` is what the evaluator actually runs
// (runner/js_plan.go), so it must remain the fallback rather than the gate silently skipping.
func TestJSTypecheckScript_preferenceOrder(t *testing.T) {
	for _, tc := range []struct{ name, scripts, want string }{
		{"typecheck wins over build", `{"build":"tsc && vite build","typecheck":"tsc --noEmit"}`, "typecheck"},
		{"hyphenated variant", `{"build":"x","type-check":"tsc --noEmit"}`, "type-check"},
		{"falls back to build", `{"build":"tsc --noEmit -p tsconfig.app.json && vite build","test":"vitest run"}`, "build"},
		{"no compile-ish script", `{"test":"vitest run","dev":"vite"}`, ""},
		{"no scripts at all", "", ""},
		{"empty script value is not a script", `{"typecheck":"   ","build":"tsc"}`, "build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsTypecheckScript(tcPkg(t, tc.scripts)); got != tc.want {
				t.Errorf("jsTypecheckScript = %q, want %q", got, tc.want)
			}
		})
	}
}

// The probe must land where the tsconfig already looks. A probe outside the program type-checks
// vacuously, which is the failure mode this whole gate exists to close.
func TestJSTypecheckProbeDir(t *testing.T) {
	for _, tc := range []struct {
		name string
		dirs []string
		want string
		ok   bool
	}{
		{"src preferred", []string{"lib", "src"}, "src", true},
		{"lib when no src", []string{"lib"}, "lib", true},
		{"app when neither", []string{"app"}, "app", true},
		{"no conventional root", []string{"packages"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := jsTypecheckProbeDir(tcPkg(t, "", tc.dirs...))
			if got != tc.want || ok != tc.ok {
				t.Errorf("jsTypecheckProbeDir = (%q,%v), want (%q,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// Classification decides whether a failure is the bootstrap's fault or the repository's. Both
// outputs below were produced by tsc 5.7.3 against the real vitest-react stack.
func TestJSOutputNamesPath_realCompilerOutput(t *testing.T) {
	const probe = "src/asqs-typecheck-probe.test.tsx"

	probeFailure := "src/asqs-typecheck-probe.test.tsx(15,51): error TS2339: " +
		"Property 'toBeInTheDocument' does not exist on type 'Assertion<HTMLElement>'.\n"
	if !jsOutputNamesPath(probeFailure, probe) {
		t.Error("a diagnostic naming the probe must be attributed to the stack")
	}

	repoFailure := "src/Broken.ts(1,14): error TS2322: Type 'string' is not assignable to type 'number'.\n"
	if jsOutputNamesPath(repoFailure, probe) {
		t.Error("a diagnostic about the repository's own source must not be blamed on the stack")
	}

	// Sandbox runs report container-absolute paths; the base name still identifies the probe.
	containerPath := "/workspace/src/asqs-typecheck-probe.test.tsx(15,51): error TS2339: nope\n"
	if !jsOutputNamesPath(containerPath, probe) {
		t.Error("a container-absolute path naming the probe must still be attributed to the stack")
	}
	// Windows separators.
	if !jsOutputNamesPath(`C:\repo\src\asqs-typecheck-probe.test.tsx(15,51): error TS2339: nope`, probe) {
		t.Error("backslash-separated output naming the probe must be attributed to the stack")
	}
	if jsOutputNamesPath("", probe) || jsOutputNamesPath(probeFailure, "") {
		t.Error("empty inputs must not classify as a probe failure")
	}
}

// Staging must put the probe inside the chosen directory, and a companion component must be written
// as a plain module rather than a second test file the runner would try to execute.
func TestWriteJSSmokeIn_stagesIntoTheChosenDir(t *testing.T) {
	dir := t.TempDir()
	probe, err := writeJSSmokeIn(dir, "src", jsTypecheckProbeBase, ".tsx", "export {};\n")
	if err != nil {
		t.Fatal(err)
	}
	if probe.Rel != "src/asqs-typecheck-probe.test.tsx" {
		t.Errorf("probe.Rel = %q, want src/asqs-typecheck-probe.test.tsx", probe.Rel)
	}
	if !probe.Wrote {
		t.Error("probe should report that it wrote the file")
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "asqs-typecheck-probe.test.tsx")); err != nil {
		t.Errorf("probe not on disk: %v", err)
	}

	companion, err := writeJSSmokeIn(dir, "src", "AsqsSmoke", ".vue", "<template><span/></template>\n")
	if err != nil {
		t.Fatal(err)
	}
	if companion.Rel != "src/AsqsSmoke.vue" {
		t.Errorf("companion.Rel = %q, want src/AsqsSmoke.vue (not a .test file)", companion.Rel)
	}

	// The default smoke directory must be unaffected by the new parameter.
	unit, err := writeJSSmoke(dir, "asqs-bootstrap-smoke", ".ts", "export {};\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(unit.Rel, jsSmokeDir+"/") {
		t.Errorf("writeJSSmoke.Rel = %q, want it under %s/", unit.Rel, jsSmokeDir)
	}
}

// Every skip path must leave the package untouched — and must not run anything, since the runner
// here has no Docker and no package manager.
func TestRunJSTypecheckGate_skipsWithoutStagingAnything(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scripts string
		dirs    []string
		isTS    bool
	}{
		{"not a TypeScript profile", `{"typecheck":"tsc --noEmit"}`, []string{"src"}, false},
		{"no compile-ish script", `{"test":"vitest run"}`, []string{"src"}, true},
		{"no conventional source root", `{"typecheck":"tsc --noEmit"}`, []string{"packages"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg := tcPkg(t, tc.scripts, tc.dirs...)
			prof := jsTestProfile{Runner: JSRunnerVitest, IsTS: tc.isTS, Framework: JSFrameworkReact, FrameworkSmoke: jsSmokeReact, Stack: "vitest-react"}
			res, _, _, _ := runJSTypecheckGate(context.Background(), jsGoalRunner{workdir: pkg}, pkg, prof)
			if res != jsTypecheckSkipped {
				t.Fatalf("result = %v, want skipped", res)
			}
			for _, d := range tc.dirs {
				entries, err := os.ReadDir(filepath.Join(pkg, d))
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Errorf("a skipped gate staged files into %s: %v", d, entries)
				}
			}
		})
	}
}

// The probe source must be the framework smoke when the profile has one: it is the file that
// exercises the matcher types a runtime smoke cannot check. If this ever stops carrying
// .toBeInTheDocument(), the gate stops catching the defect it was built for.
func TestJSTypecheckProbeSource_carriesTheMatcherAssertion(t *testing.T) {
	prof := jsTestProfile{Runner: JSRunnerVitest, IsTS: true, Framework: JSFrameworkReact, FrameworkSmoke: jsSmokeReact}
	spec := renderJSFrameworkSmokeSpec(prof)
	if !strings.Contains(spec.TestSource, "toBeInTheDocument") {
		t.Errorf("the react probe no longer asserts a jest-dom matcher:\n%s", spec.TestSource)
	}
	if spec.TestExt != ".tsx" {
		t.Errorf("TestExt = %q, want .tsx so JSX type-checks", spec.TestExt)
	}
}
