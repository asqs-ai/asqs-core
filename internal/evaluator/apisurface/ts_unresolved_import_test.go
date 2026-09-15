package apisurface

import (
	"strings"
	"testing"
)

const tsRepairTarget = `import { describe, it, expect } from 'vitest';
import { render } from './helpers';

describe('cart', () => {
  it('works', () => { expect(render()).toBeTruthy(); });
});
`

func tsAddImport(line string) string {
	return strings.Replace(tsRepairTarget, "import { render } from './helpers';",
		"import { render } from './helpers';\n"+line, 1)
}

const tsManifest = `{
  "name": "shop-web",
  "dependencies": { "react": "^18.2.0", "msw": "^2.0.0" },
  "devDependencies": { "vitest": "^1.0.0", "@testing-library/react": "^14.0.0" }
}
`

// The C# and Java arms both refuse a repair that invents a dependency; TS/JS had no such check at
// all, so a round that added `import { z } from 'zod'` to a repo without zod was written to disk and
// cost a full compile round to discover.
func TestTSIntroducedUnresolvedImport_refusesPackageNotInManifest(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{"package.json": tsManifest})
	after := tsAddImport("import { z } from 'zod';")

	reason := TSIntroducedUnresolvedImportReason(tsRepairTarget, after, root)
	if reason == "" {
		t.Fatal("accepted an import of a package the repository does not depend on")
	}
	if !strings.Contains(reason, "zod") {
		t.Errorf("the reason does not name the package:\n%s", reason)
	}
}

// Every form a specifier legitimately takes must pass: a plain dependency, a dev dependency, a
// scoped package, and a subpath export of a declared package.
func TestTSIntroducedUnresolvedImport_acceptsDeclaredDependencies(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{"package.json": tsManifest})
	for _, line := range []string{
		"import React from 'react';",
		"import { screen } from '@testing-library/react';",
		"import { setupServer } from 'msw/node';",
		"const rtl = require('@testing-library/react');",
		"export { http } from 'msw';",
		"const mod = await import('react');",
	} {
		if reason := TSIntroducedUnresolvedImportReason(tsRepairTarget, tsAddImport(line), root); reason != "" {
			t.Errorf("refused a declared dependency in %q:\n%s", line, reason)
		}
	}
}

// Node builtins are provided by the runtime and appear in no manifest, in both the bare and the
// node: spellings.
func TestTSIntroducedUnresolvedImport_acceptsNodeBuiltins(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{"package.json": tsManifest})
	for _, line := range []string{
		"import { readFile } from 'node:fs/promises';",
		"import path from 'path';",
		"import { EventEmitter } from 'events';",
	} {
		if reason := TSIntroducedUnresolvedImportReason(tsRepairTarget, tsAddImport(line), root); reason != "" {
			t.Errorf("refused a Node builtin in %q:\n%s", line, reason)
		}
	}
}

// Relative and rooted specifiers name a file, not a package; a tsconfig path alias names one too.
// None of them can be judged against a manifest, and judging them anyway refuses correct repairs.
func TestTSIntroducedUnresolvedImport_acceptsRelativeAndAliasedPaths(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		"package.json": tsManifest,
		"tsconfig.json": `{
  "compilerOptions": { "paths": { "@/*": ["./src/*"], "~utils": ["./src/utils.ts"] } }
}
`,
	})
	for _, line := range []string{
		"import { a } from '../fixtures/a';",
		"import { b } from '@/components/Cart';",
		"import { c } from '~utils';",
	} {
		if reason := TSIntroducedUnresolvedImportReason(tsRepairTarget, tsAddImport(line), root); reason != "" {
			t.Errorf("refused a path-like specifier in %q:\n%s", line, reason)
		}
	}
}

// A workspace sibling is resolvable by name without appearing in any dependencies block.
func TestTSIntroducedUnresolvedImport_acceptsWorkspaceSibling(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{
		"package.json":             `{"name": "root", "workspaces": ["packages/*"]}`,
		"packages/ui/package.json": `{"name": "@shop/ui", "version": "1.0.0"}`,
	})
	after := tsAddImport("import { Button } from '@shop/ui';")
	if reason := TSIntroducedUnresolvedImportReason(tsRepairTarget, after, root); reason != "" {
		t.Fatalf("refused a workspace sibling package:\n%s", reason)
	}
}

// Only what the round ADDED is judged. A file that already fails on an unresolvable import must
// still be repairable, or the reference no one can fix blocks every other repair in the file.
func TestTSIntroducedUnresolvedImport_onlyJudgesIntroduced(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{"package.json": tsManifest})
	before := "import { z } from 'zod';\n" + tsRepairTarget
	after := strings.Replace(before, "describe('cart'", "describe('basket'", 1)

	if reason := TSIntroducedUnresolvedImportReason(before, after, root); reason != "" {
		t.Fatalf("refused a round over an import it inherited:\n%s", reason)
	}
}

// With no manifest there is no evidence, and a negative claim with nothing behind it refuses
// correct repairs — the same rule the C# and Java arms follow.
func TestTSIntroducedUnresolvedImport_silentWithoutManifest(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{"src/index.ts": "export const a = 1;\n"})
	after := tsAddImport("import { z } from 'zod';")

	if reason := TSIntroducedUnresolvedImportReason(tsRepairTarget, after, root); reason != "" {
		t.Fatalf("claimed absence with no manifest to claim it from:\n%s", reason)
	}
}

// An import inside a comment is not an import.
func TestTSIntroducedUnresolvedImport_ignoresComments(t *testing.T) {
	root := writeCSharpRepo(t, map[string]string{"package.json": tsManifest})
	after := tsAddImport("// import { z } from 'zod';")

	if reason := TSIntroducedUnresolvedImportReason(tsRepairTarget, after, root); reason != "" {
		t.Fatalf("read a commented-out import as a real one:\n%s", reason)
	}
}
