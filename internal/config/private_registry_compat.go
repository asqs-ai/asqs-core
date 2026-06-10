package config

// Authenticated private-registry credential injection (Maven settings.xml / npm .npmrc / NuGet
// sources) is an enterprise feature and is NOT part of asqs-core. PrivateRegistryMount and
// MaterialisePrivateRegistryMounts are inert placeholders so copied callers compile; the open core
// never materialises any private-registry mounts.

// PrivateRegistryMount is an inert placeholder (no private-registry support in the open core).
type PrivateRegistryMount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// MaterialisePrivateRegistryMounts is a no-op in the open core.
func (r *RunnerConfig) MaterialisePrivateRegistryMounts() ([]PrivateRegistryMount, error) {
	return nil, nil
}
