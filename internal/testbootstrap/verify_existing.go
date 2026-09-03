package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asqs/asqs-core/internal/layout"
)

// existingStackVerification is the outcome of proving an already-complete stack actually runs tests.
type existingStackVerification struct {
	// Attempted is false when there was nothing this bootstrap knows how to drive.
	Attempted bool
	OK        bool
	Reason    string
	Detail    string
}

// verifyExistingStack proves that a repository whose test stack detection judged COMPLETE can
// actually compile and execute a test.
//
// "Every required dependency is declared" and "a test runs" are different claims, and detection can
// only make the first: it reads manifests. A Gradle build with JUnit 5 and no useJUnitPlatform()
// executes zero tests and reports success; a Jest config can parse and still fail to transform the
// repo's own syntax; a .NET solution can restore and have no matching shared runtime. Generation
// downstream then produces artifacts that nothing can run — the exact failure this whole step exists
// to make impossible.
//
// Two rules distinguish this from the bootstrap path:
//
//   - Nothing is configured. The repository already has a runner and a config, and they are the ones
//     under test; writing ours would verify a stack that is not the one generation will use.
//   - The smoke test is REMOVED afterwards, pass or fail. Bootstrap changed nothing else here, so
//     leaving a file behind would put a stray artifact in a repository it had no reason to touch.
func verifyExistingStack(ctx context.Context, audit Auditor, repo, lang, runnerTimeout string, ed *EphemeralDocker) existingStackVerification {
	switch {
	case lang == "java":
		return verifyExistingJavaStack(ctx, audit, repo, runnerTimeout, ed)
	case isCSharpLang(lang):
		return verifyExistingCSharpStack(ctx, audit, repo, runnerTimeout, ed)
	case isJSLang(lang):
		return verifyExistingJSStack(ctx, audit, repo, lang, runnerTimeout, ed)
	}
	return existingStackVerification{Reason: "verification not implemented for language " + lang}
}

func verifyExistingJavaStack(ctx context.Context, audit Auditor, repo, runnerTimeout string, ed *EphemeralDocker) existingStackVerification {
	prof, jbf, err := resolveJavaTestProfile(repo)
	if err != nil || jbf.Abs == "" || prof.Declined {
		return existingStackVerification{Reason: "no usable Java build file"}
	}
	moduleRoot := filepath.Dir(jbf.Abs)
	smoke, err := writeJavaUnitSmokeTest(moduleRoot)
	if err != nil {
		return existingStackVerification{Reason: fmt.Sprintf("could not stage a smoke test: %v", err)}
	}
	defer removeJavaSmokeFile(smoke)

	runner := javaGoalRunner{repo: repo, build: jbf, ed: ed, timeout: installTimeout(runnerTimeout)}
	logAudit(audit, ctx, "test_bootstrap.verify_existing_start", map[string]interface{}{
		"message": "Existing Java stack detected as complete; proving it can compile and run a test.",
		"command": runner.describe(runner.singleTestGoals(smoke.FQCN)),
		"smoke":   smoke.FQCN,
	})
	cmdLine, out, err := runner.run(ctx, runner.singleTestGoals(smoke.FQCN)...)
	if err != nil {
		return existingStackVerification{Attempted: true, Reason: cmdLine, Detail: truncate(string(out), 8000)}
	}
	return existingStackVerification{Attempted: true, OK: true, Reason: cmdLine}
}

func verifyExistingCSharpStack(ctx context.Context, audit Auditor, repo, runnerTimeout string, ed *EphemeralDocker) existingStackVerification {
	testDirRel := layout.DetectCSharpUnitTestProjectDir(repo)
	if testDirRel == "" {
		return existingStackVerification{Reason: "no unit test project to run the smoke test in"}
	}
	csproj := firstCsprojInDir(filepath.Join(repo, filepath.FromSlash(testDirRel)))
	if csproj == "" {
		return existingStackVerification{Reason: "no .csproj under " + testDirRel}
	}
	prof, err := resolveCSharpTestProfile(repo, "")
	if err != nil || prof.Declined {
		return existingStackVerification{Reason: "no usable C# profile"}
	}
	smoke, err := writeCSharpUnitSmokeTest(filepath.Dir(csproj), prof.TestFramework)
	if err != nil {
		return existingStackVerification{Reason: fmt.Sprintf("could not stage a smoke test: %v", err)}
	}
	defer removeCSharpSmokeFile(smoke)

	runner := dotnetGoalRunner{repo: repo, csprojAbs: csproj, ed: ed, timeout: installTimeout(runnerTimeout)}
	logAudit(audit, ctx, "test_bootstrap.verify_existing_start", map[string]interface{}{
		"message": "Existing C# stack detected as complete; proving it can build and run a test.",
		"command": "dotnet test --filter FullyQualifiedName~" + smoke.FullyQualifiedName,
		"smoke":   smoke.FullyQualifiedName,
	})
	cmdLine, out, err := runner.runTestClass(ctx, smoke.FullyQualifiedName)
	if err != nil {
		v := existingStackVerification{Attempted: true, Reason: cmdLine, Detail: truncate(string(out), 8000)}
		if rem := dotnetRuntimeRemediation(string(out), prof.TargetFramework); rem != "" {
			v.Detail = rem
		}
		return v
	}
	return existingStackVerification{Attempted: true, OK: true, Reason: cmdLine}
}

