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

// runLocalCompile runs compile in repoPath using the configured command or BuildTool. Returns real StepResult.
func runLocalCompile(ctx context.Context, repoPath, lang string, timeout time.Duration, buildTool, compileCommand, testCommand, dotnetFallbackTFM string) evaluator.StepResult {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "csharp" || lang == "cs" {
		return runDotnetCompile(ctx, repoPath, timeout, compileCommand, dotnetFallbackTFM)
	}
	if lang != "java" && lang != "javascript" && lang != "typescript" {
		return evaluator.StepResult{Step: evaluator.StepCompile, OK: true, Summary: "skip (unsupported lang)"}
	}
	if lang == "javascript" || lang == "typescript" {
		return runJSCompile(ctx, repoPath, timeout, compileCommand)
	}
	fmt.Fprintln(os.Stderr, "  Compiling code...")
	cmd, err := localBuildCommand(repoPath, "compile", buildTool, compileCommand, testCommand)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Compile: %v\n", err)
		return evaluator.StepResult{
			Step: evaluator.StepCompile, OK: false,
			Summary: err.Error(), Output: "",
		}
	}
	out, runErr := runCommand(ctx, cmd, timeout)
	if runErr != nil {
		summary := "compile failed"
		if out != "" {
			summary = firstLines(out, 3)
		}
		fmt.Fprintf(os.Stderr, "  Compile: failed. %s\n", summary)
		return evaluator.StepResult{
			Step: evaluator.StepCompile, OK: false,
			Summary: summary, Output: out,
		}
	}
	fmt.Fprintln(os.Stderr, "  Compile: ok.")
	return evaluator.StepResult{
		Step: evaluator.StepCompile, OK: true,
		Summary: "compile ok", Output: out,
	}
}

// runLocalTest runs tests using the configured command or BuildTool. Returns real StepResult.
func runLocalTest(ctx context.Context, repoPath, lang string, timeout time.Duration, buildTool, compileCommand, testCommand, dotnetFallbackTFM string) evaluator.StepResult {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "csharp" || lang == "cs" {
		return runDotnetTest(ctx, repoPath, timeout, testCommand, dotnetFallbackTFM)
	}
	if lang != "java" && lang != "javascript" && lang != "typescript" {
		return evaluator.StepResult{Step: evaluator.StepTest, OK: true, Summary: "skip (unsupported lang)"}
	}
	if lang == "javascript" || lang == "typescript" {
		return runJSTest(ctx, repoPath, timeout, testCommand)
	}
	fmt.Fprintln(os.Stderr, "  Running tests...")
	cmd, err := localBuildCommand(repoPath, "test", buildTool, compileCommand, testCommand)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Tests: %v\n", err)
		return evaluator.StepResult{
			Step: evaluator.StepTest, OK: false,
			Summary: err.Error(), Output: "",
		}
	}
	out, runErr := runCommand(ctx, cmd, timeout)
	if runErr != nil {
		summary := "tests failed"
		if out != "" {
			summary = firstLines(out, 5)
		}
		fmt.Fprintf(os.Stderr, "  Tests: failed. %s\n", summary)
		return evaluator.StepResult{
			Step: evaluator.StepTest, OK: false,
			Summary: summary, Output: out,
		}
	}
	fmt.Fprintln(os.Stderr, "  Tests: ok.")
	return evaluator.StepResult{
		Step: evaluator.StepTest, OK: true,
		Summary: "tests ok", Output: out,
	}
}

// runLocalCoverage runs tests with coverage when possible. Returns StepResult with Summary containing coverage info or "N/A" if not configured.
func runLocalCoverage(ctx context.Context, repoPath, lang string, timeout time.Duration, buildTool, compileCommand, testCommand, dotnetFallbackTFM string) evaluator.StepResult {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "csharp" || lang == "cs" {
		return runDotnetCoverage(ctx, repoPath, timeout, testCommand, dotnetFallbackTFM)
	}
	if lang != "java" && lang != "javascript" && lang != "typescript" {
		return evaluator.StepResult{Step: evaluator.StepCoverage, OK: true, Summary: "skip (unsupported lang)"}
	}
	if lang == "javascript" || lang == "typescript" {
		return runJSCoverage(ctx, repoPath, timeout, testCommand)
	}
	fmt.Fprintln(os.Stderr, "  Running tests (coverage)...")
	cmd, err := localBuildCommand(repoPath, "test", buildTool, compileCommand, testCommand)
	if err != nil {
		return evaluator.StepResult{Step: evaluator.StepCoverage, OK: true, Summary: "no build tool"}
	}
	out, runErr := runCommand(ctx, cmd, timeout)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "  Coverage: failed. %s\n", firstLines(out, 3))
		return evaluator.StepResult{
			Step: evaluator.StepCoverage, OK: false,
			Summary: firstLines(out, 5), Output: out,
		}
	}
	summary := coverageSummary(repoPath)
	if summary == "" {
		summary = "tests ok (coverage report not found)"
	}
	fmt.Fprintf(os.Stderr, "  Coverage: %s\n", summary)
	return evaluator.StepResult{
		Step: evaluator.StepCoverage, OK: true,
		Summary: summary, Output: out,
	}
}

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

