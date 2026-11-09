package request

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netrc"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

var testHome = filepath.Join(string(filepath.Separator), "home", "tester")

// memNetrc is a netrc.Env over files in memory: the process environment,
// the user's home and the file system are never touched. reads counts the
// files read, by path.
type memNetrc struct {
	// home is the home directory; "" is testHome.
	home  string
	files map[string]string
	modes map[string]fs.FileMode
	env   map[string]string
	reads map[string]int
}

func newMemNetrc(files map[string]string) *memNetrc {
	return &memNetrc{files: files, modes: map[string]fs.FileMode{}, env: map[string]string{}, reads: map[string]int{}}
}

func (m *memNetrc) Env() netrc.Env {
	return netrc.Env{
		Getenv: func(k string) string { return m.env[k] },
		HomeDir: func() (string, error) {
			if m.home != "" {
				return m.home, nil
			}
			return testHome, nil
		},
		GOOS: "linux",
		ReadFile: func(path string) ([]byte, fs.FileMode, error) {
			m.reads[path]++
			data, ok := m.files[path]
			if !ok {
				return nil, 0, fs.ErrNotExist
			}
			mode, ok := m.modes[path]
			if !ok {
				mode = 0o600
			}
			return []byte(data), mode, nil
		},
	}
}

func (m *memNetrc) total() int {
	n := 0
	for _, c := range m.reads {
		n += c
	}
	return n
}

// failingNetrc fails the test if anything of the netrc environment is
// used.
func failingNetrc(t *testing.T) netrc.Env {
	return netrc.Env{
		Getenv:  func(k string) string { t.Errorf("netrc Getenv(%q) called", k); return "" },
		HomeDir: func() (string, error) { t.Error("netrc HomeDir called"); return "", errors.New("no") },
		GOOS:    "linux",
		ReadFile: func(path string) ([]byte, fs.FileMode, error) {
			t.Errorf("netrc file %s opened", path)
			return nil, 0, fs.ErrPermission
		},
	}
}

func basicOf(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

const sampleNetrc = `# test file
machine api.example.com
  login alice
  password "s3cret pass"
machine other.example.com login bob password b0b
default login anon password anonpw
`

func buildNetrc(t *testing.T, env netrc.Env, url string, a projectfile.Auth, o HTTPOptions) (HTTPBuild, error) {
	t.Helper()
	o.Netrc.Env = env
	return BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: url}, a, o)
}

func TestNetrcSendsTheHostsEntry(t *testing.T) {
	mem := newMemNetrc(map[string]string{filepath.Join(testHome, ".netrc"): sampleNetrc})
	auth := projectfile.Auth{Type: projectfile.AuthNetrc}
	cases := []struct {
		url, want, entry string
	}{
		{"https://api.example.com/v1", basicOf("alice", "s3cret pass"), "api.example.com"},
		{"https://API.Example.COM:8443/v1?x=1", basicOf("alice", "s3cret pass"), "api.example.com"},
		{"http://other.example.com", basicOf("bob", "b0b"), "other.example.com"},
		{"http://unknown.example.com", basicOf("anon", "anonpw"), "default"},
		{"https://bob@other.example.com/", basicOf("bob", "b0b"), "other.example.com"},
	}
	for _, tc := range cases {
		b, err := buildNetrc(t, mem.Env(), tc.url, auth, HTTPOptions{})
		if err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if got, _ := headerOf(b, "Authorization"); got != tc.want {
			t.Errorf("%s: Authorization = %q, want %q", tc.url, got, tc.want)
		}
		if b.Netrc == nil || b.Netrc.Entry != tc.entry || b.Netrc.Path != filepath.Join(testHome, ".netrc") || b.Netrc.Source != netrc.SourceHome {
			t.Errorf("%s: match = %+v", tc.url, b.Netrc)
		}
		if b.HeaderSources[len(b.HeaderSources)-1] != HeaderFromAuth {
			t.Errorf("%s: sources = %v", tc.url, b.HeaderSources)
		}
		if len(b.Warnings) != 0 {
			t.Errorf("%s: warnings = %v", tc.url, b.Warnings)
		}
	}
	// The password and the header are masked.
	b, _ := buildNetrc(t, mem.Env(), "https://api.example.com", auth, HTTPOptions{})
	if !slices.Contains(b.AuthSecrets, "s3cret pass") || !slices.Contains(b.AuthSecrets, basicOf("alice", "s3cret pass")) {
		t.Errorf("secrets = %v", b.AuthSecrets)
	}
}

