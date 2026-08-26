// Package runner: local execution (mvn/gradle) for compile, test, coverage.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/asqs/asqs-core/internal/evaluator"
)

const defaultLocalTimeout = 5 * time.Minute

// jsPackageMeta holds package.json scripts, package manager, and framework detection for JS/TS projects.
type jsPackageMeta struct {
	Scripts                 map[string]string
	PackageManager          string // "npm", "yarn", "pnpm"
	HasBuild                bool
	HasTest                 bool
	HasCoverage             bool
	IsNest                  bool // @nestjs/core or @nestjs/common in dependencies
	BuildRunsStartOrInstall bool // true when "build" script runs start or npm install (e.g. angular-seed prestart); compile is treated as no-op so eval doesn't fail in sandbox
}

// buildScriptRunsStartOrInstall returns true if the build script would run "start" or "npm install" (e.g. angular-seed: "build" -> "npm run start" triggers prestart).
// In that case running "compile" in QualityBot context often fails (prestart runs npm install). We treat compile as no-op so evaluation can continue.
func buildScriptRunsStartOrInstall(buildScript string) bool {
	s := strings.TrimSpace(strings.ToLower(buildScript))
	if s == "" {
		return false
	}
	if s == "npm run start" || s == "npm start" || s == "start" || s == "yarn start" || s == "yarn run start" || s == "pnpm start" || s == "pnpm run start" {
		return true
	}
	if strings.Contains(s, "npm install") || strings.Contains(s, "prestart") {
		return true
	}
	return false
}

func readJSPackageMeta(repoPath string) (m jsPackageMeta) {
	m.Scripts = make(map[string]string)
	m.PackageManager = "npm"
	dir := filepath.Clean(repoPath)
	if pathExists(filepath.Join(dir, "yarn.lock")) {
		m.PackageManager = "yarn"
	} else if pathExists(filepath.Join(dir, "pnpm-lock.yaml")) {
		m.PackageManager = "pnpm"
	}
	path := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	var pkg struct {
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return m
	}
	if pkg.Scripts != nil {
		m.Scripts = pkg.Scripts
	}
	m.HasBuild = m.Scripts["build"] != ""
	m.HasTest = m.Scripts["test"] != ""
	m.HasCoverage = m.Scripts["coverage"] != ""
	m.BuildRunsStartOrInstall = buildScriptRunsStartOrInstall(m.Scripts["build"])
	deps := pkg.Dependencies
	if deps == nil {
		deps = make(map[string]string)
	}
	if pkg.DevDependencies != nil {
		for k := range pkg.DevDependencies {
			deps[k] = ""
		}
	}
	if _, ok := deps["@nestjs/core"]; ok {
		m.IsNest = true
	}
	if _, ok := deps["@nestjs/common"]; ok {
		m.IsNest = true
	}
	return m
}

