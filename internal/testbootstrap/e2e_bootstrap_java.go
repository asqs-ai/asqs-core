package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const mavenPlaywrightDep = `
    <dependency>
      <groupId>com.microsoft.playwright</groupId>
      <artifactId>playwright</artifactId>
      <version>` + VersionPlaywrightJava + `</version>
      <scope>test</scope>
    </dependency>`

const gradlePlaywrightGroovy = `

// ASQS e2e_framework_bootstrap: Playwright for Java
dependencies {
    testImplementation 'com.microsoft.playwright:playwright:` + VersionPlaywrightJava + `'
}
tasks.register('asqsPlaywrightInstall', JavaExec) {
    classpath = sourceSets.test.runtimeClasspath
    mainClass = 'com.microsoft.playwright.CLI'
    args 'install', 'chromium', '--with-deps'
}
`

const gradlePlaywrightKotlin = `

// ASQS e2e_framework_bootstrap: Playwright for Java
dependencies {
    testImplementation("com.microsoft.playwright:playwright:` + VersionPlaywrightJava + `")
}
tasks.register<JavaExec>("asqsPlaywrightInstall") {
    classpath = sourceSets["test"].runtimeClasspath
    mainClass.set("com.microsoft.playwright.CLI")
    args("install", "chromium", "--with-deps")
}
`

// applyPlaywrightJavaBootstrap adds Playwright Java + JUnit/Surefire if needed, a smoke IT, then resolves deps, installs browsers, runs the smoke test.
func applyPlaywrightJavaBootstrap(ctx context.Context, p E2EParams, audit Auditor, repo string, ed *EphemeralDocker) error {
	logAudit(audit, ctx, "e2e_bootstrap.apply_start", map[string]interface{}{
		"message": "Installing Playwright for Java (Maven/Gradle + smoke test)",
		"stack":   "playwright-java",
	})
	fmt.Fprintln(os.Stderr, "  e2e_framework_bootstrap: installing Playwright Java (deps + smoke test)…")

	jbf, err := primaryJavaBuildFile(repo)
	if err != nil {
		return fmt.Errorf("e2e_framework_bootstrap: discover Java build: %w", err)
	}
	if jbf.Abs == "" {
		return fmt.Errorf("e2e_framework_bootstrap: no pom.xml or build.gradle(.kts) under repo")
	}
	moduleRoot := filepath.Dir(jbf.Abs)
	var filesChanged []string

	switch jbf.Kind {
	case javaBuildMaven:
		ch, err := applyMavenPlaywrightE2E(jbf.Abs)
		if err != nil {
			logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to patch pom.xml: %v", err), "step": "maven_playwright", "error": err.Error(),
			})
			return fmt.Errorf("e2e_framework_bootstrap maven playwright: %w", err)
		}
		if ch {
			filesChanged = append(filesChanged, relPathForBootstrap(repo, jbf.Abs))
		}
	case javaBuildGradleGroovy:
		ch, err := applyGradlePlaywrightE2E(jbf.Abs, false)
		if err != nil {
			logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to patch build.gradle: %v", err), "step": "gradle_playwright", "error": err.Error(),
			})
			return fmt.Errorf("e2e_framework_bootstrap gradle playwright: %w", err)
		}
		if ch {
			filesChanged = append(filesChanged, relPathForBootstrap(repo, jbf.Abs))
		}
		if err := ensureGradlePlaywrightChromiumWithDeps(jbf.Abs, false); err != nil {
			return fmt.Errorf("e2e_framework_bootstrap gradle playwright install args: %w", err)
		}
	case javaBuildGradleKotlin:
		ch, err := applyGradlePlaywrightE2E(jbf.Abs, true)
		if err != nil {
			logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to patch build.gradle.kts: %v", err), "step": "gradle_kts_playwright", "error": err.Error(),
			})
			return fmt.Errorf("e2e_framework_bootstrap gradle.kts playwright: %w", err)
		}
		if ch {
			filesChanged = append(filesChanged, relPathForBootstrap(repo, jbf.Abs))
		}
		if err := ensureGradlePlaywrightChromiumWithDeps(jbf.Abs, true); err != nil {
			return fmt.Errorf("e2e_framework_bootstrap gradle.kts playwright install args: %w", err)
		}
	default:
		return fmt.Errorf("e2e_framework_bootstrap: unknown Java build kind")
	}

	smokePath := filepath.Join(moduleRoot, "src", "test", "java", "com", "asqs", "e2e", "AsqsPlaywrightSmokeE2E.java")
	if err := os.MkdirAll(filepath.Dir(smokePath), 0755); err != nil {
		return fmt.Errorf("e2e_framework_bootstrap mkdir smoke test: %w", err)
	}
	if _, err := os.Stat(smokePath); os.IsNotExist(err) {
		if err := atomicWrite(smokePath, []byte(javaPlaywrightSmokeClass)); err != nil {
			return fmt.Errorf("e2e_framework_bootstrap write smoke test: %w", err)
		}
		filesChanged = append(filesChanged, relPathForBootstrap(repo, smokePath))
	}

	if len(filesChanged) > 0 {
		logAudit(audit, ctx, "e2e_bootstrap.patched", map[string]interface{}{
			"message": fmt.Sprintf("Patched: %s", strings.Join(filesChanged, ", ")), "files_changed": filesChanged,
		})
	}

	timeout := installTimeout(p.RunnerTimeout)
	switch jbf.Kind {
	case javaBuildMaven:
		if err := verifyMavenPlaywright(ctx, repo, jbf.Abs, timeout, audit, ed); err != nil {
			return err
		}
	case javaBuildGradleGroovy, javaBuildGradleKotlin:
		if err := verifyGradlePlaywright(ctx, repo, jbf.Abs, timeout, audit, ed); err != nil {
			return err
		}
	}

	logAudit(audit, ctx, "e2e_bootstrap.apply_ok", map[string]interface{}{
		"message":       "Playwright Java bootstrap complete",
		"files_changed": filesChanged,
		"stack":         "playwright-java",
	})
	fmt.Fprintln(os.Stderr, "  e2e_framework_bootstrap: Playwright Java ok (deps + browser install + smoke test)")
	return nil
}

