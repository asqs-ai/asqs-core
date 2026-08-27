package config

import (
	"fmt"
	"sort"
	"strings"
)

// sectionOrder is the order the reference presents the top-level blocks: pipeline order, matching
// the schema's own organisation, rather than alphabetical. An operator reading top to bottom follows
// a run.
var sectionOrder = []string{
	topLevelSection,
	"general", "bootstrap", "indexer", "retrieval", "generation", "fixer",
}

// sectionBlurbs give each block a one-line purpose so the reference is navigable without reading
// every row.
// topLevelSection groups the two scalars that sit at the document root; a heading each would be
// three lines of chrome around one row.
const topLevelSection = "(top level)"

var sectionBlurbs = map[string]string{
	topLevelSection: "Keys at the root of the document.",
	"general":       "Shared by more than one pipeline step: connections, credentials, models, how to build the client repo, and the sandbox every containerised step runs in.",
	"bootstrap":     "The optional pre-index step that installs a test framework in a repo that has none.",
	"indexer":       "How the language indexers are located and executed.",
	"retrieval":     "Planning budgets and the context assembled for each gap.",
	"generation":    "Test generation, plus the symbol-doc and overview workstreams that run in the same phase.",
	"fixer":         "The run-scope evaluate/fix loop. Evaluation is part of this step: what an operator tunes about evaluation is how hard the fixer tries.",
}

// RenderConfigReferenceMarkdown produces the checked-in reference document.
//
// It is deliberately one table per section rather than one flat table: the sections are the schema's
// organising idea, and a single long table would hide it. The env-only appendix is included because
// those settings have no other home — they are not in the schema, and nothing else lists them.
func RenderConfigReferenceMarkdown() (string, error) {
	entries, err := BuildConfigReference()
	if err != nil {
		return "", err
	}
	bySection := map[string][]ReferenceEntry{}
	for _, e := range entries {
		bySection[e.Section] = append(bySection[e.Section], e)
	}

	var b strings.Builder
	b.WriteString(`# Configuration reference

**Generated — do not edit.** Produced from the schema structs in ` + "`internal/config/schema_v2.go`" + `
by ` + "`asqs-core config reference`" + `. A drift test regenerates it and fails when this file and the
structs disagree, which is what keeps a generated mirror from going stale the way a hand-maintained
one does.

This is the exhaustive list. The shipped ` + "`config.example.yaml`" + ` is deliberately much smaller — it
shows the keys a deployment usually sets, and everything else is documented here and omitted when it
is at its default.

**Reading the table.** *Default* is the effective value when the key is absent, not a suggestion.
` + "`unset`" + ` means the key distinguishes "not written" from "written as false", which is how a
default of true can exist. *Env* is derived from the path — ` + "`ASQS_`" + ` plus the dotted path
upper-cased, with a leading ` + "`general.`" + ` stripped — so there is no lookup table to fall out of
date.

`)

	for _, section := range sectionOrder {
		rows := bySection[section]
		if len(rows) == 0 {
			continue
		}
		b.WriteString("## " + section + "\n\n")
		if blurb := sectionBlurbs[section]; blurb != "" {
			b.WriteString(blurb + "\n\n")
		}
		b.WriteString("| Key | Type | Default | Env | Description |\n")
		b.WriteString("|---|---|---|---|---|\n")
		for _, e := range rows {
			b.WriteString(fmt.Sprintf("| `%s` | %s | %s | `%s` | %s |\n",
				e.Path, e.Type, e.Default, e.Env, escapeTableCell(e.Doc)))
		}
		b.WriteString("\n")
	}

	// Any section the order list forgot still gets printed, so a new top-level block cannot vanish
	// from the reference just because someone did not update sectionOrder.
	var extra []string
	for section := range bySection {
		known := false
		for _, s := range sectionOrder {
			if s == section {
				known = true
				break
			}
		}
		if !known {
			extra = append(extra, section)
		}
	}
	sort.Strings(extra)
	for _, section := range extra {
		b.WriteString("## " + section + "\n\n")
		b.WriteString("| Key | Type | Default | Env | Description |\n|---|---|---|---|---|\n")
		for _, e := range bySection[section] {
			b.WriteString(fmt.Sprintf("| `%s` | %s | %s | `%s` | %s |\n",
				e.Path, e.Type, e.Default, e.Env, escapeTableCell(e.Doc)))
		}
		b.WriteString("\n")
	}

	b.WriteString(renderEnvOnlyAppendix())
	return b.String(), nil
}

// renderEnvOnlyAppendix documents everything read straight from the environment that has no YAML key.
//
// Grouped, because the groups ask different things of the reader: asqs-core's own settings (which an
// operator sets), variables it INHERITS from the environment (which the platform sets, and which are
// listed so nobody is surprised that asqs-core reads them), and test-only inputs (which decide
// whether an integration test runs or silently skips).
func renderEnvOnlyAppendix() string {
	var b strings.Builder
	b.WriteString(`## Appendix: environment-only settings

Every schema key already has a derived environment variable, so this list is short by construction.
What is on it is here for a reason: a break-glass switch that should not be easy to leave on in a
config file, a variable the platform or a toolchain already defines that asqs-core reads rather than
reinventing, or a test input. The cost of env-only is discoverability, which is what this appendix
pays back.

The list is checked against a mechanical sweep of every ` + "`os.Getenv`" + ` and
` + "`os.LookupEnv`" + ` call under ` + "`cmd/`" + `, ` + "`internal/`" + ` and ` + "`tools/`" + `; a
test fails when it goes stale.

`)
	var security, asqs, inherited, testOnly []EnvOnlySwitch
	for _, sw := range EnvOnlySwitches {
		switch {
		case sw.Security:
			security = append(security, sw)
		case sw.Kind == "inherited":
			inherited = append(inherited, sw)
		case sw.Kind == "test":
			testOnly = append(testOnly, sw)
		default:
			asqs = append(asqs, sw)
		}
	}

	writeTable := func(heading, blurb string, rows []EnvOnlySwitch) {
		if len(rows) == 0 {
			return
		}
		b.WriteString("### " + heading + "\n\n")
		if blurb != "" {
			b.WriteString(blurb + "\n\n")
		}
		b.WriteString("| Variable | Read by | What it does |\n|---|---|---|\n")
		for _, sw := range rows {
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s |\n", sw.Name, sw.Component, escapeTableCell(sw.Doc)))
		}
		b.WriteString("\n")
	}

	writeTable("Security-relevant",
		"Setting these weakens a protection. Read the description before using one.", security)
	writeTable("asqs-core settings", "Set by whoever deploys asqs-core.", asqs)
	writeTable("Inherited from the environment",
		"Not asqs-core settings — variables the platform or a toolchain already defines, which "+
			"asqs-core reads rather than reinventing. Listed so it is clear what the process depends on.", inherited)
	writeTable("Test and benchmark only",
		"Unset means the corresponding test SKIPS rather than fails, so a green run does not by itself "+
			"prove these ran.", testOnly)
	return b.String()
}

// escapeTableCell keeps a doc comment from breaking the markdown table it sits in.
func escapeTableCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
