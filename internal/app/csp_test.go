package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const testPolicy = "default-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'"

// TestAssetServerSendsCSP serves index.html and an asset through the
// handler chain appOptions configures (middleware around the file server,
// as Wails' asset server builds it) and checks both carry the policy.
func TestAssetServerSendsCSP(t *testing.T) {
	t.Setenv("FRONTEND_DEVSERVER_URL", "")

	assets := fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html><title>PostSonde</title>")},
		"assets/index.js":   {Data: []byte("console.log(1)")},
		"assets/index.css":  {Data: []byte("body{}")},
		"theme-init.js":     {Data: []byte("void 0")},
		"assets/font.woff2": {Data: []byte("wOF2")},
	}
	o := appOptions(Options{Assets: assets, ContentSecurityPolicy: testPolicy}, nil)
	if o.Assets.Middleware == nil {
		t.Fatal("Assets.Middleware is nil; no CSP header would be sent")
	}
	handler := o.Assets.Middleware(o.Assets.Handler)

	for _, path := range []string{"/", "/index.html", "/assets/index.js", "/assets/index.css", "/theme-init.js", "/missing"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "wails://localhost"+path, nil))
			if got := rec.Header().Get(cspHeader); got != testPolicy {
				t.Errorf("%s: %s = %q, want %q (status %d)", path, cspHeader, got, testPolicy, rec.Code)
			}
		})
	}
}

func TestEffectivePolicy(t *testing.T) {
	t.Run("no dev server", func(t *testing.T) {
		t.Setenv("FRONTEND_DEVSERVER_URL", "")
		if got := effectivePolicy(testPolicy); got != testPolicy {
			t.Errorf("effectivePolicy = %q, want the strict policy %q", got, testPolicy)
		}
	})
	t.Run("wails3 dev", func(t *testing.T) {
		t.Setenv("FRONTEND_DEVSERVER_URL", "http://localhost:9245")
		// Build-tag dependent: production builds never relax the policy.
		want := devPolicy(testPolicy)
		if devServerURL() == "" {
			want = testPolicy
		}
		if got := effectivePolicy(testPolicy); got != want {
			t.Errorf("effectivePolicy = %q, want %q", got, want)
		}
	})
}

func TestDevPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, policy, want string
	}{
		{
			"extends existing directives",
			testPolicy,
			"default-src 'self'; script-src 'self' 'unsafe-inline'; connect-src 'self' " + devSources["connect-src"] + "; object-src 'none'",
		},
		{
			"adds missing directives from default-src",
			"default-src 'self'; object-src 'none'",
			"default-src 'self'; object-src 'none'; script-src 'self' 'unsafe-inline'; connect-src 'self' " + devSources["connect-src"],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := devPolicy(tt.policy)
			if got != tt.want {
				t.Errorf("devPolicy =\n  %q\nwant\n  %q", got, tt.want)
			}
			// Loopback only, and never eval.
			for _, bad := range []string{"unsafe-eval", "http:", "https:", " * "} {
				if strings.Contains(got, bad) {
					t.Errorf("dev policy contains %q", bad)
				}
			}
		})
	}
}

func TestNewRequiresPolicy(t *testing.T) {
	t.Parallel()

	if _, err := New(Options{ContentSecurityPolicy: "  "}); err == nil || !strings.Contains(err.Error(), "ContentSecurityPolicy") {
		t.Errorf("New without a policy = %v, want an error naming ContentSecurityPolicy", err)
	}
}
