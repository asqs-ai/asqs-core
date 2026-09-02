package testbootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The TypeScript gate: a stack that RUNS a test is not the same as a stack whose tests COMPILE.
//
// The bootstrap smoke has always executed the runner and nothing else, so `.asqs/test-stack.json`
// could record verified=true — a field whose own contract says the smoke "compiled AND ran" — for a
// stack that could not survive the evaluator's compile step. Run of 2026-09-01 is that gap in full:
// the React framework smoke asserts `.toBeInTheDocument()`, passed under `vitest run` at 15:39:09,
// and 27 minutes later the same matcher produced 63 of the 112 errors that failed
// `tsc --noEmit -p tsconfig.app.json`. jest-dom extends `expect` at run time whatever its types say,
// so the runtime gate could never have caught it. Only a type-check can.
//
// Two things make this gate honest rather than noisy:
//
//   - the probe is staged where GENERATED tests land, not in __tests__/. tsconfig include lists are
//     typically ["src"], so a probe outside it type-checks vacuously — which is the second half of
//     why the run above passed: even the correct import in vitest.setup.ts was outside the program.
//   - a failure is only blamed on the stack when the compiler NAMES the probe. A repository whose
//     own build is already broken must not be reported as a bootstrap failure; the evaluator's
//     baseline compile reports that, and it reports it about the repository.

// jsTypecheckProbeBase is the probe's file stem. The asqs- prefix keeps it recognisable in a source
// tree if a crash ever strands one.
const jsTypecheckProbeBase = "asqs-typecheck-probe"

// jsTypecheckResult is what the gate concluded.
type jsTypecheckResult int

const (
	// jsTypecheckSkipped: no probe could be staged, or the package declares no command to run.
	jsTypecheckSkipped jsTypecheckResult = iota
	// jsTypecheckOK: the probe type-checked.
	jsTypecheckOK
	// jsTypecheckProbeFailed: the command failed AND named the probe — this stack cannot host a
	// generated test that compiles.
	jsTypecheckProbeFailed
	// jsTypecheckInconclusive: the command failed without naming the probe, so the repository's own
	// sources are what did not compile. Not the bootstrap's finding to make.
	jsTypecheckInconclusive
)

// jsTypecheckScript picks the package script that type-checks the project.
//
// `typecheck` first because it is the cheap half of the gate: the evaluator's compile step for a JS
// package is `<pm> run build` (runner/js_plan.go), which on a Vite app is
// `tsc --noEmit -p tsconfig.app.json && vite build` — and only the tsc half can fail on a test file's
// types. Falling back to `build` keeps the gate faithful where no dedicated script exists; bundling
// costs time but never changes the answer for a file nothing imports.
func jsTypecheckScript(pkgDir string) string {
	scripts := jsPackageScripts(pkgDir)
	for _, name := range []string{"typecheck", "type-check", "tsc", "build"} {
		if strings.TrimSpace(scripts[name]) != "" {
			return name
		}
	}
	return ""
}

// jsPackageScripts reads package.json scripts, or nil.
func jsPackageScripts(pkgDir string) map[string]string {
	b, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return nil
	}
	var root struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &root) != nil {
		return nil
	}
	return root.Scripts
}

// jsProbeDirs are the conventional roots a tsconfig `include` covers, most specific first.
var jsProbeDirs = []string{"src", "lib", "app"}

// jsTypecheckProbeDir returns the directory to stage the probe in, relative to pkgDir.
//
// It must be a directory the project's tsconfig already compiles, because a probe outside the
// program type-checks vacuously and the gate then proves nothing. Convention covers the common
// cases; when none is present the caller skips rather than guessing, since staging into the package
// root would be both wrong (usually excluded) and intrusive.
func jsTypecheckProbeDir(pkgDir string) (string, bool) {
	for _, d := range jsProbeDirs {
		if st, err := os.Stat(filepath.Join(pkgDir, d)); err == nil && st.IsDir() {
			return d, true
		}
	}
	return "", false
}

