package frontend

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestContentSecurityPolicy(t *testing.T) {
	t.Parallel()

	p := ContentSecurityPolicy
	if !strings.HasPrefix(p, "default-src 'self'; ") {
		t.Errorf("policy must start with default-src 'self': %q", p)
	}
	for _, bad := range []string{"unsafe-eval", "http:", "https:", "*", "\""} {
		if strings.Contains(p, bad) {
			t.Errorf("policy contains %q: %q", bad, p)
		}
	}
	for _, want := range []string{"script-src 'self';", "connect-src 'self';", "object-src 'none'", "frame-src about:;", "base-uri 'none'"} {
		if !strings.Contains(p, want) {
			t.Errorf("policy lacks %q: %q", want, p)
		}
	}
}

// TestIndexHTMLHasPolicy checks that a built dist/index.html carries the
// same policy as a <meta> tag. On a fresh checkout (no npm build) there is
// no index.html and the test is skipped; scripts/check-dist.mjs checks it
// after every build.
func TestIndexHTMLHasPolicy(t *testing.T) {
	t.Parallel()

	index, err := fs.ReadFile(Assets, "index.html")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("frontend not built")
	}
	if err != nil {
		t.Fatal(err)
	}
	want := `<meta http-equiv="Content-Security-Policy" content="` + ContentSecurityPolicy + `"`
	if !strings.Contains(string(index), want) {
		t.Errorf("dist/index.html lacks %s", want)
	}
}

func TestMustPolicy(t *testing.T) {
	t.Parallel()

	if got := mustPolicy([]byte(`{"directives":["default-src 'self'","img-src data:"]}`)); got != "default-src 'self'; img-src data:" {
		t.Errorf("mustPolicy = %q", got)
	}
	for _, bad := range []string{`{}`, `not json`} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("mustPolicy(%s) did not panic", bad)
				}
			}()
			mustPolicy([]byte(bad))
		}()
	}
}
