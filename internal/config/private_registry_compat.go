package config

// Authenticated private-registry credential injection (Maven settings.xml / npm .npmrc / NuGet
// sources) is an enterprise feature and is NOT part of asqs-core. PrivateRegistryMount and
// MaterialisePrivateRegistryMounts are inert placeholders so copied callers compile; the open core
// never materialises any private-registry mounts.

// PrivateRegistryEcosystem names the package manager a generated credential file configures.
// Part of the inert seam: the open core never generates such a file, but the runner's credential
// delivery code (runner/credentials.go) compiles against these types unchanged.
type PrivateRegistryEcosystem string

const (
	// EcosystemMaven: a settings.xml, consumed by a container mount or by `mvn -s <path>`.
	EcosystemMaven PrivateRegistryEcosystem = "maven"
	// EcosystemNPM: an .npmrc, consumed by a container mount or by npm_config_userconfig.
	EcosystemNPM PrivateRegistryEcosystem = "npm"
)

// PrivateRegistryMount is an inert placeholder (no private-registry support in the open core).
type PrivateRegistryMount struct {
	// Ecosystem names the package manager this file configures.
	Ecosystem     PrivateRegistryEcosystem
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// MaterialisePrivateRegistryMounts is a no-op in the open core.
func (r *RunnerConfig) MaterialisePrivateRegistryMounts() ([]PrivateRegistryMount, error) {
	return nil, nil
}