// runJSTypecheckGate stages a representative generated test where generated tests land, type-checks
// it, and removes it. It never leaves the probe behind: every return path deletes it.
//
// Returns the outcome, the command that ran, and the compiler output for the failing cases.
func runJSTypecheckGate(ctx context.Context, runner jsGoalRunner, pkgDir string, prof jsTestProfile) (res jsTypecheckResult, cmdLine, probeRel string, out []byte) {
	if !prof.IsTS {
		return jsTypecheckSkipped, "", "", nil
	}
	script := jsTypecheckScript(pkgDir)
	if script == "" {
		return jsTypecheckSkipped, "", "", nil
	}
	dir, ok := jsTypecheckProbeDir(pkgDir)
	if !ok {
		return jsTypecheckSkipped, "", "", nil
	}

	// The framework smoke is the right probe when the profile has one: it is the file that
	// exercises the component-testing libraries whose MATCHER types are the thing a runtime smoke
	// cannot check. Profiles without one fall back to the unit smoke, which still proves the
	// runner's globals are declared to tsc.
	spec := renderJSFrameworkSmokeSpec(prof)
	source, ext := spec.TestSource, spec.TestExt
	if source == "" {
		source, ext = renderJSUnitSmoke(prof)
	}
	if strings.TrimSpace(source) == "" {
		return jsTypecheckSkipped, "", "", nil
	}

	var staged []jsSmokeFile
	defer func() {
		for _, f := range staged {
			removeJSSmokeFile(f)
		}
	}()

	// A companion component (Vue's .vue, Svelte's .svelte) must sit beside the probe or its import
	// does not resolve.
	if spec.CompanionName != "" && spec.TestSource != "" {
		companion, err := writeJSSmokeIn(pkgDir, dir, strings.TrimSuffix(spec.CompanionName, filepath.Ext(spec.CompanionName)),
			filepath.Ext(spec.CompanionName), spec.CompanionSource)
		if err != nil {
			return jsTypecheckSkipped, "", "", nil
		}
		staged = append(staged, companion)
	}
	probe, err := writeJSSmokeIn(pkgDir, dir, jsTypecheckProbeBase, ext, source)
	if err != nil {
		return jsTypecheckSkipped, "", "", nil
	}
	staged = append(staged, probe)

	cmdLine, out, rerr := runner.runScript(ctx, script)
	if rerr == nil {
		return jsTypecheckOK, cmdLine, probe.Rel, out
	}
	// Only our own file failing is the bootstrap's finding. tsc and MSBuild both name the file on
	// every diagnostic, so a probe-caused failure always mentions it.
	if jsOutputNamesPath(string(out), probe.Rel) {
		return jsTypecheckProbeFailed, cmdLine, probe.Rel, out
	}
	return jsTypecheckInconclusive, cmdLine, probe.Rel, out
}

// jsOutputNamesPath reports whether compiler output blames the given repo-relative path. Both
// separators are accepted, and the base name is enough — a container path prefix (/workspace/…) is
// ordinary in this project's sandboxes.
func jsOutputNamesPath(out, rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" {
		return false
	}
	hay := strings.ReplaceAll(out, "\\", "/")
	if strings.Contains(hay, rel) {
		return true
	}
	return strings.Contains(hay, filepath.Base(rel))
}

// runJSTypecheckGateAudited runs the gate and turns its outcome into audit rows.
//
// A probe failure is fatal, for the same reason the runner gate is: the bootstrap has just proven
// that the stack it installed cannot host a compiling test, and the fix loop is not allowed to edit
// package.json, tsconfig or runner config — so nothing downstream can repair it. Stopping here costs
// one bootstrap; continuing costs a full generation pass and every fix round it feeds, which on
// 2026-09-01 was 20 gaps and 80 minutes before the run was killed by hand.
//
// The trigger is deliberately narrow: only a failure whose output NAMES the probe. A repository
// whose own sources do not compile is reported, not blamed on the stack, and the run continues to
// the evaluator's baseline compile — which exists to say that about the repository.
func runJSTypecheckGateAudited(ctx context.Context, runner jsGoalRunner, audit Auditor, pkgDir string, prof jsTestProfile) (note string, err error) {
	res, cmdLine, probeRel, out := runJSTypecheckGate(ctx, runner, pkgDir, prof)
	switch res {
	case jsTypecheckOK:
		logAudit(audit, ctx, "test_bootstrap.typecheck_ok", map[string]interface{}{
			"message": fmt.Sprintf("Type-check gate passed: %s compiles a generated test in this package (%s).", cmdLine, probeRel),
			"command": cmdLine,
			"probe":   probeRel,
			"stack":   prof.Stack,
		})
		return "", nil

	case jsTypecheckProbeFailed:
		logAuditError(audit, ctx, "test_bootstrap.typecheck_failed", map[string]interface{}{
			"message": fmt.Sprintf("The %s stack runs a test but does not compile one: %s rejected %s. "+
				"Generated tests land beside sources and would fail the evaluator's compile step the same way, "+
				"and the fix loop may not edit tsconfig or runner config — stopping now.", prof.Stack, cmdLine, probeRel),
			"step":    "typecheck",
			"command": cmdLine,
			"probe":   probeRel,
			"stack":   prof.Stack,
			"output":  truncate(string(out), 8000),
		})
		return "", fmt.Errorf("test_framework_bootstrap typecheck %s: %s rejected %s\n%s",
			prof.Stack, cmdLine, probeRel, truncate(string(out), 4000))

	case jsTypecheckInconclusive:
		// Reported, not blamed: the command failed without naming the probe, so this is the
		// repository's own build. Said out loud because a silent skip here reads identically to a
		// gate that passed.
		logAudit(audit, ctx, "test_bootstrap.typecheck_inconclusive", map[string]interface{}{
			"message": fmt.Sprintf("Type-check gate inconclusive: %s failed without naming %s, so the repository's own sources are what did not compile. "+
				"The stack is unproven either way; the baseline compile reports the repository.", cmdLine, probeRel),
			"command": cmdLine,
			"probe":   probeRel,
			"stack":   prof.Stack,
			"output":  truncate(string(out), 4000),
		})
		return " (type-check inconclusive; the package's own build already fails)", nil
	}

	logAudit(audit, ctx, "test_bootstrap.typecheck_skipped", map[string]interface{}{
		"message": fmt.Sprintf("Type-check gate skipped for %s: no TypeScript profile, no type-check/build script, or no conventional source root (%s) to stage a probe in.",
			prof.Stack, strings.Join(jsProbeDirs, ", ")),
		"stack":      prof.Stack,
		"typescript": prof.IsTS,
	})
	return "", nil
}
