package evaluator

import "context"

// Minimal test doubles for the evaluator package. asqs-core does not carry the full asqs-go
// workflow_test.go harness, so the fix-loop tests (RC1–RC3) bring their own. Kept in sync with the
// asqs-go definitions of the same names.

type stubFixer struct {
	req  FixRequest
	resp FixResponse
	err  error
}

func (s *stubFixer) Fix(ctx context.Context, req FixRequest) (FixResponse, error) {
	s.req = req
	if s.err != nil {
		return FixResponse{}, s.err
	}
	return s.resp, nil
}

var _ Fixer = (*stubFixer)(nil)

// recordingAuditor records every Log/LogError step name and payload for assertions.
type recordingAuditor struct {
	steps      []string
	errorSteps []string
	payloads   map[string][]map[string]interface{}
}

func (r *recordingAuditor) Log(ctx context.Context, step string, payload interface{}) {
	r.steps = append(r.steps, step)
	if m, ok := payload.(map[string]interface{}); ok {
		if r.payloads == nil {
			r.payloads = make(map[string][]map[string]interface{})
		}
		r.payloads[step] = append(r.payloads[step], m)
	}
}

func (r *recordingAuditor) LogError(ctx context.Context, step string, payload interface{}) {
	r.errorSteps = append(r.errorSteps, step)
	if m, ok := payload.(map[string]interface{}); ok {
		if r.payloads == nil {
			r.payloads = make(map[string][]map[string]interface{})
		}
		r.payloads[step] = append(r.payloads[step], m)
	}
}

func (r *recordingAuditor) hasStep(step string) bool {
	for _, s := range r.steps {
		if s == step {
			return true
		}
	}
	for _, s := range r.errorSteps {
		if s == step {
			return true
		}
	}
	return false
}

func (r *recordingAuditor) lastPayload(step string) map[string]interface{} {
	if r.payloads == nil {
		return nil
	}
	ps := r.payloads[step]
	if len(ps) == 0 {
		return nil
	}
	return ps[len(ps)-1]
}

var _ Auditor = (*recordingAuditor)(nil)

type stubSandboxRunner struct {
	compile  StepResult
	test     StepResult
	lint     StepResult
	coverage StepResult
	mutation StepResult
}

func (s *stubSandboxRunner) Compile(ctx context.Context, repoPath, lang string) StepResult {
	return s.compile
}

func (s *stubSandboxRunner) Test(ctx context.Context, repoPath, lang string) StepResult {
	return s.test
}

func (s *stubSandboxRunner) TestWithCommand(ctx context.Context, repoPath, lang, testCommand string) StepResult {
	_ = testCommand
	return s.test
}

func (s *stubSandboxRunner) TestE2EPass(ctx context.Context, repoPath, lang, testCommand, e2eFramework string) StepResult {
	_, _ = testCommand, e2eFramework
	return s.test
}

func (s *stubSandboxRunner) CoverageWithCommand(ctx context.Context, repoPath, lang, testCommand string) StepResult {
	_ = testCommand
	return s.coverage
}

func (s *stubSandboxRunner) Lint(ctx context.Context, repoPath, lang string) StepResult {
	return s.lint
}

func (s *stubSandboxRunner) Coverage(ctx context.Context, repoPath, lang string) StepResult {
	return s.coverage
}

func (s *stubSandboxRunner) Mutation(ctx context.Context, repoPath, lang string, criticalModules []string) StepResult {
	return s.mutation
}

var _ SandboxRunner = (*stubSandboxRunner)(nil)
