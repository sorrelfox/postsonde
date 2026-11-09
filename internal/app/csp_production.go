//go:build production

package app

// devServerURL is always empty in production builds, as in Wails' asset
// server: the strict policy can't be relaxed by the environment.
func devServerURL() string {
	return ""
}
