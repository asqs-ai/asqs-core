package generator

import (
	"path/filepath"
	"strings"
)

// ExtendExistingTestContextPrefix is prepended to the user context when an existing test file is
// loaded so the model appends new methods only. (Ported from the engine's orchestrator.)
const ExtendExistingTestContextPrefix = `Existing test file (append new tests only):

`

// repoRelPathsEqual compares two repo-relative paths case-insensitively after slash-normalisation.
func repoRelPathsEqual(a, b string) bool {
	return strings.EqualFold(filepath.ToSlash(strings.TrimSpace(a)), filepath.ToSlash(strings.TrimSpace(b)))
}

// ExtendExistingTestContextSuffix is appended after the existing file's body: it scopes the model
// to NEW methods only and tells it how to declare imports it needs, since a methods-only payload
// has nowhere else to put them and they are lifted into the real import block on merge.
const ExtendExistingTestContextSuffix = `

Generate ONLY the additional test method(s) for the symbol below. Do not repeat or modify the existing tests above. Output only the new test method(s) or inner class to be appended to this file.

If your new tests need a symbol the existing file does not already import, put those import/using line(s) FIRST, at column 0, before the method(s) — including 'import static ...' (Java) and 'using static ...' / 'global using ...' / 'using Alias = ...' (C#). They are lifted into the file's real import block automatically — do not repeat imports the existing file already has, and do not emit a package/namespace line or a class header.

`

// ExtendExistingRedirectPrefix is prepended when the generated artifact was redirected to a
// repository test file that already exists on disk (see ExistingOrSuggestedTestPath). The wording
// forbids creating a sibling with the default convention (XTest.java / x.test.ts) and scopes the
// work to branch gaps reported in the retrieval context, matching the existing-test-coverage hint
// produced by buildExistingTestCoverageHint.
//
// It must agree with ExtendExistingTestContextSuffix, which is appended after it: the payload is spliced
// verbatim into the existing type's body by insertInsideClassBody, so a full compilation unit
// (package/imports/class header) would land *inside* the class and break the file. An earlier
// revision asked for "a single merged file" here while the suffix asked for methods only; that
// contradiction is what produced `illegal start of type` at the spliced import lines.
const ExtendExistingRedirectPrefix = `The repository already has a test file at %q for this source. Preserve every existing test verbatim and add ONLY tests that cover the branch gaps listed in the retrieval context. Your output is inserted INSIDE that file's existing test class body, so emit only the new test method(s): no package or namespace line, no class header, and no trailing closing brace. If a new test needs a symbol the existing file does not import, put the import/using line(s) FIRST at column 0 — they are lifted into the file's import block automatically rather than spliced into the class. Do NOT create a new file using the default naming convention (e.g. XTest.java or x.test.ts).

`
