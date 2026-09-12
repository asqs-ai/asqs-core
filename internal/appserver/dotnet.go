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

	// --no-build because the compile step already built this project: rebuilding here would both
	// waste the time and risk a different result from the artifact under test.
	argv := []string{"dotnet", "run", "--no-build", "--project", filepath.FromSlash(proj), "--urls", baseURL}
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
	srv := &DotnetE2EServer{BaseURL: baseURL, cmd: cmd, logFile: logFile, logPath: logFile.Name()}

	timeout := opts.ReadyTimeout
	if timeout <= 0 {
		timeout = defaultDotnetE2EReadyTimeout
	}
	if err := waitForDotnetE2EReady(ctx, baseURL, timeout, cmd); err != nil {
		tail := srv.LogTail()
		_ = srv.Stop()
		return nil, fmt.Errorf("%w\n--- application output ---\n%s", err, tail)
	}
	return srv, nil
}

// waitForDotnetE2EReady polls until the application answers, the process exits, or time runs out.
func waitForDotnetE2EReady(ctx context.Context, baseURL string, timeout time.Duration, cmd *exec.Cmd) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

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
		if s.cmd != nil && s.cmd.Process != nil {
			// The whole group: `dotnet run` is a launcher, and its child is what holds the port.
			if pgid, gerr := syscall.Getpgid(s.cmd.Process.Pid); gerr == nil {
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
				// A short grace period, then insist. An application blocked on a shutdown hook
				// would otherwise hold the port for the next step.
				time.Sleep(500 * time.Millisecond)
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			} else {
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
