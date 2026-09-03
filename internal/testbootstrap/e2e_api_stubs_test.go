package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The invented-member failure is binding-independent: run api-428e4bb1f792d71bacd54d8ba3953801
// intercepted correctly in TypeScript and then reached for `route.delay(...)`, which exists in no
// binding. Shipping helpers for JS/TS alone would fix the language we happened to be watching.
func TestAPIStubs_everyPlaywrightBindingGetsTheSameThreeHelpers(t *testing.T) {
	t.Run("java", func(t *testing.T) {
		dir := t.TempDir()
		p, wrote, err := writeJavaAPIStubs(dir)
		if err != nil || !wrote {
			t.Fatalf("wrote=%v err=%v", wrote, err)
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatal(rerr)
		}
		got := string(b)
		for _, want := range []string{
			"package com.asqs.e2e;",
			"public static void stubJson(",
			"public static void stubJsonAfter(",
			"public static void stubError(",
			"Route.FulfillOptions",
			asqsE2EGeneratedHeader,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("java helper missing %q", want)
			}
		}
		// spring-javaformat rejects space indentation; the template must use tabs like the smoke
		// class beside it.
		if strings.Contains(got, "\n    ") {
			t.Error("java helper is space-indented; spring-javaformat:apply validate will reject it")
		}
		// The point of the class is that Route has no delay member.
		if strings.Contains(got, "route.delay(") {
			t.Error("java helper calls a Route member that does not exist")
		}
	})

	t.Run("csharp", func(t *testing.T) {
		dir := t.TempDir()
		p, wrote, err := writeCSharpAPIStubs(dir)
		if err != nil || !wrote {
			t.Fatalf("wrote=%v err=%v", wrote, err)
		}
		b, _ := os.ReadFile(p)
		got := string(b)
		for _, want := range []string{
			"namespace Asqs.E2E",
			"StubJsonAsync(",
			"StubJsonAfterAsync(",
			"StubErrorAsync(",
			"RouteFulfillOptions",
			asqsE2EGeneratedHeader,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("csharp helper missing %q", want)
			}
		}
		if strings.Contains(got, "route.DelayAsync(") {
			t.Error("csharp helper calls an IRoute member that does not exist")
		}
	})
}

// A repository's own file is never clobbered; a file this tool wrote is upgraded.
func TestAPIStubs_neverClobberARepositorysOwn(t *testing.T) {
	const mine = "// my own helpers\n"

	javaDir := t.TempDir()
	javaPath := filepath.Join(javaDir, javaAPIStubsRelDir, javaAPIStubsFile)
	if err := os.MkdirAll(filepath.Dir(javaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(javaPath, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeJavaAPIStubs(javaDir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(javaPath); string(b) != mine {
		t.Error("clobbered a repository's own Java helpers")
	}
	if apiStubsMarkerPresent(javaPath) {
		t.Error("a repository's own file must not report as ASQS-owned")
	}

	csDir := t.TempDir()
	csPath := filepath.Join(csDir, "AsqsApiStubs.cs")
	if err := os.WriteFile(csPath, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeCSharpAPIStubs(csDir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(csPath); string(b) != mine {
		t.Error("clobbered a repository's own C# helpers")
	}

	// An ASQS-owned file is upgraded rather than left stale.
	owned := t.TempDir()
	ownedPath := filepath.Join(owned, "AsqsApiStubs.cs")
	if err := os.WriteFile(ownedPath, []byte("// "+asqsE2EGeneratedHeader+"\n// stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeCSharpAPIStubs(owned); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ownedPath); !strings.Contains(string(b), "StubJsonAsync(") {
		t.Error("an ASQS-owned C# helper was not upgraded")
	}
	if !apiStubsMarkerPresent(ownedPath) {
		t.Error("the upgraded file lost its ownership marker")
	}
}
