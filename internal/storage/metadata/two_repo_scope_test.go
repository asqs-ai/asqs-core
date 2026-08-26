package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Spec 30(e)'s headline regression test: a metadata database holding two repositories must answer
// every read for one of them without ever including the other's rows.
//
// This is the test the review asked for by name, and it is deliberately broad rather than deep. The
// leak it guards was never a single wrong query — it was that `symbols`, `edges` and `files` had no
// repository column at all, so EVERY lookup by name, path, language or id answered across the whole
// database. A test per query is what makes "one missed read is a remaining leak" checkable.

// twoRepoFixture indexes the SAME relative paths and the SAME fully-qualified names into two
// repositories, which is the arrangement that makes cross-repo bleed visible. Two Spring services,
// or two Angular apps, look exactly like this.
type twoRepoFixture struct {
	t        *testing.T
	s        *Store
	ctx      context.Context
	repoA    string
	repoB    string
	file     string
	testFile string
	// symbol ids by (repo, fqName)
	ids map[string]string
}

func newTwoRepoFixture(t *testing.T) *twoRepoFixture {
	t.Helper()
	conn, why := ScratchDBForTests()
	if conn == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	s, err := Open(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano()
	f := &twoRepoFixture{
		t:        t,
		s:        s,
		ctx:      ctx,
		repoA:    fmt.Sprintf("github.com/acme/a-%d", stamp),
		repoB:    fmt.Sprintf("github.com/acme/b-%d", stamp),
		file:     fmt.Sprintf("src/main/java/com/acme/Order%d.java", stamp),
		testFile: fmt.Sprintf("src/test/java/com/acme/Order%dTest.java", stamp),
		ids:      map[string]string{},
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, repo := range []string{f.repoA, f.repoB} {
			_, _ = s.db.Exec(bg, `DELETE FROM edges WHERE repo_id = $1`, repo)
			_, _ = s.db.Exec(bg, `DELETE FROM symbols WHERE repo_id = $1`, repo)
			_, _ = s.db.Exec(bg, `DELETE FROM files WHERE repo_id = $1`, repo)
		}
	})

	for _, repo := range []string{f.repoA, f.repoB} {
		f.seedRepo(repo)
	}
	return f
}