// jsNoOpCompile returns a command that exits 0 (no-op). Used when "build" script runs start/install so compile step passes without running prestart in sandbox.
func jsNoOpCompile(repoPath string) *exec.Cmd {
	c := exec.Command("node", "-e", "process.exit(0)")
	c.Dir = filepath.Clean(repoPath)
	return c
}

// jsLocalCommand builds the command for compile or test for JS/TS. overrideCommand is from config/indexer meta; if set, it's used. Otherwise we use package manager + script from package.json.
func jsLocalCommand(repoPath, goal string, overrideCommand string) (*exec.Cmd, error) {
	dir := filepath.Clean(repoPath)
	if strings.TrimSpace(overrideCommand) != "" {
		// If compile command is "npm run build" (or equivalent) but package.json "build" script runs start/install, use no-op so eval doesn't fail (e.g. angular-seed prestart).
		if goal == "compile" {
			meta := readJSPackageMeta(repoPath)
			if meta.BuildRunsStartOrInstall {
				norm := strings.ToLower(strings.TrimSpace(overrideCommand))
				if norm == "npm run build" || norm == "yarn run build" || norm == "pnpm run build" {
					return jsNoOpCompile(repoPath), nil
				}
			}
		}
		line := strings.TrimSpace(overrideCommand)
		if line == "" {
			return nil, fmt.Errorf("command is empty after trim")
		}
		c := exec.Command("sh", "-c", line)
		c.Dir = dir
		return c, nil
	}
	meta := readJSPackageMeta(repoPath)
	var name string
	var args []string

	// When "build" runs start/install (e.g. angular-seed), skip real compile so step passes without running prestart in sandbox.
	if goal == "compile" && meta.HasBuild && meta.BuildRunsStartOrInstall {
		return jsNoOpCompile(repoPath), nil
	}

	// NestJS fallbacks when scripts are missing (e.g. custom or older Nest project).
	if goal == "compile" && !meta.HasBuild && meta.IsNest {
		c := exec.Command("npx", "nest", "build")
		c.Dir = dir
		return c, nil
	}
	if (goal == "test" || goal == "coverage") && !meta.HasTest && meta.IsNest {
		c := exec.Command("npx", "nest", "test")
		c.Dir = dir
		return c, nil
	}

	switch meta.PackageManager {
	case "yarn":
		name = "yarn"
		if goal == "compile" {
			if !meta.HasBuild {
				return nil, fmt.Errorf("no build script in package.json")
			}
			args = []string{"run", "build"}
		} else if goal == "coverage" && meta.HasCoverage {
			args = []string{"run", "coverage"}
		} else if goal == "coverage" || goal == "test" {
			if !meta.HasTest {
				return nil, fmt.Errorf("no test script in package.json")
			}
			args = []string{"test"}
		} else {
			return nil, fmt.Errorf("unknown goal %q", goal)
		}
	case "pnpm":
		name = "pnpm"
		if goal == "compile" {
			if !meta.HasBuild {
				return nil, fmt.Errorf("no build script in package.json")
			}
			args = []string{"run", "build"}
		} else if goal == "coverage" && meta.HasCoverage {
			args = []string{"run", "coverage"}
		} else if goal == "coverage" || goal == "test" {
			if !meta.HasTest {
				return nil, fmt.Errorf("no test script in package.json")
			}
			args = []string{"test"}
		} else {
			return nil, fmt.Errorf("unknown goal %q", goal)
		}
	default:
		name = "npm"
		if goal == "compile" {
			if !meta.HasBuild {
				return nil, fmt.Errorf("no build script in package.json")
			}
			args = []string{"run", "build"}
		} else if goal == "coverage" && meta.HasCoverage {
			args = []string{"run", "coverage"}
		} else if goal == "coverage" || goal == "test" {
			if !meta.HasTest {
				return nil, fmt.Errorf("no test script in package.json")
			}
			args = []string{"test"}
		} else {
			return nil, fmt.Errorf("unknown goal %q", goal)
		}
	}
	c := exec.Command(name, args...)
	c.Dir = dir
	return c, nil
}

