// Package jstindexer runs the JS/TS Node indexer (tools/js-ts-indexer) and maps its
// JSONL stdout into the same ParsedFile contract as the Java indexer so the Go
// pipeline can use indexer.Run unchanged.
package jstindexer

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

// ProjectMeta is emitted by the JS/TS indexer (path "asqs-meta:project") with runtime, test framework, package manager, and scripts for evaluation.
type ProjectMeta struct {
	Runtime        string            `json:"runtime"` // "nest", "node", "react", "angular", "vue", "solid", "angularjs"
	TestFramework  string            `json:"test_framework"`
	PackageManager string            `json:"package_manager"`
	Scripts        map[string]string `json:"scripts"`
}

// metaLine is used to detect and parse the asqs-meta:project line without requiring full LangIndexerJSON.
type metaLine struct {
	Path        string       `json:"path"`
	ProjectMeta *ProjectMeta `json:"project_meta,omitempty"`
}

type diEdgeLine struct {
	CallerFQName string `json:"caller_fq_name"`
	CalleeFQName string `json:"callee_fq_name"`
	EdgeType     string `json:"edge_type"`
}

type jsDIDataLine struct {
	DIInjectedTypes     []diEdgeLine `json:"di_injected_types"`
	DIRegisteredService []diEdgeLine `json:"di_registered_services"`
	DIImplementsService []diEdgeLine `json:"di_implements_services"`
}

// RunIndexerConfig controls local `node` vs Docker execution and JSONL transport.
type RunIndexerConfig struct {
	Timeout          time.Duration
	SkipPathPrefixes []string
	// AllowEmptyResult when true returns an empty map instead of an error if the indexer emits no files
	// (e.g. mono primary workspace has no JS/TS while mono_repo_extra_paths holds a package).
	AllowEmptyResult bool
	// JsonlOutSpec: empty = stdout (local only). "temp" / explicit path = --jsonl-out.
	// Docker mode: empty spec forces a host temp file mounted into the container (avoids huge JSONL via docker attach).
	JsonlOutSpec string
	// Docker non-nil: run `node ...` inside ephemeral docker run --rm (host does not need Node).
	Docker *NodeDockerConfig
}

// RunIndexer runs the JS/TS indexer CLI, parses JSONL, returns path → ParsedFile and optional ProjectMeta.
// See RunIndexerConfig for Docker and jsonl-out behavior.
func RunIndexer(ctx context.Context, repoPath, indexerPath string, cfg RunIndexerConfig) (map[string]*indexer.ParsedFile, *ProjectMeta, error) {
	if cfg.Docker != nil {
		return runIndexerDocker(ctx, repoPath, indexerPath, cfg)
	}
	return runIndexerLocal(ctx, repoPath, indexerPath, cfg)
}

