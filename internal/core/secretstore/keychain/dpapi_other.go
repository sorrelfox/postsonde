//go:build !windows

package keychain

// DPAPI exists on Windows only; elsewhere a DPAPI without fakes is
// unavailable.

func protectData(_, _ []byte) ([]byte, error) {
	return nil, unavailable(dpapiBackend, "The Windows Data Protection API exists only on Windows.", fallbackHint)
}

func unprotectData(_, _ []byte) ([]byte, error) {
	return nil, unavailable(dpapiBackend, "The Windows Data Protection API exists only on Windows.", fallbackHint)
}