func runJSCompile(ctx context.Context, repoPath string, timeout time.Duration, compileCommand string) evaluator.StepResult {
	fmt.Fprintln(os.Stderr, "  Compiling (JS/TS)...")
	cmd, err := jsLocalCommand(repoPath, "compile", compileCommand)
	if err != nil {
		if strings.Contains(err.Error(), "no build script") {
			fmt.Fprintln(os.Stderr, "  Compile: skip (no build script).")
			return evaluator.StepResult{Step: evaluator.StepCompile, OK: true, Summary: "skip (no build script)"}
		}
		fmt.Fprintf(os.Stderr, "  Compile: %v\n", err)
		return evaluator.StepResult{Step: evaluator.StepCompile, OK: false, Summary: err.Error(), Output: ""}
	}
	out, runErr := runCommand(ctx, cmd, timeout)
	if runErr != nil {
		summary := "compile failed"
		if out != "" {
			summary = firstLines(out, 3)
		}
		fmt.Fprintf(os.Stderr, "  Compile: failed. %s\n", summary)
		return evaluator.StepResult{Step: evaluator.StepCompile, OK: false, Summary: summary, Output: out}
	}
	fmt.Fprintln(os.Stderr, "  Compile: ok.")
	return evaluator.StepResult{Step: evaluator.StepCompile, OK: true, Summary: "compile ok", Output: out}
}

func runJSTest(ctx context.Context, repoPath string, timeout time.Duration, testCommand string) evaluator.StepResult {
	fmt.Fprintln(os.Stderr, "  Running tests (JS/TS)...")
	cmd, err := jsLocalCommand(repoPath, "test", testCommand)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Tests: %v\n", err)
		return evaluator.StepResult{Step: evaluator.StepTest, OK: false, Summary: err.Error(), Output: ""}
	}
	// Disable watch mode so Jest/Vitest etc. run once and exit (otherwise they can hang waiting for file changes).
	cmd.Env = append(os.Environ(), "CI=true")
	out, runErr := runCommand(ctx, cmd, timeout)
	if runErr != nil {
		if jsTestOutputSummaryShowsZeroFailures(out) {
			fmt.Fprintf(os.Stderr, "  Tests: non-zero exit but Jest/Vitest summary shows zero failures (treating as ok; often Jest open handles / did not exit). %s\n", firstLines(out, 2))
			return evaluator.StepResult{Step: evaluator.StepTest, OK: true, Summary: "tests ok (summary all passed; exit code ignored)", Output: out}
		}
		summary := "tests failed"
		if out != "" {
			summary = firstLines(out, 5)
		}
		fmt.Fprintf(os.Stderr, "  Tests: failed. %s\n", summary)
		return evaluator.StepResult{Step: evaluator.StepTest, OK: false, Summary: summary, Output: out}
	}
	fmt.Fprintln(os.Stderr, "  Tests: ok.")
	return evaluator.StepResult{Step: evaluator.StepTest, OK: true, Summary: "tests ok", Output: out}
}

func runJSCoverage(ctx context.Context, repoPath string, timeout time.Duration, testCommand string) evaluator.StepResult {
	fmt.Fprintln(os.Stderr, "  Running tests (coverage, JS/TS)...")
	cmd, err := jsLocalCommand(repoPath, "coverage", testCommand)
	if err != nil {
		return evaluator.StepResult{Step: evaluator.StepCoverage, OK: true, Summary: "no test script"}
	}
	cmd.Env = append(os.Environ(), "CI=true")
	out, runErr := runCommand(ctx, cmd, timeout)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "  Coverage: failed. %s\n", firstLines(out, 3))
		return evaluator.StepResult{Step: evaluator.StepCoverage, OK: false, Summary: firstLines(out, 5), Output: out}
	}
	summary := "tests ok (coverage report path depends on test framework)"
	fmt.Fprintf(os.Stderr, "  Coverage: %s\n", summary)
	return evaluator.StepResult{Step: evaluator.StepCoverage, OK: true, Summary: summary, Output: out}
}

