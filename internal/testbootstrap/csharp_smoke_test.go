package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCSharpSource(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCSharpEntryPointType_topLevelStatementsFallBackToAPublicType is the case that decides whether
// the ASP.NET Core framework smoke can exist at all: top-level statements make the generated Program
// class internal, so WebApplicationFactory<Program> does not compile from a separate test project.
func TestCSharpEntryPointType_topLevelStatementsFallBackToAPublicType(t *testing.T) {
	dir := t.TempDir()
	writeCSharpSource(t, dir, "Program.cs", "var builder = WebApplication.CreateBuilder(args);\nbuilder.Build().Run();\n")
	writeCSharpSource(t, filepath.Join(dir, "Controllers"), "OrdersController.cs",
		"namespace Demo.App.Controllers;\n\npublic class OrdersController\n{\n}\n")

	got, ok := csharpEntryPointType(dir)
	if !ok {
		t.Fatal("a public controller should be usable as the WebApplicationFactory type argument")
	}
	if got != "Demo.App.Controllers.OrdersController" {
		t.Fatalf("entry point = %q", got)
	}
}

func TestCSharpEntryPointType_prefersPublicProgram(t *testing.T) {
	dir := t.TempDir()
	writeCSharpSource(t, dir, "Program.cs", "namespace Demo.App;\n\npublic partial class Program\n{\n}\n")
	writeCSharpSource(t, filepath.Join(dir, "Controllers"), "OrdersController.cs",
		"namespace Demo.App.Controllers;\n\npublic class OrdersController\n{\n}\n")

	got, ok := csharpEntryPointType(dir)
	if !ok || got != "Demo.App.Program" {
		t.Fatalf("entry point = %q ok = %v; an explicit public Program is the documented choice", got, ok)
	}
}

func TestCSharpEntryPointType_skipsStaticGenericAndCommentedTypes(t *testing.T) {
	dir := t.TempDir()
	writeCSharpSource(t, dir, "Helpers.cs", "namespace Demo;\n\npublic static class Helpers\n{\n}\n")
	writeCSharpSource(t, dir, "Commented.cs", "namespace Demo;\n\n// public class Ghost { }\n")
	if _, ok := csharpEntryPointType(dir); ok {
		t.Error("a static class cannot be a type argument and a commented-out class does not exist")
	}

	writeCSharpSource(t, dir, "Real.cs", "namespace Demo;\n\npublic sealed class Real\n{\n}\n")
	got, ok := csharpEntryPointType(dir)
	if !ok || got != "Demo.Real" {
		t.Fatalf("entry point = %q ok = %v", got, ok)
	}
}

func TestCSharpEntryPointType_noPublicTypeMeansNoFrameworkSmoke(t *testing.T) {
	dir := t.TempDir()
	writeCSharpSource(t, dir, "Program.cs", "var app = WebApplication.CreateBuilder(args).Build();\napp.Run();\n")
	writeCSharpSource(t, dir, "Internal.cs", "namespace Demo;\n\ninternal class Hidden\n{\n}\n")
	if _, ok := csharpEntryPointType(dir); ok {
		t.Error("a project with no public type gives WebApplicationFactory nothing to bind to")
	}
}

func TestWriteCSharpUnitSmokeTest_perRunnerFlavours(t *testing.T) {
	cases := []struct {
		tf       CSharpTestFramework
		wantUse  string
		wantAttr string
	}{
		{CSharpTestXunit, "using Xunit;", "[Fact]"},
		{CSharpTestNUnit, "using NUnit.Framework;", "[Test]"},
		{CSharpTestMSTest, "using Microsoft.VisualStudio.TestTools.UnitTesting;", "[TestMethod]"},
	}
	for _, tc := range cases {
		t.Run(string(tc.tf), func(t *testing.T) {
			dir := t.TempDir()
			f, err := writeCSharpUnitSmokeTest(dir, tc.tf)
			if err != nil || !f.Wrote {
				t.Fatalf("wrote = %v err = %v", f.Wrote, err)
			}
			if f.FullyQualifiedName != "Asqs.Bootstrap.AsqsBootstrapSmokeTest" {
				t.Fatalf("FQN = %s", f.FullyQualifiedName)
			}
			b, _ := os.ReadFile(f.Abs)
			src := string(b)
			for _, want := range []string{tc.wantUse, tc.wantAttr, "using Moq;", "using FluentAssertions;"} {
				if !strings.Contains(src, want) {
					t.Errorf("smoke test missing %q:\n%s", want, src)
				}
			}

			again, err := writeCSharpUnitSmokeTest(dir, tc.tf)
			if err != nil {
				t.Fatal(err)
			}
			if again.Wrote {
				t.Error("an existing file must never be clobbered")
			}
		})
	}
}

func TestWriteCSharpFrameworkSmokeTest_substitutesEntryPointAndRunner(t *testing.T) {
	repo := t.TempDir()
	webDir := filepath.Join(repo, "src", "App")
	writeCSharpSource(t, webDir, "App.csproj", aspNetCoreWebCsproj)
	writeCSharpSource(t, webDir, "Program.cs", "var app = WebApplication.CreateBuilder(args).Build();\napp.Run();\n")
	writeCSharpSource(t, filepath.Join(webDir, "Controllers"), "OrdersController.cs",
		"namespace Demo.App.Controllers;\n\npublic class OrdersController\n{\n}\n")

	prof := buildCSharpTestProfile(csharpFrameworkDetection{
		Framework: CSharpFrameworkAspNetCore, TestFramework: CSharpTestXunit,
		TargetFramework: "net8.0", NetMajor: 8, WebProjectAbs: filepath.Join(webDir, "App.csproj"),
	})
	testDir := filepath.Join(repo, "tests", "App.Tests")
	f, staged, err := writeCSharpFrameworkSmokeTest(testDir, prof)
	if err != nil || !staged {
		t.Fatalf("staged = %v err = %v", staged, err)
	}
	b, _ := os.ReadFile(f.Abs)
	src := string(b)
	if !strings.Contains(src, "WebApplicationFactory<Demo.App.Controllers.OrdersController>") {
		t.Errorf("entry point not substituted:\n%s", src)
	}
	for _, token := range []string{csharpEntryPointToken, csharpTestUsingToken, csharpClassAttrToken, csharpMethodAttrToken} {
		if strings.Contains(src, token) {
			t.Errorf("template placeholder %s survived into the written file", token)
		}
	}
	if !strings.Contains(src, "[Fact]") || !strings.Contains(src, "using Xunit;") {
		t.Errorf("xUnit tokens missing:\n%s", src)
	}
}

func TestWriteCSharpFrameworkSmokeTest_skippedForPlainProfile(t *testing.T) {
	prof := buildCSharpTestProfile(csharpFrameworkDetection{
		Framework: CSharpFrameworkPlain, TestFramework: CSharpTestXunit, TargetFramework: "net8.0", NetMajor: 8,
	})
	if _, staged, err := writeCSharpFrameworkSmokeTest(t.TempDir(), prof); staged || err != nil {
		t.Fatalf("staged = %v err = %v; a plain library has no host", staged, err)
	}
}

func TestRemoveCSharpSmokeFile_onlyRemovesWhatThisRunWrote(t *testing.T) {
	dir := t.TempDir()
	f, err := writeCSharpUnitSmokeTest(dir, CSharpTestXunit)
	if err != nil {
		t.Fatal(err)
	}
	removeCSharpSmokeFile(csharpSmokeFile{Abs: f.Abs, Wrote: false})
	if !fileExists(f.Abs) {
		t.Fatal("a file bootstrap did not create must survive removal")
	}
	removeCSharpSmokeFile(f)
	if fileExists(f.Abs) {
		t.Fatal("a failed smoke test this run wrote must be removed")
	}
}

func TestRenderCSharpTestProject_carriesTheProfilePackages(t *testing.T) {
	prof := buildCSharpTestProfile(csharpFrameworkDetection{
		Framework: CSharpFrameworkAspNetCore, TestFramework: CSharpTestXunit, TargetFramework: "net8.0", NetMajor: 8,
	})
	got := renderCSharpTestProject("net8.0", false, []string{"../../src/App/App.csproj"}, prof.Packages)
	for _, want := range []string{"Microsoft.NET.Test.Sdk", `Include="xunit"`, "Moq", "FluentAssertions", "Microsoft.AspNetCore.Mvc.Testing"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered project missing %s:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "<PrivateAssets>all</PrivateAssets>") {
		t.Error("the VS runner needs PrivateAssets")
	}
}
