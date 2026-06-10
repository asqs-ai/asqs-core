// Package jobrunner runs isolated evaluation jobs (one ephemeral container per job by default).
package jobrunner

import (
	"context"
	"time"
)

// CacheMount binds a named Docker volume (or host path) into the job for dependency caches.
type CacheMount struct {
	// Source is a Docker volume name (e.g. "asqs-m2-cache") or absolute host path.
	Source string
	// Target is the path inside the container (e.g. "/root/.m2").
	Target string
	// ReadOnly when true mounts the cache read-only (not recommended for first populate).
	ReadOnly bool
}

// JobSpec describes a single sandbox job (one container run).
type JobSpec struct {
	// Image OCI image (e.g. eclipse-temurin:21-jdk).
	Image string
	// HostWorkDir is the absolute host path to the repo root; always mounted read-write at /workspace in the container.
	HostWorkDir string
	// Workdir is the container working directory (-w), usually /workspace or a subdirectory (e.g. mono-repo workspace).
	Workdir string
	// Command is argv executed as the container entrypoint (e.g. ["./mvnw", "test", "-q", "-B"]).
	Command []string
	// Env appended to container env (e.g. CI=true).
	Env []string
	// Timeout for the whole job (wall clock).
	Timeout time.Duration
	// Memory limit Docker notation (e.g. "4g", "512m"). Empty = Docker default.
	Memory string
	// CPUs max (e.g. 2.0). 0 = Docker default.
	CPUs float64
	// PidsLimit caps processes in the container (mitigate fork bombs). 0 = Docker default.
	PidsLimit int64
	// NetworkMode: "bridge" (default), "none", or custom Docker network name.
	NetworkMode string
	// ReadonlyRootfs when true uses --read-only with tmpfs for /tmp (stricter; may break some builds).
	ReadonlyRootfs bool
	// IpcHost when true adds docker --ipc=host (recommended for Chromium in Playwright containers).
	IpcHost bool
	// CacheMounts optional dependency cache volumes.
	CacheMounts []CacheMount
	// DockerBinary path to docker CLI (default "docker").
	DockerBinary string
}

// JobResult is the outcome of a job run.
type JobResult struct {
	CombinedOutput string
	ExitCode       int
}

// Runner executes JobSpec in an isolated environment.
type Runner interface {
	Run(ctx context.Context, spec JobSpec) (JobResult, error)
}
