package browserext

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// WindowsRegistry writes to HKEY_CURRENT_USER, which needs no admin
// rights.
type WindowsRegistry struct{}

// SetDefault creates key and sets its default value.
func (WindowsRegistry) SetDefault(key, value string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("", value)
}

// Delete removes key; a missing key is fine.
func (WindowsRegistry) Delete(key string) error {
	err := registry.DeleteKey(registry.CURRENT_USER, key)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

// DefaultValue reads key's default value (for tests and checks).
func (WindowsRegistry) DefaultValue(key string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	return v, err
}

// SystemRegistry is the registry to install with.
func SystemRegistry() Registry { return WindowsRegistry{} }
