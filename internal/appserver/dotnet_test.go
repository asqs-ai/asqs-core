package appserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Readiness is "the application answered", not "the application answered successfully". An API root
// returning 404 is a running application, and requiring 2xx would time out against a server that is
// perfectly up — the same reasoning the JS side applies when it waits on a port for an API.
func TestWaitForDotnetE2EReady_anyStatusCounts(t *testing.T) {
	for _, status := range []int{200, 302, 404, 401} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			})}
			go srv.Serve(ln)
			defer srv.Close()

			base := "http://" + ln.Addr().String()
			// A process that stays alive: waitForDotnetE2EReady watches it for an early exit.
			cmd := exec.Command("sleep", "30")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			exited := waitChan(cmd)

			if err := waitForDotnetE2EReady(context.Background(), base, 5*time.Second, exited); err != nil {
				t.Fatalf("status %d should count as ready: %v", status, err)
			}
		})
	}
}

// An application that exits before answering has already failed. Waiting out the timeout after that
// adds a minute of nothing to a decision already made.
func TestWaitForDotnetE2EReady_processExitIsImmediate(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := waitForDotnetE2EReady(context.Background(), "http://127.0.0.1:1", 30*time.Second, waitChan(cmd))
	if err == nil {
		t.Fatal("a process that exited should not be reported as ready")
	}
	if !strings.Contains(err.Error(), "exited before it became ready") {
		t.Errorf("error = %q, want it to name the early exit", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("waited %s for a process that had already exited", elapsed)
	}
}

// A server that never becomes ready must say so rather than let the failure surface as a browser
// navigation timeout, which says nothing about the application.
func TestStartDotnetE2EServer_reportsAFailedStart(t *testing.T) {
	if _, err := exec.LookPath("dotnet"); err != nil {
		t.Skip("dotnet not on PATH")
	}
	repo := t.TempDir()
	// A project that does not exist: `dotnet run` fails immediately.
	_, err := StartDotnetE2EServer(context.Background(), DotnetE2EServerOptions{
		RepoAbs:       repo,
		WebProjectRel: "src/Missing/Missing.csproj",
		ReadyTimeout:  10 * time.Second,
	})
	if err == nil {
		t.Fatal("starting a project that does not exist should fail")
	}
	if !strings.Contains(err.Error(), "application output") && !strings.Contains(err.Error(), "start") {
		t.Errorf("error = %q, want it to carry the application's own output", err)
	}
}

func TestStartDotnetE2EServer_requiresRepoAndProject(t *testing.T) {
	for _, opts := range []DotnetE2EServerOptions{
		{WebProjectRel: "a.csproj"},
		{RepoAbs: t.TempDir()},
		{},
	} {
		if _, err := StartDotnetE2EServer(context.Background(), opts); err == nil {
			t.Errorf("StartDotnetE2EServer(%+v) succeeded with nothing to start", opts)
		}
	}
}

// Stop must be safe to call more than once: the caller stops it on the success path and again in a
// defer, and a double free that panics would take the run down after the tests had already passed.
func TestDotnetE2EServer_stopIsIdempotent(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "log-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("Now listening on: http://127.0.0.1:5000\n"); err != nil {
		t.Fatal(err)
	}
	s := &DotnetE2EServer{BaseURL: "http://127.0.0.1:5000", logFile: f, logPath: f.Name()}
	if tail := s.LogTail(); !strings.Contains(tail, "Now listening") {
		t.Errorf("LogTail = %q, want the application's own output", tail)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if _, err := os.Stat(filepath.Clean(f.Name())); !os.IsNotExist(err) {
		t.Error("the log file outlived the server")
	}
	// A nil server is what a caller has when the start failed, and its defer still runs.
	var nilSrv *DotnetE2EServer
	if err := nilSrv.Stop(); err != nil {
		t.Errorf("Stop on a nil server: %v", err)
	}
	if tail := nilSrv.LogTail(); tail != "" {
		t.Errorf("LogTail on a nil server = %q", tail)
	}
}

// A hard-coded port collides with whatever the developer already has running; a random guess
// collides at a rate that looks like flakiness.
func TestFreeLocalPort(t *testing.T) {
	p, err := freeLocalPort()
	if err != nil {
		t.Fatal(err)
	}
	if p <= 0 || p > 65535 {
		t.Fatalf("freeLocalPort = %d", p)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		t.Fatalf("the reported port is not free: %v", err)
	}
	ln.Close()
}

// waitChan is what StartDotnetE2EServer gives the readiness poll: one Wait() for the life of the
// process, owned by the caller so that Stop can ask whether the process has already been reaped.
func waitChan(cmd *exec.Cmd) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- cmd.Wait() }()
	return ch
}

// `dotnet run --no-build` defaults to Debug and looks for bin/Debug/<tfm>/<app>. The eval toolchain
// compiles with `dotnet build -c Release`, so without naming the configuration the launcher exits
// with "An error occurred trying to start process … No such file or directory" — every
// browser-driven C# E2E pass takes the e2e_server_failed path and the generated test then fails on
// an unset ASQS_BASE_URL, which is the failure this server exists to remove.
func TestDotnetRunArgv_namesTheConfigurationTheCompileStepBuilt(t *testing.T) {
	argv := dotnetRunArgv("src/Shop/Shop.csproj", "http://127.0.0.1:5199", "")
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-c Release") {
		t.Fatalf("argv does not build against the Release output: %s", joined)
	}
	if !strings.Contains(joined, "--no-build") {
		t.Fatalf("argv should not rebuild what the compile step already built: %s", joined)
	}
	if got := dotnetRunArgv("p.csproj", "http://x", "Debug"); !strings.Contains(strings.Join(got, " "), "-c Debug") {
		t.Fatalf("an explicit configuration is not honoured: %v", got)
	}
}

// Stop signals a process GROUP through syscall, which has none of os.Process.Kill's done guard. If
// the application exited during the step the pid has been reaped and may have been reused, so the
// signal would land on an unrelated process tree.
func TestDotnetE2EServer_stopDoesNotSignalAReapedProcess(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	srv := &DotnetE2EServer{cmd: cmd, exited: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		srv.markExited()
		srv.exited <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !srv.alreadyExited() {
		if time.Now().After(deadline) {
			t.Fatal("the process never exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop on an exited process: %v", err)
	}
	// The answer is a field, not a value taken out of the channel: asking twice gives the same one.
	if !srv.alreadyExited() {
		t.Error("the exit state did not survive being read")
	}
}
