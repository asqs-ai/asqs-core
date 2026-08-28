package main

import (
	"io"
	"testing"
)

// TestResolveMaxGaps covers the precedence chain for the unit-gap cap:
// explicit --max-gaps > indexer.max_gaps > built-in default.
func TestResolveMaxGaps(t *testing.T) {
	tests := []struct {
		name       string
		flagSet    bool
		flagVal    int
		cfgVal     int
		wantVal    int
		wantSource string
		wantErr    bool
	}{
		{
			name: "neither set falls back to the built-in default",
			// flagVal is the flag's own default, which is what FlagSet leaves behind when the
			// user does not pass --max-gaps.
			flagVal: defaultMaxGaps, cfgVal: 0,
			wantVal: defaultMaxGaps, wantSource: gapSourceDefault,
		},
		{
			name:    "config is honoured when the flag is absent (the regression this fixes)",
			flagVal: defaultMaxGaps, cfgVal: 20,
			wantVal: 20, wantSource: gapSourceConfig,
		},
		{
			name:    "config is honoured even when it equals the built-in default",
			flagVal: defaultMaxGaps, cfgVal: defaultMaxGaps,
			wantVal: defaultMaxGaps, wantSource: gapSourceConfig,
		},
		{
			name:    "explicit flag overrides config",
			flagSet: true, flagVal: 3, cfgVal: 20,
			wantVal: 3, wantSource: gapSourceFlag,
		},
		{
			name: "explicit flag that happens to equal the default still overrides config",
			// The whole point of tracking "was it set" instead of comparing against the default:
			// `--max-gaps 10` with `max_gaps: 20` must yield 10, not 20.
			flagSet: true, flagVal: defaultMaxGaps, cfgVal: 20,
			wantVal: defaultMaxGaps, wantSource: gapSourceFlag,
		},
		{
			name: "explicit zero is treated as unset and falls through to config",
			// Zero unit gaps is a no-op run and the planner clamps <= 0 to its own default,
			// so honouring it literally would be a confusing silent no-op.
			flagSet: true, flagVal: 0, cfgVal: 20,
			wantVal: 20, wantSource: gapSourceConfig,
		},
		{
			name:    "zero flag and zero config fall through to the built-in default",
			flagSet: true, flagVal: 0, cfgVal: 0,
			wantVal: defaultMaxGaps, wantSource: gapSourceDefault,
		},
		{
			name:    "negative flag is rejected",
			flagSet: true, flagVal: -1, cfgVal: 20,
			wantErr: true,
		},
		{
			name:    "negative config is rejected",
			flagVal: defaultMaxGaps, cfgVal: -5,
			wantErr: true,
		},
		{
			name: "negative config is rejected even when a valid flag is present",
			// Fail loudly on a broken config file rather than letting the flag mask it.
			flagSet: true, flagVal: 5, cfgVal: -5,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, src, err := resolveMaxGaps(tt.flagSet, tt.flagVal, tt.cfgVal)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveMaxGaps(%v, %d, %d) = (%d, %q, nil); want an error",
						tt.flagSet, tt.flagVal, tt.cfgVal, got, src)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMaxGaps(%v, %d, %d): unexpected error: %v",
					tt.flagSet, tt.flagVal, tt.cfgVal, err)
			}
			if got != tt.wantVal || src != tt.wantSource {
				t.Errorf("resolveMaxGaps(%v, %d, %d) = (%d, %q); want (%d, %q)",
					tt.flagSet, tt.flagVal, tt.cfgVal, got, src, tt.wantVal, tt.wantSource)
			}
		})
	}
}

