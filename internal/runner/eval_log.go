package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

// doOnce runs f at most once when o is non-nil; when o is nil (a Sandbox built without the
// NewSandboxFromConfig constructor) it simply runs f. This lets the env-logging guards survive the
// clone-and-override pattern in TestWithCommand / CompileWithCommand / TestE2EPass (s2 := *s)
// without copying a sync.Once value.
func doOnce(o *sync.Once, f func()) {
	if o != nil {
		o.Do(f)
		return
	}
	f()
}

func (s *Sandbox) logLocalEvalEnvOnce(repoPath string) {
	doOnce(s.localEvalEnvOnce, func() {
		abs := repoPath
		if a, err := filepath.Abs(strings.TrimSpace(repoPath)); err == nil {
			abs = a
		}
		fmt.Fprintf(os.Stderr, "[asqs-eval] evaluation runner: type=local repo=%s timeout=%v build_tool=%q compile_override=%t test_override=%t\n",
			abs, s.timeoutDuration(), strings.TrimSpace(s.BuildTool),
			strings.TrimSpace(s.CompileCommand) != "", strings.TrimSpace(s.TestCommand) != "")
	})
}

func (s *Sandbox) logDockerEvalEnvOnce(p profile.ToolchainProfile, repoAbs string) {
	doOnce(s.dockerEvalEnvOnce, func() {
		netRestore := strings.TrimSpace(s.JobNetworkRestore)
		if netRestore == "" {
			netRestore = "bridge"
		}
		netTest := strings.TrimSpace(s.JobNetworkTest)
		if netTest == "" {
			netTest = "none"
		}
		if s.DockerDisableOfflineTest {
			netTest = netRestore + " (all steps; offline disabled)"
		}
		fmt.Fprintf(os.Stderr, "[asqs-eval] evaluation runner: type=docker\n")
		fmt.Fprintf(os.Stderr, "  toolchain=%s image=%s\n", p.ID, p.Image)
		fmt.Fprintf(os.Stderr, "  docker_binary=%s job_timeout=%v memory=%q cpus=%.1f pids_limit=%d readonly_rootfs=%v\n",
			s.dockerBin(), s.jobTimeout(), strings.TrimSpace(s.JobMemory), s.JobCPUs, s.JobPidsLimit, s.JobReadonlyRootfs)
		fmt.Fprintf(os.Stderr, "  networks: restore=%s compile_test_coverage=%s\n", netRestore, netTest)
		fmt.Fprintf(os.Stderr, "  workspace_mount: host=%s -> container=/workspace:rw\n", repoAbs)
		var caches []string
		if h := strings.TrimSpace(s.CacheMavenHost); h != "" {
			caches = append(caches, fmt.Sprintf("maven %s->/root/.m2", h))
		}
		if h := strings.TrimSpace(s.CacheGradleHost); h != "" {
			caches = append(caches, fmt.Sprintf("gradle %s->/root/.gradle", h))
		}
		if h := strings.TrimSpace(s.CacheNpmHost); h != "" {
			caches = append(caches, fmt.Sprintf("npm %s->/root/.npm", h))
		}
		if h := strings.TrimSpace(s.CachePnpmHost); h != "" {
			caches = append(caches, fmt.Sprintf("pnpm %s->.../pnpm/store", h))
		}
		if h := strings.TrimSpace(s.CacheNuGetHost); h != "" {
			caches = append(caches, fmt.Sprintf("nuget %s->/root/.nuget/packages", h))
		}
		if h := strings.TrimSpace(s.CacheCypressHost); h != "" {
			caches = append(caches, fmt.Sprintf("cypress %s->/root/.cache/Cypress", h))
		}
		if len(caches) == 0 {
			fmt.Fprintf(os.Stderr, "  dependency_caches: (none mounted)\n")
		} else {
			fmt.Fprintf(os.Stderr, "  dependency_caches: %s\n", strings.Join(caches, "; "))
		}
		fmt.Fprintf(os.Stderr, "  note: each step runs a fresh container (docker run --rm); images stay on disk — see `docker images` for %s\n", p.Image)
		fmt.Fprintf(os.Stderr, "  effective_argv: compile=[%s] test=[%s] coverage=[%s]\n",
			strings.Join(p.Compile, " "), strings.Join(p.Test, " "), strings.Join(p.Coverage, " "))
	})
}
