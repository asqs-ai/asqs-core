package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three real mismatches from run api-c11271e28fd0c1e10a6e5af6263108ee, which javac rejected in
// rounds 1 and 3 and which cost two to three fixer rounds to repair:
//
//	OwnerControllerE2EIT.java    declares  class OwnerControllerE2ETests
//	WelcomeControllerE2EIT.java  declares  class WelcomeControllerE2ETest
//	VetControllerE2EIT.java      declares  class VetControllerE2ETest
func TestEnforcePrimaryTypeName_renamesToMatchFile(t *testing.T) {
	cases := []struct{ path, declared, want string }{
		{"src/test/java/p/OwnerControllerE2EIT.java", "OwnerControllerE2ETests", "OwnerControllerE2EIT"},
		{"src/test/java/p/WelcomeControllerE2EIT.java", "WelcomeControllerE2ETest", "WelcomeControllerE2EIT"},
		{"src/test/java/p/VetControllerE2EIT.java", "VetControllerE2ETest", "VetControllerE2EIT"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			content := "package p;\n\npublic class " + tc.declared + " {\n\t@Test\n\tvoid a() {\n\t}\n}\n"
			got, old, changed := enforcePrimaryTypeName(tc.path, content)
			if !changed {
				t.Fatalf("mismatch not corrected: %q", got)
			}
			if old != tc.declared {
				t.Errorf("old name = %q, want %q", old, tc.declared)
			}
			if !strings.Contains(got, "public class "+tc.want+" {") {
				t.Errorf("type not renamed:\n%s", got)
			}
			if strings.Contains(got, tc.declared) {
				t.Errorf("old name still present:\n%s", got)
			}
		})
	}
}

// Self-references travel with the declaration, or the rename trades one compile error for another.
func TestEnforcePrimaryTypeName_rewritesSelfReferences(t *testing.T) {
	content := "package p;\n\npublic class FooE2ETests {\n" +
		"\tprivate static final Logger LOG = LoggerFactory.getLogger(FooE2ETests.class);\n" +
		"\tFooE2ETests() { }\n" +
		"}\n"
	got, _, changed := enforcePrimaryTypeName("src/test/java/p/FooE2EIT.java", content)
	if !changed {
		t.Fatal("expected a rename")
	}
	for _, want := range []string{"public class FooE2EIT {", "FooE2EIT.class", "FooE2EIT() { }"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

// A name that merely CONTAINS the old one must not be rewritten.
func TestEnforcePrimaryTypeName_wholeWordOnly(t *testing.T) {
	content := "package p;\n\npublic class FooTests {\n\tFooTestsHelper h;\n\tMyFooTests m;\n}\n"
	got, _, changed := enforcePrimaryTypeName("src/test/java/p/FooIT.java", content)
	if !changed {
		t.Fatal("expected a rename")
	}
	for _, want := range []string{"public class FooIT {", "FooTestsHelper h;", "MyFooTests m;"} {
		if !strings.Contains(got, want) {
			t.Errorf("whole-word boundary violated, missing %q:\n%s", want, got)
		}
	}
}

func TestEnforcePrimaryTypeName_noOpCases(t *testing.T) {
	cases := map[string]struct{ path, content string }{
		"already matches":                 {"src/test/java/p/FooTests.java", "package p;\npublic class FooTests {}\n"},
		"package-private already matches": {"src/test/java/p/FooIT.java", "package p;\nclass FooIT {}\n"},
		"no type declaration":             {"src/test/java/p/FooIT.java", "package p;\n"},
		"unsupported lang":                {"src/test/foo.ts", "export class FooTests {}\n"},
		// A @Nested inner class is indented and legitimately named for its scenario, not the file.
		"nested inner class": {
			"src/test/java/p/FooIT.java",
			"package p;\n\nclass FooIT {\n\t@Nested\n\tclass ProcessCreationFormHasErrors {\n\t}\n}\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, _, changed := enforcePrimaryTypeName(tc.path, tc.content)
			if changed {
				t.Errorf("unexpected rename:\n%s", got)
			}
		})
	}
}

func TestEnforcePrimaryTypeName_csharp(t *testing.T) {
	content := "namespace P;\n\npublic class FooE2ETests\n{\n}\n"
	got, old, changed := enforcePrimaryTypeName("tests/P.Tests/FooE2EIT.cs", content)
	if !changed || old != "FooE2ETests" {
		t.Fatalf("changed=%v old=%q", changed, old)
	}
	if !strings.Contains(got, "public class FooE2EIT") {
		t.Errorf("C# type not renamed:\n%s", got)
	}
}

// End to end: the artifact reaching disk must compile-legally match its filename.
func TestWriteGeneratedFiles_correctsTypeNameOnDisk(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/org/springframework/samples/petclinic/owner/OwnerControllerE2EIT.java"
	n, _, _ := Write(repo, []Item{{
		Path:             rel,
		Content:          "package org.springframework.samples.petclinic.owner;\n\npublic class OwnerControllerE2ETests {\n\t@Test\n\tvoid a() {\n\t}\n}\n",
		SourceSymbolFile: "src/main/java/org/springframework/samples/petclinic/owner/OwnerController.java",
	}})
	if n != 1 {
		t.Fatalf("wrote %d file(s), want 1", n)
	}
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "public class OwnerControllerE2EIT {") {
		t.Errorf("javac would reject this file (\"class X is public, should be declared in a file named X.java\"):\n%s", got)
	}
}