// TestResolveMaxGapsE2E covers the E2E cap, where 0 is a meaningful value ("skip E2E") rather
// than a stand-in for "unset".
func TestResolveMaxGapsE2E(t *testing.T) {
	tests := []struct {
		name       string
		flagSet    bool
		flagVal    int
		cfgVal     int
		wantVal    int
		wantSource string
		wantErr    bool
	}{
		{
			name:    "neither set means E2E stays off",
			flagVal: defaultMaxGapsE2E, cfgVal: 0,
			wantVal: 0, wantSource: gapSourceDefault,
		},
		{
			name:    "config enables E2E when the flag is absent (the regression this fixes)",
			flagVal: defaultMaxGapsE2E, cfgVal: 5,
			wantVal: 5, wantSource: gapSourceConfig,
		},
		{
			name:    "explicit flag overrides config",
			flagSet: true, flagVal: 2, cfgVal: 5,
			wantVal: 2, wantSource: gapSourceFlag,
		},
		{
			name: "explicit zero disables E2E even when config enables it",
			// This is how a user turns E2E off for one run without editing the config file.
			flagSet: true, flagVal: 0, cfgVal: 5,
			wantVal: 0, wantSource: gapSourceFlag,
		},
		{
			name:    "negative flag is rejected",
			flagSet: true, flagVal: -1, cfgVal: 5,
			wantErr: true,
		},
		{
			name:    "negative config is rejected",
			flagVal: defaultMaxGapsE2E, cfgVal: -1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, src, err := resolveMaxGapsE2E(tt.flagSet, tt.flagVal, tt.cfgVal)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveMaxGapsE2E(%v, %d, %d) = (%d, %q, nil); want an error",
						tt.flagSet, tt.flagVal, tt.cfgVal, got, src)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMaxGapsE2E(%v, %d, %d): unexpected error: %v",
					tt.flagSet, tt.flagVal, tt.cfgVal, err)
			}
			if got != tt.wantVal || src != tt.wantSource {
				t.Errorf("resolveMaxGapsE2E(%v, %d, %d) = (%d, %q); want (%d, %q)",
					tt.flagSet, tt.flagVal, tt.cfgVal, got, src, tt.wantVal, tt.wantSource)
			}
		})
	}
}

// TestParseRunFlagsSetFlags is the load-bearing test for the resolution above: the whole
// precedence chain depends on setFlags being accurate, and setFlags is populated by
// FlagSet.Visit across the repeated Parse rounds that let flags and the repo argument appear in
// any order. If Visit ever stopped accumulating across rounds, a flag passed after the
// positional would silently lose to the config file.
func TestParseRunFlagsSetFlags(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantRepo       string
		wantMaxGaps    int
		wantMaxGapsE2E int
		wantSet        []string
		wantUnset      []string
	}{
		{
			name:        "flag before the positional repo",
			args:        []string{"--max-gaps", "7", "./project"},
			wantRepo:    "./project",
			wantMaxGaps: 7,
			wantSet:     []string{"max-gaps"},
			wantUnset:   []string{"max-gaps-e2e", "docs"},
		},
		{
			name: "flag after the positional repo is still recorded",
			// The reparse loop handles this; a single Parse would drop --max-gaps entirely.
			args:        []string{"./project", "--max-gaps", "7"},
			wantRepo:    "./project",
			wantMaxGaps: 7,
			wantSet:     []string{"max-gaps"},
			wantUnset:   []string{"max-gaps-e2e"},
		},
		{
			name:           "flags on both sides of the positional are all recorded",
			args:           []string{"--max-gaps", "7", "./project", "--max-gaps-e2e", "4", "--docs"},
			wantRepo:       "./project",
			wantMaxGaps:    7,
			wantMaxGapsE2E: 4,
			wantSet:        []string{"max-gaps", "max-gaps-e2e", "docs"},
		},
		{
			name:           "no gap flags leaves both unset at their defaults",
			args:           []string{"./project"},
			wantRepo:       "./project",
			wantMaxGaps:    defaultMaxGaps,
			wantMaxGapsE2E: defaultMaxGapsE2E,
			wantUnset:      []string{"max-gaps", "max-gaps-e2e"},
		},
		{
			name: "explicitly passing the default value still counts as set",
			// Guards the reason setFlags exists at all: value-equality with the default can not
			// distinguish "user typed --max-gaps 10" from "user typed nothing".
			args:        []string{"--max-gaps", "10", "./project"},
			wantRepo:    "./project",
			wantMaxGaps: defaultMaxGaps,
			wantSet:     []string{"max-gaps"},
		},
		{
			name:           "explicit zero for E2E is recorded as set",
			args:           []string{"--max-gaps-e2e", "0", "./project"},
			wantRepo:       "./project",
			wantMaxGaps:    defaultMaxGaps,
			wantMaxGapsE2E: 0,
			wantSet:        []string{"max-gaps-e2e"},
		},
		{
			name:        "--repo takes precedence over a trailing positional",
			args:        []string{"--repo", "./from-flag", "./positional", "--max-gaps", "2"},
			wantRepo:    "./from-flag",
			wantMaxGaps: 2,
			wantSet:     []string{"max-gaps", "repo"},
		},
		{
			name:           "equals form is recorded as set",
			args:           []string{"--max-gaps=3", "--max-gaps-e2e=1", "./project"},
			wantRepo:       "./project",
			wantMaxGaps:    3,
			wantMaxGapsE2E: 1,
			wantSet:        []string{"max-gaps", "max-gaps-e2e"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRunFlags(tt.args, io.Discard)
			if err != nil {
				t.Fatalf("parseRunFlags(%q): unexpected error: %v", tt.args, err)
			}
			if got.repo != tt.wantRepo {
				t.Errorf("repo = %q; want %q", got.repo, tt.wantRepo)
			}
			if got.maxGaps != tt.wantMaxGaps {
				t.Errorf("maxGaps = %d; want %d", got.maxGaps, tt.wantMaxGaps)
			}
			if got.maxGapsE2E != tt.wantMaxGapsE2E {
				t.Errorf("maxGapsE2E = %d; want %d", got.maxGapsE2E, tt.wantMaxGapsE2E)
			}
			for _, name := range tt.wantSet {
				if !got.setFlags[name] {
					t.Errorf("setFlags[%q] = false; want true (setFlags=%v)", name, got.setFlags)
				}
			}
			for _, name := range tt.wantUnset {
				if got.setFlags[name] {
					t.Errorf("setFlags[%q] = true; want false (setFlags=%v)", name, got.setFlags)
				}
			}
		})
	}
}

