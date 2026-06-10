package javaindexer

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Defaults for advanced Java indexer in Docker (P0).
const (
	DefaultJavaDockerImage    = "eclipse-temurin:21-jre-jammy"
	defaultJavaDockerWorkdir  = "/workspace"
	defaultJavaJarInContainer = "/indexer/java-indexer.jar"
	defaultDockerNetwork      = "none"
)

// JavaDockerConfig configures ephemeral `docker run --rm` for the advanced JAR indexer.
type JavaDockerConfig struct {
	Image            string  // container image (required; DefaultJavaDockerImage if empty at runtime)
	CLI              string  // docker binary; default "docker"
	Memory           string  // e.g. "4g"; empty = omit --memory
	CPUs             float64 // >0 = --cpus
	Network          string  // docker --network; empty = defaultJavaDockerNetwork
	Workdir          string  // container mount for repo; empty = defaultJavaDockerWorkdir
	JARContainerPath string  // mount target for JAR inside container; empty = defaultJavaJarInContainer
}

func (c *JavaDockerConfig) cli() string {
	if c == nil || strings.TrimSpace(c.CLI) == "" {
		return "docker"
	}
	return strings.TrimSpace(c.CLI)
}

func (c *JavaDockerConfig) workdir() string {
	if c == nil || strings.TrimSpace(c.Workdir) == "" {
		return defaultJavaDockerWorkdir
	}
	return filepath.ToSlash(strings.TrimSpace(c.Workdir))
}

func (c *JavaDockerConfig) jarInContainer() string {
	if c == nil || strings.TrimSpace(c.JARContainerPath) == "" {
		return defaultJavaJarInContainer
	}
	return filepath.ToSlash(strings.TrimSpace(c.JARContainerPath))
}

func (c *JavaDockerConfig) network() string {
	if c == nil || strings.TrimSpace(c.Network) == "" {
		return defaultDockerNetwork
	}
	return strings.TrimSpace(c.Network)
}

func (c *JavaDockerConfig) image() string {
	if c == nil || strings.TrimSpace(c.Image) == "" {
		return DefaultJavaDockerImage
	}
	return strings.TrimSpace(c.Image)
}

// BuildJavaDockerRunArgs returns argv for `docker <args...>` (excluding the docker binary itself).
// hostRepoAbs and hostJarAbs must be absolute paths on the host suitable for bind mounts.
func BuildJavaDockerRunArgs(hostRepoAbs, hostJarAbs string, cfg *JavaDockerConfig) ([]string, error) {
	hostRepoAbs = filepath.Clean(hostRepoAbs)
	hostJarAbs = filepath.Clean(hostJarAbs)
	if !filepath.IsAbs(hostRepoAbs) || !filepath.IsAbs(hostJarAbs) {
		return nil, fmt.Errorf("java-indexer docker: host repo and jar paths must be absolute")
	}
	if cfg == nil {
		cfg = &JavaDockerConfig{}
	}
	w := cfg.workdir()
	jarIn := cfg.jarInContainer()
	args := []string{"run", "--rm", "--init",
		"-v", hostRepoAbs + ":" + w + ":ro",
		"-v", hostJarAbs + ":" + jarIn + ":ro",
		"-w", w,
		"--network", cfg.network(),
	}
	if m := strings.TrimSpace(cfg.Memory); m != "" {
		args = append(args, "--memory", m)
	}
	if cfg.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(cfg.CPUs, 'f', -1, 64))
	}
	args = append(args, cfg.image(), "java", "-jar", jarIn, w)
	return args, nil
}
