package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAt(t *testing.T, repo, rel, body string) string {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

const existingJavaSuite = `package com.acme;

import org.junit.jupiter.api.Test;
import static org.assertj.core.api.Assertions.assertThat;

class OwnerTests {

	@Test
	void findsOwner() {
		assertThat(service.find(1)).isNotNull();
	}
}
`

// The whole point of extending: the new methods land INSIDE the existing type, and every symbol
// they introduce arrives imported. A naive splice ships a file that cannot compile, and the fixer
// then spends a round rediscovering imports the merge could have supplied.
func TestWrite_extendUnionsImportsAndSplicesInsideTheType(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/com/acme/OwnerTests.java"
	writeAt(t, repo, rel, existingJavaSuite)

	// A methods-only payload that needs two imports the file does not have.
	payload := `import java.util.List;
import static org.mockito.Mockito.verify;

	@Test
	void rejectsUnknownOwner() {
		List<String> ids = List.of("x");
		verify(repo).lookup(ids);
	}
`
	wrote, written, skips := Write(repo, []Item{{
		Path: rel, Content: payload, ExtendExisting: true,
		SourceSymbolFile: "src/main/java/com/acme/OwnerService.java",
	}})
	if wrote != 1 {
		t.Fatalf("extend did not land: wrote=%d skips=%v", wrote, skips)
	}
	if len(written) != 1 || written[0] != rel {
		t.Fatalf("written = %v, want the extended path", written)
	}

	got, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	body := string(got)

	if !strings.Contains(body, "rejectsUnknownOwner") {
		t.Fatal("the new test did not reach the file")
	}
	if !strings.Contains(body, "findsOwner") {
		t.Fatal("extending must not discard the existing tests")
	}
	// Imports unioned into the real import block, not left stranded in the class body.
	for _, want := range []string{"import java.util.List;", "import static org.mockito.Mockito.verify;"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q — the merge ships a file that cannot compile", want)
		}
	}
	idxImport := strings.Index(body, "import java.util.List;")
	idxType := strings.Index(body, "class OwnerTests")
	if idxImport < 0 || idxType < 0 || idxImport > idxType {
		t.Errorf("imports must precede the type declaration (import at %d, type at %d)", idxImport, idxType)
	}
	// Position: the payload must be inside the type body, not appended after its closing brace —
	// which stays brace-balanced and keeps its type declaration, so no syntactic gate can see it.
	idxNew := strings.Index(body, "rejectsUnknownOwner")
	if idxNew < idxType || idxNew > strings.LastIndex(body, "}") {
		t.Error("the new test landed outside the type body")
	}
	// The existing import survives exactly once — a union, not a duplication.
	if n := strings.Count(body, "import org.junit.jupiter.api.Test;"); n != 1 {
		t.Errorf("existing import appears %d time(s), want 1", n)
	}
}

// A payload that only restates members the file already declares gains nothing. Writing it back
// would report a successful extend for a gap that added no coverage.
func TestWrite_extendRefusesAnAllDuplicatePayload(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/com/acme/OwnerTests.java"
	writeAt(t, repo, rel, existingJavaSuite)

	payload := `	@Test
	void findsOwner() {
		assertThat(service.find(1)).isNotNull();
	}
`
	wrote, _, skips := Write(repo, []Item{{Path: rel, Content: payload, ExtendExisting: true}})
	if wrote != 0 {
		t.Fatal("a payload of only already-defined members must not count as a write")
	}
	if len(skips) != 1 || !strings.Contains(skips[0], "already-defined") {
		t.Errorf("the skip must say why: %v", skips)
	}
}

// Creating (not extending) must still refuse to overwrite a file that already exists, and must
// never write a unit test into production source.
func TestWrite_createRefusesOverwriteAndSourcePaths(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/com/acme/OwnerTests.java"
	writeAt(t, repo, rel, existingJavaSuite)

	wrote, _, skips := Write(repo, []Item{{Path: rel, Content: existingJavaSuite}})
	if wrote != 0 || len(skips) == 0 || !strings.Contains(skips[0], "already exists") {
		t.Fatalf("a create onto an existing path must be refused: wrote=%d skips=%v", wrote, skips)
	}

	src := "src/main/java/com/acme/OwnerService.java"
	wrote, _, skips = Write(repo, []Item{{
		Path: src, Content: "package com.acme;\nclass OwnerService {}\n", SourceSymbolFile: src,
	}})
	if wrote != 0 || len(skips) == 0 {
		t.Fatalf("writing tests into production source must be refused: wrote=%d skips=%v", wrote, skips)
	}
}
