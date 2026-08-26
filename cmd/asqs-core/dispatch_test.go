package main

import (
	"sort"
	"strings"
	"testing"
)

// The dispatch extraction (CP07) must not move `run` at all: this pins its flag set and its usage
// text, so a later subcommand cannot change what `asqs-core run` accepts or prints without this
// test naming the difference.
func TestRunFlagSet_isPinned(t *testing.T) {
	want := []string{
		"audit-log", "base-branch", "config", "docs", "dry-run", "lang",
		"max-gaps", "max-gaps-e2e", "repo", "sandbox", "ship", "ship-branch",
	}

	// parseRunFlags does not expose its FlagSet, so the flag names are read from the usage text —
	// which is itself half of what this test pins.
	var b strings.Builder
	f, err := parseRunFlags(nil, &b)
	if err != nil {
		t.Fatal(err)
	}
	f.usage()
	usage := b.String()

	if !strings.Contains(usage, "usage: asqs-core run [flags] [<repo-path-or-git-url>]") {
		t.Errorf("run usage header changed:\n%s", usage)
	}
	var got []string
	for _, line := range strings.Split(usage, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "-") {
			name := strings.Fields(strings.TrimPrefix(line, "-"))[0]
			got = append(got, name)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("run flag set changed:\n  got  %v\n  want %v", got, want)
	}
}

func TestDispatch_rejectsUnknownCommandsNamingTheValidOnes(t *testing.T) {
	err := dispatch([]string{"migrte"})
	if err == nil {
		t.Fatal("an unknown command must be an error")
	}
	for _, want := range []string{"migrte", "run", "migrate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	if err := dispatch(nil); err == nil {
		t.Fatal("no command must be an error")
	}
}
