package testbootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func withLocalTsc(t *testing.T, pkg string) {
	t.Helper()
	writeFile(t, pkg, "node_modules/.bin/tsc", "#!/bin/sh\nexit 0\n")
}

// The no-emit run replaces the build script only where it can compile the same program: a plain
// tsconfig.json and a local tsc. A dedicated typecheck script, a solution-style tsconfig or a
// missing compiler keep the package's own script as the gate.
func TestJSNoEmitTypecheckArgv(t *testing.T) {
	want := "npx --no-install tsc --noEmit -p tsconfig.json"
	cases := []struct {
		name    string
		script  string
		prepare func(pkg string)
		wantCmd bool
	}{
		{"build script, plain tsconfig, local tsc", "build", func(pkg string) {
			writeFile(t, pkg, "tsconfig.json", `{"compilerOptions":{"outDir":"./dist","incremental":true}}`)
			withLocalTsc(t, pkg)
		}, true},
		{"no script at all but tsc present", "", func(pkg string) {
			writeFile(t, pkg, "tsconfig.json", `{"compilerOptions":{}}`)
			withLocalTsc(t, pkg)
		}, true},
		{"dedicated typecheck script wins", "typecheck", func(pkg string) {
			writeFile(t, pkg, "tsconfig.json", `{"compilerOptions":{}}`)
			withLocalTsc(t, pkg)
		}, false},
		{"solution-style tsconfig with references", "build", func(pkg string) {
			writeFile(t, pkg, "tsconfig.json", "{\n  \"files\": [],\n  \"references\": [{\"path\": \"./tsconfig.app.json\"}]\n}\n")
			withLocalTsc(t, pkg)
		}, false},
		{"no local tsc", "build", func(pkg string) {
			writeFile(t, pkg, "tsconfig.json", `{"compilerOptions":{}}`)
		}, false},
		{"no tsconfig", "build", func(pkg string) { withLocalTsc(t, pkg) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg := t.TempDir()
			tc.prepare(pkg)
			argv := jsNoEmitTypecheckArgv(pkg, tc.script)
			if tc.wantCmd && (argv == nil || joinArgv(argv) != want) {
				t.Fatalf("argv = %q, want %q", argv, want)
			}
			if !tc.wantCmd && argv != nil {
				t.Fatalf("argv = %q, want nil", argv)
			}
		})
	}
}

func joinArgv(argv []string) string {
	s := ""
	for i, a := range argv {
		if i > 0 {
			s += " "
		}
		s += a
	}
	return s
}

// A build script leaves the probe's compiled twins under outDir; Jest matched
// dist/src/asqs-typecheck-probe.test.js in run api-d01f66ab5b4c58f8e129844d98f8e370. Only files
// named after the probe are removed; the rest of the build output is the repository's.
func TestRemoveEmittedProbeOutput_removesOnlyTheProbe(t *testing.T) {
	pkg := t.TempDir()
	writeFile(t, pkg, "tsconfig.json", `{"compilerOptions":{"outDir":"./dist"}}`)
	writeFile(t, pkg, "dist/src/"+jsTypecheckProbeBase+".test.js", "x")
	writeFile(t, pkg, "dist/src/"+jsTypecheckProbeBase+".test.d.ts", "x")
	writeFile(t, pkg, "dist/src/"+jsTypecheckProbeBase+".test.js.map", "x")
	writeFile(t, pkg, "dist/src/main.js", "x")
	writeFile(t, pkg, "dist/__tests__/asqs-bootstrap-smoke.test.js", "x")
	writeFile(t, pkg, "src/"+jsTypecheckProbeBase+".test.ts", "x") // the source is the gate's own defer, not ours
	removeEmittedProbeOutput(pkg)
	for _, gone := range []string{"dist/src/" + jsTypecheckProbeBase + ".test.js", "dist/src/" + jsTypecheckProbeBase + ".test.d.ts", "dist/src/" + jsTypecheckProbeBase + ".test.js.map"} {
		if _, err := os.Stat(filepath.Join(pkg, filepath.FromSlash(gone))); err == nil {
			t.Errorf("%s still exists", gone)
		}
	}
	for _, kept := range []string{"dist/src/main.js", "dist/__tests__/asqs-bootstrap-smoke.test.js", "src/" + jsTypecheckProbeBase + ".test.ts"} {
		if _, err := os.Stat(filepath.Join(pkg, filepath.FromSlash(kept))); err != nil {
			t.Errorf("%s was removed", kept)
		}
	}
}

func TestRemoveEmittedProbeOutput_neverWalksThePackageItself(t *testing.T) {
	pkg := t.TempDir()
	writeFile(t, pkg, "tsconfig.json", `{"compilerOptions":{"outDir":"."}}`)
	writeFile(t, pkg, "src/"+jsTypecheckProbeBase+".test.ts", "x")
	removeEmittedProbeOutput(pkg)
	if _, err := os.Stat(filepath.Join(pkg, "src", jsTypecheckProbeBase+".test.ts")); err != nil {
		t.Fatal("outDir '.' must not make the cleaner delete the staged probe source")
	}
}
