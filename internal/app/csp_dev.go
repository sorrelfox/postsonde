//go:build !production

package app

import "os"

// devServerURL is the frontend dev server `wails3 dev` points the app at,
// read from the same variable as Wails' asset server
// (assetserver.GetDevServerURL in v3.0.0). Empty otherwise.
func devServerURL() string {
	return os.Getenv("FRONTEND_DEVSERVER_URL")
}
