package teststack

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRead_absentIsNotAnError is the property every consumer depends on. Bootstrap is off by
// default and skips repositories that are already equipped, so "no contract" is the common case —
// it must be indistinguishable from a clean no-op, never an error.
func TestRead_absentIsNotAnError(t *testing.T) {
	if _, ok := Read(t.TempDir()); ok {
		t.Fatal("an empty workspace must report no contract")
	}
	if _, ok := Read(filepath.Join(t.TempDir(), "does", "not", "exist")); ok {
		t.Fatal("a missing directory must report no contract")
	}
	if _, ok := Read(""); ok {
		t.Fatal("an empty repo path must report no contract")
	}
}

func writeRaw(t *testing.T, repo, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path(repo)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(repo), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRead_unusableContentsReportAbsent(t *testing.T) {
	cases := map[string]string{
		"malformed json":     `{ not json`,
		"empty object":       `{}`,
		"future version":     `{"version": 99, "language": "java", "runner": "junit5"}`,
		"version zero":       `{"version": 0, "language": "java", "runner": "junit5"}`,
		"structurally empty": `{"version": 1}`,
		"wrong root type":    `["nope"]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			writeRaw(t, repo, body)
			if _, ok := Read(repo); ok {
				t.Fatalf("%s must report no contract", name)
			}
		})
	}
}

func TestWriteThenRead_roundTrips(t *testing.T) {
	repo := t.TempDir()
	in := Contract{
		Language:          "java",
		Framework:         "spring-boot",
		FrameworkVersion:  "3.2.5",
		Runner:            "junit5",
		Stack:             "spring-boot-test",
		AvailablePackages: []string{"org.springframework.boot:spring-boot-starter-test"},
		AvailableImports:  []string{"org.assertj.core.*", "org.mockito.*"},
		Verified:          true,
		Smoke:             Smoke{Kind: "spring-boot", Status: SmokePassed},
	}
	if err := Write(repo, in); err != nil {
		t.Fatal(err)
	}
	got, ok := Read(repo)
	if !ok {
		t.Fatal("round trip failed")
	}
	if got.Version != SchemaVersion {
		t.Errorf("version = %d", got.Version)
	}
	if got.GeneratedAt == "" {
		t.Error("generated_at should be stamped on write")
	}
	if got.Framework != in.Framework || got.Runner != in.Runner || got.Smoke.Status != SmokePassed {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if len(got.AvailableImports) != 2 {
		t.Errorf("imports = %v", got.AvailableImports)
	}
}

func TestWrite_createsAsqsDirAndIsIdempotent(t *testing.T) {
	repo := t.TempDir()
	c := Contract{Language: "csharp", Runner: "xunit", AvailableImports: []string{"Xunit"}}
	for i := 0; i < 2; i++ {
		if err := Write(repo, c); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := os.Stat(Path(repo)); err != nil {
		t.Fatalf("contract not at %s: %v", RelPath, err)
	}
	// No temp files left behind by the atomic write.
	ents, err := os.ReadDir(filepath.Dir(Path(repo)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() != "test-stack.json" {
			t.Errorf("stray file in .asqs: %s", e.Name())
		}
	}
}

func TestWrite_defaultsSmokeStatus(t *testing.T) {
	repo := t.TempDir()
	if err := Write(repo, Contract{Language: "java", Runner: "junit5", AvailableImports: []string{"org.junit.jupiter.*"}}); err != nil {
		t.Fatal(err)
	}
	got, ok := Read(repo)
	if !ok {
		t.Fatal("read failed")
	}
	if got.Smoke.Status != SmokeNone {
		t.Errorf("smoke status = %q, want %q", got.Smoke.Status, SmokeNone)
	}
}
