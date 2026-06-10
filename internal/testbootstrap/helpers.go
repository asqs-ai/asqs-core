package testbootstrap

import (
	"os"
	"strings"
	"time"
)

const (
	defaultInstallTimeout = 15 * time.Minute
	maxInstallTimeout     = 30 * time.Minute
)

// installTimeout maps runner.timeout to a subprocess budget; empty → 15m, parsed value capped at 30m.
func installTimeout(runnerTimeout string) time.Duration {
	s := strings.TrimSpace(runnerTimeout)
	if s == "" {
		return defaultInstallTimeout
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultInstallTimeout
	}
	if d > maxInstallTimeout {
		return maxInstallTimeout
	}
	return d
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
