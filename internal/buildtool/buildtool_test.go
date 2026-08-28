package buildtool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestResolve_autoDetection(t *testing.T) {
	tests := []struct {
		name   string
		files  []string
		want   Kind
		binary string
	}{
		{"maven", []string{"pom.xml"}, Maven, "mvn"},
		{"gradle groovy", []string{"build.gradle"}, Gradle, "gradle"},
		{"gradle kotlin", []string{"build.gradle.kts"}, Gradle, "gradle"},
		{"both prefers maven", []string{"pom.xml", "build.gradle"}, Maven, "mvn"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(repo(t, tc.files...), "auto")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Kind != tc.want || got.Binary != tc.binary {
				t.Errorf("got %+v, want kind %s binary %s", got, tc.want, tc.binary)
			}
		})
	}
}

// D3: the wrapper is never selected, even when the repository ships one and it is the only build
// tool present. This is the whole point of the package.
func TestResolve_neverReturnsARepoWrapper(t *testing.T) {
	dir := repo(t, "pom.xml", "mvnw", "mvnw.cmd", "build.gradle", "gradlew", "gradlew.bat")
	for _, configured := range []string{"auto", "mvn", "mvnw"} {
		got, err := Resolve(dir, configured)
		if err != nil {
			t.Fatalf("%s: %v", configured, err)
		}
		if got.Binary != "mvn" {
			t.Errorf("build_tool=%s selected %q; wrappers are removed by D3", configured, got.Binary)
		}
	}
	for _, configured := range []string{"gradle", "gradlew"} {
		got, err := Resolve(dir, configured)
		if err != nil {
			t.Fatalf("%s: %v", configured, err)
		}
		if got.Binary != "gradle" {
			t.Errorf("build_tool=%s selected %q; wrappers are removed by D3", configured, got.Binary)
		}
	}
}

// The resolver must produce the same answer on every host OS. Two of the copies it replaces
// returned "mvnw.cmd" on Windows and fed that into a Linux container.
func TestResolve_hasNoWindowsVariant(t *testing.T) {
	dir := repo(t, "pom.xml", "mvnw.cmd", "gradlew.bat")
	got, err := Resolve(dir, "auto")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{".cmd", ".bat", "./"} {
		if strings.Contains(got.Binary, bad) {
			t.Errorf("binary %q contains host-shaped fragment %q", got.Binary, bad)
		}
	}
}

func TestResolve_noBuildFile(t *testing.T) {
	_, err := Resolve(repo(t, "README.md"), "auto")
	if !errors.Is(err, ErrNoBuildFile) {
		t.Fatalf("err = %v, want ErrNoBuildFile", err)
	}
}

func TestResolve_explicitToolMissingItsBuildFile(t *testing.T) {
	if _, err := Resolve(repo(t, "build.gradle"), "mvn"); err == nil {
		t.Error("build_tool=mvn with no pom.xml must fail")
	}
	if _, err := Resolve(repo(t, "pom.xml"), "gradle"); err == nil {
		t.Error("build_tool=gradle with no build.gradle must fail")
	}
}

func TestResolve_rejectsUnknownBuildTool(t *testing.T) {
	_, err := Resolve(repo(t, "pom.xml"), "bazel")
	if err == nil {
		t.Fatal("an unknown build_tool must be rejected")
	}
	if !strings.Contains(err.Error(), "bazel") {
		t.Errorf("error should name the offending value: %v", err)
	}
}

func TestCanonicalize(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		alias  bool
		wantOK bool
	}{
		{"", "auto", false, true},
		{"auto", "auto", false, true},
		{"AUTO", "auto", false, true},
		{"mvn", "mvn", false, true},
		{"maven", "mvn", false, true},
		{"gradle", "gradle", false, true},
		{"mvnw", "mvn", true, true},
		{"Gradlew", "gradle", true, true},
		{"  mvnw  ", "mvn", true, true},
		{"bazel", "bazel", false, false},
	}
	for _, tc := range tests {
		got, alias, ok := Canonicalize(tc.in)
		if got != tc.want || alias != tc.alias || ok != tc.wantOK {
			t.Errorf("Canonicalize(%q) = (%q,%v,%v), want (%q,%v,%v)",
				tc.in, got, alias, ok, tc.want, tc.alias, tc.wantOK)
		}
	}
}

// A directory is not a build file.
func TestResolve_ignoresDirectoriesNamedLikeBuildFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "pom.xml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(dir, "auto"); !errors.Is(err, ErrNoBuildFile) {
		t.Fatalf("err = %v, want ErrNoBuildFile", err)
	}
}