func shutdownDotnetBuildServers(dir string) {
	cmd := exec.Command("dotnet", "build-server", "shutdown")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	_ = cmd.Run()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runCommand(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (output string, err error) {
	if cmd.Dir == "" {
		cmd.Dir = "."
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if ctx != nil && timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Use a process group on Unix so we can kill the whole tree (e.g. npm + jest). Otherwise
	// when the context times out we only kill the parent and the child can keep the pipe open,
	// causing Wait() to hang.
	run := exec.Command(cmd.Args[0], cmd.Args[1:]...)
	run.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		run.Env = cmd.Env
	} else {
		run.Env = os.Environ()
	}
	if runCtx != nil && runtime.GOOS != "windows" {
		run.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	var out strings.Builder
	run.Stdout = &out
	run.Stderr = &out

	if runCtx == nil {
		return out.String(), run.Run()
	}

	if err := run.Start(); err != nil {
		return "", err
	}
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = run.Wait()
		close(done)
	}()
	select {
	case <-runCtx.Done():
		killProcessGroup(run.Process)
		<-done
		return out.String(), runCtx.Err()
	case <-done:
		return out.String(), runErr
	}
}

// killProcessGroup kills the process and its children so pipes close and Wait() returns.
// On Unix we kill the process group (we set Setpgid: true so pgid == pid); on Windows we only kill the process.
func killProcessGroup(proc *os.Process) {
	if proc == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = proc.Kill()
		return
	}
	_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// formatCommandNeedsShell returns true if the command contains shell operators (e.g. "&&", "|") and must be run via a shell.
func formatCommandNeedsShell(s string) bool {
	return strings.Contains(s, "&&") || strings.Contains(s, "||") ||
		strings.Contains(s, "|") || strings.Contains(s, ";") ||
		strings.Contains(s, "\n")
}

// formatEnv returns the environment for a command run in repoPath. When repoPath/node_modules/.bin exists,
// it is prepended to PATH so local tools (e.g. prettier, eslint) are found without needing "npx" or a full path.
func formatEnv(repoPath string) []string {
	env := os.Environ()
	nmBin := filepath.Join(filepath.Clean(repoPath), "node_modules", ".bin")
	if _, err := os.Stat(nmBin); err != nil {
		return env
	}
	nmBinAbs, err := filepath.Abs(nmBin)
	if err != nil {
		return env
	}
	for i, s := range env {
		if strings.HasPrefix(s, "PATH=") {
			env[i] = "PATH=" + nmBinAbs + string(filepath.ListSeparator) + strings.TrimPrefix(s, "PATH=")
			return env
		}
	}
	return append(env, "PATH="+nmBinAbs+string(filepath.ListSeparator)+os.Getenv("PATH"))
}

// RunFormatCommand runs a format command in the repo root (e.g. "mvn spring-javaformat:apply -q" or "prettier --write .").
// Used after writing generated test files so that formatting checks pass.
// If the command contains shell operators (&&, |, ;, etc.), it is run via sh -c "..."; otherwise it is split on spaces and exec'd directly.
// When the repo has node_modules/.bin, that directory is prepended to PATH so local tools (prettier, eslint) are found.
// Returns nil when command succeeds.
func RunFormatCommand(ctx context.Context, repoPath, formatCommand string, timeout time.Duration) error {
	formatCommand = strings.TrimSpace(formatCommand)
	if formatCommand == "" {
		return nil
	}
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	dir := filepath.Clean(repoPath)
	var cmd *exec.Cmd
	if formatCommandNeedsShell(formatCommand) {
		cmd = exec.CommandContext(runCtx, "sh", "-c", formatCommand)
	} else {
		parts := strings.Fields(formatCommand)
		if len(parts) == 0 {
			return nil
		}
		if strings.EqualFold(parts[0], "dotnet") && !dotnetOnPATH() {
			fmt.Fprintf(os.Stderr, "  warning: %v (%q)\n", ErrFormatSkippedNoDotnet, formatCommand)
			return ErrFormatSkippedNoDotnet
		}
		cmd = exec.CommandContext(runCtx, parts[0], parts[1:]...)
	}
	cmd.Dir = dir
	cmd.Env = formatEnv(dir)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("format command %q: %w\n%s", formatCommand, err, out.String())
	}
	return nil
}

// RunFormatCommandFiles runs the format command once per file, with the repo-relative path appended as the last argument.
// Use when format_only_added is true so only written files are formatted (e.g. "google-java-format -i" → for each file: "google-java-format -i path/to/File.java").
// formatCommand is split on spaces. Only paths with the given extensions are included (e.g. []string{".java"}); pass nil to include all.
func RunFormatCommandFiles(ctx context.Context, repoPath, formatCommand string, files []string, extensions []string, timeout time.Duration) error {
	formatCommand = strings.TrimSpace(formatCommand)
	if formatCommand == "" || len(files) == 0 {
		return nil
	}
	parts := strings.Fields(formatCommand)
	if len(parts) == 0 {
		return nil
	}
	filtered := files
	if len(extensions) > 0 {
		filtered = make([]string, 0, len(files))
		for _, f := range files {
			lf := strings.ToLower(f)
			for _, ext := range extensions {
				if strings.HasSuffix(lf, strings.ToLower(ext)) {
					filtered = append(filtered, f)
					break
				}
			}
		}
	}
	dir := filepath.Clean(repoPath)
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	for _, f := range filtered {
		f = strings.TrimSpace(filepath.FromSlash(f))
		if f == "" {
			continue
		}
		args := append(append([]string{}, parts[1:]...), f)
		cmd := exec.CommandContext(runCtx, parts[0], args...)
		cmd.Dir = dir
		cmd.Env = formatEnv(dir)
		var out strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("format command %q [file %s]: %w\n%s", formatCommand, f, err, out.String())
		}
	}
	return nil
}

