package indexer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// ---- fixture builders ----

// writeSourcesJar creates <m2>/<group dirs>/<artifact>/<version>/<artifact>-<version>-sources.jar
// containing the given source files — the exact layout `mvn dependency:sources` populates.
func writeSourcesJar(t *testing.T, m2 string, c mavenCoord, files map[string]string) {
	t.Helper()
	dir := filepath.Join(append(append([]string{m2}, strings.Split(c.group, ".")...), c.artifact, c.version)...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(dir, c.artifact+"-"+c.version+"-sources.jar")
	if err := os.WriteFile(jar, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

const splitterJava = `package org.example.util;

import java.util.List;

/**
 * Utility for splitting strings on commas. Not thread safe.
 */
public final class Splitter {
    /** Splits by comma. */
    public static List<String> split(String input) { return null; }

    public int count() { return 0; }

    protected void reset() {}

    private void hidden() {}
}
`

// writeNuGetPackage creates <root>/<id lower>/<version lower>/lib/net8.0/<Id>.xml — the layout of
// the NuGet global packages folder.
func writeNuGetPackage(t *testing.T, root string, ref nugetRef, xmlDoc string) {
	t.Helper()
	dir := filepath.Join(root, strings.ToLower(ref.id), strings.ToLower(ref.version), "lib", "net8.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ref.id+".xml"), []byte(xmlDoc), 0o644); err != nil {
		t.Fatal(err)
	}
}

const widgetsXML = `<?xml version="1.0"?>
<doc>
  <assembly><name>Acme.Widgets</name></assembly>
  <members>
    <member name="T:Acme.Widgets.Widget">
      <summary>A spinnable widget.</summary>
    </member>
    <member name="M:Acme.Widgets.Widget.Spin(System.Int32)">
      <summary>Spins the widget the given number of times.</summary>
    </member>
    <member name="P:Acme.Widgets.Widget.Size">
      <summary>Widget size in units.</summary>
    </member>
    <member name="M:Acme.Widgets.Orphan.Run">
      <summary>Member without a type entry.</summary>
    </member>
  </members>
</doc>
`

// writeNodeModule creates <repo>/node_modules/<name>/package.json (+ .d.ts files).
func writeNodeModule(t *testing.T, repo, name, version, typesEntry string, dts map[string]string) {
	t.Helper()
	root := filepath.Join(repo, "node_modules", filepath.FromSlash(name))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := map[string]string{"name": name, "version": version}
	if typesEntry != "" {
		pkg["types"] = typesEntry
	}
	b, _ := json.Marshal(pkg)
	if err := os.WriteFile(filepath.Join(root, "package.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range dts {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// depDocEmbWriter records DeleteByFile and InsertChunks calls.
type depDocEmbWriter struct {
	deletes []string // "repoID|file"
	chunks  []*embeddings.Chunk
}

func (w *depDocEmbWriter) InsertChunks(_ context.Context, chunks []*embeddings.Chunk) ([]string, error) {
	w.chunks = append(w.chunks, chunks...)
	ids := make([]string, len(chunks))
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", len(w.chunks)+i)
	}
	return ids, nil
}
func (w *depDocEmbWriter) DeleteByFile(_ context.Context, repoID, file string) (int64, error) {
	w.deletes = append(w.deletes, repoID+"|"+file)
	kept := w.chunks[:0]
	var n int64
	for _, c := range w.chunks {
		if c.RepoID == repoID && c.File == file {
			n++
			continue
		}
		kept = append(kept, c)
	}
	w.chunks = kept
	return n, nil
}
func (w *depDocEmbWriter) DeleteByRepo(context.Context, string) (int64, error) { return 0, nil }
func (w *depDocEmbWriter) SetEmbeddingProvider(context.Context, string, string, int) error {
	return nil
}
func (w *depDocEmbWriter) CountChunksByRepo(context.Context, string) (int64, error) {
	return int64(len(w.chunks)), nil
}

// ---- manifest parsing ----

func TestParsePomDirectDeps(t *testing.T) {
	repo := t.TempDir()
	pom := `<?xml version="1.0"?>
<project>
  <properties>
    <spring.version>6.2.1</spring.version>
  </properties>
  <dependencies>
    <dependency>
      <groupId>org.example</groupId><artifactId>util</artifactId><version>1.0.0</version>
    </dependency>
    <dependency>
      <groupId>org.springframework</groupId><artifactId>spring-core</artifactId><version>${spring.version}</version>
    </dependency>
    <dependency>
      <groupId>org.unresolved</groupId><artifactId>managed</artifactId>
    </dependency>
    <dependency>
      <groupId>org.unresolved</groupId><artifactId>propmiss</artifactId><version>${no.such.prop}</version>
    </dependency>
    <dependency>
      <groupId>com.oracle</groupId><artifactId>ojdbc</artifactId><version>21.1</version><scope>system</scope>
    </dependency>
  </dependencies>
</project>`
	if err := os.WriteFile(filepath.Join(repo, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}
	// Module pom one level down, with a duplicate of the root dep (must dedupe).
	modDir := filepath.Join(repo, "core")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modPom := `<project><dependencies>
    <dependency><groupId>org.example</groupId><artifactId>util</artifactId><version>1.0.0</version></dependency>
    <dependency><groupId>org.example</groupId><artifactId>extra</artifactId><version>2.0.0</version></dependency>
  </dependencies></project>`
	if err := os.WriteFile(filepath.Join(modDir, "pom.xml"), []byte(modPom), 0o644); err != nil {
		t.Fatal(err)
	}

	got := parsePomDirectDeps(repo)
	var coords []string
	for _, c := range got {
		coords = append(coords, c.coordinate())
	}
	want := []string{
		"org.example:extra:2.0.0",
		"org.example:util:1.0.0",
		"org.springframework:spring-core:6.2.1",
	}
	if strings.Join(coords, ",") != strings.Join(want, ",") {
		t.Fatalf("parsePomDirectDeps = %v; want %v", coords, want)
	}
}

func TestParseCsprojPackageRefs(t *testing.T) {
	repo := t.TempDir()
	sub := filepath.Join(repo, "src", "App")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	csproj := `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Acme.Widgets" Version="1.2.0" />
    <PackageReference Include="xunit" Version="2.9.0" />
  </ItemGroup>
</Project>`
	if err := os.WriteFile(filepath.Join(sub, "App.csproj"), []byte(csproj), 0o644); err != nil {
		t.Fatal(err)
	}
	// bin/ must be skipped even if it contains a csproj copy.
	binDir := filepath.Join(repo, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "Copy.csproj"), []byte(`<PackageReference Include="Skipped" Version="9.9.9" />`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := parseCsprojPackageRefs(repo)
	if len(got) != 2 || got[0].id != "Acme.Widgets" || got[0].version != "1.2.0" || got[1].id != "xunit" {
		t.Fatalf("parseCsprojPackageRefs = %+v; want Acme.Widgets@1.2.0, xunit@2.9.0", got)
	}
}

func TestParsePackageJSONDeps_DirectOnly(t *testing.T) {
	repo := t.TempDir()
	pkg := `{"dependencies":{"react":"^18.3.1","left-pad":"1.3.0"},"devDependencies":{"jest":"^29.0.0"}}`
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	got := parsePackageJSONDeps(repo)
	if strings.Join(got, ",") != "left-pad,react" {
		t.Fatalf("parsePackageJSONDeps = %v; want [left-pad react] (devDependencies excluded)", got)
	}
}

// ---- extraction ----

func TestJavaDocsForCoord(t *testing.T) {
	m2 := t.TempDir()
	c := mavenCoord{group: "org.example", artifact: "util", version: "1.0.0"}
	writeSourcesJar(t, m2, c, map[string]string{
		"org/example/util/Splitter.java":     splitterJava,
		"org/example/util/package-info.java": "/** pkg docs */\npackage org.example.util;\n",
	})

	docs := javaDocsForCoord(c, m2)
	if len(docs) != 1 {
		t.Fatalf("javaDocsForCoord returned %d docs; want 1 (package-info skipped)", len(docs))
	}
	d := docs[0]
	if d.FQName != "org.example.util.Splitter" {
		t.Errorf("FQName = %q; want org.example.util.Splitter", d.FQName)
	}
	if d.Source != "maven" || d.Coordinate != "org.example:util:1.0.0" {
		t.Errorf("Source/Coordinate = %q/%q", d.Source, d.Coordinate)
	}
	for _, want := range []string{
		"Utility for splitting strings on commas.",
		"public static List<String> split(String input)",
		"public int count()",
		"protected void reset()",
	} {
		if !strings.Contains(d.Content, want) {
			t.Errorf("content missing %q\n---\n%s", want, d.Content)
		}
	}
	if strings.Contains(d.Content, "hidden") {
		t.Errorf("private member leaked into content:\n%s", d.Content)
	}
	// First sentence only: the second sentence of the javadoc must be dropped.
	if strings.Contains(d.Content, "Not thread safe") {
		t.Errorf("javadoc summary not truncated to first sentence:\n%s", d.Content)
	}

	if docs := javaDocsForCoord(mavenCoord{group: "com.missing", artifact: "gone", version: "2.0"}, m2); docs != nil {
		t.Errorf("missing jar: got %d docs; want nil", len(docs))
	}
}

func TestCsharpDocsForPackage(t *testing.T) {
	root := t.TempDir()
	ref := nugetRef{id: "Acme.Widgets", version: "1.2.0"}
	writeNuGetPackage(t, root, ref, widgetsXML)

	docs := csharpDocsForPackage(ref, root)
	if len(docs) != 1 {
		t.Fatalf("csharpDocsForPackage returned %d docs; want 1 (orphan member has no type)", len(docs))
	}
	d := docs[0]
	if d.FQName != "Acme.Widgets.Widget" || d.Source != "nuget" || d.Coordinate != "Acme.Widgets@1.2.0" {
		t.Errorf("FQName/Source/Coordinate = %q/%q/%q", d.FQName, d.Source, d.Coordinate)
	}
	for _, want := range []string{
		"A spinnable widget.",
		"M: Acme.Widgets.Widget.Spin(System.Int32) — Spins the widget the given number of times.",
		"P: Acme.Widgets.Widget.Size — Widget size in units.",
	} {
		if !strings.Contains(d.Content, want) {
			t.Errorf("content missing %q\n---\n%s", want, d.Content)
		}
	}
	if strings.Contains(d.Content, "Orphan") {
		t.Errorf("orphan member (no T: entry) leaked:\n%s", d.Content)
	}

	if docs := csharpDocsForPackage(nugetRef{id: "Nope", version: "1.0"}, root); docs != nil {
		t.Errorf("missing package: got %d docs; want nil", len(docs))
	}
}

func TestTsDocsForPackage(t *testing.T) {
	repo := t.TempDir()
	writeNodeModule(t, repo, "left-pad", "1.3.0", "index.d.ts", map[string]string{
		"index.d.ts": "declare function leftPad(s: string, n: number): string;\nexport = leftPad;\n",
	})
	// Scoped package, no explicit types entry — .d.ts found by walking.
	writeNodeModule(t, repo, "@acme/ui", "2.1.0", "", map[string]string{
		"dist/button.d.ts": "export declare function Button(props: {label: string}): unknown;\n",
	})

	coord, docs := tsDocsForPackage(repo, "left-pad")
	if coord != "left-pad@1.3.0" || len(docs) != 1 {
		t.Fatalf("tsDocsForPackage(left-pad) = %q, %d docs; want left-pad@1.3.0, 1", coord, len(docs))
	}
	if !strings.Contains(docs[0].Content, "declare function leftPad") || docs[0].FQName != "left-pad/index.d.ts" {
		t.Errorf("doc = %+v", docs[0])
	}

	coord, docs = tsDocsForPackage(repo, "@acme/ui")
	if coord != "@acme/ui@2.1.0" || len(docs) != 1 {
		t.Fatalf("tsDocsForPackage(@acme/ui) = %q, %d docs; want @acme/ui@2.1.0, 1", coord, len(docs))
	}
	if docs[0].FQName != "@acme/ui/dist/button.d.ts" {
		t.Errorf("FQName = %q; want @acme/ui/dist/button.d.ts", docs[0].FQName)
	}

	if coord, docs := tsDocsForPackage(repo, "not-installed"); coord != "" || docs != nil {
		t.Errorf("missing package: got %q, %d docs", coord, len(docs))
	}
}

// ---- ingestion ----

// depDocFixtureRepo builds a repo with all three manifests plus local artifact caches:
// maven (one dep resolvable, one missing), nuget (resolvable), npm (resolvable).
func depDocFixtureRepo(t *testing.T) (repo string, dd DependencyDocOptions) {
	t.Helper()
	repo = t.TempDir()
	pom := `<project><dependencies>
    <dependency><groupId>org.example</groupId><artifactId>util</artifactId><version>1.0.0</version></dependency>
    <dependency><groupId>com.missing</groupId><artifactId>gone</artifactId><version>2.0</version></dependency>
  </dependencies></project>`
	if err := os.WriteFile(filepath.Join(repo, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}
	csproj := `<Project><ItemGroup><PackageReference Include="Acme.Widgets" Version="1.2.0" /></ItemGroup></Project>`
	if err := os.WriteFile(filepath.Join(repo, "App.csproj"), []byte(csproj), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := `{"dependencies":{"left-pad":"1.3.0"}}`
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}

	m2 := t.TempDir()
	writeSourcesJar(t, m2, mavenCoord{group: "org.example", artifact: "util", version: "1.0.0"},
		map[string]string{"org/example/util/Splitter.java": splitterJava})
	nuget := t.TempDir()
	writeNuGetPackage(t, nuget, nugetRef{id: "Acme.Widgets", version: "1.2.0"}, widgetsXML)
	writeNodeModule(t, repo, "left-pad", "1.3.0", "index.d.ts", map[string]string{
		"index.d.ts": "declare function leftPad(s: string, n: number): string;\n",
	})

	return repo, DependencyDocOptions{Enabled: true, MavenRepoDir: m2, NuGetPackagesDir: nuget}
}

func TestIngestDependencyDocs_EndToEnd(t *testing.T) {
	repo, dd := depDocFixtureRepo(t)
	w := &depDocEmbWriter{}
	opts := RunOptions{RepoPath: repo, RepoID: "r1", Embedder: fixedDimEmbedder{dim: 8}}

	stats, err := ingestDependencyDocs(context.Background(), w, opts, dd, DefaultChunkConfig())
	if err != nil {
		t.Fatalf("ingestDependencyDocs: %v", err)
	}
	if stats.DepsScanned != 4 || stats.DepsIngested != 3 || stats.NoArtifact != 1 || stats.Capped {
		t.Fatalf("stats = %+v; want scanned 4, ingested 3, no_artifact 1, not capped", stats)
	}
	if strings.Join(stats.NoArtifactCoordinates, ",") != "com.missing:gone:2.0" {
		t.Errorf("NoArtifactCoordinates = %v; want the missing maven dep named", stats.NoArtifactCoordinates)
	}
	if strings.Join(stats.IngestedCoordinates, ",") != "org.example:util:1.0.0,Acme.Widgets@1.2.0,left-pad@1.3.0" {
		t.Errorf("IngestedCoordinates = %v", stats.IngestedCoordinates)
	}
	if stats.Chunks != len(w.chunks) {
		t.Fatalf("stats.Chunks = %d but writer holds %d", stats.Chunks, len(w.chunks))
	}

	wantFiles := map[string]string{
		"dep://maven/org.example:util:1.0.0": "java",
		"dep://nuget/Acme.Widgets@1.2.0":     "csharp",
		"dep://npm/left-pad@1.3.0":           "typescript",
	}
	seenFiles := map[string]bool{}
	for _, c := range w.chunks {
		if c.ChunkType != ChunkTypeDependencyDoc {
			t.Errorf("chunk_type = %q; want %q", c.ChunkType, ChunkTypeDependencyDoc)
		}
		if c.RepoID != "r1" {
			t.Errorf("repo_id = %q; want r1", c.RepoID)
		}
		wantLang, ok := wantFiles[c.File]
		if !ok {
			t.Errorf("unexpected virtual file %q", c.File)
			continue
		}
		seenFiles[c.File] = true
		if c.Lang != wantLang {
			t.Errorf("lang for %s = %q; want %q", c.File, c.Lang, wantLang)
		}
		var meta map[string]string
		if err := json.Unmarshal(c.MetadataJSON, &meta); err != nil {
			t.Fatalf("metadata for %s: %v", c.File, err)
		}
		for _, key := range []string{"coordinate", "dependency_source", "fq_name", "simple_name"} {
			if meta[key] == "" {
				t.Errorf("metadata[%s] empty for %s (metadata=%v)", key, c.File, meta)
			}
		}
	}
	if len(seenFiles) != 3 {
		t.Fatalf("chunks cover %d virtual files; want 3 (%v)", len(seenFiles), seenFiles)
	}
	// The Java chunk carries a usable simple_name for get_symbol fallback.
	foundSplitter := false
	for _, c := range w.chunks {
		var meta map[string]string
		_ = json.Unmarshal(c.MetadataJSON, &meta)
		if meta["simple_name"] == "Splitter" && meta["fq_name"] == "org.example.util.Splitter" {
			foundSplitter = true
		}
	}
	if !foundSplitter {
		t.Error("no chunk with simple_name=Splitter / fq_name=org.example.util.Splitter")
	}

	// Idempotence: a second run deletes each virtual file before re-inserting.
	before := len(w.chunks)
	stats2, err := ingestDependencyDocs(context.Background(), w, opts, dd, DefaultChunkConfig())
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if len(w.chunks) != before || stats2.Chunks != stats.Chunks {
		t.Fatalf("second run: %d chunks (was %d), stats %+v — not idempotent", len(w.chunks), before, stats2)
	}
	for _, f := range []string{"dep://maven/org.example:util:1.0.0", "dep://nuget/Acme.Widgets@1.2.0", "dep://npm/left-pad@1.3.0"} {
		n := 0
		for _, d := range w.deletes {
			if d == "r1|"+f {
				n++
			}
		}
		if n != 2 {
			t.Errorf("DeleteByFile(%s) called %d times; want 2 (once per run)", f, n)
		}
	}
}

func TestIngestDependencyDocs_Caps(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{"dependencies":{"big":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dts := map[string]string{}
	for i := 0; i < 5; i++ {
		dts[fmt.Sprintf("d%d.d.ts", i)] = fmt.Sprintf("export declare function f%d(): void;\n", i)
	}
	writeNodeModule(t, repo, "big", "1.0.0", "", dts)

	w := &depDocEmbWriter{}
	opts := RunOptions{RepoPath: repo, RepoID: "r1", Embedder: fixedDimEmbedder{dim: 8}}

	dd := DependencyDocOptions{Enabled: true, MaxChunksPerDependency: 2}
	stats, err := ingestDependencyDocs(context.Background(), w, opts, dd, DefaultChunkConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Capped || stats.Chunks != 2 || len(w.chunks) != 2 {
		t.Fatalf("per-dep cap: stats %+v, %d chunks; want capped, 2 chunks", stats, len(w.chunks))
	}

	w2 := &depDocEmbWriter{}
	dd = DependencyDocOptions{Enabled: true, MaxChunksTotal: 1}
	stats, err = ingestDependencyDocs(context.Background(), w2, opts, dd, DefaultChunkConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Capped || stats.Chunks != 1 || len(w2.chunks) != 1 {
		t.Fatalf("total cap: stats %+v, %d chunks; want capped, 1 chunk", stats, len(w2.chunks))
	}
}

func TestIngestDependencyDocs_DisabledOrNilDeps(t *testing.T) {
	repo, dd := depDocFixtureRepo(t)
	w := &depDocEmbWriter{}
	opts := RunOptions{RepoPath: repo, RepoID: "r1", Embedder: fixedDimEmbedder{dim: 8}}

	dd.Enabled = false
	stats, err := ingestDependencyDocs(context.Background(), w, opts, dd, DefaultChunkConfig())
	if err != nil || !reflect.DeepEqual(stats, DependencyDocStats{}) || len(w.chunks) != 0 || len(w.deletes) != 0 {
		t.Fatalf("disabled: stats %+v err %v, %d chunks, %d deletes; want all zero", stats, err, len(w.chunks), len(w.deletes))
	}

	dd.Enabled = true
	optsNoEmbedder := RunOptions{RepoPath: repo, RepoID: "r1"}
	stats, err = ingestDependencyDocs(context.Background(), w, optsNoEmbedder, dd, DefaultChunkConfig())
	if err != nil || !reflect.DeepEqual(stats, DependencyDocStats{}) {
		t.Fatalf("nil embedder: stats %+v err %v; want zero, nil", stats, err)
	}
}

// Upstream additionally drives ingestion through indexer.Run end to end. That test needs its
// full run harness — mock metadata/embeddings writers built on types core does not carry
// (metadata.SymbolVersion among them) — so it is not ported. The ingestion itself is covered
// directly by TestIngestDependencyDocs_EndToEnd above, and the Run wiring by
// TestRun_dependencyDocsRunOnlyWhenEnabled below.
func TestDependencyDocOptions_capDefaults(t *testing.T) {
	var o DependencyDocOptions
	if o.perDep() != 80 || o.total() != 400 {
		t.Errorf("defaults = %d/dep, %d total; docs promise 80 and 400", o.perDep(), o.total())
	}
	o = DependencyDocOptions{MaxChunksPerDependency: 5, MaxChunksTotal: 7}
	if o.perDep() != 5 || o.total() != 7 {
		t.Errorf("explicit values not honored: %d, %d", o.perDep(), o.total())
	}
}

// The wiring guard: ingestion must sit inside Run, gated on the option, and BEFORE the totals are
// counted — otherwise the dependency chunks it adds are missing from ChunksTotal and the run
// under-reports what it stored. That ordering is a few lines deep in a long function, so this
// asserts on the source rather than waiting for a harness core does not have.
func TestRun_dependencyDocsRunOnlyWhenEnabled(t *testing.T) {
	b, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	call := strings.Index(src, "ingestDependencyDocs(ctx, emb, opts, opts.DependencyDocs, chunkCfg)")
	if call < 0 {
		t.Fatal("Run never calls ingestDependencyDocs; the feature is unreachable")
	}
	gate := strings.LastIndex(src[:call], "if opts.DependencyDocs.Enabled")
	if gate < 0 || call-gate > 400 {
		t.Error("ingestion is not gated on opts.DependencyDocs.Enabled; the feature would run when off")
	}
	totals := strings.Index(src, "Best-effort post-run totals")
	if totals < 0 {
		t.Fatal("totals block not found; this guard needs repointing")
	}
	if call > totals {
		t.Error("ingestion runs AFTER the totals are counted, so its chunks are missing from ChunksTotal")
	}
}

// The failure mode this bundle guards is DILUTION, not correctness: a large dependency corpus can
// crowd repository chunks out of every retrieval result. The guard has to be structural — its own
// chunk type, excluded from open search unless asked for — because a tuned weight silently stops
// working as the corpus grows. This pins the type is distinct and that ingestion only ever writes
// under it.
func TestDependencyDocs_areStructurallySeparatedFromRepositoryChunks(t *testing.T) {
	if ChunkTypeDependencyDoc == "" {
		t.Fatal("dependency docs need their own chunk type, or they compete with repository chunks")
	}
	for _, repoType := range []string{"definition", "test", "fixture", "route"} {
		if ChunkTypeDependencyDoc == repoType {
			t.Fatalf("dependency chunk type collides with the repository type %q", repoType)
		}
	}
	b, err := os.ReadFile("dependency_docs.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// Every chunk this file writes must carry the dependency type. A single untyped write would
	// put library text into the repository's own retrieval budget.
	if n := strings.Count(src, "ChunkType:"); n != strings.Count(src, "ChunkType:    ChunkTypeDependencyDoc") {
		t.Errorf("ingestion writes a chunk without the dependency chunk type (%d ChunkType assignments)", n)
	}
}
