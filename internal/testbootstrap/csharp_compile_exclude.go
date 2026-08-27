package testbootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// excludeTestRootFromProductionProjects stops production projects from compiling the dedicated test
// directory.
//
// SDK-style projects glob `**/*.cs` from their own directory downwards. When a production .csproj
// sits at the repository root — common in single-project repos — the tests/ directory created for
// generated tests is INSIDE that glob, so every test file is compiled twice: once by the test project
// (which has xUnit, Moq and FluentAssertions) and once by the production project (which has none).
// The result is CS0246 on Xunit/Moq/FluentAssertions attributed to the production project, and the
// test project cannot build because it references it.
//
// This was latent before bootstrap wrote a smoke test: the dedicated project was created empty and
// nothing collided until generation produced the first test file, at which point the failure landed
// in the evaluator instead — where the fix loop may not edit project files.
//
// `<Compile Remove="tests/**" />` is the documented MSBuild fix and the smallest possible edit.
func excludeTestRootFromProductionProjects(testDirAbs string, prodCsprojs []string) (changed []string, err error) {
	for _, csproj := range prodCsprojs {
		prodDir := filepath.Dir(csproj)
		rel, rerr := filepath.Rel(prodDir, testDirAbs)
		if rerr != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue // the test directory is not inside this project's glob
		}
		relSlash := filepath.ToSlash(rel)

		b, rerr := os.ReadFile(csproj)
		if rerr != nil {
			continue
		}
		s := string(b)
		if strings.Contains(strings.ToLower(s), strings.ToLower(`compile remove="`+relSlash+`/`)) {
			continue // already excluded
		}
		block := fmt.Sprintf("  <ItemGroup>\n    <!-- ASQS test_framework_bootstrap: generated tests live in %s and are compiled by the test project. -->\n    <Compile Remove=\"%s/**\" />\n  </ItemGroup>\n", relSlash, relSlash)
		out := insertBeforeClosingCsproj(s, block)
		if out == s {
			continue
		}
		if werr := atomicWrite(csproj, []byte(out)); werr != nil {
			return changed, werr
		}
		changed = append(changed, csproj)
	}
	return changed, nil
}
