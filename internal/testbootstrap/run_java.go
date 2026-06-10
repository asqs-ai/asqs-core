package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
)

func runJavaBootstrap(ctx context.Context, repo string, cfg *config.TestFrameworkBootstrapConfig, audit Auditor, runnerTimeout string, ed *EphemeralDocker) error {
	pom := filepath.Join(repo, "pom.xml")
	gradle := filepath.Join(repo, "build.gradle")
	gradlekts := filepath.Join(repo, "build.gradle.kts")
	_, errPom := os.Stat(pom)
	_, errGradle := os.Stat(gradle)
	_, errKts := os.Stat(gradlekts)
	hasPom := errPom == nil
	hasGradle := errGradle == nil
	hasKts := errKts == nil
	if !hasPom && !hasGradle && !hasKts {
		return fmt.Errorf("test_framework_bootstrap: no pom.xml or build.gradle(.kts) at repo root")
	}

	logAudit(audit, ctx, "test_bootstrap.apply_start", map[string]interface{}{
		"message": "Adding JUnit 5 + Surefire (Maven) or Gradle test dependencies",
		"stack":   "junit5",
	})

	var filesChanged []string
	if hasPom {
		changed, err := applyMavenJUnit(pom)
		if err != nil {
			logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to patch pom.xml: %v", err),
				"step":    "maven_pom",
				"error":   err.Error(),
			})
			return fmt.Errorf("test_framework_bootstrap maven: %w", err)
		}
		if changed {
			filesChanged = append(filesChanged, "pom.xml")
		}
	} else if hasGradle {
		changed, err := applyGradleJUnit(gradle, false)
		if err != nil {
			logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to patch build.gradle: %v", err),
				"step":    "gradle_groovy",
				"error":   err.Error(),
			})
			return fmt.Errorf("test_framework_bootstrap gradle: %w", err)
		}
		if changed {
			filesChanged = append(filesChanged, "build.gradle")
		}
	} else {
		changed, err := applyGradleJUnit(gradlekts, true)
		if err != nil {
			logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to patch build.gradle.kts: %v", err),
				"step":    "gradle_kotlin",
				"error":   err.Error(),
			})
			return fmt.Errorf("test_framework_bootstrap gradle.kts: %w", err)
		}
		if changed {
			filesChanged = append(filesChanged, "build.gradle.kts")
		}
	}

	if len(filesChanged) > 0 {
		logAudit(audit, ctx, "test_bootstrap.patched", map[string]interface{}{
			"message":       fmt.Sprintf("Patched: %s", strings.Join(filesChanged, ", ")),
			"files_changed": filesChanged,
		})
	}

	timeout := installTimeout(runnerTimeout)
	vCtx, vCancel := context.WithTimeout(ctx, timeout)
	defer vCancel()

	name, args, ok := javaVerifyCommand(repo)
	if !ok {
		return fmt.Errorf("test_framework_bootstrap: could not determine mvn/gradle verify command")
	}
	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": fmt.Sprintf("Verifying Java test compile: %s %s", name, strings.Join(args, " ")),
		"command": name + " " + strings.Join(args, " "),
	})

	out, err := runJavaVerify(vCtx, repo, ed)
	if err != nil {
		logAuditError(audit, ctx, "test_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("Java verify failed: %v", err),
			"command": name + " " + strings.Join(args, " "),
			"output":  truncate(string(out), 8000),
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap verify java: %w\n%s", err, truncate(string(out), 4000))
	}

	logAudit(audit, ctx, "test_bootstrap.apply_ok", map[string]interface{}{
		"message":       fmt.Sprintf("JUnit 5 bootstrap ok; verified with %s", name),
		"files_changed": filesChanged,
		"stack":         "junit5",
	})
	fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: added JUnit 5; %s verify ok\n", filepath.Base(name))
	return nil
}
