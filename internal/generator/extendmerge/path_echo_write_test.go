package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A generated artifact whose first line is the artifact's own path must reach disk WITHOUT that
// line, in every language.
//
// The shape is the model labelling a code block whose fence it then forgot to write, and it fails
// differently per language — which is why the repair belongs here rather than in a per-language
// gate. On Java the stray token above the type declaration is a compile error javac reports as
// "class, interface, enum, or record expected". On TypeScript nothing rejects it at all, because
// `src/app/AppLayout.test.tsx` alone is valid TS (a chain of divisions over undeclared
// identifiers): an asqs-go React run wrote the file, vitest could not collect it
// (`ReferenceError: src is not defined`, reported as `(0 test)`), and the fix loop spent six
// rounds on it.
func TestWrite_stripsLeadingPathEcho(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		body      string
		wantFirst string
	}{
		{
			name:      "typescript",
			path:      "src/app/AppLayout.test.tsx",
			body:      "import { render, screen } from '@testing-library/react';\nimport { describe, it, expect } from 'vitest';\n\ndescribe('AppLayout', () => {\n\tit('renders', () => {\n\t\texpect(1).toBe(1);\n\t});\n});\n",
			wantFirst: "import { render, screen } from '@testing-library/react';",
		},
		{
			name:      "java",
			path:      "src/test/java/p/PetTests.java",
			body:      "package p;\n\nclass PetTests {\n\t@Test\n\tvoid t() {}\n}\n",
			wantFirst: "package p;",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			n, paths, skips := Write(dir, []Item{{
				Path:    tc.path,
				Content: tc.path + "\n" + tc.body,
			}})
			if len(skips) != 0 {
				t.Fatalf("payload was refused: %v", skips)
			}
			if n != 1 || len(paths) != 1 {
				t.Fatalf("expected one write, got n=%d paths=%v", n, paths)
			}
			got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(tc.path)))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.body {
				t.Fatalf("on disk =\n%q\nwant\n%q", got, tc.body)
			}
			if first := strings.SplitN(string(got), "\n", 2)[0]; first != tc.wantFirst {
				t.Errorf("first line = %q, want %q", first, tc.wantFirst)
			}
		})
	}
}
