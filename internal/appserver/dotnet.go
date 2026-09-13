// Package appserver starts and stops the application under test for an end-to-end step.
//
// It is its own package because of where it is CALLED from. The evaluator's workflow loop decides
// when an E2E pass runs, and the evaluator cannot import the runner — the runner already imports
// the evaluator. Putting process lifecycle beneath both keeps that dependency one-directional, and
// the concern is genuinely separate from either: nothing here knows about steps, fixes or
// containers.
package appserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DotnetE2EServer is a running ASP.NET application started for the duration of an E2E step.
//
// Playwright .NET has no `webServer` block. The JS side writes one into playwright.config.ts, and
// Playwright itself then starts the application, waits for it and stops it. The .NET binding offers
// nothing equivalent, so for a browser-driven C# E2E step ASQS has to do all three — otherwise a
// generated `page.GotoAsync(baseUrl)` has no URL to go to, and the test fails on an empty variable
// rather than on anything about the application.
type DotnetE2EServer struct {
	BaseURL string

	cmd     *exec.Cmd
	logFile *os.File
	logPath string
	once    sync.Once
	// pgid is the process group recorded at start, when the process is certainly alive. Asking the
	// kernel for it later would mean asking about a pid that may already have been reaped.
	pgid int
	// exited receives the result of cmd.Wait() exactly once, for the readiness poll to select on.
	exited chan error
	// mu guards the exit state, which Stop() reads to decide whether signalling is still its
	// business. It is a field rather than a second read of the channel: taking the value out and
	// putting it back made the answer depend on who asked first.
	mu        sync.Mutex
	hasExited bool
}

// DotnetE2EServerOptions configures the application ASQS starts for an E2E step.
type DotnetE2EServerOptions struct {
	// RepoAbs is the working directory the command runs in.
	RepoAbs string
	// WebProjectRel is the repo-relative .csproj of the application to start.
	WebProjectRel string
	// ReadyTimeout bounds how long to wait for the application to answer. Zero uses the default.
	ReadyTimeout time.Duration
	// Env is appended to the process environment.
	Env []string
	// Configuration is the MSBuild configuration the application was BUILT in. Empty means Release,
	// which is what the eval toolchain compiles with.
	Configuration string
}

const (
	// defaultDotnetE2EReadyTimeout bounds startup. An ASP.NET application with EF migrations can
	// take a while on a cold start, and the alternative to waiting is a browser timeout that says
	// nothing about why.
	defaultDotnetE2EReadyTimeout = 90 * time.Second
	// dotnetE2EReadyPollInterval is how often readiness is probed.
	dotnetE2EReadyPollInterval = 250 * time.Millisecond
	// dotnetE2EServerLogTail is how much of the application's own output is reported when it never
	// becomes ready. The reason is almost always in the last few lines — a port already in use, a
	// database that is not there, a missing connection string.
	dotnetE2EServerLogTail = 4000
	// defaultDotnetE2EConfiguration must match what the eval toolchain builds with
	// (runner/profile.ToolchainProfile.Compile for CSharpDotnet: `dotnet build -c Release`).
	defaultDotnetE2EConfiguration = "Release"
)

// StartDotnetE2EServer starts the application and waits for it to answer.
//
// Readiness is "GET / returned ANY status", not "returned 2xx". An API root answering 404 is a
// running application, and requiring success would time out against a server that is perfectly up —
// the same reasoning the JS side applies when it waits on a port rather than a URL for an API.
//
// A server that never becomes ready is an error carrying the tail of its own log. That is the whole
// point of doing this here: without it the failure surfaces as a browser navigation timeout, and
// nothing in the output says the application never started.
func StartDotnetE2EServer(ctx context.Context, opts DotnetE2EServerOptions) (*DotnetE2EServer, error) {
	repo := strings.TrimSpace(opts.RepoAbs)
	proj := strings.TrimSpace(opts.WebProjectRel)
	if repo == "" || proj == "" {
		return nil, errors.New("dotnet e2e server: repository path and web project are both required")
	}
	port, err := freeLocalPort()
	if err != nil {
		return nil, fmt.Errorf("dotnet e2e server: reserve a port: %w", err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	logFile, err := os.CreateTemp("", "asqs-e2e-app-*.log")
	if err != nil {
		return nil, fmt.Errorf("dotnet e2e server: create log: %w", err)
	}

	argv := dotnetRunArgv(proj, baseURL, opts.Configuration)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = repo
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(append(os.Environ(),
		"ASPNETCORE_ENVIRONMENT=Development",
		"ASPNETCORE_URLS="+baseURL,
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
	), opts.Env...)
	// Its own process group: `dotnet run` spawns the application as a CHILD, so killing the
	// launcher alone leaves the application holding the port and the step after it fails on an
	// address already in use.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		os.Remove(logFile.Name())
		return nil, fmt.Errorf("dotnet e2e server: start %s: %w", proj, err)
	}
	srv := &DotnetE2EServer{
		BaseURL: baseURL, cmd: cmd, logFile: logFile, logPath: logFile.Name(),
		exited: make(chan error, 1),
	}
	// The group is this process's own, recorded now: Setpgid made the child a group leader, so the
	// group id is its pid, and reading it here rather than in Stop means never asking the kernel
	// about a pid that has since been reaped.
	if pgid, gerr := syscall.Getpgid(cmd.Process.Pid); gerr == nil {
		srv.pgid = pgid
	}
	// One Wait() for the life of the server, owned here rather than by the readiness poll. The poll
	// started its own and left it running on the success path, so the process could be reaped after
	// the server was handed back — and Stop() would then signal a pid that was no longer its own.
	go func() {
		err := cmd.Wait()
		srv.markExited()
		srv.exited <- err
	}()

	timeout := opts.ReadyTimeout
	if timeout <= 0 {
		timeout = defaultDotnetE2EReadyTimeout
	}
	if err := waitForDotnetE2EReady(ctx, baseURL, timeout, srv.exited); err != nil {
		tail := srv.LogTail()
		_ = srv.Stop()
		return nil, fmt.Errorf("%w\n--- application output ---\n%s", err, tail)
	}
	return srv, nil
}

// waitForDotnetE2EReady polls until the application answers, the process exits, or time runs out.
//
// The exit channel is the server's own, not one this opens: a second Wait() on the same process
// would race the first for the exit status, and the loser gets an error about a child that is not
// there rather than the reason the application stopped.
func waitForDotnetE2EReady(ctx context.Context, baseURL string, timeout time.Duration, exited <-chan error) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}

	for {
		select {
		case err := <-exited:
			// The application stopped before it ever answered. Waiting out the timeout after that
			// would add a minute of nothing to a failure already decided.
			return fmt.Errorf("the application exited before it became ready (%v)", err)
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			// ANY status means the host is up and routing. A 404 at the root is an ordinary answer
			// from an API, and requiring 2xx would time out against a working application.
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the application did not answer %s within %s", baseURL, timeout)
		}
		time.Sleep(dotnetE2EReadyPollInterval)
	}
}

