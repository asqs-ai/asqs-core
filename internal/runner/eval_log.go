package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// The evaluation environment block: one function for both targets (U8).
//
// There used to be two, written months apart, answering different questions. Both printed the same
// "[asqs-eval]" banner, so a reader could reasonably assume the blocks were comparable — but only
// one named the effective argv, only one named the environment, and only the Docker one named its
// resource limits. Reconstructing what a run did meant first knowing which of the two you were
// reading.
//
// The shared core now comes from the StepPlan, the same object the executor runs, so the block
// cannot describe something other than what happened. Only the closing section differs, over
// exactly the things §1 permits: an image, mounts and networks on one side; the host's PATH
// resolution on the other.

// logEvalEnvOnce prints the evaluation environment once per run.
func (s *Sandbox) logEvalEnvOnce(plan StepPlan, gitRootAbs string) {
	once := &s.runState().localEvalEnvOnce
	if plan.Target == TargetDocker {
		once = &s.runState().dockerEvalEnvOnce
	}
	once.Do(func() {
		abs := gitRootAbs
		if a, err := filepath.Abs(strings.TrimSpace(gitRootAbs)); err == nil {
			abs = a
		}
		cwd := s.evalHostCwd(abs)

		fmt.Fprintf(os.Stderr, "[asqs-eval] evaluation runner: type=%s\n", plan.Target)
		fmt.Fprintf(os.Stderr, "  lang=%s toolchain=%s build_tool=%q step_timeout=%v\n",
			plan.Lang, plan.Toolchain, strings.TrimSpace(s.BuildTool), s.timeoutDuration())
		fmt.Fprintf(os.Stderr, "  workdir: %s\n", cwd)
		if sub := strings.TrimSpace(s.EvalWorkSubpath); sub != "" {
			fmt.Fprintf(os.Stderr, "  mono_repo_workspace: %s (toolchain cwd; DB paths stay git-root-relative)\n", sub)
		}
		fmt.Fprintf(os.Stderr, "  command_overrides: compile=%s test=%s\n",
			overrideDesc(s.CompileCommand), overrideDesc(s.TestCommand))
		fmt.Fprintf(os.Stderr, "  effective_argv: restore=[%s] compile=[%s] test=[%s] coverage=[%s]\n",
			strings.Join(plan.Restore, " "),
			planArgvDesc(plan, evaluator.StepCompile),
			planArgvDesc(plan, evaluator.StepTest),
			planArgvDesc(plan, evaluator.StepCoverage))
		fmt.Fprintf(os.Stderr, "  environment: %s\n", planEnvDesc(plan))

		if plan.Target == TargetDocker {
			s.logDockerEvalEnvTail(plan, abs)
			return
		}
		s.logLocalEvalEnvTail(cwd)
	})
}

// planArgvDesc renders a step's argv, or the reason it will not run. A skipped step has no argv,
// and an empty bracket would read as "nothing configured" rather than "deliberately not run".
func planArgvDesc(plan StepPlan, step evaluator.SandboxStep) string {
	if d := plan.DecisionFor(step); d.Action != ActionRun {
		return d.Reason
	}
	return strings.Join(plan.ArgvFor(step), " ")
}

// planEnvDesc lists the variables the steps set explicitly. They are uniform across steps in every
// current plan; the per-step map stays the source of truth in case that stops being true.
func planEnvDesc(plan StepPlan) string {
	for _, step := range planSteps {
		if env := plan.EnvFor(step); len(env) > 0 {
			return strings.Join(env, " ")
		}
	}
	return "(none)"
}

func (s *Sandbox) logLocalEvalEnvTail(cwd string) {
	fmt.Fprintf(os.Stderr, "  host toolchain: %s\n", localToolchainDesc(cwd, s))
	fmt.Fprintf(os.Stderr, "  environment_inheritance: the variables above are appended to this process's environment (PATH, JAVA_HOME, MAVEN_OPTS, ~/.m2/settings.xml)\n")
	fmt.Fprintf(os.Stderr, "  note: steps run as host processes — no image, no network isolation, no per-step resource limits\n")
}

func (s *Sandbox) logDockerEvalEnvTail(plan StepPlan, repoAbs string) {
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
	fmt.Fprintf(os.Stderr, "  image=%s docker_binary=%s job_timeout=%v memory=%q cpus=%.1f pids_limit=%d readonly_rootfs=%v\n",
		plan.Image, s.dockerBin(), s.jobTimeout(), strings.TrimSpace(s.JobMemory), s.JobCPUs, s.JobPidsLimit, s.JobReadonlyRootfs)
	fmt.Fprintf(os.Stderr, "  networks: restore=%s compile_test_coverage=%s\n", netRestore, netTest)
	fmt.Fprintf(os.Stderr, "  workspace_mount: host=%s -> container=/workspace:rw\n", repoAbs)
	fmt.Fprintf(os.Stderr, "  dependency_caches: %s\n", s.dependencyCacheDesc())
	fmt.Fprintf(os.Stderr, "  note: each step runs a fresh container (docker run --rm); images stay on disk — see `docker images` for %s\n", plan.Image)
}

func (s *Sandbox) dependencyCacheDesc() string {
	var caches []string
	for _, c := range []struct{ host, target string }{
		{s.CacheMavenHost, "/root/.m2"},
		{s.CacheGradleHost, "/root/.gradle"},
		{s.CacheNpmHost, "/root/.npm"},
		{s.CachePnpmHost, ".../pnpm/store"},
		{s.CacheNuGetHost, "/root/.nuget/packages"},
		{s.CacheCypressHost, "/root/.cache/Cypress"},
	} {
		if h := strings.TrimSpace(c.host); h != "" {
			caches = append(caches, fmt.Sprintf("%s->%s", h, c.target))
		}
	}
	if len(caches) == 0 {
		return "(none mounted)"
	}
	return strings.Join(caches, "; ")
}

// logLocalEvalStep echoes the exact argv and cwd for one step. Without it a run recorded which STEP
// ran but never what it executed, so a build tool resolving differently than the operator expected
// left no trace of the difference.
func logLocalEvalStep(step evaluator.SandboxStep, cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[asqs-eval] step=%s phase=main argv=[%s] cwd=%s\n",
		step, strings.Join(cmd.Args, " "), cmd.Dir)
}

func overrideDesc(cmd string) string {
	if c := strings.TrimSpace(cmd); c != "" {
		return "sh -c " + strconv.Quote(c)
	}
	return "(none; using build_tool default)"
}

// localToolchainDesc reports where the build tool actually came from. On a host this is the single
// most useful line when a run behaves differently from the operator's shell.
//
// Since D3 the binary is always a PATH name, never a repo wrapper, so the only fact left worth
// printing is which one PATH resolved to — exactly what differs between the operator's shell and
// the service account's.
func localToolchainDesc(dir string, s *Sandbox) string {
	cmd, err := localBuildCommand(dir, "compile", s.BuildTool, s.CompileCommand, s.TestCommand)
	if err != nil || cmd == nil || len(cmd.Args) == 0 {
		return "(resolved at step time)"
	}
	bin := cmd.Args[0]
	if resolved, lerr := exec.LookPath(bin); lerr == nil {
		return bin + " -> " + resolved
	}
	return bin
}
