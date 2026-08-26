package runner

import (
	"bytes"
	"context"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

const localEnvBlockMarker = "[asqs-eval] evaluation runner: type=local"

// captureStderr swaps os.Stderr for a pipe while fn runs. The eval env block is written with
// fmt.Fprintf(os.Stderr, …), which reads the package variable at call time, so the swap is picked
// up. No test in this package calls t.Parallel(), so the global swap cannot race another test.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	// Drain concurrently: a pipe holds ~64KB, and the docker path can print more than that.
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stderr = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// "python" is deliberate: Compile/Test/Coverage log the env block and then return
// "skip (unsupported lang)" without spawning anything, so these tests exercise the state sharing
// and nothing else.
const unsupportedLang = "python"

func TestClone_sharesRunStateEvenWhenParentWasNeverUsed(t *testing.T) {
	sb := &Sandbox{Type: "local"} // struct literal: run is nil

	c := sb.clone()

	if sb.run == nil {
		t.Fatal("clone must allocate the parent's run state BEFORE copying, or the copy forks")
	}
	if c.run != sb.run {
		t.Fatal("clone does not share run state with its parent")
	}
}

func TestNewSandboxFromConfig_allocatesRunStateEagerly(t *testing.T) {
	if sb := NewSandboxFromConfig(nil); sb.run == nil {
		t.Error("nil-config Sandbox has no run state")
	}
}

// The zero-stderr-diff requirement: in the order production actually uses (parent Compile first,
// clones afterwards), the env block is printed exactly once — same as before U0.
func TestEvalEnvBlock_printedOnceInProductionOrder(t *testing.T) {
	sb := NewSandboxFromConfig(nil)
	repo := t.TempDir()
	ctx := context.Background()

	out := captureStderr(t, func() {
		sb.Compile(ctx, repo, unsupportedLang)
		sb.CompileWithCommand(ctx, repo, unsupportedLang, "echo compile")
		sb.TestWithCommand(ctx, repo, unsupportedLang, "echo test")
		sb.CoverageWithCommand(ctx, repo, unsupportedLang, "echo cov")
	})

	if n := strings.Count(out, localEnvBlockMarker); n != 1 {
		t.Fatalf("env block printed %d times, want exactly 1\n---\n%s", n, out)
	}
}

// The structural property, and the case the pre-U0 code got wrong: when a CLONE runs before the
// parent ever has, the shared state must still make the block print once.
//
// Before U0 each method did `s2 := *s` on a Sandbox whose sync.Once had not yet fired, so the
// clone fired its own copy and the parent later fired the original — two blocks. Production never
// hit it because RunCompile always calls Compile on the parent first, which is exactly the kind of
// invariant nothing enforced.
func TestEvalEnvBlock_printedOnceWhenACloneRunsFirst(t *testing.T) {
	sb := NewSandboxFromConfig(nil)
	repo := t.TempDir()
	ctx := context.Background()

	out := captureStderr(t, func() {
		sb.CompileWithCommand(ctx, repo, unsupportedLang, "echo compile") // clone runs first
		sb.Compile(ctx, repo, unsupportedLang)                            // parent runs after
	})

	if n := strings.Count(out, localEnvBlockMarker); n != 1 {
		t.Fatalf("env block printed %d times, want exactly 1 — clone and parent are not sharing state\n---\n%s", n, out)
	}
}

// A clone of a clone must still share, or a future TestE2EPass-of-a-TestWithCommand would fork.
func TestClone_isTransitive(t *testing.T) {
	sb := &Sandbox{Type: "local"}
	a := sb.clone()
	b := a.clone()

	if a.run != sb.run || b.run != sb.run {
		t.Fatal("run state is not shared transitively across nested clones")
	}
}

// Structural guard: Sandbox is copied by value at four call sites, so it must contain no
// copy-unsafe sync type by value. A pointer to one is fine — that is the whole design.
//
// This is what keeps `go vet ./internal/runner/...` clean. It also catches the sequel vet cannot
// report: adding a sync.Mutex beside an existing sync.Once produces the same copylocks finding
// count with the same message, so a new lock would otherwise land with no new signal at all.
func TestSandbox_holdsNoSyncTypeByValue(t *testing.T) {
	var offenders []string

	var walk func(t reflect.Type, path string, depth int)
	walk = func(rt reflect.Type, path string, depth int) {
		if depth > 6 || rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			where := path + "." + f.Name
			if f.Type.PkgPath() == "sync" {
				offenders = append(offenders, where+" ("+f.Type.String()+")")
				continue
			}
			if f.Type.Kind() == reflect.Struct {
				walk(f.Type, where, depth+1)
			}
		}
	}
	walk(reflect.TypeOf(Sandbox{}), "Sandbox", 0)

	if len(offenders) > 0 {
		t.Fatalf("Sandbox is copied by value in clone(); these fields make that unsafe and "+
			"reintroduce copylocks findings: %s\nMove them behind the *sandboxRunState pointer.",
			strings.Join(offenders, ", "))
	}
}
