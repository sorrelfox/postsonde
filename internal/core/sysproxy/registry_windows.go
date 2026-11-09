//go:build windows

package sysproxy

import (
	"errors"
	"syscall"
)

// internetSettings is the key of the current user's proxy settings.
const internetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// ReadRegistry reads the current user's Internet Settings with syscall's
// registry functions (no golang.org/x/sys). A missing key or value is no
// proxy.
func ReadRegistry() (RegistryValues, error) {
	path, err := syscall.UTF16PtrFromString(internetSettings)
	if err != nil {
		return RegistryValues{}, err
	}
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, path, 0, syscall.KEY_READ, &key); err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return RegistryValues{}, nil
		}
		return RegistryValues{}, err
	}
	defer syscall.RegCloseKey(key)
	var v RegistryValues
	enable, err := queryValue(key, "ProxyEnable")
	if err != nil {
		return RegistryValues{}, err
	}
	v.ProxyEnable = regDWORD(enable) != 0
	for _, f := range []struct {
		name string
		dst  *string
	}{{"ProxyServer", &v.ProxyServer}, {"ProxyOverride", &v.ProxyOverride}, {"AutoConfigURL", &v.AutoConfigURL}} {
		b, err := queryValue(key, f.name)
		if err != nil {
			return RegistryValues{}, err
		}
		*f.dst = regString(b)
	}
	return v, nil
}

// queryValue returns a value's data; nil when it does not exist.
func queryValue(key syscall.Handle, name string) ([]byte, error) {
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var typ, size uint32
	err = syscall.RegQueryValueEx(key, n, nil, &typ, nil, &size)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	if err := syscall.RegQueryValueEx(key, n, nil, &typ, &buf[0], &size); err != nil {
		return nil, err
	}
	return buf[:size], nil
}
