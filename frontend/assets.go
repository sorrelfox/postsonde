// Package frontend embeds the built React UI into the Go binary.
//
// Vite writes the production build to frontend/dist, which is embedded here
// so the app loads nothing from the network at runtime. csp.json, the
// Content Security Policy that enforces that, is embedded too. The directory is
// gitignored except for a committed dist/.gitkeep placeholder: it keeps
// `go build ./...` and `go test ./...` working on a fresh checkout, before
// npm has ever run. The Vite config recreates the placeholder after each
// build, since Vite empties dist/ first.
//
// A binary built without a frontend build (plain `go build`) starts but has
// no page to show; use `wails3 build` or `wails3 dev` for a working app.
package frontend

import (
	"embed"
	"encoding/json"
	"io/fs"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Assets is the built frontend, rooted at dist/ so that index.html sits at
// the top level.
var Assets fs.FS = mustSub(dist, "dist")

//go:embed csp.json
var cspJSON []byte

// ContentSecurityPolicy is the UI's Content Security Policy from csp.json,
// directives joined with "; " (docs/privacy.md). The app sends it as a
// response header; the production build also writes it into index.html as
// a <meta> tag (vite.config.ts), from the same file.
var ContentSecurityPolicy = mustPolicy(cspJSON)

func mustPolicy(data []byte) string {
	var csp struct {
		Directives []string `json:"directives"`
	}
	if err := json.Unmarshal(data, &csp); err != nil {
		panic("frontend: csp.json: " + err.Error())
	}
	if len(csp.Directives) == 0 {
		panic("frontend: csp.json: no directives")
	}
	return strings.Join(csp.Directives, "; ")
}

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic("frontend: " + err.Error())
	}
	return sub
}
