package pipeline

import (
	"strings"

	"github.com/asqs/asqs-core/internal/runner"
)

// formatSkipReason names why no formatter resolved, for the skipped-format audit row.
func formatSkipReason(r runner.FormatResolveResult) string {
	if s := strings.TrimSpace(r.SkipReason); s != "" {
		return s
	}
	return "no formatter resolved"
}