func TestNetrcFileChoice(t *testing.T) {
	// The project folder and the home directory exist, as a file the auth
	// names is checked against the allowlist on disk (PRV-10); the files
	// themselves are in memory. testHome, which does not exist, holds the
	// default file: it is not limited.
	root, home := t.TempDir(), t.TempDir()
	files := map[string]string{
		filepath.Join(testHome, ".netrc"):    "default login home password h",
		filepath.Join("/etc", "netrc-env"):   "default login env password e",
		filepath.Join(root, "auth", "netrc"): "default login project password p",
		filepath.Join(home, ".netrc-other"):  "default login tilde password t",
	}
	cases := []struct {
		name, file, env, user, source string
	}{
		{"home", "", "", "home", netrc.SourceHome},
		{"NETRC", "", filepath.Join("/etc", "netrc-env"), "env", netrc.SourceEnv},
		{"relative to the project", "auth/netrc", filepath.Join("/etc", "netrc-env"), "project", netrc.SourceRequest},
		{"variable rendered", "{{dir}}/netrc", "", "project", netrc.SourceRequest},
		{"home prefix", "~/.netrc-other", "", "tilde", netrc.SourceRequest},
	}
	expand := func(s string) (string, error) { return strings.ReplaceAll(s, "{{dir}}", "auth"), nil }
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem := newMemNetrc(files)
			if tc.env != "" {
				mem.env["NETRC"] = tc.env
			}
			if tc.source == netrc.SourceRequest {
				mem.home = home
			}
			b, err := buildNetrc(t, mem.Env(), "https://x.example", projectfile.Auth{Type: projectfile.AuthNetrc, NetrcFile: tc.file},
				HTTPOptions{Root: root, AllowedDirs: []string{home}, Expand: expand})
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := headerOf(b, "Authorization"); !strings.HasPrefix(got, "Basic ") {
				t.Fatalf("Authorization = %q", got)
			}
			if b.Netrc.Login != tc.user || b.Netrc.Source != tc.source {
				t.Errorf("match = %+v, want login %s from %s", b.Netrc, tc.user, tc.source)
			}
			if mem.total() != 1 {
				t.Errorf("reads = %v, want one", mem.reads)
			}
		})
	}
}