// localBuildCommand returns the command for a local Java build step goal (compile, test,
// coverage). compileCommand/testCommand override when set; otherwise buildTool
// (auto|mvn|mvnw|gradle|gradlew) selects the executable and the argv comes goal-for-goal from the
// same shapes the Docker toolchain profiles carry — same flags, same goals, flags before goals —
// so the two targets produce the same string (CP31). The wrapper axis (mvnw/gradlew) is CP32's to
// remove.
func localBuildCommand(repoPath, goal, buildTool, compileCommand, testCommand string) (*exec.Cmd, error) {
	dir := filepath.Clean(repoPath)
	if goal == "compile" && strings.TrimSpace(compileCommand) != "" {
		return localBuildCmd(dir, []string{"sh", "-c", strings.TrimSpace(compileCommand)})
	}
	if (goal == "test" || goal == "default" || goal == "coverage") && strings.TrimSpace(testCommand) != "" {
		return localBuildCmd(dir, []string{"sh", "-c", strings.TrimSpace(testCommand)})
	}
	tool := strings.TrimSpace(strings.ToLower(buildTool))
	if tool == "" {
		tool = "auto"
	}
	hasPom := pathExists(filepath.Join(dir, "pom.xml"))
	hasMvnw := pathExists(filepath.Join(dir, "mvnw")) || pathExists(filepath.Join(dir, "mvnw.cmd"))
	hasGradle := pathExists(filepath.Join(dir, "build.gradle")) || pathExists(filepath.Join(dir, "build.gradle.kts"))
	hasGradlew := pathExists(filepath.Join(dir, "gradlew")) || pathExists(filepath.Join(dir, "gradlew.bat"))
	if tool == "auto" {
		switch {
		case hasPom && hasMvnw:
			tool = "mvnw"
		case hasPom:
			tool = "mvn"
		case hasGradle && hasGradlew:
			tool = "gradlew"
		case hasGradle:
			tool = "gradle"
		default:
			return nil, fmt.Errorf("no pom.xml or build.gradle in %s", dir)
		}
	}
	switch tool {
	case "mvn", "mvnw":
		if !hasPom {
			return nil, fmt.Errorf("build_tool is %s but no pom.xml in %s", tool, dir)
		}
		name := "mvn"
		if tool == "mvnw" {
			if !hasMvnw {
				return nil, fmt.Errorf("build_tool is mvnw but mvnw not found in %s", dir)
			}
			if runtime.GOOS == "windows" && pathExists(filepath.Join(dir, "mvnw.cmd")) {
				name = "mvnw.cmd"
			} else {
				name = "./mvnw"
			}
		}
		// -DskipTests on test-compile is inert (that phase runs no tests) and is carried only so
		// the two targets produce the same string; test-compile (not compile) so generated TEST
		// sources compile here too instead of first failing in the test phase.
		args := []string{"-q", "-B", "-DskipTests", "test-compile"}
		switch goal {
		case "test", "default":
			args = []string{"-q", "-B", "test"}
		case "coverage":
			// Without jacoco:report the step is a byte-identical re-run of the test step that
			// produces no report; with it on a pom that never declares the plugin, Maven fails the
			// whole step with "No plugin found for prefix 'jacoco'". Hence the gate.
			if !javaBuildFileDeclaresJaCoCo(dir) {
				return nil, errLocalCoverageUnavailable
			}
			args = []string{"-q", "-B", "test", "jacoco:report"}
		}
		return localBuildCmd(dir, append([]string{name}, args...))
	case "gradle", "gradlew":
		if !hasGradle {
			return nil, fmt.Errorf("build_tool is %s but no build.gradle in %s", tool, dir)
		}
		name := "gradle"
		if tool == "gradlew" {
			if !hasGradlew {
				return nil, fmt.Errorf("build_tool is gradlew but gradlew not found in %s", dir)
			}
			if runtime.GOOS == "windows" && pathExists(filepath.Join(dir, "gradlew.bat")) {
				name = "gradlew.bat"
			} else {
				name = "./gradlew"
			}
		}
		args := []string{"--no-daemon", "-q"}
		switch goal {
		case "compile":
			// compileTestJava (depends on compileJava) so generated TEST sources compile here too.
			args = append(args, "compileTestJava")
		case "coverage":
			// Same gate and rationale as the Maven branch: an undeclared jacoco plugin means
			// Gradle has no jacocoTestReport task to run.
			if !javaBuildFileDeclaresJaCoCo(dir) {
				return nil, errLocalCoverageUnavailable
			}
			args = append(args, "test", "jacocoTestReport")
		default:
			args = append(args, "test")
		}
		return localBuildCmd(dir, append([]string{name}, args...))
	}
	return nil, fmt.Errorf("unsupported build_tool %q (want auto|mvn|mvnw|gradle|gradlew)", tool)
}

