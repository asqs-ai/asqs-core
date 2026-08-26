package runner

import "testing"

func TestUsePlaywrightDockerForJSE2E_csharpNodePlaywright(t *testing.T) {
	if !usePlaywrightDockerForJSE2E("csharp", "playwright") {
		t.Fatal("want true: polyglot repo uses Node Playwright while lang is C#")
	}
	if usePlaywrightDockerForJSE2E("csharp", "cypress") {
		t.Fatal("want false: cypress E2E for csharp-only detection not supported on Node image")
	}
	if !usePlaywrightDockerForJSE2E("typescript", "cypress") {
		t.Fatal("want true for TS + cypress")
	}
}

func TestUsePlaywrightDockerForJavaE2E(t *testing.T) {
	if !usePlaywrightDockerForJavaE2E("java", "playwright-java") {
		t.Fatal("want true for java + playwright-java")
	}
	if usePlaywrightDockerForJavaE2E("java", "selenium") {
		t.Fatal("want false for selenium (different image policy)")
	}
	if usePlaywrightDockerForJavaE2E("typescript", "playwright-java") {
		t.Fatal("want false for non-java")
	}
}

func TestUsePlaywrightDockerForCSharpE2E(t *testing.T) {
	if !usePlaywrightDockerForCSharpE2E("csharp", "playwright-dotnet") {
		t.Fatal("want true for csharp + playwright-dotnet")
	}
	if usePlaywrightDockerForCSharpE2E("csharp", "selenium") {
		t.Fatal("want false for selenium")
	}
	if usePlaywrightDockerForCSharpE2E("java", "playwright-dotnet") {
		t.Fatal("want false for non-csharp")
	}
}

func TestPlaywrightDotnetDockerImageRef(t *testing.T) {
	var nilSb *Sandbox
	if g := nilSb.playwrightDotnetDockerImageRef(); g != DefaultPlaywrightDotnetDockerImage {
		t.Fatalf("nil sandbox got %q", g)
	}
	s := &Sandbox{ImagePlaywrightDotnet: "my/playwright:1"}
	if s.playwrightDotnetDockerImageRef() != "my/playwright:1" {
		t.Fatalf("override got %q", s.playwrightDotnetDockerImageRef())
	}
	if g := (&Sandbox{}).playwrightDotnetDockerImageRef(); g != DefaultPlaywrightDotnetDockerImage {
		t.Fatalf("default got %q", g)
	}
}

func TestDockerImageNeedsPlaywrightIPC(t *testing.T) {
	if !dockerImageNeedsPlaywrightIPC("mcr.microsoft.com/playwright/java:v1.49.0-jammy") {
		t.Fatal("want playwright/java")
	}
	if !dockerImageNeedsPlaywrightIPC("mcr.microsoft.com/playwright:v1.49.1-jammy") {
		t.Fatal("want playwright node")
	}
	if !dockerImageNeedsPlaywrightIPC("mcr.microsoft.com/playwright/dotnet:v1.49.0-jammy") {
		t.Fatal("want playwright/dotnet")
	}
	if dockerImageNeedsPlaywrightIPC("maven:3.9-eclipse-temurin-21") {
		t.Fatal("want false for maven")
	}
}

func TestDockerImageIsPlaywrightDotnet(t *testing.T) {
	if !dockerImageIsPlaywrightDotnet("mcr.microsoft.com/playwright/dotnet:v1.49.0-jammy") {
		t.Fatal("want playwright/dotnet")
	}
	if dockerImageIsPlaywrightDotnet("mcr.microsoft.com/playwright/java:v1.49.0-jammy") {
		t.Fatal("want false for playwright/java")
	}
	if dockerImageIsPlaywrightDotnet("mcr.microsoft.com/playwright:v1.49.1-jammy") {
		t.Fatal("want false for node playwright image")
	}
}

func TestPlaywrightJavaDockerImageRef(t *testing.T) {
	s := &Sandbox{ImagePlaywrightJava: "custom/playwright:jdk"}
	if g := s.playwrightJavaDockerImageRef(); g != "custom/playwright:jdk" {
		t.Fatalf("got %q", g)
	}
	if g := (&Sandbox{}).playwrightJavaDockerImageRef(); g != DefaultPlaywrightJavaDockerImage {
		t.Fatalf("default got %q", g)
	}
}
