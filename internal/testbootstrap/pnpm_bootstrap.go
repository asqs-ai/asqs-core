package testbootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BootstrapPnpmStorePath returns the absolute pnpm content-store directory used during
// test/E2E bootstrap so installs do not create a repo-local .pnpm-store (often from
// store-dir in the project's .npmrc). Host: user cache; Docker: path inside the eval image.
func BootstrapPnpmStorePath(dockerEphemeral bool) (string, error) {
	if dockerEphemeral {
		return "/root/.local/share/pnpm/store", nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("bootstrap pnpm store: user cache dir: %w", err)
	}
	return filepath.Join(base, "asqs-pnpm-store"), nil
}

const pnpmBootstrapGitignoreBanner = "# ASQS bootstrap: ignore local pnpm store if project pins store-dir under the repo"

// EnsurePnpmBootstrapGitignore appends .pnpm-store/ to .gitignore when missing so a
// repo-local store (from pre-bootstrap installs or .npmrc) is not committed with generated tests/docs.
func EnsurePnpmBootstrapGitignore(repo string) error {
	repo = strings.TrimSpace(filepath.Clean(repo))
	if repo == "" {
		return fmt.Errorf("bootstrap gitignore: empty repo path")
	}
	path := filepath.Join(repo, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existing := string(data)
	if gitignoreCoversPnpmStore(existing) {
		return nil
	}
	var b strings.Builder
	if strings.TrimSpace(existing) != "" {
		b.WriteString(strings.TrimRight(existing, "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString(pnpmBootstrapGitignoreBanner)
	b.WriteString("\n.pnpm-store/\n")
	return atomicWrite(path, []byte(b.String()))
}

func gitignoreCoversPnpmStore(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "!") {
			continue // negated pattern — not an ignore rule for our purposes
		}
		pat := line
		pat = strings.TrimSpace(pat)
		pat = strings.TrimSuffix(pat, "/")
		base := filepath.Base(pat)
		if base == ".pnpm-store" || pat == ".pnpm-store" {
			return true
		}
		if strings.HasSuffix(pat, "/.pnpm-store") {
			return true
		}
	}
	return false
}