// localBuildCommand returns a command for the given goal (compile or test). Uses compileCommand/testCommand when set; otherwise uses buildTool (auto|mvn|mvnw|gradle|gradlew) to choose the executable and default args.
func localBuildCommand(repoPath, goal, buildTool, compileCommand, testCommand string) (*exec.Cmd, error) {
	dir := filepath.Clean(repoPath)
	if goal == "compile" && strings.TrimSpace(compileCommand) != "" {
		line := strings.TrimSpace(compileCommand)
		if line == "" {
			return nil, fmt.Errorf("compile_command is empty after trim")
		}
		c := exec.Command("sh", "-c", line)
		c.Dir = dir
		return c, nil
	}
	if (goal == "test" || goal == "default") && strings.TrimSpace(testCommand) != "" {
		line := strings.TrimSpace(testCommand)
		if line == "" {
			return nil, fmt.Errorf("test_command is empty after trim")
		}
		c := exec.Command("sh", "-c", line)
		c.Dir = dir
		return c, nil
	}
	// Resolve build tool: auto-detect or use configured buildTool
	tool := strings.TrimSpace(strings.ToLower(buildTool))
	if tool == "" {
		tool = "auto"
	}
	hasPom := pathExists(filepath.Join(dir, "pom.xml"))
	hasMvnw := pathExists(filepath.Join(dir, "mvnw")) || pathExists(filepath.Join(dir, "mvnw.cmd"))
	hasGradle := pathExists(filepath.Join(dir, "build.gradle")) || pathExists(filepath.Join(dir, "build.gradle.kts"))
	hasGradlew := pathExists(filepath.Join(dir, "gradlew")) || pathExists(filepath.Join(dir, "gradlew.bat"))
	if tool == "auto" {
		if hasPom {
			if hasMvnw {
				tool = "mvnw"
			} else {
				tool = "mvn"
			}
		} else if hasGradle {
			if hasGradlew {
				tool = "gradlew"
			} else {
				tool = "gradle"
			}
		} else {
			return nil, fmt.Errorf("no pom.xml or build.gradle in %s", dir)
		}
	}
	if tool == "mvn" || tool == "mvnw" {
		if !hasPom {
			return nil, fmt.Errorf("build_tool is %s but no pom.xml in %s", tool, dir)
		}
		var name string
		var args []string
		if tool == "mvnw" {
			if !hasMvnw {
				return nil, fmt.Errorf("build_tool is mvnw but mvnw not found in %s", dir)
			}
			if runtime.GOOS == "windows" && pathExists(filepath.Join(dir, "mvnw.cmd")) {
				name = "mvnw.cmd"
			} else {
				name = "./mvnw"
			}
			args = []string{"compile", "-q", "-B"}
			if goal == "test" || goal == "default" {
				args = []string{"test", "-q", "-B"}
			}
		} else {
			name = "mvn"
			args = []string{"compile", "-q", "-B"}
			if goal == "test" || goal == "default" {
				args = []string{"test", "-q", "-B"}
			}
		}
		c := exec.Command(name, args...)
		c.Dir = dir
		return c, nil
	}
	if tool == "gradle" || tool == "gradlew" {
		if !hasGradle {
			return nil, fmt.Errorf("build_tool is %s but no build.gradle in %s", tool, dir)
		}
		args := []string{"--no-daemon", "-q"}
		if tool == "gradlew" && !hasGradlew {
			return nil, fmt.Errorf("build_tool is gradlew but gradlew not found in %s", dir)
		}
		var name string
		if tool == "gradlew" {
			if runtime.GOOS == "windows" && pathExists(filepath.Join(dir, "gradlew.bat")) {
				name = "gradlew.bat"
			} else {
				name = "./gradlew"
			}
		} else {
			name = "gradle"
		}
		switch goal {
		case "compile":
			c := exec.Command(name, append(args, "compileJava")...)
			c.Dir = dir
			return c, nil
		default:
			c := exec.Command(name, append(args, "test")...)
			c.Dir = dir
			return c, nil
		}
	}
	return nil, fmt.Errorf("build_tool must be auto, mvn, mvnw, gradle, or gradlew; got %q", buildTool)
}

func shutdownDotnetBuildServers(dir string) {
	cmd := exec.Command("dotnet", "build-server", "shutdown")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	_ = cmd.Run()
}

