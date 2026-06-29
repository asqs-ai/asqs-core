package projectintel

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findKind(cands []Candidate, rel string) DocKind {
	for _, c := range cands {
		if c.RelPath == rel {
			return c.Kind
		}
	}
	return ""
}

func TestDiscover_MarkdownAsDoc(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "README.md", "# Readme")
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "README.md") != DocKindDoc {
		t.Fatalf("expected DocKindDoc, cands=%v", cands)
	}
}

func TestDiscover_CursorSkillAsSkill(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, ".cursor/skills/testing/SKILL.md", "description: write tests")
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, ".cursor/skills/testing/SKILL.md") != DocKindSkill {
		t.Fatalf("expected DocKindSkill, cands=%v", cands)
	}
}

func TestDiscover_AgentSkillAsSkill(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, ".agent/skills/fmt/SKILL.md", "description: format")
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, ".agent/skills/fmt/SKILL.md") != DocKindSkill {
		t.Fatalf("expected DocKindSkill")
	}
}

func TestDiscover_OpenAPIYAMLAsAPI(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "api/openapi.yaml", "openapi: 3.0.0\ninfo:\n  title: test")
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "api/openapi.yaml") != DocKindAPI {
		t.Fatalf("expected DocKindAPI, cands=%v", cands)
	}
}

func TestDiscover_SwaggerYMLAsAPI(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "swagger.yml", "swagger: \"2.0\"\ninfo:\n  title: test")
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "swagger.yml") != DocKindAPI {
		t.Fatalf("expected DocKindAPI")
	}
}

func TestDiscover_PlainYAMLNotAPI(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "config.yaml", "key: value\nfoo: bar")
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "config.yaml") != "" {
		t.Fatalf("plain YAML should not be discovered")
	}
}

func TestDiscover_SqlAsSchema(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "db/schema.sql", "CREATE TABLE users (id INT);")
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "db/schema.sql") != DocKindSchema {
		t.Fatalf("expected DocKindSchema, cands=%v", cands)
	}
}

func TestDiscover_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "node_modules/pkg/README.md", "# pkg")
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "node_modules/pkg/README.md") != "" {
		t.Fatal("should skip node_modules")
	}
}

func TestDiscover_SkipsVendor(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "vendor/lib/README.md", "# lib")
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "vendor/lib/README.md") != "" {
		t.Fatal("should skip vendor")
	}
}

func TestDiscover_RespectsMonoPrefix(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "services/api/README.md", "# API")
	writeRepoFile(t, dir, "services/worker/README.md", "# Worker")
	cands, err := Discover(dir, "services/api", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "services/api/README.md") == "" {
		t.Fatal("api README should be included")
	}
	if findKind(cands, "services/worker/README.md") != "" {
		t.Fatal("worker README should be excluded by mono prefix")
	}
}

func TestDiscover_FileSizeLimitEnforced(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, 3<<20) // 3MB > 2MB limit
	abs := filepath.Join(dir, "BIG.md")
	if err := os.WriteFile(abs, big, 0o644); err != nil {
		t.Fatal(err)
	}
	cands, err := Discover(dir, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "BIG.md") != "" {
		t.Fatal("file over 2MB should be skipped")
	}
}

func TestDiscover_ExtraGlobsIncluded(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "docs/conventions.md", "# Conventions")
	cands, err := Discover(dir, "", []string{"docs/*.md"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findKind(cands, "docs/conventions.md") == "" {
		t.Fatal("extra glob should include docs/conventions.md")
	}
}