func runIndexerLocal(ctx context.Context, repoPath, indexerPath string, cfg RunIndexerConfig) (map[string]*indexer.ParsedFile, *ProjectMeta, error) {
	indexerPath = strings.TrimSpace(indexerPath)
	if indexerPath == "" {
		return nil, nil, fmt.Errorf("js-ts-indexer: empty indexer path")
	}
	if !filepath.IsAbs(indexerPath) {
		abs, err := filepath.Abs(indexerPath)
		if err != nil {
			return nil, nil, fmt.Errorf("js-ts-indexer: resolve path: %w", err)
		}
		indexerPath = abs
	}
	if _, err := os.Stat(indexerPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("js-ts-indexer: not found at %s (build with: cd tools/js-ts-indexer && npm run build)", indexerPath)
		}
		return nil, nil, fmt.Errorf("js-ts-indexer: %w", err)
	}
	repoPath = filepath.Clean(repoPath)
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("js-ts-indexer: repo path: %w", err)
	}

	jsonlPath, removeJSONL, err := resolveJSTJsonlOut(strings.TrimSpace(cfg.JsonlOutSpec))
	if err != nil {
		return nil, nil, fmt.Errorf("js-ts-indexer: jsonl out: %w", err)
	}
	if removeJSONL != nil {
		defer removeJSONL()
	}

	runCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	args := []string{indexerPath, "--repo", absRepo}
	if jsonlPath != "" {
		args = append(args, "--jsonl-out", jsonlPath)
	}
	cmd := exec.CommandContext(runCtx, "node", args...)
	cmd.Dir = absRepo
	baseEnv := os.Environ()
	if len(cfg.SkipPathPrefixes) > 0 {
		var normalized []string
		for _, p := range cfg.SkipPathPrefixes {
			p = strings.TrimSpace(filepath.ToSlash(strings.TrimSuffix(p, "/")))
			if p != "" {
				normalized = append(normalized, p)
			}
		}
		if len(normalized) > 0 {
			baseEnv = append(baseEnv, "ASQS_INDEXER_SKIP_PATH_PREFIXES="+strings.Join(normalized, ","))
		}
	}
	cmd.Env = envWithNodeMemory(baseEnv, 4096)

	var stdoutPipe io.ReadCloser
	if jsonlPath == "" {
		stdoutPipe, err = cmd.StdoutPipe()
		if err != nil {
			return nil, nil, fmt.Errorf("js-ts-indexer: stdout pipe: %w", err)
		}
	} else {
		cmd.Stdout = io.Discard
	}

	var stderrBuf strings.Builder
	cmd.Stderr = io.MultiWriter(&stderrBuf, os.Stderr)
	if err := cmd.Start(); err != nil {
		if stdoutPipe != nil {
			_ = stdoutPipe.Close()
		}
		return nil, nil, fmt.Errorf("js-ts-indexer: start: %w", err)
	}

	var byPath map[string]*indexer.ParsedFile
	var projectMeta *ProjectMeta

	if jsonlPath == "" {
		byPath, projectMeta, err = parseIndexerOutput(stdoutPipe, nil)
		_ = stdoutPipe.Close()
		if err != nil {
			_ = cmd.Wait()
			return nil, nil, err
		}
	}

	waitErr := cmd.Wait()
	if jsonlPath != "" {
		if waitErr != nil {
			return nil, nil, fmt.Errorf("js-ts-indexer: exit: %w\n%s", waitErr, strings.TrimSpace(stderrBuf.String()))
		}
		f, oerr := os.Open(jsonlPath)
		if oerr != nil {
			return nil, nil, fmt.Errorf("js-ts-indexer: open jsonl out %q: %w", jsonlPath, oerr)
		}
		byPath, projectMeta, err = parseIndexerOutput(f, nil)
		_ = f.Close()
		if err != nil {
			return nil, nil, err
		}
	}

	return finishRunIndexer(byPath, projectMeta, waitErr, cfg.AllowEmptyResult, jsonlPath, indexerPath, absRepo, stderrBuf)
}