func applyMavenPlaywrightE2E(pomPath string) (bool, error) {
	b, err := os.ReadFile(pomPath)
	if err != nil {
		return false, err
	}
	s := string(b)
	orig := s
	var err2 error
	if !strings.Contains(s, "com.microsoft.playwright") {
		s, err2 = insertMavenPlaywrightDependency(s)
		if err2 != nil {
			return false, err2
		}
	}
	if !strings.Contains(s, "junit-jupiter") {
		s, err2 = insertMavenJUnitDependency(s)
		if err2 != nil {
			return false, err2
		}
	}
	if !strings.Contains(s, "maven-surefire-plugin") {
		s, err2 = insertMavenSurefirePlugin(s)
		if err2 != nil {
			return false, err2
		}
	}
	if s == orig {
		return false, nil
	}
	return true, atomicWrite(pomPath, []byte(s))
}

func insertMavenPlaywrightDependency(pom string) (string, error) {
	if strings.Contains(pom, "com.microsoft.playwright") {
		return pom, nil
	}
	const open = "<dependencies>"
	const close = "</dependencies>"
	start := strings.Index(pom, open)
	if start < 0 {
		block := "  <dependencies>" + mavenPlaywrightDep + "\n  </dependencies>\n\n"
		return insertBeforeClosingProject(pom, block), nil
	}
	afterOpen := start + len(open)
	closeIdx := strings.Index(pom[afterOpen:], close)
	if closeIdx < 0 {
		return "", fmt.Errorf("pom.xml: unclosed <dependencies>")
	}
	closeIdx += afterOpen
	return pom[:afterOpen] + mavenPlaywrightDep + "\n" + pom[afterOpen:], nil
}

// ensureGradlePlaywrightChromiumWithDeps upgrades older bootstrap snippets that used
// `install-deps` + `install chromium` to Playwright's recommended `install chromium --with-deps`.
func ensureGradlePlaywrightChromiumWithDeps(path string, kotlinDSL bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := string(b)
	if !strings.Contains(s, "asqsPlaywrightInstall") {
		return nil
	}
	if strings.Contains(s, `args 'install', 'chromium', '--with-deps'`) ||
		strings.Contains(s, `args("install", "chromium", "--with-deps")`) {
		return nil
	}
	orig := s
	if kotlinDSL {
		s = strings.Replace(s, `args("install", "chromium")`, `args("install", "chromium", "--with-deps")`, 1)
	} else {
		s = strings.Replace(s, `args 'install', 'chromium'`, `args 'install', 'chromium', '--with-deps'`, 1)
	}
	if s == orig {
		return nil
	}
	return atomicWrite(path, []byte(s))
}

