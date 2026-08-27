package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/asqs/asqs-core/internal/config"
)

// runConfigReference implements `asqs-core config reference`.
//
// It exists as a subcommand rather than a `go generate` step so the document can be produced from a
// SHIPPED BINARY with no source tree: the schema source is embedded, so an operator debugging a
// deployment can ask the exact build they are running what keys it understands, instead of reading a
// document written for some other version.
func runConfigReference(args []string) error {
	fs := flag.NewFlagSet("config reference", flag.ContinueOnError)
	out := fs.String("o", "", "write to this file instead of stdout")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: asqs-core config reference [-o FILE]\n\n"+
			"Renders the exhaustive configuration reference from the schema this binary was built\n"+
			"with. The checked-in copy lives at docs/CONFIG-REFERENCE.md and a drift test regenerates\n"+
			"it, so `-o docs/CONFIG-REFERENCE.md` is how you refresh it after a schema change.\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	body, err := config.RenderConfigReferenceMarkdown()
	if err != nil {
		return fmt.Errorf("render config reference: %w", err)
	}
	if *out == "" {
		_, err := io.WriteString(os.Stdout, body)
		return err
	}
	if err := os.WriteFile(*out, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "asqs-core: wrote %s (%d bytes)\n", *out, len(body))
	return nil
}

// runConfig routes the `config` subcommands. Only `reference` exists today; the extra level is
// deliberate so a later `config validate` or `config explain` is one case here rather than a fourth
// top-level verb.
func runConfig(args []string) error {
	if len(args) < 1 {
		printConfigUsage()
		return fmt.Errorf("missing config subcommand")
	}
	switch args[0] {
	case "reference":
		return runConfigReference(args[1:])
	default:
		printConfigUsage()
		return fmt.Errorf("unknown config subcommand %q (supported: reference)", args[0])
	}
}

func printConfigUsage() {
	fmt.Fprintf(os.Stderr, "usage: asqs-core config <subcommand>\n\nsubcommands:\n"+
		"  reference  render the exhaustive configuration reference (see -o)\n")
}