// runLocalPlannedStep executes one evaluation step on the host from the StepPlan. Since CP31 every
// local ecosystem runs what the plan records — the plan is the single source of argv, restore and
// skip/fail decisions, which is what makes the parity harness's claims about the local target true
// rather than aspirational. Step summaries keep core's existing wording; CP34 unifies them with
// the Docker target's.
func (s *Sandbox) runLocalPlannedStep(ctx context.Context, gitRootAbs, cwd, lang string, step evaluator.SandboxStep, label string) evaluator.StepResult {
	plan, err := s.buildStepPlan(gitRootAbs, lang, "")
	if err != nil {
		return evaluator.StepResult{Step: step, OK: false, Summary: err.Error()}
	}
	// Restore before the step, at most once per manifest fingerprint (see restore.go). Best-effort
	// by contract, matching the Docker path.
	s.runLocalRestoreOnce(ctx, plan, cwd)

	switch dec := plan.DecisionFor(step); dec.Action {
	case ActionSkip:
		fmt.Fprintf(os.Stderr, "  %s: %s\n", label, dec.Reason)
		return evaluator.StepResult{Step: step, OK: true, Summary: dec.Reason}
	case ActionFail:
		fmt.Fprintf(os.Stderr, "  %s: %s\n", label, dec.Reason)
		return evaluator.StepResult{Step: step, OK: false, Summary: dec.Reason}
	}
	argv := plan.ArgvFor(step)
	if len(argv) == 0 {
		return evaluator.StepResult{Step: step, OK: true, Summary: "skip (no command)"}
	}
	if err := requireLocalToolchain(argv[0]); err != nil {
		fmt.Fprintf(os.Stderr, "  %s: %v\n", label, err)
		return evaluator.StepResult{Step: step, OK: false, Summary: err.Error()}
	}
	if step == evaluator.StepCompile && isCSharpLang(lang) {
		// Drop lingering MSBuild/VBCSCompiler nodes so a prior timed-out test/build cannot keep
		// bin/obj locked. Compile-only: Docker also shuts them down AFTER test, but that reaches
		// every node on the machine and must not be inherited by a host (§1 row 5).
		shutdownDotnetBuildServers(cwd)
	}
	fmt.Fprintf(os.Stderr, "  %s...\n", label)
	cmd := newLocalBuildCmd(cwd, argv)
	if env := plan.EnvFor(step); len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, runErr := runCommand(ctx, cmd, s.timeoutDuration())
	if runErr != nil {
		if step == evaluator.StepTest && isJSLang(lang) && jsTestOutputSummaryShowsZeroFailures(out) {
			fmt.Fprintf(os.Stderr, "  %s: non-zero exit but the runner's own summary shows zero failures (treating as ok; often Jest open handles). %s\n", label, firstLines(out, 2))
			return evaluator.StepResult{Step: step, OK: true, Summary: "tests ok (summary all passed; exit code ignored)", Output: out}
		}
		lines := 5
		fallback := "tests failed"
		if step == evaluator.StepCompile {
			lines, fallback = 3, "compile failed"
		}
		summary := fallback
		if out != "" {
			summary = firstLines(out, lines)
		}
		fmt.Fprintf(os.Stderr, "  %s: failed. %s\n", label, firstLines(summary, 2))
		return evaluator.StepResult{Step: step, OK: false, Summary: summary, Output: out}
	}
	var summary string
	switch step {
	case evaluator.StepCompile:
		summary = "compile ok"
	case evaluator.StepCoverage:
		summary = coverageSummaryFromPlan(cwd, plan)
	default:
		summary = "tests ok"
	}
	fmt.Fprintf(os.Stderr, "  %s: %s\n", label, summary)
	return evaluator.StepResult{Step: step, OK: true, Summary: summary, Output: out}
}
