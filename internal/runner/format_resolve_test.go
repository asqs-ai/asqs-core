package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveFormatCommand_onlyAddedJava_googleJavaFormatOnPATH(t *testing.T) {
	if _, err := exec.LookPath("google-java-format"); err != nil {
		t.Skip("google-java-format not on PATH")
	}
	r := ResolveFormatCommand(t.TempDir(), "java", "", "auto", true)
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
	r := ResolveFormatCommand(dir, "java", "", "auto", true)
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
	r := ResolveFormatCommand(t.TempDir(), "java", "google-java-format -i", "auto", true)
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
	r := ResolveFormatCommand(dir, "java", "", "mvnw", false)
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

// (Upstream additionally tests the Target-aware half here — docker never probes the host for
// image toolchains, per-file prettier is repo-relative in a container, and a repo wrapper does not
// count as availability. ResolveFormatCommand gains its Target parameter with CP35; port those
// tests with it.)
