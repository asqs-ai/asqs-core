package testbootstrap

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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
	// CP32: the PATH binary, never the repo wrapper. Bootstrap used to prefer ./mvnw while the
	// eval step ran host `mvn`, so a single run could compile the same sources with two different
	// Maven versions — the split this decision exists to remove.
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
	// CP32: the PATH binary, never the repo wrapper. See mavenVerifyCommand.
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
		// The `chmod +x ./mvnw` variant is gone with CP32: there is no wrapper to make
		// executable, and the image supplies Maven.
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
		// As the Maven branch: no wrapper, no chmod (CP32).
		return "gradle" + p + " -q testClasses --no-daemon", true
	default:
		return "", false
	}
}
