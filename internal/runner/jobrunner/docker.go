package jobrunner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// forceRemoveContainerByCIDFile runs `docker rm -f <id>` when cidPath contains a non-empty container ID.
// When a job hits its wall-clock timeout, Go's CommandContext typically SIGKILLs the docker CLI process; that
// does not always stop the container, which may keep test/build processes alive and hold locks on the
// bind-mounted workspace (e.g. MSB3026 copying PDBs on the next compile). --cidfile plus this cleanup
// ensures the container is torn down after every run.
func forceRemoveContainerByCIDFile(dockerBin, cidPath string) {
	if dockerBin == "" {
		dockerBin = "docker"
	}
	data, err := os.ReadFile(cidPath)
	if err != nil {
		return
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return
	}
	_ = exec.Command(dockerBin, "rm", "-f", id).Run()
}

// DockerRunner runs each job in a fresh ephemeral container (`docker run --rm`).
type DockerRunner struct {
	Docker string // default "docker"
}

// dockerVolumeBindAndWorkdir returns the docker "-v" bind value and "-w" working directory.
// The git root is always mounted at /workspace; workdir may be a subdirectory for mono-repo scoping.
func dockerVolumeBindAndWorkdir(spec JobSpec) (volumeBind string, workdir string) {
	const repoMount = "/workspace"
	workdir = strings.TrimSpace(spec.Workdir)
	if workdir == "" {
		workdir = repoMount
	}
	// Do not mount at workdir: when workdir is /workspace/prefix, that would map the repo root onto
	// that path so bare project names (e.g. App.csproj) resolve under the wrong directory.
	return spec.HostWorkDir + ":" + repoMount + ":rw", workdir
}

// Run implements Runner.
func (d *DockerRunner) Run(ctx context.Context, spec JobSpec) (JobResult, error) {
	bin := d.Docker
	if bin == "" {
		bin = "docker"
	}
	if spec.Image == "" {
		return JobResult{}, fmt.Errorf("jobrunner: JobSpec.Image required")
	}
	if spec.HostWorkDir == "" {
		return JobResult{}, fmt.Errorf("jobrunner: JobSpec.HostWorkDir required")
	}
	if len(spec.Command) == 0 {
		return JobResult{}, fmt.Errorf("jobrunner: JobSpec.Command required")
	}
	volumeBind, workdir := dockerVolumeBindAndWorkdir(spec)
	// Reserve a unique path; the file must not exist when docker run starts — Docker creates --cidfile itself
	// and exits 125 if the path already exists (including an empty file from CreateTemp).
	cidFile, err := os.CreateTemp("", "asqs-docker-job-*.cid")
	if err != nil {
		return JobResult{}, fmt.Errorf("jobrunner: create cidfile: %w", err)
	}
	cidPath := cidFile.Name()
	_ = cidFile.Close()
	if err := os.Remove(cidPath); err != nil {
		return JobResult{}, fmt.Errorf("jobrunner: remove cidfile placeholder: %w", err)
	}
	defer func() {
		forceRemoveContainerByCIDFile(bin, cidPath)
		_ = os.Remove(cidPath)
	}()

	args := []string{"run", "--rm", "--init",
		"--cidfile", cidPath,
		"-v", volumeBind,
		"-w", workdir,
	}
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64))
	}
	if spec.PidsLimit > 0 {
		args = append(args, "--pids-limit", strconv.FormatInt(spec.PidsLimit, 10))
	}
	net := strings.TrimSpace(spec.NetworkMode)
	if net == "" {
		net = "bridge"
	}
	args = append(args, "--network", net)
	for _, m := range spec.CacheMounts {
		if m.Source == "" || m.Target == "" {
			continue
		}
		vol := m.Source + ":" + m.Target
		if m.ReadOnly {
			vol += ":ro"
		}
		args = append(args, "-v", vol)
	}
	if spec.ReadonlyRootfs {
		args = append(args, "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=1g")
	}
	if spec.IpcHost {
		args = append(args, "--ipc=host")
	}
	for _, e := range spec.Env {
		if strings.TrimSpace(e) != "" {
			args = append(args, "-e", e)
		}
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)

	fmt.Fprintf(os.Stderr, "[asqs-eval] docker run (--rm, container removed after step):\n  %s\n", FormatDockerInvocation(bin, args))

	runCtx := ctx
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, bin, args...)
	out, err := cmd.CombinedOutput()
	res := JobResult{CombinedOutput: string(out)}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded || runCtx.Err() == context.Canceled {
			return res, fmt.Errorf("jobrunner: docker job: %w", runCtx.Err())
		}
		if ee, ok := err.(*exec.ExitError); ok && ee.ProcessState != nil {
			res.ExitCode = ee.ProcessState.ExitCode()
			return res, nil // caller treats non-zero exit as failure via ExitCode
		}
		return res, fmt.Errorf("jobrunner: docker: %w", err)
	}
	return res, nil
}

// DefaultJobTimeout is used when spec.Timeout is zero.
const DefaultJobTimeout = 15 * time.Minute