// verifyExistingJSStack drives the runner the REPOSITORY established, not the one this bootstrap
// would have chosen.
//
// A Vite repo that already uses Jest must be verified with Jest: running our preferred Vitest would
// test a stack generation is not going to use. Repos on Karma, Jasmine, Mocha or AVA are not driven
// at all — bootstrap has no smoke test in those dialects, and a Jest-shaped file would fail for
// reasons that say nothing about the repository.
func verifyExistingJSStack(ctx context.Context, audit Auditor, repo, lang, runnerTimeout string, ed *EphemeralDocker) existingStackVerification {
	// "" for the runtime: this path installs only the repository's OWN dependencies and uses the
	// profile for the smoke test shape, so no pin from it is ever written to package.json.
	prof, pkgDir, err := resolveJSTestProfile(repo, lang, "")
	if err != nil || prof.Declined {
		return existingStackVerification{Reason: "no usable JS package"}
	}
	established, ok := establishedJSRunner(pkgDir)
	if !ok {
		return existingStackVerification{Reason: "the repository's runner is not one bootstrap can drive (Karma, Jasmine, Mocha and AVA are left alone)"}
	}
	prof.Runner = established

	// npx would otherwise fetch a runner from the registry and execute it without the repository's
	// own dependencies, which proves nothing about this repo.
	if !dirExists(filepath.Join(pkgDir, "node_modules")) {
		npmWorkdir := npmInstallWorkdir(repo, pkgDir)
		pm := detectPackageManager(npmWorkdir)
		hasLock := hasLockfile(npmWorkdir, pm)
		logAudit(audit, ctx, "test_bootstrap.verify_existing_install", map[string]interface{}{
			"message": fmt.Sprintf("Installing dependencies before verifying the existing stack (%s).", installCmdLine(pm, false, hasLock, false, "")),
			"pm":      string(pm),
		})
		instCtx, cancel := context.WithTimeout(ctx, installTimeout(runnerTimeout))
		out, ierr := RunPackageManagerInstall(instCtx, ed, npmWorkdir, pm, false, hasLock, false, []string{"CI=true", "NPM_CONFIG_YES=true"})
		cancel()
		if ierr != nil {
			return existingStackVerification{Attempted: true, Reason: "dependency install failed", Detail: truncate(string(out), 8000)}
		}
		auditInstallRetried(audit, ctx, "test_bootstrap.verify_existing_install_retried_newer_npm", installCmdLine(pm, false, hasLock, false, ""), out)
	}

	smoke, err := writeJSUnitSmokeTest(pkgDir, prof)
	if err != nil {
		return existingStackVerification{Reason: fmt.Sprintf("could not stage a smoke test: %v", err)}
	}
	defer removeJSSmokeFile(smoke)

	runner := jsGoalRunner{repo: repo, workdir: pkgDir, profile: prof, ed: ed, timeout: installTimeout(runnerTimeout)}
	logAudit(audit, ctx, "test_bootstrap.verify_existing_start", map[string]interface{}{
		"message": fmt.Sprintf("Existing %s stack detected as complete; proving it can run a test.", established),
		"command": runner.describe(runner.testArgv(smoke.Rel)),
		"runner":  string(established),
		"smoke":   smoke.Rel,
	})
	cmdLine, out, err := runner.runTestFile(ctx, smoke.Rel)
	if err != nil {
		return existingStackVerification{Attempted: true, Reason: cmdLine, Detail: truncate(string(out), 8000)}
	}
	return existingStackVerification{Attempted: true, OK: true, Reason: cmdLine}
}

// establishedJSRunner reports the runner already in use, when bootstrap can drive it.
func establishedJSRunner(pkgDir string) (JSRunner, bool) {
	pkg, err := readJSPackageJSON(pkgDir)
	if err != nil {
		return "", false
	}
	for _, name := range []string{"vitest.config.js", "vitest.config.ts", "vitest.config.mjs"} {
		if fileExists(filepath.Join(pkgDir, name)) {
			return JSRunnerVitest, true
		}
	}
	for _, name := range []string{"jest.config.js", "jest.config.ts", "jest.config.mjs", "jest.config.cjs"} {
		if fileExists(filepath.Join(pkgDir, name)) {
			return JSRunnerJest, true
		}
	}
	if _, ok := pkg.dep("vitest"); ok {
		return JSRunnerVitest, true
	}
	if _, ok := pkg.dep("jest"); ok {
		return JSRunnerJest, true
	}
	return "", false
}

// auditExistingStackVerification records the outcome and returns an error when the run must stop.
func auditExistingStackVerification(ctx context.Context, audit Auditor, v existingStackVerification, rep Report) error {
	if !v.Attempted {
		logAudit(audit, ctx, "test_bootstrap.verify_existing_skipped", map[string]interface{}{
			"message": fmt.Sprintf("Existing stack not verified: %s. Detection's reading of the manifests is all that backs it.", v.Reason),
			"reason":  v.Reason,
		})
		return nil
	}
	if v.OK {
		logAudit(audit, ctx, "test_bootstrap.verify_existing_ok", map[string]interface{}{
			"message": "The repository's own test stack compiled and ran a test; generated tests can run here.",
			"command": v.Reason,
		})
		return nil
	}
	logAuditError(audit, ctx, "test_bootstrap.verify_existing_failed", map[string]interface{}{
		"message": "The repository declares a complete test stack but cannot run a trivial test with it. " +
			"Generated tests would fail the same way, and the repair loop cannot fix a runner or a build manifest. " +
			"Fix the repository's test setup before re-running.",
		"detected": rep.Reason,
		"command":  v.Reason,
		"output":   v.Detail,
	})
	fmt.Fprintln(os.Stderr, "  test_framework_bootstrap: the existing test stack does not run a test — stopping.")
	return fmt.Errorf("test_framework_bootstrap: the existing test stack cannot run a test (%s)\n%s", v.Reason, truncate(v.Detail, 4000))
}
