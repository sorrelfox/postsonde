//go:build !windows

package sysproxy

import "errors"

// ReadRegistry is Windows only.
func ReadRegistry() (RegistryValues, error) {
	return RegistryValues{}, errors.New("the Windows registry is not available on this system")
}
