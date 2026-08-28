package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveFormatCommand_onlyAddedJava_googleJavaFormatOnPATH(t *testing.T) {
	if _, err := exec.LookPath("google-java-format"); err != nil {
		t.Skip("google-java-format not on PATH")
	}
	r := ResolveFormatCommand(t.TempDir(), "java", "", "auto", true, TargetLocal)
	if r.Command != "google-java-format -i" {
		t.Fatalf("Command = %q, want google-java-format -i", r.Command)
	}
	if !r.PerFile {
		t.Fatal("PerFile = false, want true")
	}
	if r.Source != "auto_google_java_format" {
		t.Fatalf("Source = %q, want auto_google_java_format", r.Source)
	}
	if r.SkipReason != "" {
		t.Fatalf("SkipReason = %q, want empty", r.SkipReason)
	}
}

func TestResolveFormatCommand_onlyAddedJava_noPerFileTool(t *testing.T) {
	if _, err := exec.LookPath("google-java-format"); err == nil {
		t.Skip("google-java-format is on PATH; test requires it absent")
	}
	dir := t.TempDir()
	pom := `<project><build><plugins><plugin><artifactId>spotless-maven-plugin</artifactId></plugin></plugins></build></project>`
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pom), 0644); err != nil {
		t.Fatal(err)
	}
	r := ResolveFormatCommand(dir, "java", "", "auto", true, TargetLocal)
	if r.Command != "" {
		t.Fatalf("Command = %q, want empty (must not use spotless:apply when onlyAdded)", r.Command)
	}
	if r.SkipReason == "" {
		t.Fatal("SkipReason empty, want explanation")
	}
	if r.Source != "none" {
		t.Fatalf("Source = %q, want none", r.Source)
	}
}

func TestResolveFormatCommand_onlyAddedJava_explicitConfig(t *testing.T) {
	if _, err := exec.LookPath("google-java-format"); err != nil {
		t.Skip("google-java-format not on PATH")
	}
	r := ResolveFormatCommand(t.TempDir(), "java", "google-java-format -i", "auto", true, TargetLocal)
	if r.Command != "google-java-format -i" {
		t.Fatalf("Command = %q", r.Command)
	}
	if r.Source != "config" {
		t.Fatalf("Source = %q, want config", r.Source)
	}
	if !r.PerFile {
		t.Fatal("PerFile = false, want true for non-shell per-file command")
	}
}

func TestResolveFormatCommand_repoWideJava_spotless(t *testing.T) {
	dir := t.TempDir()
	pom := `<project><build><plugins><plugin><artifactId>spotless-maven-plugin</artifactId></plugin></plugins></build></project>`
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pom), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mvnw"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	// build_tool: mvnw is a deprecated alias for mvn since D3 (U3b); the repo wrapper is never
	// selected, so the format step and the eval step invoke the same Maven.
	stubToolsOnPATH(t, "mvn")
	r := ResolveFormatCommand(dir, "java", "", "mvnw", false, TargetLocal)
	if r.Command != "mvn spotless:apply -q" {
		t.Fatalf("Command = %q, want mvn spotless:apply -q", r.Command)
	}
	if r.Source != "auto_spotless" {
		t.Fatalf("Source = %q, want auto_spotless", r.Source)
	}
	if r.PerFile {
		t.Fatal("PerFile = true, want false for repo-wide spotless")
	}
}

// A docker run must not skip a Maven formatter because the HOST has no Maven: the toolchain image
// supplies it. Before U3b the availability probe was host-only and unconditional, so a Docker-less
// host silently lost the format step entirely.
func TestResolveFormatCommand_dockerDoesNotProbeTheHostForImageToolchains(t *testing.T) {
	dir := t.TempDir()
	pom := `<project><build><plugins><plugin><artifactId>spotless-maven-plugin</artifactId></plugin></plugins></build></project>`
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pom), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no mvn anywhere on this host

	if r := ResolveFormatCommand(dir, "java", "", "auto", false, TargetLocal); r.Command != "" {
		t.Errorf("local target with no host mvn should skip, got %q", r.Command)
	} else if r.SkipReason == "" {
		t.Error("local skip should carry a reason")
	}

	r := ResolveFormatCommand(dir, "java", "", "auto", false, TargetDocker)
	if r.Command != "mvn spotless:apply -q" {
		t.Fatalf("docker target: Command = %q (skip %q), want the maven command", r.Command, r.SkipReason)
	}
}

// An absolute HOST path to node_modules/.bin/prettier does not exist inside the container: the
// repository is mounted at /workspace. The docker target must therefore emit a repo-relative
// command, and must never carry a Windows ".cmd" suffix derived from the host's OS.
func TestPrettierCommand_dockerTargetIsRepoRelative(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "prettier"
	if runtime.GOOS == "windows" {
		name += ".cmd"
	}
	if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, ok := prettierPerFileCommand(dir, TargetDocker)
	if !ok {
		t.Fatal("docker target should find the repo-local prettier")
	}
	if filepath.IsAbs(strings.Fields(got)[0]) {
		t.Errorf("docker command %q uses an absolute host path", got)
	}
	if !strings.HasPrefix(got, "node_modules/.bin/prettier ") {
		t.Errorf("docker command = %q, want a repo-relative node_modules/.bin/prettier", got)
	}
	if strings.Contains(got, ".cmd") {
		t.Errorf("docker command %q carries a host Windows suffix", got)
	}

	// The local target keeps the absolute host path it has always used.
	if got, ok := prettierPerFileCommand(dir, TargetLocal); !ok || !filepath.IsAbs(strings.Fields(got)[0]) {
		t.Errorf("local command = %q (ok=%v), want an absolute host path", got, ok)
	}
}

// A repo that ships ./mvnw no longer counts as "Maven is available": nothing invokes the wrapper
// since D3, so the host (or image) must actually provide mvn.
func TestFormatAvailability_repoWrapperIsNotAvailability(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mvnw"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no mvn on this host

	if got := shellFormatAvailabilitySkipReason(dir, "mvn spotless:apply -q", TargetLocal); got == "" {
		t.Error("local target with no mvn must skip even though the repo ships mvnw")
	}
	if got := shellFormatAvailabilitySkipReason(dir, "mvn spotless:apply -q", TargetDocker); got != "" {
		t.Errorf("docker target must not skip: the image supplies mvn (got %q)", got)
	}
}