// TestParseRunFlagsInvalidValue confirms a malformed gap value fails the parse instead of
// silently falling back to the config file.
func TestParseRunFlagsInvalidValue(t *testing.T) {
	if _, err := parseRunFlags([]string{"--max-gaps", "not-a-number", "./project"}, io.Discard); err == nil {
		t.Fatal("parseRunFlags with a non-numeric --max-gaps: got nil error; want a parse error")
	}
}

// TestParseRunFlagsEndToEndPrecedence wires parseRunFlags into the resolvers exactly as run()
// does, so the two halves are covered together rather than only in isolation.
func TestParseRunFlagsEndToEndPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		cfgMaxGaps     int
		cfgMaxGapsE2E  int
		wantMaxGaps    int
		wantMaxGapsSrc string
		wantE2E        int
		wantE2ESrc     string
	}{
		{
			name:       "config drives both when no flags are passed",
			args:       []string{"./project"},
			cfgMaxGaps: 20, cfgMaxGapsE2E: 5,
			wantMaxGaps: 20, wantMaxGapsSrc: gapSourceConfig,
			wantE2E: 5, wantE2ESrc: gapSourceConfig,
		},
		{
			name:       "flags after the positional still beat config",
			args:       []string{"./project", "--max-gaps", "3", "--max-gaps-e2e", "1"},
			cfgMaxGaps: 20, cfgMaxGapsE2E: 5,
			wantMaxGaps: 3, wantMaxGapsSrc: gapSourceFlag,
			wantE2E: 1, wantE2ESrc: gapSourceFlag,
		},
		{
			name:       "one flag overrides while the other still comes from config",
			args:       []string{"--max-gaps", "3", "./project"},
			cfgMaxGaps: 20, cfgMaxGapsE2E: 5,
			wantMaxGaps: 3, wantMaxGapsSrc: gapSourceFlag,
			wantE2E: 5, wantE2ESrc: gapSourceConfig,
		},
		{
			name:        "empty config leaves the built-in defaults",
			args:        []string{"./project"},
			wantMaxGaps: defaultMaxGaps, wantMaxGapsSrc: gapSourceDefault,
			wantE2E: defaultMaxGapsE2E, wantE2ESrc: gapSourceDefault,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, err := parseRunFlags(tt.args, io.Discard)
			if err != nil {
				t.Fatalf("parseRunFlags(%q): %v", tt.args, err)
			}
			gaps, gapsSrc, err := resolveMaxGaps(flags.setFlags["max-gaps"], flags.maxGaps, tt.cfgMaxGaps)
			if err != nil {
				t.Fatalf("resolveMaxGaps: %v", err)
			}
			e2e, e2eSrc, err := resolveMaxGapsE2E(flags.setFlags["max-gaps-e2e"], flags.maxGapsE2E, tt.cfgMaxGapsE2E)
			if err != nil {
				t.Fatalf("resolveMaxGapsE2E: %v", err)
			}
			if gaps != tt.wantMaxGaps || gapsSrc != tt.wantMaxGapsSrc {
				t.Errorf("max-gaps = (%d, %q); want (%d, %q)", gaps, gapsSrc, tt.wantMaxGaps, tt.wantMaxGapsSrc)
			}
			if e2e != tt.wantE2E || e2eSrc != tt.wantE2ESrc {
				t.Errorf("max-gaps-e2e = (%d, %q); want (%d, %q)", e2e, e2eSrc, tt.wantE2E, tt.wantE2ESrc)
			}
		})
	}
}

