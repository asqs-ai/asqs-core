package pipeline

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/tools"
)

type capsCompleter struct{ caps *model.Capabilities }

func (c *capsCompleter) Complete(context.Context, []model.Message, model.CompleteOptions) (*model.CompleteResult, error) {
	return &model.CompleteResult{}, nil
}
func (c *capsCompleter) Capabilities() model.Capabilities { return *c.caps }

type plainCompleter struct{}

func (plainCompleter) Complete(context.Context, []model.Message, model.CompleteOptions) (*model.CompleteResult, error) {
	return &model.CompleteResult{}, nil
}

// Defaults must keep the pipeline on the one-shot path: enabling tools is an explicit act, and a
// run with tools off has to stay byte-identical to pre-wave behaviour.
func TestToolLoopFromConfig_defaultsToOneShot(t *testing.T) {
	opts, reason := toolLoopFromConfig(&config.Config{}, &capsCompleter{caps: &model.Capabilities{ToolCalling: true}})
	if opts.Mode != tools.ModeOneShot {
		t.Errorf("mode = %q, want one_shot when generation.policy.tools.enabled is unset", opts.Mode)
	}
	if reason == "" {
		t.Error("a non-native mode must carry a reason for the audit log")
	}
}

func TestToolLoopFromConfig_nativeWhenCapableAndEnabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Generation.ToolsEnabled = true
	opts, reason := toolLoopFromConfig(cfg, &capsCompleter{caps: &model.Capabilities{ToolCalling: true}})
	if opts.Mode != tools.ModeNative {
		t.Errorf("mode = %q, want native", opts.Mode)
	}
	if reason != "" {
		t.Errorf("native is not a downgrade; reason should be empty, got %q", reason)
	}
}

// A provider that never implemented CapabilityReporter is undeclared, not incapable — it gets
// prompted mode when that is allowed. This is B08's distinction reaching the loop.
func TestToolLoopFromConfig_undeclaredProviderGetsPrompted(t *testing.T) {
	cfg := &config.Config{}
	cfg.Generation.ToolsEnabled = true
	cfg.Generation.PromptedToolsEnabled = true
	opts, reason := toolLoopFromConfig(cfg, plainCompleter{})
	if opts.Mode != tools.ModePrompted {
		t.Errorf("mode = %q, want prompted for an undeclared provider", opts.Mode)
	}
	if reason == "" {
		t.Error("the downgrade must be auditable")
	}
}

// Configured bounds must reach the loop; unset ones fall back to the loop's own defaults rather
// than to zero, which would mean "no turns at all".
func TestToolLoopFromConfig_carriesBounds(t *testing.T) {
	cfg := &config.Config{}
	cfg.Generation.ToolsEnabled = true
	cfg.Generation.MaxToolTurns = 7
	cfg.Generation.MaxToolCallsPerRun = 9
	opts, _ := toolLoopFromConfig(cfg, &capsCompleter{caps: &model.Capabilities{ToolCalling: true}})
	if opts.MaxTurns != 7 || opts.MaxCallsPerRun != 9 {
		t.Errorf("bounds not carried: %+v", opts)
	}
	// Unset bounds stay zero here and are defaulted inside the loop, not silently treated as "none".
	if opts.MaxCallsPerTurn != 0 {
		t.Errorf("unset bound should stay zero for the loop to default: %d", opts.MaxCallsPerTurn)
	}
}

func TestToolLoopFromConfig_nilConfigIsOneShot(t *testing.T) {
	opts, reason := toolLoopFromConfig(nil, plainCompleter{})
	if opts.Mode != tools.ModeOneShot || reason == "" {
		t.Errorf("nil config must be one-shot with a reason: %q / %q", opts.Mode, reason)
	}
}

// The fixer's gate is independent of generation's: a fix-quality A/B has to move one without the
// other, so tools_enabled must not turn the fixer's loop on.
func TestFixerToolLoopFromConfig_isIndependentOfGeneration(t *testing.T) {
	cc := &capsCompleter{caps: &model.Capabilities{ToolCalling: true}}

	genOnly := &config.Config{}
	genOnly.Generation.ToolsEnabled = true
	if opts, _ := fixerToolLoopFromConfig(genOnly, cc); opts.Mode != tools.ModeOneShot {
		t.Fatalf("fixer mode = %q with only generation enabled; the two gates must be independent", opts.Mode)
	}

	fixOnly := &config.Config{}
	fixOnly.Generation.FixerToolsEnabled = true
	opts, reason := fixerToolLoopFromConfig(fixOnly, cc)
	if opts.Mode != tools.ModeNative {
		t.Fatalf("mode = %q (%s), want native", opts.Mode, reason)
	}
	// The fixer's turn default is deliberately tighter than generation's: each extra turn
	// multiplies across fix attempts, not just gaps.
	if opts.MaxTurns != DefaultFixerMaxToolTurns {
		t.Fatalf("MaxTurns = %d, want the fixer default %d", opts.MaxTurns, DefaultFixerMaxToolTurns)
	}
	if DefaultFixerMaxToolTurns >= tools.DefaultMaxToolTurns {
		t.Fatalf("the fixer default (%d) must be tighter than generation's (%d)", DefaultFixerMaxToolTurns, tools.DefaultMaxToolTurns)
	}

	explicit := &config.Config{}
	explicit.Generation.FixerToolsEnabled = true
	explicit.Generation.FixerMaxToolTurns = 7
	if opts, _ := fixerToolLoopFromConfig(explicit, cc); opts.MaxTurns != 7 {
		t.Fatalf("MaxTurns = %d, want the configured 7", opts.MaxTurns)
	}
}

// Ships off by default, like generation's.
func TestFixerToolLoopFromConfig_defaultsToOneShot(t *testing.T) {
	opts, reason := fixerToolLoopFromConfig(&config.Config{}, &capsCompleter{caps: &model.Capabilities{ToolCalling: true}})
	if opts.Mode != tools.ModeOneShot {
		t.Fatalf("mode = %q, want one_shot by default", opts.Mode)
	}
	if reason == "" {
		t.Error("a downgrade must carry a reason for the audit")
	}
	if buildFixerTools(&config.Config{}, nil, nil, nil, "r", "java", "/tmp") != nil {
		t.Error("a default config must build no fixer registry")
	}
}
