package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
)

// PackageManager is npm, yarn, or pnpm inferred from lockfiles.
type PackageManager string

const (
	PMNpm  PackageManager = "npm"
	PMYarn PackageManager = "yarn"
	PMPnpm PackageManager = "pnpm"
)

func detectPackageManager(dir string) PackageManager {
	if _, err := os.Stat(filepath.Join(dir, "pnpm-lock.yaml")); err == nil {
		return PMPnpm
	}
	if _, err := os.Stat(filepath.Join(dir, "yarn.lock")); err == nil {
		return PMYarn
	}
	return PMNpm
}

func hasLockfile(dir string, pm PackageManager) bool {
	switch pm {
	case PMPnpm:
		_, err := os.Stat(filepath.Join(dir, "pnpm-lock.yaml"))
		return err == nil
	case PMYarn:
		_, err := os.Stat(filepath.Join(dir, "yarn.lock"))
		return err == nil
	default:
		_, err := os.Stat(filepath.Join(dir, "package-lock.json"))
		return err == nil
	}
}

// installCmdLine returns a human-readable install command for logging.
// pnpmStoreAbs is used only when pm is pnpm (empty otherwise): --store-dir for bootstrap installs.
func installCmdLine(pm PackageManager, allowLockfileChange bool, hasLock bool, mustSyncLockfile bool, pnpmStoreAbs string) string {
	name, args := installCmd(pm, allowLockfileChange, hasLock, mustSyncLockfile, pnpmStoreAbs)
	return name + " " + strings.Join(args, " ")
}

// installCmd picks npm/pnpm/yarn install argv. When mustSyncLockfile is true (e.g. bootstrap just
// edited package.json), the lockfile must be updated: use explicit --no-frozen-lockfile for pnpm
// (pnpm treats CI=true as frozen by default), npm install not npm ci, and yarn install with
// YARN_ENABLE_IMMUTABLE_INSTALLS=false supplied by RunPackageManagerInstall.
func installCmd(pm PackageManager, allowLockfileChange bool, hasLock bool, mustSyncLockfile bool, pnpmStoreAbs string) (name string, args []string) {
	if mustSyncLockfile {
		switch pm {
		case PMPnpm:
			a := []string{"install"}
			if s := strings.TrimSpace(pnpmStoreAbs); s != "" {
				a = append(a, "--store-dir", s)
			}
			a = append(a, "--no-frozen-lockfile")
			return "pnpm", a
		case PMYarn:
			return "yarn", []string{"install"}
		default:
			return "npm", []string{"install"}
		}
	}
	switch pm {
	case PMPnpm:
		a := []string{"install"}
		if s := strings.TrimSpace(pnpmStoreAbs); s != "" {
			a = append(a, "--store-dir", s)
		}
		if hasLock && !allowLockfileChange {
			a = append(a, "--frozen-lockfile")
		}
		return "pnpm", a
	case PMYarn:
		if hasLock && !allowLockfileChange {
			return "yarn", []string{"install", "--frozen-lockfile"}
		}
		return "yarn", []string{"install"}
	default:
		if hasLock && !allowLockfileChange {
			return "npm", []string{"ci"}
		}
		return "npm", []string{"install"}
	}
}
