package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// javaVerifyCommand returns argv for validating Java test classpath (test-compile / testClasses).
func javaVerifyCommand(repo string) (cmd string, args []string, ok bool) {
	repo = filepath.Clean(repo)
	jbf, err := primaryJavaBuildFile(repo)
	if err != nil || jbf.Abs == "" {
		return "", nil, false
	}
	switch jbf.Kind {
	case javaBuildMaven:
		return mavenVerifyCommand(repo, jbf.Abs)
	case javaBuildGradleGroovy, javaBuildGradleKotlin:
		return gradleVerifyCommand(repo, jbf.Abs)
	default:
		return "", nil, false
	}
}

func mavenVerifyCommand(repo, pomAbs string) (cmd string, args []string, ok bool) {
	rel, err := filepath.Rel(repo, pomAbs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", nil, false
	}
	rel = filepath.ToSlash(rel)
	mvnArgs := []string{"-q", "test-compile", "-B"}
	if rel != "pom.xml" {
		mvnArgs = append([]string{"-f", rel}, mvnArgs...)
	}
	if runtime.GOOS == "windows" {
		mc := filepath.Join(repo, "mvnw.cmd")
		if _, err := os.Stat(mc); err == nil {
			return mc, mvnArgs, true
		}
	}
	mvnw := filepath.Join(repo, "mvnw")
	if _, err := os.Stat(mvnw); err == nil {
		return mvnw, mvnArgs, true
	}
	return "mvn", mvnArgs, true
}

func gradleVerifyCommand(repo, gradleAbs string) (cmd string, args []string, ok bool) {
	modDir := filepath.Dir(gradleAbs)
	rel, err := filepath.Rel(repo, modDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", nil, false
	}
	gargs := []string{"-q", "testClasses", "--no-daemon"}
	if rel != "." && rel != "" {
		gargs = append([]string{"-p", filepath.ToSlash(rel)}, gargs...)
	}
	gw := filepath.Join(repo, "gradlew")
	if runtime.GOOS == "windows" {
		bat := filepath.Join(repo, "gradlew.bat")
		if _, err := os.Stat(bat); err == nil {
			return bat, gargs, true
		}
	}
	if _, err := os.Stat(gw); err == nil {
		return gw, gargs, true
	}
	return "gradle", gargs, true
}

func runJavaVerify(ctx context.Context, repo string, ed *EphemeralDocker) ([]byte, error) {
	if ed == nil {
		return runJavaVerifyLocal(ctx, repo)
	}
	script, ok := javaVerifyDockerScript(repo)
	if !ok {
		return nil, fmt.Errorf("no Maven or Gradle project under repo")
	}
	return ed.sh(ctx, script, nil)
}

func runJavaVerifyLocal(ctx context.Context, repo string) ([]byte, error) {
	name, args, ok := javaVerifyCommand(repo)
	if !ok {
		return nil, fmt.Errorf("no Maven or Gradle project under repo")
	}
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = repo
	return c.CombinedOutput()
}

// javaVerifyDockerScript returns a shell script for /workspace (Linux container).
func javaVerifyDockerScript(repo string) (string, bool) {
	repo = filepath.Clean(repo)
	jbf, err := primaryJavaBuildFile(repo)
	if err != nil || jbf.Abs == "" {
		return "", false
	}
	switch jbf.Kind {
	case javaBuildMaven:
		rel, e := filepath.Rel(repo, jbf.Abs)
		if e != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}
		rel = filepath.ToSlash(rel)
		f := ""
		if rel != "pom.xml" {
			f = " " + shellQuoteArg("-f") + " " + shellQuoteArg(rel)
		}
		if fileExists(filepath.Join(repo, "mvnw")) {
			return "chmod +x ./mvnw 2>/dev/null || true; ./mvnw -q test-compile -B" + f, true
		}
		return "mvn -q test-compile -B" + f, true
	case javaBuildGradleGroovy, javaBuildGradleKotlin:
		modDir := filepath.Dir(jbf.Abs)
		rel, e := filepath.Rel(repo, modDir)
		if e != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}
		p := ""
		if rel != "." && rel != "" {
			p = " " + shellQuoteArg("-p") + " " + shellQuoteArg(filepath.ToSlash(rel))
		}
		if fileExists(filepath.Join(repo, "gradlew")) {
			return "chmod +x ./gradlew 2>/dev/null || true; ./gradlew" + p + " -q testClasses --no-daemon", true
		}
		return "gradle" + p + " -q testClasses --no-daemon", true
	default:
		return "", false
	}
}
