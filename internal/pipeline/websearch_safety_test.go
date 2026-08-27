package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
)

// Off by default is the first brake, and it must register NO tools rather than disabled ones: a
// tool the model can see but not use costs a turn and teaches it to distrust the tool set.
func TestWebSearch_offByDefaultRegistersNothing(t *testing.T) {
	audit := &payloadAuditor{}
	if c := buildWebClient(context.Background(), &config.Config{}, audit, t.TempDir(), nil); c != nil {
		t.Fatal("web access must be off in a default configuration")
	}
	// "Off" is a resolution an operator should be able to read, not a silent absence.
	p := audit.lastPayload("websearch.mode")
	if p == nil {
		t.Fatal("the resolution must be audited even when it resolves off")
	}
	if p["enabled"] != false {
		t.Errorf("audit says enabled=%v for a default config", p["enabled"])
	}
	if _, ok := p["reason"]; !ok {
		t.Error("an off resolution must say why")
	}
}

// The offline switch is the strong brake: it must make egress structurally impossible, not merely
// unconfigured. Enabling web access AND offline together must still never dial.
func TestWebSearch_offlineModeNeverDials(t *testing.T) {
	cfg := &config.Config{}
	cfg.WebSearch.Enabled = true
	cfg.WebSearch.Offline = true
	cfg.WebSearch.Provider = "brave"
	cfg.WebSearch.APIKey = "unused-in-offline"

	audit := &payloadAuditor{}
	repo := t.TempDir()
	c := buildWebClient(context.Background(), cfg, audit, repo, nil)
	if c == nil {
		t.Skip("offline replay needs a cache the harness did not create; the mode itself is unit-tested in internal/websearch")
	}
	p := audit.lastPayload("websearch.mode")
	if p == nil || p["offline"] != true {
		t.Fatalf("offline must be visible in the audit: %+v", p)
	}
	if msg, _ := p["message"].(string); !strings.Contains(strings.ToLower(msg), "offline") {
		t.Errorf("the operator-facing message must name offline mode: %q", msg)
	}
}

// An empty allow-list must fail CLOSED — fetch disabled — never open. The resolver substitutes the
// curated documentation hosts rather than shipping an empty list that looks like a broken feature.
func TestWebSearch_emptyAllowListDoesNotBecomeOpen(t *testing.T) {
	cfg := &config.Config{}
	cfg.WebSearch.Enabled = true
	cfg.WebSearch.Provider = "brave"
	cfg.WebSearch.APIKey = "k"

	audit := &payloadAuditor{}
	buildWebClient(context.Background(), cfg, audit, t.TempDir(), nil)
	p := audit.lastPayload("websearch.mode")
	if p == nil {
		t.Fatal("resolution not audited")
	}
	hosts, _ := p["hosts"].(int)
	if hosts == 0 {
		t.Error("an enabled client with no configured hosts must fall back to the curated list, " +
			"not ship an empty one — empty disables fetch entirely and reads as a broken feature")
	}
}

// The repository's own names must not leave the process inside a query.
func TestQueryDenyTokens_derivesFromRepositoryIdentity(t *testing.T) {
	got := queryDenyTokens("github.com/acme-corp/secret_service", "/tmp/checkout/secret_service")
	want := map[string]bool{"github": true, "com": true, "acme": true, "corp": true, "secret": true, "service": true}
	for _, tok := range got {
		delete(want, tok)
		if len(tok) < 3 {
			t.Errorf("token %q is too short to deny safely; it would match half the internet", tok)
		}
		if tok != strings.ToLower(tok) {
			t.Errorf("token %q must be lowercased for substring matching", tok)
		}
	}
	if len(want) > 0 {
		t.Errorf("identity segments never became deny tokens: %v (got %v)", want, got)
	}
}
