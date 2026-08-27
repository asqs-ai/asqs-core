package testbootstrap

import (
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/teststack"
)

// javaImportRootsForArtifact maps a Maven/Gradle artifact to the package roots a generated test may
// import from it.
//
// Coordinates and import roots are different namespaces in the JVM, so this mapping cannot be derived
// mechanically the way it can in .NET or npm: spring-boot-starter-test is an aggregate that brings
// JUnit, Mockito, AssertJ, Hamcrest and Spring's own test support under five unrelated roots.
func javaImportRootsForArtifact(artifactID string) []string {
	switch strings.ToLower(artifactID) {
	case "spring-boot-starter-test":
		return []string{
			"org.junit.jupiter.*", "org.mockito.*", "org.assertj.core.*", "org.hamcrest.*",
			"org.springframework.test.*", "org.springframework.boot.test.*",
		}
	case "junit-jupiter", "junit-platform-launcher":
		return []string{"org.junit.jupiter.*"}
	case "mockito-core", "mockito-junit-jupiter":
		return []string{"org.mockito.*"}
	case "assertj-core":
		return []string{"org.assertj.core.*"}
	case "quarkus-junit5":
		return []string{"io.quarkus.test.*", "org.junit.jupiter.*"}
	case "quarkus-junit5-mockito":
		return []string{"io.quarkus.test.*", "org.mockito.*"}
	case "micronaut-test-junit5":
		return []string{"io.micronaut.test.*", "org.junit.jupiter.*"}
	default:
		return nil
	}
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// javaContract renders a Java profile as a contract.
func javaContract(p javaTestProfile) teststack.Contract {
	var pkgs, imports []string
	for _, d := range p.Deps {
		pkgs = append(pkgs, d.coord())
		imports = append(imports, javaImportRootsForArtifact(d.ArtifactID)...)
	}
	return teststack.Contract{
		Language:          "java",
		Framework:         string(p.Framework),
		FrameworkVersion:  p.FrameworkVersion,
		Runner:            "junit5",
		Stack:             p.Stack,
		AvailablePackages: dedupeSorted(pkgs),
		AvailableImports:  dedupeSorted(imports),
	}
}

// csharpNamespacesForPackage maps a NuGet package to the namespaces a test may reference. Unlike the
// JVM, .NET package ids and root namespaces mostly coincide, so this only covers the exceptions.
func csharpNamespacesForPackage(id string) []string {
	switch strings.ToLower(id) {
	case "microsoft.net.test.sdk":
		return nil // build-time only; nothing to import
	case "xunit", "xunit.runner.visualstudio":
		return []string{"Xunit"}
	case "nunit", "nunit3testadapter":
		return []string{"NUnit.Framework"}
	case "mstest.testframework", "mstest.testadapter":
		return []string{"Microsoft.VisualStudio.TestTools.UnitTesting"}
	case "moq":
		return []string{"Moq"}
	case "fluentassertions":
		return []string{"FluentAssertions"}
	case "microsoft.aspnetcore.mvc.testing":
		return []string{"Microsoft.AspNetCore.Mvc.Testing"}
	case "microsoft.entityframeworkcore.inmemory":
		return []string{"Microsoft.EntityFrameworkCore"}
	default:
		return []string{id}
	}
}

// csharpContract renders a C# profile as a contract.
func csharpContract(p csharpTestProfile) teststack.Contract {
	var pkgs, imports []string
	for _, pkg := range p.Packages {
		pkgs = append(pkgs, pkg.ID)
		imports = append(imports, csharpNamespacesForPackage(pkg.ID)...)
	}
	return teststack.Contract{
		Language:          "csharp",
		Framework:         string(p.Framework),
		FrameworkVersion:  p.TargetFramework,
		Runner:            string(p.TestFramework),
		Stack:             p.Stack,
		AvailablePackages: dedupeSorted(pkgs),
		AvailableImports:  dedupeSorted(imports),
	}
}

// jsContract renders a JS/TS profile as a contract. npm specifiers are the import specifiers, so the
// two lists differ only in that the runner itself is importable under Vitest but ambient under Jest.
func jsContract(p jsTestProfile, lang string) teststack.Contract {
	var pkgs, imports []string
	for _, d := range p.Deps {
		pkgs = append(pkgs, d.Name)
		switch d.Name {
		case "jest", "jest-environment-jsdom", "jest-preset-angular", "ts-jest", "jsdom":
			// Configured, not imported: Jest's globals are ambient and jsdom is the environment.
			continue
		}
		imports = append(imports, d.Name)
	}
	if p.Runner == JSRunnerJest {
		imports = append(imports, "@jest/globals")
	}
	normalized := "javascript"
	if p.IsTS || strings.EqualFold(lang, "typescript") || strings.EqualFold(lang, "ts") {
		normalized = "typescript"
	}
	return teststack.Contract{
		Language:          normalized,
		Framework:         string(p.Framework),
		FrameworkVersion:  p.FrameworkVersion,
		Runner:            string(p.Runner),
		Stack:             p.Stack,
		TestEnvironment:   p.TestEnvironment,
		AvailablePackages: dedupeSorted(pkgs),
		AvailableImports:  dedupeSorted(imports),
	}
}
