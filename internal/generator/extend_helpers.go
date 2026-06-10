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
