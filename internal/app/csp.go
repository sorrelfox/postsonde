package app

import (
	"net/http"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// cspHeader is the response header carrying the Content Security Policy.
const cspHeader = "Content-Security-Policy"

// cspMiddleware sets the Content-Security-Policy header on every response
// of the Wails asset server: the embedded index.html and assets, the
// runtime IPC endpoints and, in `wails3 dev`, the pages proxied from Vite
// (docs/privacy.md). A header, unlike the <meta> tag the build also
// writes, covers the document before any byte of it is parsed.
func cspMiddleware(policy string) application.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(cspHeader, policy)
			next.ServeHTTP(w, r)
		})
	}
}

// effectivePolicy is the policy the asset server sends: the strict one,
// or its dev relaxation while a `wails3 dev` frontend dev server is in use
// (devServerURL is always empty in production builds).
func effectivePolicy(policy string) string {
	if devServerURL() != "" {
		return devPolicy(policy)
	}
	return policy
}

// devSources are added to the strict policy under `wails3 dev` only. The
// Vite dev server injects an inline module script (the React Refresh
// preamble) and connects its HMR WebSocket straight to the dev server on
// loopback, which the custom-scheme page origin doesn't cover. Nothing
// here reaches beyond the local machine.
var devSources = map[string]string{
	"script-src":  "'unsafe-inline'",
	"connect-src": "ws://localhost:* ws://127.0.0.1:* wss://localhost:* wss://127.0.0.1:*",
}

// devPolicy returns policy with devSources appended to their directives.
// A directive the policy lacks is added, seeded with default-src's sources.
func devPolicy(policy string) string {
	directives := strings.Split(policy, "; ")
	var defaultSrc string
	for _, d := range directives {
		if name, sources, _ := strings.Cut(d, " "); name == "default-src" {
			defaultSrc = sources
		}
	}
	for _, name := range []string{"script-src", "connect-src"} {
		found := false
		for i, d := range directives {
			if n, _, _ := strings.Cut(d, " "); n == name {
				directives[i] = d + " " + devSources[name]
				found = true
			}
		}
		if !found {
			directives = append(directives, strings.TrimSpace(name+" "+defaultSrc+" "+devSources[name]))
		}
	}
	return strings.Join(directives, "; ")
}
