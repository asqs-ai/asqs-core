package jstindexer

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Defaults for JS/TS indexer in Docker (P1).
const (
	DefaultNodeDockerImage    = "node:20-bookworm"
	defaultNodeDockerWorkdir  = "/workspace"
	defaultNodeIndexerMount   = "/indexer"
	defaultNodeJsonlMountPath = "/out/asqs-jst-index.jsonl"
	defaultNodeDockerNetwork  = "none"
	defaultNodeDockerHeapMB   = 4096
)

// NodeDockerConfig configures ephemeral `docker run --rm` for the JS/TS Node indexer.
type NodeDockerConfig struct {
	Image          string
	CLI            string
	Memory         string
	CPUs           float64
	Network        string
	Workdir        string // repo mount in container
	HeapMB         int    // NODE_OPTIONS --max-old-space-size; 0 = defaultNodeDockerHeapMB
	IndexerMount   string // mount point for tools/js-ts-indexer root; empty = defaultNodeIndexerMount
	JsonlMountPath string // path inside container for --jsonl-out; empty = defaultNodeJsonlMountPath
}

func (c *NodeDockerConfig) cli() string {
	if c == nil || strings.TrimSpace(c.CLI) == "" {
		return "docker"
	}
	return strings.TrimSpace(c.CLI)
}

func (c *NodeDockerConfig) workdir() string {
	if c == nil || strings.TrimSpace(c.Workdir) == "" {
		return defaultNodeDockerWorkdir
	}
	return filepath.ToSlash(strings.TrimSpace(c.Workdir))
}

func (c *NodeDockerConfig) indexerMount() string {
	if c == nil || strings.TrimSpace(c.IndexerMount) == "" {
		return defaultNodeIndexerMount
	}
	return filepath.ToSlash(strings.TrimSpace(c.IndexerMount))
}

func (c *NodeDockerConfig) jsonlInContainer() string {
	if c == nil || strings.TrimSpace(c.JsonlMountPath) == "" {
		return defaultNodeJsonlMountPath
	}
	return filepath.ToSlash(strings.TrimSpace(c.JsonlMountPath))
}

func (c *NodeDockerConfig) network() string {
	if c == nil || strings.TrimSpace(c.Network) == "" {
		return defaultNodeDockerNetwork
	}
	return strings.TrimSpace(c.Network)
}

func (c *NodeDockerConfig) image() string {
	if c == nil || strings.TrimSpace(c.Image) == "" {
		return DefaultNodeDockerImage
	}
	return strings.TrimSpace(c.Image)
}

func (c *NodeDockerConfig) heapMB() int {
	if c == nil || c.HeapMB <= 0 {
		return defaultNodeDockerHeapMB
	}
	return c.HeapMB
}

// JSTToolRootFromEntry returns the package root directory given an absolute path to dist/index.js (or any file under dist/).
func JSTToolRootFromEntry(absEntry string) (string, error) {
	absEntry = filepath.Clean(absEntry)
	d := filepath.Dir(absEntry)
	if !strings.EqualFold(filepath.Base(d), "dist") {
		return "", fmt.Errorf("js-ts-indexer: entry must be under a dist/ directory (e.g. tools/js-ts-indexer/dist/index.js); got %q", absEntry)
	}
	return filepath.Dir(d), nil
}

// BuildNodeDockerRunArgs returns argv for `docker <args...>` (excluding the docker binary).
// hostRepoAbs, hostToolRootAbs, hostJsonlAbs must be absolute. relEntry is repo-relative path from hostToolRoot to the indexer script (e.g. dist/index.js).
func BuildNodeDockerRunArgs(hostRepoAbs, hostToolRootAbs, hostJsonlAbs, relEntry string, skipPrefixesCSV string, cfg *NodeDockerConfig) ([]string, error) {
	hostRepoAbs = filepath.Clean(hostRepoAbs)
	hostToolRootAbs = filepath.Clean(hostToolRootAbs)
	hostJsonlAbs = filepath.Clean(hostJsonlAbs)
	if !filepath.IsAbs(hostRepoAbs) || !filepath.IsAbs(hostToolRootAbs) || !filepath.IsAbs(hostJsonlAbs) {
		return nil, fmt.Errorf("js-ts-indexer docker: host repo, tool root, and jsonl paths must be absolute")
	}
	relEntry = filepath.ToSlash(strings.TrimSpace(relEntry))
	if relEntry == "" || strings.HasPrefix(relEntry, "../") {
		return nil, fmt.Errorf("js-ts-indexer docker: invalid relEntry %q", relEntry)
	}
	if cfg == nil {
		cfg = &NodeDockerConfig{}
	}
	w := cfg.workdir()
	idx := cfg.indexerMount()
	jOut := cfg.jsonlInContainer()
	nodeScript := idx + "/" + strings.TrimPrefix(relEntry, "/")

	args := []string{"run", "--rm", "--init",
		"-v", hostRepoAbs + ":" + w + ":ro",
		"-v", hostToolRootAbs + ":" + idx + ":ro",
		"-v", hostJsonlAbs + ":" + jOut + ":rw",
		"-w", w,
		"--network", cfg.network(),
		"-e", "NODE_OPTIONS=--max-old-space-size=" + strconv.Itoa(cfg.heapMB()),
	}
	if strings.TrimSpace(skipPrefixesCSV) != "" {
		args = append(args, "-e", "ASQS_INDEXER_SKIP_PATH_PREFIXES="+skipPrefixesCSV)
	}
	if m := strings.TrimSpace(cfg.Memory); m != "" {
		args = append(args, "--memory", m)
	}
	if cfg.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(cfg.CPUs, 'f', -1, 64))
	}
	args = append(args, cfg.image(), "node", nodeScript, "--repo", w, "--jsonl-out", jOut)
	return args, nil
}
