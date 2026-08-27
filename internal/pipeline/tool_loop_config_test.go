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
