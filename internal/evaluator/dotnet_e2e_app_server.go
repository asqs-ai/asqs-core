package evaluator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/asqs/asqs-core/internal/appserver"
	"github.com/asqs/asqs-core/internal/langid"
	"github.com/asqs/asqs-core/internal/layout"
)

// csharpBrowserE2EFrameworks drive a real browser, which is what makes a running application
// necessary. The in-process API stack does not: WebApplicationFactory hosts the application inside
// the test, so starting a second copy would only contend for a port.
func csharpBrowserE2EFramework(fw string) bool {
	switch strings.ToLower(strings.TrimSpace(fw)) {
	case "playwright-dotnet", "selenium", "selenium-dotnet":
		return true
	}
	return false
}

// startCSharpE2EAppServer starts the application under test for a browser-driven C# E2E step and
// returns the environment the step needs, plus a stop function.
//
// Playwright .NET has no `webServer` block. On the JS side ASQS writes one into
// playwright.config.ts and Playwright starts, waits for and stops the application itself; the .NET
// binding offers nothing equivalent. So without this a generated `page.GotoAsync(baseUrl)` has no
// URL: the test fails on an empty environment variable, which says nothing about the application
// and which no fix round can repair, because the missing piece is not in any file the fixer writes.
//
// Returns nothing at all — and no error — whenever this does not apply: another language, an
// in-process API stack, a surface with no UI, or no web project to start. Every one of those is a
// run that should proceed exactly as before.
func startCSharpE2EAppServer(ctx context.Context, opts EvalOptions, audit Auditor) (env []string, stop func()) {
	noop := func() {}
	if !langid.IsCSharp(opts.Lang) || !csharpBrowserE2EFramework(opts.E2EFramework) {
		return nil, noop
	}
	if !csharpSurfaceHasUI(opts.E2ESurface) {
		return nil, noop
	}
	repo := strings.TrimSpace(opts.RepoPath)
	if repo == "" {
		return nil, noop
	}
	webProj := layout.DetectCSharpWebProjectRel(repo)
	if webProj == "" {
		auditE2EServer(ctx, audit, "evaluator.e2e_server_skipped", map[string]interface{}{
			"message": "A browser E2E pass was requested for a UI surface, but no ASP.NET web project was found to start. " +
				"A generated test that navigates ASQS_BASE_URL will fail with that variable unset.",
		})
		return nil, noop
	}

	srv, err := appserver.StartDotnetE2EServer(ctx, appserver.DotnetE2EServerOptions{
		RepoAbs:       repo,
		WebProjectRel: webProj,
	})
	if err != nil {
		// The E2E step still runs. It will fail — which is the truth — but it fails with this
		// reason on the audit trail instead of as an unexplained browser timeout.
		auditE2EServer(ctx, audit, "evaluator.e2e_server_failed", map[string]interface{}{
			"message":     fmt.Sprintf("Could not start %s for the browser E2E pass: %v", webProj, err),
			"web_project": webProj,
			"error":       err.Error(),
		})
		return nil, noop
	}
	auditE2EServer(ctx, audit, "evaluator.e2e_server_started", map[string]interface{}{
		"message":       fmt.Sprintf("Started %s at %s for the browser E2E pass; ASQS_BASE_URL is exported into the test environment.", webProj, srv.BaseURL),
		"web_project":   webProj,
		"asqs_base_url": srv.BaseURL,
	})
	return []string{"ASQS_BASE_URL=" + srv.BaseURL}, func() {
		_ = srv.Stop()
		auditE2EServer(ctx, audit, "evaluator.e2e_server_stopped", map[string]interface{}{
			"message":     fmt.Sprintf("Stopped %s after the browser E2E pass.", webProj),
			"web_project": webProj,
		})
	}
}

// csharpSurfaceHasUI reports whether the surface includes pages a browser can drive. An empty
// surface is treated as having one: the surface is a refinement, and refusing to start the
// application because nobody detected it would break the case this exists for.
func csharpSurfaceHasUI(surface string) bool {
	switch strings.ToLower(strings.TrimSpace(surface)) {
	case "none", "api":
		return false
	}
	return true
}

func auditE2EServer(ctx context.Context, audit Auditor, event string, payload map[string]interface{}) {
	if audit == nil {
		return
	}
	audit.Log(ctx, event, payload)
}

// applyCSharpE2EAppServerEnv starts the application for a browser E2E pass and exports
// ASQS_BASE_URL for the duration of the step, returning the function that undoes both.
//
// The variable is set in THIS process's environment because the local sandbox runs the test command
// as a child and children inherit it. That bounds where this works: the Docker target runs the test
// inside a container, which inherits nothing from here, and the application would have to run in
// that same container to be reachable at all — a different arrangement, and CS24's own task 3.
//
// The variable is restored rather than merely unset: an operator who exported ASQS_BASE_URL to
// point at a deployed environment meant it, and a run must not silently delete their setting.
//
// The process environment is shared by everything in the process, so the lock holds from the moment
// the variable is set until it is put back. Two browser-driven C# runs in one process would
// otherwise interleave: the second would overwrite the first's URL, and the first's restore would
// hand the second whichever value predated it — mid-step, so the test navigates somewhere else or
// nowhere. This mirror runs one pipeline per process today, which makes the lock a guard against a
// future caller rather than a live fix; the private distribution runs jobs concurrently in one
// process and needs it now. It buys nothing against a process outside this one; the Docker bound
// above is the other half of the same problem.
var csharpE2EAppServerEnvMu sync.Mutex

func applyCSharpE2EAppServerEnv(ctx context.Context, opts EvalOptions, audit Auditor) func() {
	env, stop := startCSharpE2EAppServer(ctx, opts, audit)
	if len(env) == 0 {
		return stop
	}
	csharpE2EAppServerEnvMu.Lock()
	unlock := &sync.Once{}
	var restores []func()
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		previous, had := os.LookupEnv(name)
		if err := os.Setenv(name, value); err != nil {
			continue
		}
		restores = append(restores, func() {
			if had {
				_ = os.Setenv(name, previous)
				return
			}
			_ = os.Unsetenv(name)
		})
	}
	return func() {
		for _, r := range restores {
			r()
		}
		unlock.Do(csharpE2EAppServerEnvMu.Unlock)
		stop()
	}
}
