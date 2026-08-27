package apisurface

import (
	"fmt"
	"strings"
	"testing"
)

func TestPregenerateTargets_scope(t *testing.T) {
	cases := []struct {
		name      string
		lang      string
		framework string
		isE2E     bool
		wantCount int
	}{
		// Three independent groups. Java carries 5 framework-annotation symbols on EVERY gap
		// (the Boot 3 -> Boot 4 package moves); Playwright E2E adds the assertion types —
		// 4 for Java and .NET (static factory + three assertion types), 3 for TypeScript
		// (expect() is a function, so there is no factory type), and Java adds a fifth, AssertJ's
		// Assertions, because the Playwright four cannot assert on a plain value and the block
		// they are rendered in claims to be exhaustive; and an API-driven E2E gap adds the REQUEST
		// types, 5 for Java and 4 for .NET. TypeScript gets none of the third group: its default
		// E2E profile is browser-driven (see e2eRequestTargets).
		{name: "java playwright e2e", lang: "java", framework: "playwright", isE2E: true, wantCount: 5 + 5 + 5},
		{name: "case insensitive", lang: "Java", framework: "Playwright-Java", isE2E: true, wantCount: 5 + 5 + 5},
		{name: "java unit gap keeps the annotations", lang: "java", framework: "playwright", isE2E: false, wantCount: 5},
		{name: "java unit gap with no framework", lang: "java", framework: "", isE2E: false, wantCount: 5},
		{name: "cypress java keeps annotations, drops assertions", lang: "java", framework: "cypress", isE2E: true, wantCount: 5},
		{name: "csharp", lang: "csharp", framework: "playwright-dotnet", isE2E: true, wantCount: 4 + 4},
		{name: "csharp cs alias", lang: "cs", framework: "playwright", isE2E: true, wantCount: 4 + 4},
		{name: "csharp unit gap has no annotation group", lang: "csharp", framework: "playwright", isE2E: false, wantCount: 0},
		{name: "typescript", lang: "typescript", framework: "playwright", isE2E: true, wantCount: 3},
		{name: "javascript alias", lang: "js", framework: "playwright", isE2E: true, wantCount: 3},
		{name: "unknown language is out of scope", lang: "python", framework: "playwright", isE2E: true, wantCount: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PregenerateTargets(tc.lang, tc.framework, tc.isE2E)
			if len(got) != tc.wantCount {
				t.Fatalf("got %d targets, want %d: %+v", len(got), tc.wantCount, got)
			}
			for _, tgt := range got {
				switch tgt.Kind {
				case KindType:
					// Java and .NET assertion types are looked up by fully-qualified name.
					// TypeScript has no qualification to give: playwright's test.d.ts declares
					// `interface LocatorAssertions` at the top level of a module.
					if NormalizeLang(tc.lang) != LangNode && !strings.Contains(tgt.Name, ".") {
						t.Errorf("type target %+v should be fully qualified for %s", tgt, tc.lang)
					}
				case KindSymbol:
					// Annotations are searched by simple name precisely because the failure mode
					// is that the model does not know their package.
					if strings.Contains(tgt.Name, ".") {
						t.Errorf("symbol target %+v should be a bare simple name", tgt)
					}
				default:
					t.Errorf("unexpected target kind: %+v", tgt)
				}
			}
		})
	}
}

// The three assertion types the run invented members on must all be covered, in every binding —
// the same concepts carry different names per language, and a typo in one list is a silent miss.
func TestPregenerateTargets_coversTheInventedTypes(t *testing.T) {
	cases := []struct {
		lang  string
		want  []string
		notes string
	}{
		{
			lang: "java",
			want: []string{
				"com.microsoft.playwright.assertions.LocatorAssertions",     // hasTextContaining
				"com.microsoft.playwright.assertions.APIResponseAssertions", // hasStatus, hasHeader
				"com.microsoft.playwright.assertions.PlaywrightAssertions",  // assertThat factory
			},
		},
		{
			lang: "csharp",
			want: []string{
				"Microsoft.Playwright.ILocatorAssertions",
				"Microsoft.Playwright.IAPIResponseAssertions",
				"Microsoft.Playwright.Assertions",
			},
			notes: ".NET assertion types are I-prefixed interfaces; the factory is not",
		},
		{
			lang:  "typescript",
			want:  []string{"LocatorAssertions", "APIResponseAssertions"},
			notes: "TS interfaces are declared bare in playwright's test.d.ts",
		},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			names := map[string]bool{}
			for _, tgt := range PregenerateTargets(tc.lang, "playwright", true) {
				names[tgt.Name] = true
			}
			for _, want := range tc.want {
				if !names[want] {
					t.Errorf("missing %s (%s)", want, tc.notes)
				}
			}
		})
	}
}

