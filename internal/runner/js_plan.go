package runner

import (
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// JS/TS command construction, shared by both sandbox targets (CP31).
//
// Named js_plan.go, not plan_js.go: Go reads a trailing `_js` as the js/wasm GOOS build
// constraint, so the latter compiles only for GOOS=js and is invisible everywhere else.
//
// The two paths used to disagree about almost everything here, and the disagreements were
// semantic, not cosmetic:
//
//   - Docker ran `npm run build` unconditionally and FAILED on repositories the local path
//     handled: a NestJS project with no build script, or an angular-seed whose "build" script
//     runs `start` (and therefore `prestart`, and therefore `npm install`).
//   - Local had no `corepack enable` for pnpm/yarn and no node_modules/.bin on PATH, so a script
//     calling `tsc` found nothing.
//   - "Nothing to run" produced three different answers. A missing test script FAILED locally,
//     failed under npm/yarn, and PASSED under pnpm's `--if-present`.
//   - `--if-present` and `||` between them made the Docker coverage step incapable of reporting a
//     problem: npm exits 0 when the script is absent so the `||` never fired and nothing ran at
//     all, and when a coverage script did run and fail, the `||` swallowed it and re-ran the unit
//     suite, reporting success.
//
// The policy settled here: nothing to run is a SKIP with a named reason — never a failure, never a
// silent pass. `--if-present` and the `||` fallbacks are gone together; they were one mechanism.

// jsToolchainFor maps a detected package manager onto its toolchain profile.
func jsToolchainFor(packageManager string) profile.ToolchainID {
	switch packageManager {
	case "yarn":
		return profile.TypeScriptYarn
	case "pnpm":
		return profile.TypeScriptPNPM
	default:
		return profile.TypeScriptNPM
	}
}

// jsCoverageScriptNames is probed in order. Every package manager probes BOTH: the Docker pnpm
// profile looked for `test:coverage` while npm, yarn and the local path all looked for `coverage`,
// so the same repository resolved a different script depending on which lockfile it shipped.
// Checking both also closes the inverse gap, where an npm repo with only `test:coverage` got no
// coverage on either target.
var jsCoverageScriptNames = []string{"coverage", "test:coverage"}

// planJS plans the three JS/TS steps identically for both targets.
func (s *Sandbox) planJS(plan *StepPlan, absCwd string) {
	meta := readJSPackageMeta(absCwd)
	plan.Toolchain = jsToolchainFor(meta.PackageManager)
	// runner.eval_profile can pin the package manager (typescript-yarn, …). Resolved through
	// profile.ResolveToolchain so the alias list lives in one place; the image it also returns is
	// irrelevant here, which is what keeps this usable on the local target (§1 row 1).
	if pinned := jsToolchainFromEvalProfile(absCwd, plan.Lang, s.EvalProfile); pinned != profile.UnsupportedDocker {
		plan.Toolchain = pinned
		meta.PackageManager = jsPackageManagerFor(pinned)
	}
	plan.Restore = restoreArgvFor(plan.Toolchain)
	plan.RestoreKey = restoreKeyFor(absCwd, plan.Toolchain, absCwd)
	plan.RestoreDecision = runStep()
	plan.CoverageReportPaths = coverageReportPathsFor(plan.Toolchain)

	hasPackageJSON := pathExists(filepath.Join(absCwd, "package.json"))
	for _, step := range planSteps {
		// From the shared source (CP33). CI=true lives in the environment rather than inline in
		// the argv, which is why the argv no longer carries the `CI=true ` prefix the Docker
		// profiles used.
		env := stepEnv(plan.Toolchain, plan.Target, s.DockerEvalExtraEnv)
		if plan.Target == TargetLocal {
			env = append(env, s.localCredentialEnv(plan.Toolchain)...)
		}
		plan.Env[step] = env

		if !hasPackageJSON {
			plan.Decisions[step] = skipStep("skip (no package.json)")
			continue
		}
		script, dec := s.jsStepScript(plan, meta, step)
		if dec.Action != ActionRun {
			plan.Decisions[step] = dec
			continue
		}
		plan.setArgv(step, []string{"sh", "-c", wrapJSShell(plan.Toolchain, step, script)})
		plan.Decisions[step] = runStep()
	}
}

// jsStepScript returns the bare package-manager invocation for a step, or the decision that no
// command should run.
func (s *Sandbox) jsStepScript(plan *StepPlan, meta jsPackageMeta, step evaluator.SandboxStep) (string, StepDecision) {
	override := s.CompileCommand
	if step != evaluator.StepCompile {
		override = s.TestCommand
	}
	if line := strings.TrimSpace(override); line != "" {
		// One heuristic still applies to an override: a configured `npm run build` is just as
		// unable to compile when the build script runs start/install.
		if step == evaluator.StepCompile && meta.BuildRunsStartOrInstall && isPlainBuildInvocation(line) {
			return "", skipStep(jsBuildRunsStartSkipReason)
		}
		return line, runStep()
	}
	pm := meta.PackageManager

	switch step {
	case evaluator.StepCompile:
		switch {
		case meta.HasBuild && meta.BuildRunsStartOrInstall:
			// angular-seed and friends: "build" runs start, which triggers prestart, which runs
			// npm install. Running it would not compile anything and usually fails in a sandbox.
			// A skip states that outright; the old no-op `node -e process.exit(0)` only pretended.
			return "", skipStep(jsBuildRunsStartSkipReason)
		case !meta.HasBuild && meta.IsNest:
			return "npx nest build", runStep()
		case !meta.HasBuild:
			return "", skipStep("skip (no build script)")
		default:
			return pm + " run build", runStep()
		}

	case evaluator.StepTest:
		switch {
		case !meta.HasTest && meta.IsNest:
			return "npx nest test", runStep()
		case !meta.HasTest:
			// A repository with no test script is a repo fact the fixer cannot repair.
			return "", skipStep("skip (no test script)")
		default:
			return pm + " test", runStep()
		}

	case evaluator.StepCoverage:
		if name := firstPresentScript(meta, jsCoverageScriptNames); name != "" {
			return pm + " run " + name, runStep()
		}
		// Same reasoning as the Java JaCoCo gate: with no coverage script the only thing left to
		// run is the unit suite, which the test step already ran and which produces no report.
		return "", skipStep("skip (no coverage script declared in package.json)")
	}
	return "", skipStep("skip")
}

const jsBuildRunsStartSkipReason = "skip (build script runs start/install; it would not compile)"

// isPlainBuildInvocation reports whether a configured compile command is just "<pm> run build".
func isPlainBuildInvocation(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "npm run build", "yarn run build", "pnpm run build":
		return true
	}
	return false
}