func TestNetrcErrors(t *testing.T) {
	path := filepath.Join(testHome, ".netrc")
	cases := []struct {
		name, text, url string
		mode            fs.FileMode
		kind            errs.Kind
		code            string
		msg             string
		details         map[string]string
	}{
		{"missing", "", "https://a.example", 0, errs.NotFound, CodeAuthNetrcFailed, "was not found: " + path, map[string]string{DetailPath: path, DetailAuthField: "netrcFile"}},
		{"no entry", "machine b.example login b password topsecret", "https://a.example:8080/x", 0o600, errs.NotFound, CodeAuthNetrcNoEntry, "no entry for a.example", map[string]string{DetailHost: "a.example", DetailPath: path}},
		{"no entry for the URL's login", "machine a.example login b password topsecret", "https://zed@a.example/", 0o600, errs.NotFound, CodeAuthNetrcNoEntry, `login "zed"`, map[string]string{DetailHost: "a.example"}},
		{"syntax", "machine a.example login b\npassword", "https://a.example", 0o600, errs.Parse, CodeAuthNetrcFailed, "at line 2", map[string]string{DetailLine: "2"}},
		{"empty entry", "machine a.example\nmachine b login b password topsecret", "https://a.example", 0o600, errs.InvalidArgument, CodeAuthInvalid, "no login or password", nil},
		{"colon in login", "machine a.example login a:b password topsecret", "https://a.example", 0o600, errs.InvalidArgument, CodeAuthInvalid, "colon", nil},
		{"control character", `machine a.example login a password "top\nsecret"`, "https://a.example", 0o600, errs.InvalidArgument, CodeAuthInvalid, "control character", nil},
		{"no host", "default login a password topsecret", "https:///path", 0o600, errs.InvalidArgument, CodeAuthInvalid, "no host", map[string]string{DetailAuthField: "url"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			if tc.mode != 0 {
				files[path] = tc.text
			}
			mem := newMemNetrc(files)
			_, err := buildNetrc(t, mem.Env(), tc.url, projectfile.Auth{Type: projectfile.AuthNetrc}, HTTPOptions{})
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v", err)
			}
			if e.Kind != tc.kind || e.Code != tc.code || !strings.Contains(e.Message, tc.msg) {
				t.Errorf("err = %s %s %q, want %s %s containing %q", e.Kind, e.Code, e.Message, tc.kind, tc.code, tc.msg)
			}
			for k, v := range tc.details {
				if e.Details[k] != v {
					t.Errorf("detail %s = %q, want %q (all %v)", k, e.Details[k], v, e.Details)
				}
			}
			if e.Details[DetailAuthType] != projectfile.AuthNetrc {
				t.Errorf("type detail = %q", e.Details[DetailAuthType])
			}
			if strings.Contains(err.Error(), "topsecret") || strings.Contains(e.Hint, "topsecret") {
				t.Errorf("the error shows the password: %v / %s", err, e.Hint)
			}
		})
	}
}

func TestNetrcPermissionDenied(t *testing.T) {
	env := netrc.Env{
		Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return testHome, nil }, GOOS: "linux",
		ReadFile: func(string) ([]byte, fs.FileMode, error) { return nil, 0, fs.ErrPermission },
	}
	_, err := buildNetrc(t, env, "https://a.example", projectfile.Auth{Type: projectfile.AuthNetrc}, HTTPOptions{})
	if errs.KindOf(err) != errs.PermissionDenied || !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("err = %v (%s)", err, errs.KindOf(err))
	}
}

func TestNetrcInsecureFileWarns(t *testing.T) {
	path := filepath.Join(testHome, ".netrc")
	mem := newMemNetrc(map[string]string{path: sampleNetrc})
	mem.modes[path] = 0o644
	b, err := buildNetrc(t, mem.Env(), "https://api.example.com", projectfile.Auth{Type: projectfile.AuthNetrc}, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Warnings) != 1 || b.Warnings[0].Code != WarningNetrcInsecure || !strings.Contains(b.Warnings[0].Message, path) {
		t.Errorf("warnings = %+v", b.Warnings)
	}
	if got, _ := headerOf(b, "Authorization"); got != basicOf("alice", "s3cret pass") || !b.Netrc.Insecure {
		t.Errorf("Authorization = %q, match %+v", got, b.Netrc)
	}
}

