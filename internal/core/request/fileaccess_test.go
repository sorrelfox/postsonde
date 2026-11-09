package request

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/netrc"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// accessDirs lays out, under one temp dir, a project folder, an allowed
// directory and one outside both, each holding the file name with
// content, plus project/escape (a link to the outside file) and
// project/outdir (a link to the outside folder).
type accessDirs struct {
	project, allowed, outside string
}

func newAccessDirs(t *testing.T, name, content string) accessDirs {
	t.Helper()
	base := t.TempDir()
	d := accessDirs{filepath.Join(base, "project"), filepath.Join(base, "allowed"), filepath.Join(base, "outside")}
	for _, dir := range []string{d.project, d.allowed, d.outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for target, link := range map[string]string{filepath.Join(d.outside, name): "escape", d.outside: "outdir"} {
		if err := os.Symlink(target, filepath.Join(d.project, link)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("symbolic links need privileges on Windows:", err)
			}
			t.Fatal(err)
		}
	}
	return d
}

// accessCases are the paths every limited feature is tried with: relative
// to the project, absolute in the project, in an allowed directory, outside,
// a link in the project escaping out, a folder link escaping out, and a
// missing file outside (not allowed, not "not found").
func (d accessDirs) cases(name string) []struct {
	name, path string
	allowed    bool
} {
	return []struct {
		name, path string
		allowed    bool
	}{
		{"project-relative", name, true},
		{"absolute in the project", filepath.Join(d.project, name), true},
		{"allowed directory", filepath.Join(d.allowed, name), true},
		{"outside", filepath.Join(d.outside, name), false},
		{"link escaping", "escape", false},
		{"folder link escaping", "outdir/" + name, false},
		{"missing outside", filepath.Join(d.outside, "nope"), false},
	}
}

func wantNotAllowed(t *testing.T, err error, subject, field string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != fileaccess.CodeNotAllowed || e.Kind != errs.PermissionDenied {
		t.Fatalf("err = %v, want %s", err, fileaccess.CodeNotAllowed)
	}
	if !strings.HasPrefix(e.Message, subject+" ") || !strings.Contains(e.Message, "outside the directories allowed for file access") {
		t.Errorf("message = %q", e.Message)
	}
	if e.Hint != fileaccess.HintAllow {
		t.Errorf("hint = %q", e.Hint)
	}
	if e.Details[DetailAuthField] != field || e.Details[DetailPath] == "" {
		t.Errorf("details = %v", e.Details)
	}
}

// A JWT key file is read from the project folder or an allowed directory
// only (PRV-10).
func TestJWTKeyFileAllowlist(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	d := newAccessDirs(t, "ed.pem", pemKey(t, key))
	for _, tc := range d.cases("ed.pem") {
		t.Run(tc.name, func(t *testing.T) {
			a := projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: "EdDSA", KeyFile: tc.path}
			got, err := AuthWith(a, nil, AuthOptions{Root: d.project, AllowedDirs: []string{d.allowed}})
			if tc.allowed {
				if err != nil || got.JWT == nil {
					t.Fatalf("got %+v, %v", got, err)
				}
				return
			}
			wantNotAllowed(t, err, "The JWT key file", "keyFile")
		})
	}
	// Without a project nothing is allowed but the allowed directories.
	a := projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: "EdDSA", KeyFile: filepath.Join(d.project, "ed.pem")}
	_, err := AuthWith(a, nil, AuthOptions{AllowedDirs: []string{d.allowed}})
	wantNotAllowed(t, err, "The JWT key file", "keyFile")
	// A build passes the allowlist on.
	_, err = BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.example.com"},
		projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: "EdDSA", KeyFile: filepath.Join(d.allowed, "ed.pem")},
		HTTPOptions{Root: d.project, AllowedDirs: []string{d.allowed}})
	if err != nil {
		t.Errorf("build with an allowed key file: %v", err)
	}
}

// A netrc file the auth names is limited like a JWT key file; the one of
// NETRC or the home directory is the user's choice and is not.
func TestNetrcFileAllowlist(t *testing.T) {
	d := newAccessDirs(t, "netrc", "default login anna password pw\n")
	home := t.TempDir() // outside every allowed directory
	if err := os.WriteFile(filepath.Join(home, ".netrc"), []byte("default login home password h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := netrc.Env{
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return home, nil },
		GOOS:    "linux",
	}
	build := func(file string, env netrc.Env) (HTTPBuild, error) {
		return buildNetrc(t, env, "https://api.example.com", projectfile.Auth{Type: projectfile.AuthNetrc, NetrcFile: file},
			HTTPOptions{Root: d.project, AllowedDirs: []string{d.allowed}})
	}
	for _, tc := range d.cases("netrc") {
		t.Run(tc.name, func(t *testing.T) {
			b, err := build(tc.path, env)
			if tc.allowed {
				if err != nil || b.Netrc == nil || b.Netrc.Login != "anna" {
					t.Fatalf("got %+v, %v", b.Netrc, err)
				}
				return
			}
			wantNotAllowed(t, err, "The .netrc file", "netrcFile")
		})
	}
	t.Run("home directory", func(t *testing.T) {
		b, err := build("", env)
		if err != nil || b.Netrc.Login != "home" || b.Netrc.Source != netrc.SourceHome {
			t.Fatalf("got %+v, %v", b.Netrc, err)
		}
	})
	t.Run("NETRC", func(t *testing.T) {
		e := env
		e.Getenv = func(k string) string {
			if k == "NETRC" {
				return filepath.Join(d.outside, "netrc")
			}
			return ""
		}
		b, err := build("", e)
		if err != nil || b.Netrc.Login != "anna" || b.Netrc.Source != netrc.SourceEnv {
			t.Fatalf("got %+v, %v", b.Netrc, err)
		}
	})
	t.Run("missing inside", func(t *testing.T) {
		_, err := build("nope", env)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeAuthNetrcFailed || e.Kind != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a folder", func(t *testing.T) {
		_, err := build(".", env)
		if !strings.Contains(errs.Chain(err)[0], "not a file") {
			t.Fatalf("err = %v", err)
		}
	})
}