func applyGradlePlaywrightE2E(path string, kotlinDSL bool) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	s := string(b)
	if strings.Contains(s, "com.microsoft.playwright") || strings.Contains(s, "asqsPlaywrightInstall") {
		return false, nil
	}
	block := gradlePlaywrightGroovy
	if kotlinDSL {
		block = gradlePlaywrightKotlin
	}
	if !strings.HasSuffix(strings.TrimSpace(s), "\n") {
		s += "\n"
	}
	s += block
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return true, atomicWrite(path, []byte(s))
}

func mvnCmd(repo, pomAbs string) (name string, prefix []string, ok bool) {
	if !fileExists(pomAbs) {
		return "", nil, false
	}
	rel, err := filepath.Rel(repo, pomAbs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", nil, false
	}
	rel = filepath.ToSlash(rel)
	prefix = []string{"-q", "-B"}
	if rel != "pom.xml" {
		prefix = append([]string{"-f", rel}, prefix...)
	}
	if runtime.GOOS == "windows" {
		mc := filepath.Join(repo, "mvnw.cmd")
		if fileExists(mc) {
			return mc, prefix, true
		}
	}
	mw := filepath.Join(repo, "mvnw")
	if fileExists(mw) {
		return mw, prefix, true
	}
	return "mvn", prefix, true
}

func mavenDockerArgv(repo, pomAbs string, goals ...string) ([]string, bool) {
	if !fileExists(pomAbs) {
		return nil, false
	}
	rel, err := filepath.Rel(repo, pomAbs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, false
	}
	rel = filepath.ToSlash(rel)
	bin := "mvn"
	if fileExists(filepath.Join(repo, "mvnw")) {
		bin = "./mvnw"
	}
	argv := []string{bin}
	if rel != "pom.xml" {
		argv = append(argv, "-f", rel)
	}
	argv = append(argv, "-q", "-B")
	argv = append(argv, goals...)
	return argv, true
}

func gradleDockerArgv(repoRoot, gradleFileAbs string, goals ...string) ([]string, bool) {
	if !fileExists(gradleFileAbs) {
		return nil, false
	}
	modDir := filepath.Dir(gradleFileAbs)
	rel, err := filepath.Rel(repoRoot, modDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, false
	}
	bin := "gradle"
	if fileExists(filepath.Join(repoRoot, "gradlew")) {
		bin = "./gradlew"
	}
	argv := []string{bin}
	if rel != "." && rel != "" {
		argv = append(argv, "-p", filepath.ToSlash(rel))
	}
	argv = append(argv, "--no-daemon", "-q")
	argv = append(argv, goals...)
	return argv, true
}

func verifyMavenPlaywright(ctx context.Context, repo, pomAbs string, timeout time.Duration, audit Auditor, ed *EphemeralDocker) error {
	name, prefix, ok := mvnCmd(repo, pomAbs)
	if !ok {
		return fmt.Errorf("e2e_framework_bootstrap: no pom.xml")
	}
	run := func(step string, args ...string) error {
		vCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var out []byte
		var err error
		var cmdLine string
		if ed == nil {
			full := append(append([]string(nil), prefix...), args...)
			cmdLine = name + " " + strings.Join(full, " ")
			cmd := exec.CommandContext(vCtx, name, full...)
			cmd.Dir = repo
			out, err = cmd.CombinedOutput()
		} else {
			argv, ok2 := mavenDockerArgv(repo, pomAbs, args...)
			if !ok2 {
				return fmt.Errorf("e2e_framework_bootstrap: maven docker argv")
			}
			cmdLine = strings.Join(argv, " ")
			out, err = RunArgv(vCtx, ed, repo, argv, nil)
		}
		if err != nil {
			logAuditError(audit, ctx, "e2e_bootstrap.verify_failed", map[string]interface{}{
				"message": fmt.Sprintf("%s failed: %v", step, err), "command": cmdLine,
				"output": truncate(string(out), 8000), "error": err.Error(),
			})
			return fmt.Errorf("e2e_framework_bootstrap %s: %w\n%s", step, err, truncate(string(out), 4000))
		}
		return nil
	}
	logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{
		"message": "Maven test-compile (download Playwright + JUnit)", "command": name + " test-compile",
	})
	if err := run("test-compile", "test-compile"); err != nil {
		return err
	}
	execPlugin := []string{
		"org.codehaus.mojo:exec-maven-plugin:3.3.0:java",
		"-Dexec.classpathScope=test",
		"-Dexec.mainClass=com.microsoft.playwright.CLI",
	}
	installArgs := append(append([]string(nil), execPlugin...), "-Dexec.args=install chromium --with-deps")
	logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{
		"message": "Playwright CLI install chromium --with-deps (browser + OS packages; recommended in Docker)",
		"command": name + " " + strings.Join(installArgs, " "),
	})
	if err := run("playwright_install", installArgs...); err != nil {
		return err
	}
	testArgs := []string{"test", "-Dtest=com.asqs.e2e.AsqsPlaywrightSmokeE2E"}
	logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{
		"message": "Run Playwright Java smoke test", "command": name + " " + strings.Join(testArgs, " "),
	})
	if err := run("smoke_test", testArgs...); err != nil {
		return err
	}
	return nil
}