func TestNewProviderForLang(t *testing.T) {
	cases := []struct {
		lang     string
		wantNil  bool
		wantType string
	}{
		{lang: "java", wantType: "*apisurface.JavaProvider"},
		{lang: "kotlin", wantType: "*apisurface.JavaProvider"},
		{lang: "csharp", wantType: "*apisurface.CSharpProvider"},
		{lang: "cs", wantType: "*apisurface.CSharpProvider"},
		{lang: "typescript", wantType: "*apisurface.NodeProvider"},
		{lang: "js", wantType: "*apisurface.NodeProvider"},
		{lang: "python", wantNil: true},
		{lang: "", wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			got := NewProviderForLang(tc.lang)
			if tc.wantNil {
				// Must be an untyped nil: both consumers check `provider == nil` to mean
				// "render no block", and a typed nil would pass that check and then panic.
				if got != nil {
					t.Fatalf("got %T, want an untyped nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %s", tc.wantType)
			}
			if gotType := typeNameOf(got); gotType != tc.wantType {
				t.Errorf("got %s, want %s", gotType, tc.wantType)
			}
		})
	}
}

func TestRenderSurfaces(t *testing.T) {
	t.Run("empty input renders nothing", func(t *testing.T) {
		if got := RenderSurfaces(nil); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("zero-member surfaces render as import lines", func(t *testing.T) {
		// An annotation resolves to no members, and the fully-qualified name IS the fact the model
		// is missing: run api-c3e4a6ea003d0f9b1aeb487b4a8faec6 emitted the Boot 3 package for
		// LocalServerPort against a Boot 4 project.
		surfaces := []TypeSurface{{FQCN: "org.springframework.boot.web.server.test.LocalServerPort"}}
		got := RenderSurfaces(surfaces)
		if !strings.Contains(got, "org.springframework.boot.web.server.test.LocalServerPort") {
			t.Errorf("rendered block must carry the FQN:\n%s", got)
		}
		if !strings.Contains(got, "import ") {
			t.Errorf("a member-less type must be presented as an import:\n%s", got)
		}
	})

	t.Run("members and completeness are stated", func(t *testing.T) {
		surfaces := []TypeSurface{{
			FQCN:    "com.microsoft.playwright.assertions.APIResponseAssertions",
			Members: []string{"public void isOK();", "public APIResponseAssertions not();"},
			Origin:  "playwright-1.49.0.jar",
		}}
		got := RenderSurfaces(surfaces)
		for _, want := range []string{
			"APIResponseAssertions",
			"public void isOK();",
			"playwright-1.49.0.jar",
			"complete member list",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("rendered block missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "truncated") {
			t.Error("a complete list must not claim truncation")
		}
	})

	t.Run("truncation is stated so absence is not read as proof", func(t *testing.T) {
		surfaces := []TypeSurface{{
			FQCN:      "com.microsoft.playwright.assertions.LocatorAssertions",
			Members:   []string{"public void hasText(java.lang.String);"},
			Truncated: true,
		}}
		got := RenderSurfaces(surfaces)
		if !strings.Contains(got, "truncated") || !strings.Contains(got, "NOT proof") {
			t.Errorf("truncated list must say so explicitly:\n%s", got)
		}
		if strings.Contains(got, "complete member list") {
			t.Error("a truncated list must not also claim completeness")
		}
	})

	t.Run("mixed: import lines and member lists coexist", func(t *testing.T) {
		surfaces := []TypeSurface{
			{FQCN: "a.b.AnnotationOnly"},
			{FQCN: "a.b.Real", Members: []string{"public void x();"}},
		}
		got := RenderSurfaces(surfaces)
		if !strings.Contains(got, "a.b.AnnotationOnly") {
			t.Error("member-less type must still be rendered as an import")
		}
		if !strings.Contains(got, "a.b.Real") || !strings.Contains(got, "public void x();") {
			t.Error("member-bearing surface must keep its member list")
		}
	})
}

func typeNameOf(v any) string { return fmt.Sprintf("%T", v) }

// C1. Both runs on record lost their first compile round to a Spring Boot 3 import path emitted
// against a Boot 4.0.1 project: WebMvcTest in api-d4895d20922fd19a9a35fab4ec5dea88, LocalServerPort
// in api-c3e4a6ea003d0f9b1aeb487b4a8faec6. These annotations must be resolved for every Java gap,
// not just E2E ones — @WebMvcTest is a unit-test annotation.
func TestPregenerateTargets_coversSpringBootPackageMoves(t *testing.T) {
	for _, isE2E := range []bool{false, true} {
		names := map[string]bool{}
		for _, tgt := range PregenerateTargets("java", "playwright", isE2E) {
			names[tgt.Name] = true
		}
		for _, want := range []string{"WebMvcTest", "LocalServerPort"} {
			if !names[want] {
				t.Errorf("isE2E=%v: missing %s; this annotation moved package in Spring Boot 4", isE2E, want)
			}
		}
	}
}

// The annotation group is Java-only: .NET and TypeScript have no equivalent stale-package problem
// in this codebase's evidence, and adding speculative targets would spend lookup budget for nothing.
func TestPregenerateTargets_annotationsAreJavaOnly(t *testing.T) {
	for _, lang := range []string{"csharp", "typescript", "python"} {
		for _, tgt := range PregenerateTargets(lang, "playwright", false) {
			if tgt.Kind == KindSymbol {
				t.Errorf("%s: unexpected annotation target %+v", lang, tgt)
			}
		}
	}
}
