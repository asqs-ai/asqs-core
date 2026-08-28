package runner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// U10: `format_only_added` used to mean "only the files just written" on a local runner and "the
// entire tree" under Docker. That was documented, but it meant the same config reformatted
// unrelated files depending on the sandbox — and put them in the pull request.
func TestDockerPerFileFormatScript_formatsExactlyTheGivenFiles(t *testing.T) {
	got := dockerPerFileFormatScript("google-java-format -i", []string{"src/A.java", "src/B.java"})

	for _, want := range []string{`"src/A.java"`, `"src/B.java"`, `google-java-format -i "$f"`, "|| exit $?"} {
		if !strings.Contains(got, want) {
			t.Errorf("script %q missing %q", got, want)
		}
	}
	if strings.Contains(got, " .") || strings.Contains(got, "--write .") {
		t.Errorf("script %q must not widen to the whole repository", got)
	}
}

// Paths with spaces or shell metacharacters must survive the loop intact.
func TestDockerPerFileFormatScript_quotesPaths(t *testing.T) {
	got := dockerPerFileFormatScript("fmt -i", []string{"src/My Class.java", "src/a$b.java"})
	if !strings.Contains(got, `"src/My Class.java"`) {
		t.Errorf("script %q should quote a path containing a space", got)
	}
	if !strings.Contains(got, `src/a$b.java`) || !strings.Contains(got, `"src/a`) {
		t.Errorf("script %q should quote a path containing a shell metacharacter", got)
	}
}

func TestDockerPerFileFormatScript_emptyInputsProduceNoScript(t *testing.T) {
	if got := dockerPerFileFormatScript("fmt -i", nil); got != "" {
		t.Errorf("no files should produce no script, got %q", got)
	}
	if got := dockerPerFileFormatScript("   ", []string{"A.java"}); got != "" {
		t.Errorf("no command should produce no script, got %q", got)
	}
	if got := dockerPerFileFormatScript("fmt -i", []string{"  ", ""}); got != "" {
		t.Errorf("blank paths should produce no script, got %q", got)
	}
}

// Both targets filter the written paths the same way, so a Java formatter cannot be handed a .ts
// file on one target and not the other.
func TestFilterFormatFilesByExtension(t *testing.T) {
	in := []string{"src/A.java", "src/b.ts", "src/C.JAVA", "README.md"}
	got := filterFormatFilesByExtension(in, []string{".java"})
	if strings.Join(got, ",") != "src/A.java,src/C.JAVA" {
		t.Errorf("got %v, want the two Java files (case-insensitive)", got)
	}
	if n := len(filterFormatFilesByExtension(in, nil)); n != len(in) {
		t.Errorf("no extensions should keep everything, got %d of %d", n, len(in))
	}
}

// executingFakeDocker stands in for `docker run … IMAGE sh -c <script>` by actually EXECUTING the
// trailing script on the host, so a test observes what the container would do rather than what the
// argv looks like. A stub formatter on PATH records the paths it is handed.
func executingFakeDocker(t *testing.T, formatterExit int) (sb *Sandbox, repo, counter string) {
	t.Helper()
	counter = filepath.Join(t.TempDir(), "formatted.log")
	bin := t.TempDir()
	formatter := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in -*) ;; *) echo \"$a\" >> " + counter +
		" ;; esac; done\nexit " + strconv.Itoa(formatterExit) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "google-java-format"), []byte(formatter), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	sb, repo = fakeDockerSandbox(t, "30s", `script=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-c" ]; then script="$a"; fi
  prev="$a"
done
[ -n "$script" ] || exit 0
sh -c "$script"`)
	return sb, repo, counter
}

// End to end: a per-file format under Docker must run the formatter once per written file and must
// not fall back to a whole-repo run.
func TestFormatAfterFix_dockerPerFileRunsOnlyTheWrittenFiles(t *testing.T) {
	sb, repo, counter := executingFakeDocker(t, 0)

	err := FormatAfterFixForSandbox(sb, context.Background(), repo, "java",
		FormatResolveResult{Command: "google-java-format -i", Source: "test", PerFile: true},
		[]string{"src/A.java", "src/B.java", "src/notes.md"}, 30*time.Second)
	if err != nil {
		t.Fatalf("FormatAfterFixForSandbox: %v", err)
	}

	b, rerr := os.ReadFile(counter)
	if rerr != nil {
		t.Fatalf("the formatter was never invoked: %v", rerr)
	}
	got := strings.Fields(string(b))
	if len(got) != 2 {
		t.Fatalf("formatter ran on %v, want exactly the two .java files", got)
	}
	for _, f := range got {
		if !strings.HasSuffix(f, ".java") {
			t.Errorf("formatter received a non-Java path: %q", f)
		}
	}
}

// The loop must stop at the first failure, matching RunFormatCommandFiles on the local target, and
// surface the error rather than reporting success.
func TestFormatAfterFix_dockerPerFileStopsAtTheFirstFailure(t *testing.T) {
	sb, repo, counter := executingFakeDocker(t, 3)

	err := FormatAfterFixForSandbox(sb, context.Background(), repo, "java",
		FormatResolveResult{Command: "google-java-format -i", Source: "test", PerFile: true},
		[]string{"src/A.java", "src/B.java"}, 30*time.Second)
	if err == nil {
		t.Fatal("a failing formatter must be reported, not swallowed")
	}
	if !strings.Contains(err.Error(), "exit 3") {
		t.Errorf("error %q should carry the formatter's exit code", err)
	}
	b, _ := os.ReadFile(counter)
	if n := len(strings.Fields(string(b))); n != 1 {
		t.Errorf("formatter ran on %d files after a failure, want 1 (the loop must stop)", n)
	}
}