// Stop terminates the application and removes its log. Safe to call more than once.
func (s *DotnetE2EServer) Stop() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil && !s.alreadyExited() {
			// The whole group: `dotnet run` is a launcher, and its child is what holds the port.
			if s.pgid > 0 {
				_ = syscall.Kill(-s.pgid, syscall.SIGTERM)
				// A short grace period, then insist. An application blocked on a shutdown hook
				// would otherwise hold the port for the next step.
				time.Sleep(500 * time.Millisecond)
				_ = syscall.Kill(-s.pgid, syscall.SIGKILL)
			} else {
				// No group to signal: os.Process.Kill refuses on a reaped process, so this is the
				// safe fallback rather than a second-best one.
				_ = s.cmd.Process.Kill()
			}
		}
		if s.logFile != nil {
			_ = s.logFile.Close()
		}
		if s.logPath != "" {
			err = os.Remove(s.logPath)
			if os.IsNotExist(err) {
				err = nil
			}
		}
	})
	return err
}

// markExited records that the process has been reaped. Called once, by the goroutine that reaped it.
func (s *DotnetE2EServer) markExited() {
	s.mu.Lock()
	s.hasExited = true
	s.mu.Unlock()
}

// alreadyExited reports whether the process has been reaped, in which case its pid is no longer
// its own.
//
// This is the difference between signalling a process group and signalling whatever the kernel
// handed that number to next. os.Process.Kill would have refused on a reaped process — it keeps a
// done flag — but the group kill goes through syscall with the raw pid and has no such guard, and
// SIGKILL to a recycled group would take out an unrelated process tree.
//
// What remains after this check is the interval between it and the signal, during which this
// process's own reaper may run. It is not closed here because it cannot be closed portably: a pid
// is reserved only while the child is unreaped, and the call that would observe an exit WITHOUT
// reaping — waitpid with WNOWAIT — is valid on Darwin and returns EINVAL on Linux, which is where
// evaluation runs. Narrowing it to a few instructions, against sequential pid allocation over a
// 32-bit space, is what is available.
func (s *DotnetE2EServer) alreadyExited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasExited
}

// dotnetRunArgv is the command that starts the application.
//
// `--no-build` because the compile step already built this project: rebuilding here would both
// waste the time and risk a different result from the artifact under test. The configuration has to
// be named for the same reason — `dotnet run --no-build` defaults to Debug and looks for
// bin/Debug/<tfm>/<app>, while the eval toolchain compiles with `dotnet build -c Release`. Without
// it the launcher exits immediately with "An error occurred trying to start process … No such file
// or directory", every browser-driven C# E2E pass takes the e2e_server_failed path, and the
// generated test fails on an unset ASQS_BASE_URL — the exact failure this server exists to remove.
func dotnetRunArgv(projectRel, baseURL, configuration string) []string {
	cfg := strings.TrimSpace(configuration)
	if cfg == "" {
		cfg = defaultDotnetE2EConfiguration
	}
	return []string{
		"dotnet", "run", "--no-build", "-c", cfg,
		"--project", filepath.FromSlash(projectRel), "--urls", baseURL,
	}
}

// LogTail returns the end of the application's own output, which is where the reason for a failed
// start almost always is: a port in use, a database that is not there, a missing connection string.
func (s *DotnetE2EServer) LogTail() string {
	if s == nil || s.logPath == "" {
		return ""
	}
	b, err := os.ReadFile(s.logPath)
	if err != nil {
		return ""
	}
	if len(b) > dotnetE2EServerLogTail {
		b = b[len(b)-dotnetE2EServerLogTail:]
	}
	return strings.TrimSpace(string(b))
}

// freeLocalPort asks the kernel for a port nothing is using.
//
// Asking rather than picking: a hard-coded port collides with whatever the developer already has
// running, and a random guess collides at a rate that looks like flakiness.
func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
