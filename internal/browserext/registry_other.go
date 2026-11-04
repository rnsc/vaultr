//go:build !windows

package browserext

// SystemRegistry is the registry to install with: none outside Windows.
func SystemRegistry() Registry { return nil }
