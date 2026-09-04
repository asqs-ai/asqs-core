package uitesthooks

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Hook is one attribute the pass added.
type Hook struct {
	Name    string `json:"name"`
	Line    int    `json:"line"`
	Element string `json:"element"`
}

// FileApplication is the outcome for one target file.
type FileApplication struct {
	Rel     string
	Kind    string
	Added   []Hook
	Skipped int
	// Error is set when the file was left untouched because the inserter refused it.
	Error string
}

// Result is the outcome of Apply.
type Result struct {
	Applied    []FileApplication
	Unchanged  []string
	Failed     []FileApplication
	TotalAdded int
}

// ChangedRels lists the files Apply wrote, sorted.
func (r Result) ChangedRels() []string {
	out := make([]string, 0, len(r.Applied))
	for _, a := range r.Applied {
		out = append(out, a.Rel)
	}
	sort.Strings(out)
	return out
}

// NodePathEnv lets an operator point the pass at a directory whose node_modules contains the
// typescript package, for repositories that do not vendor it themselves.
const NodePathEnv = "ASQS_UI_TEST_HOOKS_NODE_PATH"

//go:embed jsxhooks/apply.mjs
var jsxApplyScript string

var (
	scriptOnce sync.Once
	scriptPath string
	scriptErr  error
)

// applyScriptPath materializes the embedded script once per process, exactly as the TS/JS seam
// does: locating it relative to the binary would break in any container or customer install.
func applyScriptPath() (string, error) {
	scriptOnce.Do(func() {
		dir, err := os.MkdirTemp("", "asqs-ui-test-hooks-")
		if err != nil {
			scriptErr = fmt.Errorf("ui test hooks: create script dir: %w", err)
			return
		}
		p := filepath.Join(dir, "apply.mjs")
		if err := os.WriteFile(p, []byte(jsxApplyScript), 0o600); err != nil {
			scriptErr = fmt.Errorf("ui test hooks: write apply script: %w", err)
			return
		}
		scriptPath = p
	})
	return scriptPath, scriptErr
}

// ResolveDirs is the ordered list of directories the script resolves `typescript` from: the
// repository first (a TS/JS project almost always vendors it), then the operator's override, then
// any extra fallbacks the caller knows about (asqs-go passes the js-ts-indexer's node_modules).
func ResolveDirs(repoRoot string, extra ...string) []string {
	var dirs []string
	if r := strings.TrimSpace(repoRoot); r != "" {
		dirs = append(dirs, r)
	}
	if env := strings.TrimSpace(os.Getenv(NodePathEnv)); env != "" {
		dirs = append(dirs, env)
	}
	for _, e := range extra {
		if strings.TrimSpace(e) != "" {
			dirs = append(dirs, e)
		}
	}
	return dirs
}

type jsxRequest struct {
	FileName   string `json:"fileName"`
	Source     string `json:"source"`
	Prefix     string `json:"prefix"`
	MaxPerFile int    `json:"maxPerFile"`
}

type jsxResult struct {
	OK      bool   `json:"ok"`
	Source  string `json:"source"`
	Changed bool   `json:"changed"`
	Added   []Hook `json:"added"`
	Skipped int    `json:"skipped"`
	Error   string `json:"error"`
}

type jsxBatch struct {
	ResolveFrom []string     `json:"resolveFrom"`
	Requests    []jsxRequest `json:"requests"`
}

type jsxBatchResponse struct {
	OK      bool        `json:"ok"`
	Error   string      `json:"error"`
	Results []jsxResult `json:"results"`
}

// batchTimeout bounds one node invocation for the whole batch; loading the TypeScript compiler
// dominates, so one process serves every file.
const batchTimeout = 120 * time.Second

