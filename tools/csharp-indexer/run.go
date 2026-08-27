// Package csharpindexer runs the Roslyn-based C# indexer (tools/csharp-indexer) and aggregates
// stdout JSONL into map[path]*indexer.ParsedFile, matching the Java advanced JAR contract.
package csharpindexer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/intelligence/indexer"
)

// RunConfig controls local vs Docker execution of the published CSharpIndexer.dll.
type RunConfig struct {
	Timeout time.Duration
	Docker  *DotnetDockerConfig
	// AllowEmptyResult when true returns an empty map instead of an error if the indexer emits no JSONL
	// (e.g. mono primary workspace has no C# while mono_repo_extra_paths holds a shared library).
	AllowEmptyResult bool
	// OnSummary receives the indexer's trailing run summary when the tool emits one.
	// Older tool builds emit none; the callback is simply never invoked then.
	OnSummary func(RunSummary)
}

// RunSummary is the trailing JSONL object the indexer emits after all file lines: how the repo
// was compiled and how invocation resolution fared. `invocations_unresolved` is the health metric
// per-project compilation exists to reduce — before per-project compilation an unresolved callee was a silent continue.
type RunSummary struct {
	Summary               string `json:"summary"` // "csharp_indexer_run"
	Projects              int    `json:"projects"`
	ProjectFiles          int    `json:"project_files"`
	LooseFiles            int    `json:"loose_files"`
	InvocationsResolved   int    `json:"invocations_resolved"`
	InvocationsUnresolved int    `json:"invocations_unresolved"`
}

// Run executes the indexer against repoPath. dllPath must be absolute path to CSharpIndexer.dll (dotnet publish output).
// Docker mode mounts filepath.Dir(dllPath) at /indexer (or the directory of DllContainerPath) so runtimeconfig.json,
// deps.json, and assemblies are available; mounting only the DLL breaks framework-dependent execution.
// Each stdout line is one LangIndexerJSON object. Source bytes are left empty; LangIndexerFromMap fills them at lookup.
func Run(ctx context.Context, repoPath, dllPath string, cfg RunConfig) (map[string]*indexer.ParsedFile, error) {
	dllPath = strings.TrimSpace(dllPath)
	if dllPath == "" {
		return nil, fmt.Errorf("csharp-indexer: empty DLL path (set indexer.csharp_indexer_dll_path)")
	}
	if !filepath.IsAbs(dllPath) {
		abs, err := filepath.Abs(dllPath)
		if err != nil {
			return nil, fmt.Errorf("csharp-indexer: resolve DLL path: %w", err)
		}
		dllPath = abs
	}
	if _, err := os.Stat(dllPath); err != nil {
		return nil, fmt.Errorf("csharp-indexer: DLL: %w", err)
	}
	repoPath = filepath.Clean(repoPath)
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("csharp-indexer: repo path: %w", err)
	}
	if cfg.Docker != nil {
		return runDocker(ctx, absRepo, dllPath, cfg)
	}
	return runLocal(ctx, absRepo, dllPath, cfg)
}

func runLocal(ctx context.Context, absRepo, dllPath string, cfg RunConfig) (map[string]*indexer.ParsedFile, error) {
	runCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "dotnet", dllPath, absRepo)
	cmd.Dir = absRepo
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("csharp-indexer: stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("csharp-indexer: start dotnet: %w", err)
	}
	byPath, summary, scanErr := parseJSONL(stdout, &stderr)
	waitErr := cmd.Wait()
	if scanErr != nil {
		return nil, scanErr
	}
	if summary != nil && cfg.OnSummary != nil {
		cfg.OnSummary(*summary)
	}
	if len(byPath) == 0 {
		if cfg.AllowEmptyResult {
			return map[string]*indexer.ParsedFile{}, nil
		}
		msg := "csharp-indexer: produced no parsed files (dotnet " + dllPath + " " + absRepo + ")"
		if stderr.Len() > 0 {
			msg += "; stderr: " + strings.TrimSpace(stderr.String())
		}
		if waitErr != nil {
			msg += "; exit: " + waitErr.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return byPath, nil
}

func runDocker(ctx context.Context, absRepo, dllPath string, cfg RunConfig) (map[string]*indexer.ParsedFile, error) {
	dargs, err := BuildDotnetDockerRunArgs(absRepo, dllPath, cfg.Docker)
	if err != nil {
		return nil, err
	}
	runCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	cli := cfg.Docker.cli()
	fmt.Fprintf(os.Stderr, "  csharp-indexer: %s %s\n", cli, strings.Join(dargs, " "))
	cmd := exec.CommandContext(runCtx, cli, dargs...)
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("csharp-indexer docker: stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("csharp-indexer docker: start %s: %w", cli, err)
	}
	byPath, summary, scanErr := parseJSONL(stdout, &stderr)
	waitErr := cmd.Wait()
	if scanErr != nil {
		return nil, scanErr
	}
	if summary != nil && cfg.OnSummary != nil {
		cfg.OnSummary(*summary)
	}
	if len(byPath) == 0 {
		if cfg.AllowEmptyResult {
			return map[string]*indexer.ParsedFile{}, nil
		}
		msg := "csharp-indexer: docker produced no parsed files (docker " + strings.Join(dargs, " ") + ")"
		if stderr.Len() > 0 {
			msg += "; stderr: " + strings.TrimSpace(stderr.String())
		}
		if waitErr != nil {
			msg += "; exit: " + waitErr.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return byPath, nil
}

func parseJSONL(stdout io.Reader, stderr *strings.Builder) (map[string]*indexer.ParsedFile, *RunSummary, error) {
	byPath := make(map[string]*indexer.ParsedFile)
	var summary *RunSummary
	scanner := bufio.NewScanner(stdout)
	// Lines can be large for big files; extend buffer beyond default 64K.
	const max = 64 * 1024 * 1024
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, max)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// The summary line has no "path"; a strict prefix check keeps this from ever
		// swallowing a real file document.
		if strings.HasPrefix(line, `{"summary":"csharp_indexer_run"`) {
			var sum RunSummary
			if err := json.Unmarshal([]byte(line), &sum); err == nil && sum.Summary == "csharp_indexer_run" {
				summary = &sum
			}
			continue
		}
		pf, err := indexer.ParsedFileFromJSON([]byte(line), "")
		if err != nil {
			continue
		}
		if pf == nil || pf.Path == "" {
			continue
		}
		path := filepath.ToSlash(strings.TrimSpace(pf.Path))
		path = strings.TrimPrefix(path, "/")
		pf.Path = path
		pf.Lang = "csharp"
		byPath[path] = pf
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("csharp-indexer: read stdout: %w", err)
	}
	return byPath, summary, nil
}