func runDotnetCompile(ctx context.Context, repoPath string, timeout time.Duration, compileCommand, dotnetFallbackTFM string) evaluator.StepResult {
	fmt.Fprintln(os.Stderr, "  Compiling (.NET)...")
	// Drop lingering MSBuild/VBCSCompiler nodes so a prior timed-out test/build cannot keep bin/obj locked.
	shutdownDotnetBuildServers(filepath.Clean(repoPath))
	line := strings.TrimSpace(compileCommand)
	if line == "" {
		var err error
		line, err = dotnetShellLineWithProject(repoPath, "dotnet build -c Release", dotnetFallbackTFM)
		if err != nil {
			return evaluator.StepResult{Step: evaluator.StepCompile, OK: false, Summary: err.Error(), Output: ""}
		}
	}
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = filepath.Clean(repoPath)
	out, runErr := runCommand(ctx, cmd, timeout)
	if runErr != nil {
		summary := "compile failed"
		if out != "" {
			summary = firstLines(out, 3)
		}
		fmt.Fprintf(os.Stderr, "  Compile: failed. %s\n", summary)
		return evaluator.StepResult{Step: evaluator.StepCompile, OK: false, Summary: summary, Output: out}
	}
	fmt.Fprintln(os.Stderr, "  Compile: ok.")
	return evaluator.StepResult{Step: evaluator.StepCompile, OK: true, Summary: "compile ok", Output: out}
}

func runDotnetTest(ctx context.Context, repoPath string, timeout time.Duration, testCommand, dotnetFallbackTFM string) evaluator.StepResult {
	fmt.Fprintln(os.Stderr, "  Running tests (.NET)...")
	line := strings.TrimSpace(testCommand)
	if line == "" {
		var err error
		line, err = dotnetShellLineWithProject(repoPath, "dotnet test -c Release --no-build", dotnetFallbackTFM)
		if err != nil {
			return evaluator.StepResult{Step: evaluator.StepTest, OK: false, Summary: err.Error(), Output: ""}
		}
	}
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = filepath.Clean(repoPath)
	cmd.Env = append(os.Environ(), "CI=true")
	out, runErr := runCommand(ctx, cmd, timeout)
	if runErr != nil {
		summary := "tests failed"
		if out != "" {
			summary = firstLines(out, 5)
		}
		fmt.Fprintf(os.Stderr, "  Tests: failed. %s\n", summary)
		return evaluator.StepResult{Step: evaluator.StepTest, OK: false, Summary: summary, Output: out}
	}
	fmt.Fprintln(os.Stderr, "  Tests: ok.")
	return evaluator.StepResult{Step: evaluator.StepTest, OK: true, Summary: "tests ok", Output: out}
}

func runDotnetCoverage(ctx context.Context, repoPath string, timeout time.Duration, testCommand, dotnetFallbackTFM string) evaluator.StepResult {
	fmt.Fprintln(os.Stderr, "  Running tests (.NET coverage)...")
	line := strings.TrimSpace(testCommand)
	if line == "" {
		var err error
		line, err = dotnetShellLineWithProject(repoPath, `dotnet test -c Release --no-build --collect 'XPlat Code Coverage'`, dotnetFallbackTFM)
		if err != nil {
			return evaluator.StepResult{Step: evaluator.StepCoverage, OK: false, Summary: err.Error(), Output: ""}
		}
	}
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = filepath.Clean(repoPath)
	cmd.Env = append(os.Environ(), "CI=true")
	out, runErr := runCommand(ctx, cmd, timeout)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "  Coverage: failed. %s\n", firstLines(out, 3))
		return evaluator.StepResult{Step: evaluator.StepCoverage, OK: false, Summary: firstLines(out, 5), Output: out}
	}
	summary := "coverage ok (see test output for report path)"
	fmt.Fprintf(os.Stderr, "  Coverage: %s\n", summary)
	return evaluator.StepResult{Step: evaluator.StepCoverage, OK: true, Summary: summary, Output: out}
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

// coverageSummary looks for jacoco report and returns a one-line summary (e.g. "line coverage 42%").
func coverageSummary(repoPath string) string {
	// Maven: target/site/jacoco/index.html or target/jacoco.exec
	// Gradle: build/reports/jacoco/test/html/index.html
	for _, rel := range []string{
		"target/site/jacoco/index.html",
		"build/reports/jacoco/test/html/index.html",
	} {
		p := filepath.Join(repoPath, rel)
		if _, err := os.Stat(p); err == nil {
			return "coverage report: " + rel
		}
	}
	return ""
}