func runIndexerDocker(ctx context.Context, repoPath, indexerPath string, cfg RunIndexerConfig) (map[string]*indexer.ParsedFile, *ProjectMeta, error) {
	if cfg.Docker == nil {
		return nil, nil, fmt.Errorf("js-ts-indexer: docker run requested but Docker config is nil")
	}
	indexerPath = strings.TrimSpace(indexerPath)
	if indexerPath == "" {
		return nil, nil, fmt.Errorf("js-ts-indexer: empty indexer path")
	}
	if !filepath.IsAbs(indexerPath) {
		abs, err := filepath.Abs(indexerPath)
		if err != nil {
			return nil, nil, fmt.Errorf("js-ts-indexer: resolve path: %w", err)
		}
		indexerPath = abs
	}
	if _, err := os.Stat(indexerPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("js-ts-indexer: not found at %s (build with: cd tools/js-ts-indexer && npm run build)", indexerPath)
		}
		return nil, nil, fmt.Errorf("js-ts-indexer: %w", err)
	}
	toolRoot, err := JSTToolRootFromEntry(indexerPath)
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(filepath.Join(toolRoot, "node_modules")); err != nil {
		return nil, nil, fmt.Errorf("js-ts-indexer docker: node_modules missing under %s (run npm ci in tools/js-ts-indexer): %w", toolRoot, err)
	}

	repoPath = filepath.Clean(repoPath)
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("js-ts-indexer: repo path: %w", err)
	}

	jsonlSpec := strings.TrimSpace(cfg.JsonlOutSpec)
	if jsonlSpec == "" {
		jsonlSpec = "temp"
	}
	jsonlPath, removeJSONL, err := resolveJSTJsonlOut(jsonlSpec)
	if err != nil {
		return nil, nil, fmt.Errorf("js-ts-indexer: jsonl out: %w", err)
	}
	if removeJSONL != nil {
		defer removeJSONL()
	}

	relEntry, err := filepath.Rel(toolRoot, indexerPath)
	if err != nil || strings.HasPrefix(relEntry, "..") {
		return nil, nil, fmt.Errorf("js-ts-indexer docker: indexer path %q must be under tool root %q", indexerPath, toolRoot)
	}
	relSlash := filepath.ToSlash(relEntry)

	skipCSV := skipPrefixesCSV(cfg.SkipPathPrefixes)

	dargs, err := BuildNodeDockerRunArgs(absRepo, toolRoot, jsonlPath, relSlash, skipCSV, cfg.Docker)
	if err != nil {
		return nil, nil, err
	}

	runCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	cli := cfg.Docker.cli()
	fmt.Fprintf(os.Stderr, "  js-ts-indexer: %s %s\n", cli, strings.Join(dargs, " "))
	cmd := exec.CommandContext(runCtx, cli, dargs...)
	cmd.Env = os.Environ()
	cmd.Stdout = io.Discard

	var stderrBuf strings.Builder
	cmd.Stderr = io.MultiWriter(&stderrBuf, os.Stderr)
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("js-ts-indexer docker: start: %w", err)
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		return nil, nil, fmt.Errorf("js-ts-indexer docker: exit: %w\n%s", waitErr, strings.TrimSpace(stderrBuf.String()))
	}

	f, oerr := os.Open(jsonlPath)
	if oerr != nil {
		return nil, nil, fmt.Errorf("js-ts-indexer docker: open jsonl %q: %w", jsonlPath, oerr)
	}
	norm := &PathNormalizer{HostRepoAbs: absRepo, ContainerWorkDir: cfg.Docker.workdir()}
	byPath, projectMeta, err := parseIndexerOutput(f, norm)
	_ = f.Close()
	if err != nil {
		return nil, nil, err
	}

	return finishRunIndexer(byPath, projectMeta, waitErr, cfg.AllowEmptyResult, jsonlPath, indexerPath, absRepo, stderrBuf)
}

func skipPrefixesCSV(prefixes []string) string {
	var normalized []string
	for _, p := range prefixes {
		p = strings.TrimSpace(filepath.ToSlash(strings.TrimSuffix(p, "/")))
		if p != "" {
			normalized = append(normalized, p)
		}
	}
	return strings.Join(normalized, ",")
}

func finishRunIndexer(byPath map[string]*indexer.ParsedFile, projectMeta *ProjectMeta, waitErr error, allowEmpty bool, jsonlPath, indexerPath, absRepo string, stderrBuf strings.Builder) (map[string]*indexer.ParsedFile, *ProjectMeta, error) {
	if len(byPath) == 0 {
		if allowEmpty {
			if byPath == nil {
				byPath = map[string]*indexer.ParsedFile{}
			}
			return byPath, projectMeta, nil
		}
		if projectMeta != nil {
			return byPath, projectMeta, nil
		}
		msg := "js-ts-indexer: produced no parsed files (check --repo path and that package.json/tsconfig exists)"
		if stderrBuf.Len() > 0 {
			msg += "; stderr: " + strings.TrimSpace(stderrBuf.String())
		}
		if waitErr != nil {
			msg += "; exit: " + waitErr.Error()
		}
		msg += ". If the process was killed (e.g. out of memory), set NODE_OPTIONS=--max-old-space-size=4096 or higher. To debug, run: node " + indexerPath + " --repo " + absRepo
		return nil, nil, fmt.Errorf("%s", msg)
	}
	return byPath, projectMeta, nil
}

// resolveJSTJsonlOut returns absolute path for Node --jsonl-out, or ("", nil, nil) for stdout mode.
// remove callback deletes a temp file when non-nil.
func resolveJSTJsonlOut(spec string) (path string, remove func(), err error) {
	if spec == "" {
		return "", nil, nil
	}
	low := strings.ToLower(spec)
	if low == "temp" || low == "tmp" || low == ":temp" {
		f, err := os.CreateTemp("", "asqs-jst-index-*.jsonl")
		if err != nil {
			return "", nil, err
		}
		name := f.Name()
		if err := f.Close(); err != nil {
			_ = os.Remove(name)
			return "", nil, err
		}
		return name, func() { _ = os.Remove(name) }, nil
	}
	abs, err := filepath.Abs(spec)
	if err != nil {
		return "", nil, err
	}
	if d := filepath.Dir(abs); d != "." && d != "/" {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", nil, err
		}
	}
	return abs, nil, nil
}

