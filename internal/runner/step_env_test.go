package runner

import (
	"testing"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

// CI=true makes vitest/vite colour their output (their colour libraries read CI as an enable
// flag), and coloured output broke every log parser downstream in run
// api-72dad6bb281cacee338f43c48432a780. NO_COLOR must accompany CI on every target, and
// FORCE_COLOR must never appear: those libraries treat its presence, any value, as "enable".
func TestBaseStepEnv_disablesColour(t *testing.T) {
	for _, target := range []Target{TargetLocal, TargetDocker} {
		env := stepEnv(profile.JavaMaven, target, nil)
		has := map[string]bool{}
		for _, kv := range env {
			has[kv] = true
		}
		if !has["CI=true"] {
			t.Errorf("%s: CI=true missing from %v", target, env)
		}
		if !has["NO_COLOR=1"] {
			t.Errorf("%s: NO_COLOR=1 missing from %v", target, env)
		}
		for _, kv := range env {
			if len(kv) >= 11 && kv[:11] == "FORCE_COLOR" {
				t.Errorf("%s: FORCE_COLOR must not be set (its presence enables colour): %v", target, env)
			}
		}
	}
}