// TestResolveAuditLogPath covers the precedence chain for the audit JSONL path:
// explicit --audit-log (even empty) > audit.file_path > none.
func TestResolveAuditLogPath(t *testing.T) {
	tests := []struct {
		name       string
		flagSet    bool
		flagVal    string
		cfgVal     string
		wantVal    string
		wantSource string
	}{
		{
			name:    "neither set means no audit file",
			wantVal: "", wantSource: gapSourceDefault,
		},
		{
			name:    "config path is honoured when the flag is absent",
			cfgVal:  ".asqs/audit.jsonl",
			wantVal: ".asqs/audit.jsonl", wantSource: gapSourceConfig,
		},
		{
			name:    "explicit flag overrides config",
			flagSet: true, flagVal: "/tmp/run.jsonl", cfgVal: ".asqs/audit.jsonl",
			wantVal: "/tmp/run.jsonl", wantSource: gapSourceFlag,
		},
		{
			name: "explicit empty flag disables a config-set path for one run",
			// The same shape as --max-gaps-e2e 0: an explicit "off" must beat the config file.
			flagSet: true, flagVal: "", cfgVal: ".asqs/audit.jsonl",
			wantVal: "", wantSource: gapSourceFlag,
		},
		{
			name:    "surrounding whitespace is trimmed",
			flagSet: true, flagVal: "  out.jsonl  ",
			wantVal: "out.jsonl", wantSource: gapSourceFlag,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, src := resolveAuditLogPath(tc.flagSet, tc.flagVal, tc.cfgVal)
			if got != tc.wantVal || src != tc.wantSource {
				t.Errorf("resolveAuditLogPath(%v, %q, %q) = (%q, %s), want (%q, %s)",
					tc.flagSet, tc.flagVal, tc.cfgVal, got, src, tc.wantVal, tc.wantSource)
			}
		})
	}
}

// The --audit-log flag parses in any position and records itself in setFlags, so an explicit
// empty value is distinguishable from the flag being absent.
func TestParseRunFlagsAuditLog(t *testing.T) {
	f, err := parseRunFlags([]string{"./project", "--audit-log", "run.jsonl"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if f.auditLog != "run.jsonl" || !f.setFlags["audit-log"] {
		t.Errorf("auditLog = %q setFlags[audit-log] = %v, want run.jsonl / true", f.auditLog, f.setFlags["audit-log"])
	}

	f, err = parseRunFlags([]string{"./project"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if f.auditLog != "" || f.setFlags["audit-log"] {
		t.Errorf("without the flag: auditLog = %q setFlags[audit-log] = %v, want empty / false", f.auditLog, f.setFlags["audit-log"])
	}

	f, err = parseRunFlags([]string{"--audit-log", "", "./project"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if f.auditLog != "" || !f.setFlags["audit-log"] {
		t.Errorf("explicit empty: auditLog = %q setFlags[audit-log] = %v, want empty / true", f.auditLog, f.setFlags["audit-log"])
	}
}