// seedRepo writes one production class + method and one test class, plus their files rows.
func (f *twoRepoFixture) seedRepo(repo string) {
	f.t.Helper()
	for _, fv := range []struct {
		path   string
		isTest bool
	}{{f.file, false}, {f.testFile, true}} {
		if err := f.s.UpsertFile(f.ctx, &File{
			File: fv.path, SHA: "sha-" + repo, Lang: "java", Module: "core", IsTest: fv.isTest, RepoID: repo,
		}); err != nil {
			f.t.Fatal(err)
		}
	}
	mk := func(fq, kind, path string) string {
		id, err := f.s.InsertSymbol(f.ctx, &Symbol{
			Lang: "java", Kind: kind, FQName: fq, File: path,
			StartLine: 1, EndLine: 9, RepoID: repo,
		})
		if err != nil {
			f.t.Fatal(err)
		}
		f.ids[repo+"|"+fq] = id
		return id
	}
	cls := mk("com.acme.Order", "class", f.file)
	meth := mk("com.acme.Order#place", "method", f.file)
	testCls := mk("com.acme.OrderTest", "class", f.testFile)
	if err := f.s.InsertEdge(f.ctx, &Edge{
		CallerSymbolID: cls, CalleeSymbolID: meth, EdgeType: "CONTAINS", RepoID: repo,
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.s.InsertEdge(f.ctx, &Edge{
		CallerSymbolID: testCls, CalleeSymbolID: meth, EdgeType: "CALLS", RepoID: repo,
	}); err != nil {
		f.t.Fatal(err)
	}
}

// assertAllFromRepo fails when any returned symbol belongs to another repository.
func (f *twoRepoFixture) assertAllFromRepo(what, repo string, syms []*Symbol) {
	f.t.Helper()
	other := f.repoA
	if repo == f.repoA {
		other = f.repoB
	}
	if len(syms) == 0 {
		f.t.Errorf("%s: returned nothing for %s; the scoping is too tight, not just safe", what, repo)
		return
	}
	for _, s := range syms {
		if s == nil {
			continue
		}
		if s.RepoID == other {
			f.t.Errorf("%s: leaked a symbol from %s while querying %s (%s)", what, other, repo, s.FQName)
		}
	}
}

func TestTwoRepos_symbolLookupsAreScoped(t *testing.T) {
	f := newTwoRepoFixture(t)

	// By fully-qualified name — the graph API's primary lookup, and the one behind finding F-7.
	got, err := f.s.ListSymbolsByFQName(f.ctx, f.repoA, "com.acme.Order")
	if err != nil {
		t.Fatal(err)
	}
	f.assertAllFromRepo("ListSymbolsByFQName", f.repoA, got)
	if len(got) != 1 {
		t.Errorf("ListSymbolsByFQName returned %d rows; both repositories declare this name, so "+
			"anything but 1 means the other repository's row came back too", len(got))
	}

	// By file — the same relative path exists in both repositories.
	got, err = f.s.ListSymbolsByFile(f.ctx, f.repoB, f.file)
	if err != nil {
		t.Fatal(err)
	}
	f.assertAllFromRepo("ListSymbolsByFile", f.repoB, got)

	// By language and kind — what gap listing enumerates.
	got, err = f.s.ListSymbolsByLang(f.ctx, f.repoA, "java", "method")
	if err != nil {
		t.Fatal(err)
	}
	f.assertAllFromRepo("ListSymbolsByLang", f.repoA, got)

	// By substring — the interactive `?q=` autocomplete.
	got, err = f.s.ListSymbolsByFQSubstring(f.ctx, f.repoA, "acme.Order", 50)
	if err != nil {
		t.Fatal(err)
	}
	f.assertAllFromRepo("ListSymbolsByFQSubstring", f.repoA, got)

	// By simple type name — the per-gap domain-model resolution.
	got, err = f.s.ListSymbolsByTypeSimpleName(f.ctx, f.repoA, "Order", 50)
	if err != nil {
		t.Fatal(err)
	}
	f.assertAllFromRepo("ListSymbolsByTypeSimpleName", f.repoA, got)
}

// GetSymbolByID takes an id straight from a URL, so it is the one lookup an outsider can aim.
func TestTwoRepos_getSymbolByIDRefusesAnotherRepositorysID(t *testing.T) {
	f := newTwoRepoFixture(t)
	idInB := f.ids[f.repoB+"|com.acme.Order"]
	if idInB == "" {
		t.Fatal("fixture did not record repo B's symbol id")
	}

	got, err := f.s.GetSymbolByID(f.ctx, f.repoA, idInB)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("GetSymbolByID answered repo A with a symbol belonging to repo B; a symbol id from " +
			"a URL is enough to read across the tenant boundary")
	}
	if got, err = f.s.GetSymbolByID(f.ctx, f.repoB, idInB); err != nil || got == nil {
		t.Fatalf("GetSymbolByID refused the owning repository: got=%v err=%v", got, err)
	}
}

func TestTwoRepos_gapListingIsScoped(t *testing.T) {
	f := newTwoRepoFixture(t)

	nonTest, err := f.s.ListSymbolsInNonTestFiles(f.ctx, f.repoA, "java", "method")
	if err != nil {
		t.Fatal(err)
	}
	f.assertAllFromRepo("ListSymbolsInNonTestFiles", f.repoA, nonTest)

	inTest, err := f.s.ListSymbolsInTestFiles(f.ctx, f.repoA, "java", "class")
	if err != nil {
		t.Fatal(err)
	}
	f.assertAllFromRepo("ListSymbolsInTestFiles", f.repoA, inTest)

	// The join is on (file, repo_id). With a file-only join, repo B marking the shared path as a
	// test file would hide repo A's symbols from gap analysis entirely.
	for _, s := range nonTest {
		if s != nil && s.File != f.file {
			t.Errorf("ListSymbolsInNonTestFiles returned a symbol from %q; the files join is wrong", s.File)
		}
	}
}

func TestTwoRepos_edgesAreScoped(t *testing.T) {
	f := newTwoRepoFixture(t)
	methA := f.ids[f.repoA+"|com.acme.Order#place"]
	clsA := f.ids[f.repoA+"|com.acme.Order"]

	in, err := f.s.GetEdgesTo(f.ctx, f.repoA, methA)
	if err != nil {
		t.Fatal(err)
	}
	if len(in) == 0 {
		t.Fatal("GetEdgesTo returned nothing for a symbol that has inbound edges")
	}
	for _, e := range in {
		if e != nil && e.RepoID != f.repoA {
			t.Errorf("GetEdgesTo leaked an edge from %q", e.RepoID)
		}
	}

	out, err := f.s.GetEdgesFrom(f.ctx, f.repoA, clsA)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range out {
		if e != nil && e.RepoID != f.repoA {
			t.Errorf("GetEdgesFrom leaked an edge from %q", e.RepoID)
		}
	}

	// The ExpandGraph assertions from upstream's version of this test arrive with the unified
	// graph-traversal port (CP12), which brings ExpandGraph itself.
}

func TestTwoRepos_countersAndFilesAreScoped(t *testing.T) {
	f := newTwoRepoFixture(t)

	symA, err := f.s.CountSymbols(f.ctx, f.repoA)
	if err != nil {
		t.Fatal(err)
	}
	if symA != 3 {
		t.Errorf("CountSymbols(repoA) = %d, want 3; a global count reports the whole database as "+
			"one repository's size", symA)
	}
	edgeA, err := f.s.CountEdges(f.ctx, f.repoA)
	if err != nil {
		t.Fatal(err)
	}
	if edgeA != 2 {
		t.Errorf("CountEdges(repoA) = %d, want 2", edgeA)
	}

	files, err := f.s.ListFiles(f.ctx, f.repoA, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("ListFiles(repoA) returned %d rows, want 2", len(files))
	}
	for _, fl := range files {
		if fl != nil && fl.RepoID != f.repoA {
			t.Errorf("ListFiles leaked a row from %q", fl.RepoID)
		}
	}

	// is_test-only filter: its own branch in ListFiles, and the one that was missed first time.
	isTest := true
	files, err = f.s.ListFiles(f.ctx, f.repoA, "", &isTest)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("ListFiles(repoA, is_test) returned %d rows, want 1", len(files))
	}
	for _, fl := range files {
		if fl != nil && fl.RepoID != f.repoA {
			t.Errorf("ListFiles(is_test) leaked a row from %q", fl.RepoID)
		}
	}

	// Same path, different repository, different metadata.
	fa, err := f.s.GetFile(f.ctx, f.repoA, f.file)
	if err != nil || fa == nil {
		t.Fatalf("GetFile(repoA): %v %v", fa, err)
	}
	fb, err := f.s.GetFile(f.ctx, f.repoB, f.file)
	if err != nil || fb == nil {
		t.Fatalf("GetFile(repoB): %v %v", fb, err)
	}
	if fa.SHA == fb.SHA {
		t.Error("both repositories' rows for the shared path report the same SHA; change detection " +
			"is reading whichever repository wrote last")
	}
}

