package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The LLM sometimes opens an artifact with the artifact's own path, as if labelling a code block
// whose fence it then forgot to write. Run an asqs-go React run shipped
// src/app/AppLayout.test.tsx with `src/app/AppLayout.test.tsx` on line 1 — which is VALID
// TypeScript (it parses as `src / app / AppLayout.test.tsx`, a chain of divisions), so no
// structural gate refused it. vitest failed to collect the suite with `ReferenceError: src is not
// defined`, reported it as `(0 test)`, and the six fixer rounds that followed were spent on a file
// one deleted line would have fixed.
func TestStripLeadingPathEcho_removesTheEcho(t *testing.T) {
	cases := []struct{ name, path, first string }{
		{"typescript", "src/app/AppLayout.test.tsx", "src/app/AppLayout.test.tsx"},
		{"java", "src/test/java/p/PetTests.java", "src/test/java/p/PetTests.java"},
		{"base name only", "src/app/AppLayout.test.tsx", "AppLayout.test.tsx"},
		{"trailing colon", "src/app/AppLayout.test.tsx", "src/app/AppLayout.test.tsx:"},
		{"backticked", "src/app/AppLayout.test.tsx", "`src/app/AppLayout.test.tsx`"},
		{"container prefix", "src/app/AppLayout.test.tsx", "/workspace/src/app/AppLayout.test.tsx"},
		{"windows separators", "src/app/AppLayout.test.tsx", `src\app\AppLayout.test.tsx`},
		{"indented", "src/app/AppLayout.test.tsx", "   src/app/AppLayout.test.tsx  "},
	}
	const body = "import { render } from '@testing-library/react';\n\ndescribe('x', () => {});\n"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stripped := StripLeadingPathEcho(tc.path, tc.first+"\n"+body)
			if !stripped {
				t.Fatal("echo not stripped")
			}
			if got != body {
				t.Fatalf("content =\n%q\nwant\n%q", got, body)
			}
		})
	}
}

// Blank lines above the echo are part of the same accident.
func TestStripLeadingPathEcho_skipsLeadingBlankLines(t *testing.T) {
	const path = "src/app/AppLayout.test.tsx"
	const body = "import x from 'y';\n"
	got, stripped := StripLeadingPathEcho(path, "\n\n"+path+"\n"+body)
	if !stripped {
		t.Fatal("echo not stripped")
	}
	if got != body {
		t.Fatalf("content = %q, want %q", got, body)
	}
}

// The bar is "the line is nothing but the path". Anything that merely mentions the path is real
// source: a header comment is idiomatic, and an import naming the file must survive untouched.
// Both error directions are not equal here — a false positive silently deletes a line of a correct
// artifact — so the match has to stay exact.
func TestStripLeadingPathEcho_leavesRealSourceAlone(t *testing.T) {
	const path = "src/app/AppLayout.test.tsx"
	cases := []struct{ name, content string }{
		{"line comment naming the file", "// src/app/AppLayout.test.tsx\nimport x from 'y';\n"},
		{"block comment naming the file", "/* src/app/AppLayout.test.tsx */\nimport x from 'y';\n"},
		{"import mentioning the path", "import x from './src/app/AppLayout.test.tsx';\n"},
		{"a different file's path", "src/app/router.test.tsx\nimport x from 'y';\n"},
		{"ordinary source", "import { render } from '@testing-library/react';\n"},
		{"empty", ""},
		{"blank lines only", "\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stripped := StripLeadingPathEcho(path, tc.content)
			if stripped {
				t.Fatalf("stripped a line it should not have; result = %q", got)
			}
			if got != tc.content {
				t.Fatalf("content mutated: %q -> %q", tc.content, got)
			}
		})
	}
}