// C2. The public-only rule missed the case that actually occurred. Spring Petclinic's test classes
// are package-private, so when run api-c3e4a6ea003d0f9b1aeb487b4a8faec6 generated
// `class WelcomeControllerE2ETest` inside WelcomeControllerE2EIT.java the rename never fired: javac
// accepted it, and the mismatch surfaced downstream as diagnostics naming a class no file is named
// after.
func TestEnforcePrimaryTypeName_renamesPackagePrivateType(t *testing.T) {
	content := "package org.springframework.samples.petclinic.system;\n\nclass WelcomeControllerE2ETest {\n\t@Test\n\tvoid t() {}\n}\n"
	got, old, changed := enforcePrimaryTypeName(
		"src/test/java/org/springframework/samples/petclinic/system/WelcomeControllerE2EIT.java", content)
	if !changed {
		t.Fatal("a package-private top-level type must be renamed to match its file")
	}
	if old != "WelcomeControllerE2ETest" {
		t.Errorf("old = %q", old)
	}
	if !strings.Contains(got, "class WelcomeControllerE2EIT {") {
		t.Errorf("not renamed:\n%s", got)
	}
}

// When a file declares both a public and a package-private top-level type, the public one is the
// one javac binds to the filename and must win.
func TestEnforcePrimaryTypeName_prefersPublicWhenBothPresent(t *testing.T) {
	content := "package p;\n\nclass Helper {}\n\npublic class WrongName {\n}\n"
	got, old, changed := enforcePrimaryTypeName("src/test/java/p/FooIT.java", content)
	if !changed || old != "WrongName" {
		t.Fatalf("changed=%v old=%q, want the PUBLIC type renamed", changed, old)
	}
	if !strings.Contains(got, "class Helper {}") {
		t.Errorf("the package-private helper must be left alone:\n%s", got)
	}
}

// C2. Package declaration must match the directory. The E2E bootstrap plants a smoke test under
// com/asqs/e2e/, and the generator copied that package into an artifact living elsewhere.
func TestEnforceJavaPackageMatchesPath(t *testing.T) {
	t.Run("rewrites a wrong package", func(t *testing.T) {
		content := "package com.asqs.e2e;\n\nclass WelcomeControllerE2EIT {}\n"
		got, old, changed := enforceJavaPackageMatchesPath(
			"src/test/java/org/springframework/samples/petclinic/system/WelcomeControllerE2EIT.java", content)
		if !changed || old != "com.asqs.e2e" {
			t.Fatalf("changed=%v old=%q", changed, old)
		}
		if !strings.Contains(got, "package org.springframework.samples.petclinic.system;") {
			t.Errorf("package not rewritten:\n%s", got)
		}
	})

	noop := map[string]struct{ path, content string }{
		"already correct":     {"src/test/java/p/q/FooIT.java", "package p.q;\nclass FooIT {}\n"},
		"no package decl":     {"src/test/java/p/FooIT.java", "class FooIT {}\n"},
		"unrecognised layout": {"weird/place/FooIT.java", "package anything.at.all;\nclass FooIT {}\n"},
		"not java":            {"tests/P/FooIT.cs", "namespace Other;\nclass FooIT {}\n"},
		"source root only":    {"src/test/java/FooIT.java", "package p;\nclass FooIT {}\n"},
	}
	for name, tc := range noop {
		t.Run("no-op: "+name, func(t *testing.T) {
			got, _, changed := enforceJavaPackageMatchesPath(tc.path, tc.content)
			if changed {
				t.Errorf("unexpected rewrite:\n%s", got)
			}
		})
	}
}

func TestJavaPackageForPath(t *testing.T) {
	cases := map[string]string{
		"src/test/java/org/springframework/samples/petclinic/system/FooIT.java": "org.springframework.samples.petclinic.system",
		"src/main/java/com/example/App.java":                                    "com.example",
		"src/it/java/a/b/FooIT.java":                                            "a.b",
		"module/src/test/java/x/y/FooIT.java":                                   "x.y",
		"src/test/java/FooIT.java":                                              "",
		"no/root/here/FooIT.java":                                               "",
		"":                                                                      "",
		// A directory segment that is not a legal Java identifier means the layout is not what we
		// think it is; guessing would risk corrupting a correct package.
		"src/test/java/a/9bad/FooIT.java": "",
		"src/test/java/a/b-c/FooIT.java":  "",
	}
	for path, want := range cases {
		if got := javaPackageForPath(path); got != want {
			t.Errorf("javaPackageForPath(%q) = %q, want %q", path, got, want)
		}
	}
}