// Deleting one repository's file must leave the other's symbols and files intact.
func TestTwoRepos_deleteByFileDoesNotCrossRepositories(t *testing.T) {
	f := newTwoRepoFixture(t)

	if _, err := f.s.DeleteSymbolsByFile(f.ctx, f.repoB, f.file); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DeleteFile(f.ctx, f.repoB, f.file); err != nil {
		t.Fatal(err)
	}

	got, err := f.s.ListSymbolsByFile(f.ctx, f.repoA, f.file)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("repo A's symbols for the shared path were destroyed by repo B's delete — this is " +
			"the original data-loss incident")
	}
	if fa, err := f.s.GetFile(f.ctx, f.repoA, f.file); err != nil || fa == nil {
		t.Errorf("repo A's files row was destroyed by repo B's delete: %v %v", fa, err)
	}
}

// The reindex warning must see rows that no scoped query can reach.
func TestUnscopedRows_areReportedByTheReindexWarning(t *testing.T) {
	conn, why := ScratchDBForTests()
	if conn == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	s, err := Open(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	path := fmt.Sprintf("legacy-unscoped-%d.java", time.Now().UnixNano())
	if _, err := s.InsertSymbol(ctx, &Symbol{
		Lang: "java", Kind: "class", FQName: "com.acme.Legacy", File: path, StartLine: 1, EndLine: 2,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Exec(context.Background(), `DELETE FROM symbols WHERE file = $1`, path)
	})

	counts, err := s.CountUnscopedRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Symbols == 0 {
		t.Fatal("CountUnscopedRows missed a symbol with an empty repo_id")
	}
	w := ReindexRequiredWarning(counts)
	if w == "" {
		t.Fatal("ReindexRequiredWarning stayed silent while unreadable rows exist")
	}
	if !contains(w, "--reindex-required") {
		t.Errorf("warning does not name the remedy: %q", w)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A repository with symbols but no `files` rows must be reported. This is the state a run lands in
// when file upserts fail — the count of UNSCOPED rows reads zero, which looks healthy, while every
// consumer of `files` sees an empty repository and the run plans nothing.
func TestReposMissingFileRows_detectsAnIndexWithNoFiles(t *testing.T) {
	conn, why := ScratchDBForTests()
	if conn == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	s, err := Open(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	repo := fmt.Sprintf("github.com/acme/nofiles-%d", time.Now().UnixNano())
	path := fmt.Sprintf("src/NoFiles%d.java", time.Now().UnixNano())
	if _, err := s.InsertSymbol(ctx, &Symbol{
		Lang: "java", Kind: "class", FQName: "com.acme.NoFiles", File: path,
		StartLine: 1, EndLine: 2, RepoID: repo,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.DeleteSymbolsByFile(bg, repo, path)
		_, _ = s.DeleteFile(bg, repo, path)
	})

	repos, err := s.ReposMissingFileRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range repos {
		if r == repo {
			found = true
		}
	}
	if !found {
		t.Fatalf("a repository with symbols and no files rows was not reported; got %v", repos)
	}
	if w := MissingFileRowsWarning(repos); w == "" {
		t.Error("MissingFileRowsWarning stayed silent for a broken index")
	}

	// Once the file row exists, the repository is healthy and must drop off the list.
	if err := s.UpsertFile(ctx, &File{File: path, SHA: "s", Lang: "java", RepoID: repo}); err != nil {
		t.Fatal(err)
	}
	repos, err = s.ReposMissingFileRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range repos {
		if r == repo {
			t.Error("a repository with file rows is still reported as missing them")
		}
	}
}