func firstPresentScript(meta jsPackageMeta, names []string) string {
	for _, n := range names {
		if strings.TrimSpace(meta.Scripts[n]) != "" {
			return n
		}
	}
	return ""
}

// wrapJSShell adds the prefixes the command needs to run at all.
//
// corepack enable: official Node images do not put pnpm or yarn on PATH until the Corepack shims
// exist, so a bare `pnpm test` fails with "pnpm: not found". node_modules/.bin on PATH: a build
// script that calls `tsc` needs the locally installed TypeScript, which slim images do not carry.
// Both were Docker-only and are now applied identically on the host.
func wrapJSShell(id profile.ToolchainID, step evaluator.SandboxStep, script string) string {
	script = strings.TrimSpace(script)
	if !strings.Contains(script, "node_modules/.bin") {
		script = `export PATH="${PWD}/node_modules/.bin:${PATH}" && ` + script
	}
	if id == profile.TypeScriptPNPM || id == profile.TypeScriptYarn {
		if !strings.Contains(script, "corepack enable") {
			script = "corepack enable && " + script
		}
	}
	return script
}

// jsToolchainFromEvalProfile returns the package manager runner.eval_profile pins, or
// UnsupportedDocker when it pins nothing relevant (auto, or a Java/C# profile).
func jsToolchainFromEvalProfile(absCwd, lang, evalProfile string) profile.ToolchainID {
	if strings.TrimSpace(evalProfile) == "" || strings.EqualFold(strings.TrimSpace(evalProfile), "auto") {
		return profile.UnsupportedDocker
	}
	p, err := profile.ResolveToolchain(absCwd, lang, evalProfile, "", "", "", "")
	if err != nil {
		return profile.UnsupportedDocker
	}
	switch p.ID {
	case profile.TypeScriptNPM, profile.TypeScriptPNPM, profile.TypeScriptYarn:
		return p.ID
	}
	return profile.UnsupportedDocker
}

func jsPackageManagerFor(id profile.ToolchainID) string {
	switch id {
	case profile.TypeScriptPNPM:
		return "pnpm"
	case profile.TypeScriptYarn:
		return "yarn"
	default:
		return "npm"
	}
}