func gradleCmd(repoRoot, gradleFileAbs string) (name string, prefix []string, ok bool) {
	modDir := filepath.Dir(gradleFileAbs)
	rel, err := filepath.Rel(repoRoot, modDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", nil, false
	}
	prefix = []string{"--no-daemon", "-q"}
	if rel != "." && rel != "" {
		prefix = append([]string{"-p", filepath.ToSlash(rel)}, prefix...)
	}
	gw := filepath.Join(repoRoot, "gradlew")
	if runtime.GOOS == "windows" {
		bat := filepath.Join(repoRoot, "gradlew.bat")
		if fileExists(bat) {
			return bat, prefix, true
		}
	}
	if fileExists(gw) {
		return gw, prefix, true
	}
	return "gradle", prefix, true
}

func verifyGradlePlaywright(ctx context.Context, repo string, gradleFileAbs string, timeout time.Duration, audit Auditor, ed *EphemeralDocker) error {
	name, prefix, ok := gradleCmd(repo, gradleFileAbs)
	if !ok {
		return fmt.Errorf("e2e_framework_bootstrap: gradle build file outside repo")
	}
	run := func(step string, args ...string) error {
		vCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var out []byte
		var err error
		var cmdLine string
		if ed == nil {
			full := append(append([]string(nil), prefix...), args...)
			cmdLine = name + " " + strings.Join(full, " ")
			cmd := exec.CommandContext(vCtx, name, full...)
			cmd.Dir = repo
			out, err = cmd.CombinedOutput()
		} else {
			argv, ok2 := gradleDockerArgv(repo, gradleFileAbs, args...)
			if !ok2 {
				return fmt.Errorf("e2e_framework_bootstrap: gradle docker argv")
			}
			cmdLine = strings.Join(argv, " ")
			out, err = RunArgv(vCtx, ed, repo, argv, nil)
		}
		if err != nil {
			logAuditError(audit, ctx, "e2e_bootstrap.verify_failed", map[string]interface{}{
				"message": fmt.Sprintf("%s failed: %v", step, err), "command": cmdLine,
				"output": truncate(string(out), 8000), "error": err.Error(),
			})
			return fmt.Errorf("e2e_framework_bootstrap %s: %w\n%s", step, err, truncate(string(out), 4000))
		}
		return nil
	}
	logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{"message": "Gradle testClasses", "command": name + " testClasses"})
	if err := run("testClasses", "testClasses"); err != nil {
		return err
	}
	logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{
		"message": "Gradle asqsPlaywrightInstall (chromium + OS deps via --with-deps)", "command": name + " asqsPlaywrightInstall",
	})
	if err := run("playwright_install", "asqsPlaywrightInstall"); err != nil {
		return err
	}
	logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{"message": "Gradle smoke test", "command": name + " test --tests com.asqs.e2e.AsqsPlaywrightSmokeE2E"})
	if err := run("smoke_test", "test", "--tests", "com.asqs.e2e.AsqsPlaywrightSmokeE2E"); err != nil {
		return err
	}
	return nil
}
