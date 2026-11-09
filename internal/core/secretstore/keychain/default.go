package keychain

import "runtime"

// Default returns the keychain of this system for the data directory
// dataDir: Security on macOS, DPAPI on Windows, SecretTool elsewhere. Only
// the app uses it; tests build a backend with fakes.
func Default(dataDir string) KeyStore {
	return forOS(runtime.GOOS, dataDir)
}

func forOS(goos, dataDir string) KeyStore {
	switch goos {
	case "darwin":
		return &Security{}
	case "windows":
		return &DPAPI{Dir: dataDir}
	default:
		return &SecretTool{}
	}
}