// Only the first line. A second occurrence lower down is inside the file's own body, where this
// function has no business guessing.
func TestStripLeadingPathEcho_onlyTheFirstLine(t *testing.T) {
	const path = "src/app/AppLayout.test.tsx"
	content := path + "\nimport x from 'y';\n" + path + "\n"
	want := "import x from 'y';\n" + path + "\n"
	got, stripped := StripLeadingPathEcho(path, content)
	if !stripped {
		t.Fatal("echo not stripped")
	}
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// An empty path cannot be echoed, and must never make every first line look like one.
func TestStripLeadingPathEcho_emptyPath(t *testing.T) {
	const content = "import x from 'y';\n"
	if got, stripped := StripLeadingPathEcho("", content); stripped || got != content {
		t.Fatalf("StripLeadingPathEcho(\"\", …) = %q, %v", got, stripped)
	}
}

// The fixer emits the echo too, and there SyntacticShellReason runs before the write — so on Java
// the round was refused outright ("stray token … before the first type declaration") and produced
// nothing, while on TypeScript the echo sailed through and broke the file it was repairing. The
// strip has to happen ahead of the gate on this path as well.
func TestApplyLLMFix_stripsLeadingPathEcho(t *testing.T) {
	cases := []struct {
		name, rel, before, after string
	}{
		{
			name:   "typescript",
			rel:    "src/app/AppLayout.test.tsx",
			before: "import { describe, it, expect } from 'vitest';\n\ndescribe('AppLayout', () => {\n\tit('renders', () => {\n\t\texpect(missingSymbol).toBe(1);\n\t});\n});\n",
			after:  "import { describe, it, expect } from 'vitest';\n\ndescribe('AppLayout', () => {\n\tit('renders', () => {\n\t\texpect(1).toBe(1);\n\t});\n});\n",
		},
		{
			name:   "java",
			rel:    "src/test/java/p/PetTests.java",
			before: "package p;\n\nimport org.junit.jupiter.api.Test;\n\nclass PetTests {\n\n\t@Test\n\tvoid t() {\n\t\tassertThat(missingSymbol).isTrue();\n\t}\n\n}\n",
			after:  "package p;\n\nimport org.junit.jupiter.api.Test;\n\nclass PetTests {\n\n\t@Test\n\tvoid t() {\n\t\tassertThat(true).isTrue();\n\t}\n\n}\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			full := filepath.Join(repo, filepath.FromSlash(tc.rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(tc.before), 0o644); err != nil {
				t.Fatal(err)
			}
			fixer := &stubFixer{resp: FixResponse{Files: map[string]string{tc.rel: tc.rel + "\n" + tc.after}}}
			opts := EvalOptions{RepoPath: repo, Fixer: fixer, ArtifactPaths: []string{tc.rel}}

			applied, touched, skip := applyLLMFix(context.Background(), opts, StepCompile,
				"error in "+tc.rel+": cannot find name 'missingSymbol'\n", &recordingAuditor{}, new(int), 3, nil, "")

			if !applied || len(touched) != 1 {
				t.Fatalf("fix not applied (applied=%v touched=%v skip=%q)", applied, touched, skip)
			}
			onDisk, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			if string(onDisk) != tc.after {
				t.Fatalf("on disk =\n%q\nwant\n%q", onDisk, tc.after)
			}
		})
	}
}

// The echo is not always the whole line. Run a later asqs-go Java run lost
// PetTests.java to the stray-token gate a SECOND time, one run after StripLeadingPathEcho was
// added, because the matcher demanded the line be nothing but the path: it refused anything
// containing whitespace, to keep an import that merely mentions the file from being eaten.
//
// The audit pins the shape. javaStrayTokenBeforeTypeReason reports the offending line truncated at
// 60 runes, and it reported exactly the path's own first 60 characters — so the line STARTS with
// the path, and a decorating prefix ("- ", "File: ", "+++ ") is ruled out because it would have
// shown up in those 60 characters.
//
// Removing just the path token and keeping the remainder is right for both readings of what
// follows it: real source (the model ran the label into the first statement) is preserved, and
// trailing prose leaves the file no worse off than the refusal it gets today.
func TestStripLeadingPathEcho_removesTheEchoWhenTheLineContinues(t *testing.T) {
	const path = "src/test/java/p/PetTests.java"
	cases := []struct{ name, first, wantFirst string }{
		{"ran into the package declaration", path + " package p;", "package p;"},
		{"trailing prose", path + " (new file)", "(new file)"},
		{"trailing tab then code", path + "\tpackage p;", "package p;"},
		{"decorated and continued", "`" + path + "` package p;", "package p;"},
	}
	const rest = "\nclass PetTests {\n\t@Test void t() {}\n}\n"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stripped := StripLeadingPathEcho(path, tc.first+rest)
			if !stripped {
				t.Fatal("echo not stripped")
			}
			if want := tc.wantFirst + rest; got != want {
				t.Fatalf("content =\n%q\nwant\n%q", got, want)
			}
		})
	}
}

// The whitespace rule existed for a reason and the reason still holds: a line that merely MENTIONS
// the path is source, and only a line that BEGINS with it is a label.
func TestStripLeadingPathEcho_stillIgnoresMerelyMentioningLines(t *testing.T) {
	const path = "src/app/AppLayout.test.tsx"
	cases := []struct{ name, content string }{
		{"import mentioning the path", "import x from './src/app/AppLayout.test.tsx';\n"},
		{"prose mentioning the path", "// see src/app/AppLayout.test.tsx for details\nimport x from 'y';\n"},
		{"a longer path that merely starts the same", "src/app/AppLayout.test.tsx.bak is stale\nimport x from 'y';\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stripped := StripLeadingPathEcho(path, tc.content)
			if stripped {
				t.Fatalf("stripped a line it should not have; result = %q", got)
			}
			if got != tc.content {
				t.Fatalf("content mutated: %q -> %q", tc.content, got)
			}
		})
	}
}