// runJSXBatch transforms every request in one node process.
func runJSXBatch(ctx context.Context, resolveFrom []string, reqs []jsxRequest) ([]jsxResult, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	script, err := applyScriptPath()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(jsxBatch{ResolveFrom: resolveFrom, Requests: reqs})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("ui test hooks node: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("ui test hooks node: %w", err)
	}
	var resp jsxBatchResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("ui test hooks: invalid json from apply script: %w (stdout=%q)", err, strings.TrimSpace(stdout.String()))
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "unknown error from apply script"
		}
		return nil, fmt.Errorf("ui test hooks: %s", resp.Error)
	}
	if len(resp.Results) != len(reqs) {
		return nil, fmt.Errorf("ui test hooks: apply script returned %d results for %d requests", len(resp.Results), len(reqs))
	}
	return resp.Results, nil
}

// Apply edits the planned targets in place. beforeWrite is called with the absolute and
// repo-relative path of every file about to change, BEFORE it changes, so the caller can journal
// it for rollback; nil is allowed. resolveExtra are additional directories for resolving the
// typescript package (see ResolveDirs).
//
// Files are read and written whole; an inserter that refuses a file (parse got worse, no
// typescript available) leaves it byte-identical and reports it under Failed.
func Apply(ctx context.Context, repoRoot string, plan Plan, opts Options, beforeWrite func(full, rel string), resolveExtra ...string) (Result, error) {
	opts = opts.Normalized()
	var res Result
	root := filepath.Clean(repoRoot)
	var jsxTargets []Target
	var jsxReqs []jsxRequest
	for _, t := range plan.Targets {
		full := filepath.Join(root, filepath.FromSlash(t.Rel))
		raw, err := os.ReadFile(full)
		if err != nil {
			res.Failed = append(res.Failed, FileApplication{Rel: t.Rel, Kind: t.Kind, Error: err.Error()})
			continue
		}
		switch t.Kind {
		case "html":
			out := ApplyHTML(string(raw), FilePrefix(t.Rel), opts.MaxPerFile)
			if !out.Changed {
				res.Unchanged = append(res.Unchanged, t.Rel)
				continue
			}
			if beforeWrite != nil {
				beforeWrite(full, t.Rel)
			}
			if err := os.WriteFile(full, []byte(out.Source), 0o644); err != nil {
				res.Failed = append(res.Failed, FileApplication{Rel: t.Rel, Kind: t.Kind, Error: err.Error()})
				continue
			}
			res.Applied = append(res.Applied, FileApplication{Rel: t.Rel, Kind: t.Kind, Added: out.Added, Skipped: out.Skipped})
			res.TotalAdded += len(out.Added)
		case "jsx":
			jsxTargets = append(jsxTargets, t)
			jsxReqs = append(jsxReqs, jsxRequest{FileName: t.Rel, Source: string(raw), Prefix: FilePrefix(t.Rel), MaxPerFile: opts.MaxPerFile})
		}
	}
	if len(jsxReqs) == 0 {
		return res, nil
	}
	results, err := runJSXBatch(ctx, ResolveDirs(root, resolveExtra...), jsxReqs)
	if err != nil {
		return res, err
	}
	for i, r := range results {
		t := jsxTargets[i]
		full := filepath.Join(root, filepath.FromSlash(t.Rel))
		switch {
		case !r.OK:
			res.Failed = append(res.Failed, FileApplication{Rel: t.Rel, Kind: t.Kind, Error: r.Error})
		case !r.Changed:
			res.Unchanged = append(res.Unchanged, t.Rel)
		default:
			if beforeWrite != nil {
				beforeWrite(full, t.Rel)
			}
			if err := os.WriteFile(full, []byte(r.Source), 0o644); err != nil {
				res.Failed = append(res.Failed, FileApplication{Rel: t.Rel, Kind: t.Kind, Error: err.Error()})
				continue
			}
			res.Applied = append(res.Applied, FileApplication{Rel: t.Rel, Kind: t.Kind, Added: r.Added, Skipped: r.Skipped})
			res.TotalAdded += len(r.Added)
		}
	}
	return res, nil
}
