package csharpindexer

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultDotnetDockerWorkdir   = "/workspace"
	defaultDllInContainer        = "/indexer/CSharpIndexer.dll"
	defaultDockerNetwork         = "none"
	defaultDotnetIndexerImage    = "mcr.microsoft.com/dotnet/sdk:10.0"
)

// DotnetDockerConfig configures ephemeral `docker run --rm` for the C# indexer DLL.
type DotnetDockerConfig struct {
	Image            string
	CLI              string
	Memory           string
	CPUs             float64
	Network          string
	Workdir          string
	DllContainerPath string
}

func (c *DotnetDockerConfig) cli() string {
	if c == nil || strings.TrimSpace(c.CLI) == "" {
		return "docker"
	}
	return strings.TrimSpace(c.CLI)
}

func (c *DotnetDockerConfig) workdir() string {
	if c == nil || strings.TrimSpace(c.Workdir) == "" {
		return defaultDotnetDockerWorkdir
	}
	return filepath.ToSlash(strings.TrimSpace(c.Workdir))
}

func (c *DotnetDockerConfig) dllInContainer() string {
	if c == nil || strings.TrimSpace(c.DllContainerPath) == "" {
		return defaultDllInContainer
	}
	return filepath.ToSlash(strings.TrimSpace(c.DllContainerPath))
}

func (c *DotnetDockerConfig) network() string {
	if c == nil || strings.TrimSpace(c.Network) == "" {
		return defaultDockerNetwork
	}
	return strings.TrimSpace(c.Network)
}

func (c *DotnetDockerConfig) image() string {
	if c == nil || strings.TrimSpace(c.Image) == "" {
		return defaultDotnetIndexerImage
	}
	return strings.TrimSpace(c.Image)
}

// BuildDotnetDockerRunArgs returns argv for `docker <args...>` (excluding the docker binary).
// hostDllAbs must point at a DLL inside a dotnet publish output directory; the whole directory is
// mounted read-only so runtimeconfig.json, deps.json, and dependency assemblies are visible (mounting
// only the DLL makes dotnet treat the app as broken self-contained and fail on libhostpolicy.so).
func BuildDotnetDockerRunArgs(hostRepoAbs, hostDllAbs string, cfg *DotnetDockerConfig) ([]string, error) {
	hostRepoAbs = filepath.Clean(hostRepoAbs)
	hostDllAbs = filepath.Clean(hostDllAbs)
	if !filepath.IsAbs(hostRepoAbs) || !filepath.IsAbs(hostDllAbs) {
		return nil, fmt.Errorf("csharp-indexer docker: host repo and dll paths must be absolute")
	}
	if cfg == nil {
		cfg = &DotnetDockerConfig{}
	}
	w := cfg.workdir()
	dllIn := cfg.dllInContainer()
	hostBase := filepath.Base(hostDllAbs)
	if !strings.EqualFold(path.Base(dllIn), hostBase) {
		return nil, fmt.Errorf("csharp-indexer docker: DLL container path %q must end with host DLL name %q", dllIn, hostBase)
	}
	containerIndexerDir := path.Dir(dllIn)
	if containerIndexerDir == "" || containerIndexerDir == "." || !strings.HasPrefix(containerIndexerDir, "/") {
		return nil, fmt.Errorf("csharp-indexer docker: invalid DLL container path %q (use an absolute path like /indexer/CSharpIndexer.dll)", dllIn)
	}
	hostPublishDir := filepath.Dir(hostDllAbs)
	args := []string{"run", "--rm", "--init",
		"-v", hostRepoAbs + ":" + w + ":ro",
		"-v", hostPublishDir + ":" + containerIndexerDir + ":ro",
		"-w", w,
		"--network", cfg.network(),
	}
	if m := strings.TrimSpace(cfg.Memory); m != "" {
		args = append(args, "--memory", m)
	}
	if cfg.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(cfg.CPUs, 'f', -1, 64))
	}
	args = append(args, cfg.image(), "dotnet", dllIn, w)
	return args, nil
}