func TestNetrcURLCredentialsWin(t *testing.T) {
	b, err := buildNetrc(t, failingNetrc(t), "https://u:p@api.example.com/", projectfile.Auth{Type: projectfile.AuthNetrc}, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := headerOf(b, "Authorization"); ok || b.Netrc == nil || !b.Netrc.URLCredentials {
		t.Errorf("URL credentials: headers %v, match %+v", b.Request.Headers, b.Netrc)
	}
}

func TestNetrcHeaderRowWins(t *testing.T) {
	mem := newMemNetrc(map[string]string{filepath.Join(testHome, ".netrc"): sampleNetrc})
	f := projectfile.HTTPRequest{Method: "GET", URL: "https://api.example.com", Headers: []projectfile.KeyValue{{Name: "Authorization", Value: "Bearer typed"}}}
	b, err := BuildHTTP(f, projectfile.Auth{Type: projectfile.AuthNetrc}, HTTPOptions{Netrc: NetrcOptions{Env: mem.Env()}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := headerOf(b, "Authorization"); got != "Bearer typed" {
		t.Errorf("Authorization = %q", got)
	}
	if len(b.Suppressed) != 1 || b.Suppressed[0].Reason != SuppressedOverridden {
		t.Errorf("suppressed = %+v", b.Suppressed)
	}
}

// Defer (the header preview) shows a placeholder and reads nothing.
func TestNetrcDeferReadsNothing(t *testing.T) {
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://api.example.com"}, projectfile.Auth{Type: projectfile.AuthNetrc},
		HTTPOptions{Netrc: NetrcOptions{Env: failingNetrc(t), Defer: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := headerOf(b, "Authorization"); got != NetrcPlaceholder || b.Netrc != nil {
		t.Errorf("Authorization = %q, match %+v", got, b.Netrc)
	}
}

// The netrc file is opened for the netrc type only: never for the other
// types, a disabled netrc auth, or an inherit resolved to another type.
func TestNetrcNotReadForOtherTypes(t *testing.T) {
	auths := []projectfile.Auth{
		{Type: projectfile.AuthNone},
		{Type: projectfile.AuthBasic, Username: "u", Password: "p"},
		{Type: projectfile.AuthBearer, Token: "t"},
		{Type: projectfile.AuthAPIKey, Key: "X-Key", Value: "v"},
		{Type: projectfile.AuthDigest, Username: "u", Password: "p"},
		{Type: projectfile.AuthJWT, JWTSecret: "s"},
		{Type: projectfile.AuthOAuth1, ConsumerKey: "k", ConsumerSecret: "s"},
		{Type: projectfile.AuthNetrc, Disabled: true, NetrcFile: "/some/netrc"},
	}
	for _, a := range auths {
		for _, f := range []projectfile.HTTPRequest{
			{Method: "GET", URL: "https://api.example.com/"},
			{Method: "GET", URL: "https://u@api.example.com/"},
		} {
			b, err := BuildHTTP(f, a, HTTPOptions{Netrc: NetrcOptions{Env: failingNetrc(t)}})
			if err != nil {
				t.Errorf("%s: %v", a.Type, err)
			}
			if b.Netrc != nil {
				t.Errorf("%s: netrc match %+v", a.Type, b.Netrc)
			}
		}
	}
	gql := projectfile.GraphQLRequest{URL: "https://api.example.com/graphql", Query: "{ a }"}
	if _, err := BuildGraphQL(gql, projectfile.Auth{Type: projectfile.AuthBearer, Token: "t"}, HTTPOptions{Netrc: NetrcOptions{Env: failingNetrc(t)}}); err != nil {
		t.Error(err)
	}
}

// GraphQL requests (over HTTP and WebSocket) look up their endpoint's host.
func TestNetrcGraphQL(t *testing.T) {
	mem := newMemNetrc(map[string]string{filepath.Join(testHome, ".netrc"): sampleNetrc})
	o := HTTPOptions{Netrc: NetrcOptions{Env: mem.Env()}}
	b, err := BuildGraphQL(projectfile.GraphQLRequest{URL: "https://other.example.com/graphql", Query: "{ a }"}, projectfile.Auth{Type: projectfile.AuthNetrc}, o)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := headerOf(b, "Authorization"); got != basicOf("bob", "b0b") {
		t.Errorf("Authorization = %q", got)
	}
}

func TestNetrcFileLocation(t *testing.T) {
	mem := newMemNetrc(nil)
	loc, err := NetrcFile("", "", mem.Env())
	if err != nil || loc.Path != filepath.Join(testHome, ".netrc") || loc.Source != netrc.SourceHome {
		t.Errorf("got %+v, %v", loc, err)
	}
	if mem.total() != 0 {
		t.Errorf("NetrcFile read %v", mem.reads)
	}
	_, err = NetrcFile("rel/netrc", "", mem.Env())
	if errs.KindOf(err) != errs.NotFound || !strings.Contains(err.Error(), "rel/netrc") {
		t.Errorf("relative without a project: %v", err)
	}
}