// maxLineSize is the maximum length of a single JSONL line we accept (default bufio.Scanner is 64KB).
const maxLineSize = 16 * 1024 * 1024 // 16 MB

const parseErrLineMaxRunes = 512

func lineParseErrSnippet(line string) string {
	line = strings.TrimSpace(line)
	if len(line) <= parseErrLineMaxRunes {
		return line
	}
	return line[:parseErrLineMaxRunes] + "…(truncated)"
}

// parseIndexerOutput reads JSONL from r. norm adjusts absolute paths from Docker or host into repo-relative keys.
func parseIndexerOutput(r io.Reader, norm *PathNormalizer) (map[string]*indexer.ParsedFile, *ProjectMeta, error) {
	byPath := make(map[string]*indexer.ParsedFile)
	var projectMeta *ProjectMeta
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxLineSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var metaOnly metaLine
		if err := json.Unmarshal([]byte(line), &metaOnly); err == nil && metaOnly.Path == "asqs-meta:project" && metaOnly.ProjectMeta != nil {
			projectMeta = metaOnly.ProjectMeta
			continue
		}
		pf, err := indexer.ParsedFileFromJSON([]byte(line), "")
		if err != nil {
			return nil, nil, fmt.Errorf("js-ts-indexer: parse line %q: %w", lineParseErrSnippet(line), err)
		}
		var diLine jsDIDataLine
		if err := json.Unmarshal([]byte(line), &diLine); err == nil {
			appendDIEdgesFromExtractor(pf, diLine)
		}
		path := NormalizeJSTIndexerPath(pf.Path, norm)
		path = filepath.ToSlash(path)
		path = strings.TrimPrefix(path, "/")
		pf.Path = path
		byPath[path] = pf
		putByPathVariants(byPath, path, pf)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("js-ts-indexer: read: %w", err)
	}
	return byPath, projectMeta, nil
}

// envWithNodeMemory returns env with NODE_OPTIONS=--max-old-space-size=<mb> set if NODE_OPTIONS is not already set.
func envWithNodeMemory(env []string, heapMB int) []string {
	for _, e := range env {
		if strings.HasPrefix(e, "NODE_OPTIONS=") {
			return env
		}
	}
	return append(env, fmt.Sprintf("NODE_OPTIONS=--max-old-space-size=%d", heapMB))
}

func putByPathVariants(byPath map[string]*indexer.ParsedFile, path string, pf *indexer.ParsedFile) {
	byPath[path] = pf
	byPath[strings.TrimPrefix(path, "/")] = pf
	byPath[filepath.ToSlash(filepath.Clean(path))] = pf
}

func appendDIEdgesFromExtractor(pf *indexer.ParsedFile, in jsDIDataLine) {
	if pf == nil {
		return
	}
	add := func(edges []diEdgeLine) {
		for _, de := range edges {
			caller := strings.TrimSpace(de.CallerFQName)
			callee := strings.TrimSpace(de.CalleeFQName)
			edgeType := indexer.CanonicalEdgeType(de.EdgeType)
			if caller == "" || callee == "" || edgeType == "" {
				continue
			}
			pf.Edges = append(pf.Edges, indexer.ParsedEdge{
				CallerFQName: caller,
				CalleeFQName: callee,
				EdgeType:     edgeType,
			})
		}
	}
	add(in.DIInjectedTypes)
	add(in.DIRegisteredService)
	add(in.DIImplementsService)
	if len(pf.Edges) > 1 {
		pf.Edges = dedupeParsedEdges(pf.Edges)
	}
}

func dedupeParsedEdges(in []indexer.ParsedEdge) []indexer.ParsedEdge {
	seen := make(map[string]struct{}, len(in))
	out := make([]indexer.ParsedEdge, 0, len(in))
	for _, e := range in {
		key := strings.TrimSpace(e.CallerFQName) + "\x00" +
			strings.TrimSpace(e.CalleeFQName) + "\x00" +
			indexer.CanonicalEdgeType(e.EdgeType)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return out
}
