package netrc

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, text string) *File {
	t.Helper()
	f, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []Entry
	}{
		{
			name: "one line",
			text: "machine api.example.com login alice password s3cret",
			want: []Entry{{Machine: "api.example.com", Login: "alice", Password: "s3cret", HasPassword: true, Line: 1}},
		},
		{
			name: "several lines and entries",
			text: "machine a.example\n  login a\n  password pa\n\nmachine b.example login b password pb account acc\n",
			want: []Entry{
				{Machine: "a.example", Login: "a", Password: "pa", HasPassword: true, Line: 1},
				{Machine: "b.example", Login: "b", Password: "pb", Account: "acc", HasPassword: true, Line: 5},
			},
		},
		{
			name: "default",
			text: "machine a login a password pa\ndefault login anonymous password me@example.com\n",
			want: []Entry{
				{Machine: "a", Login: "a", Password: "pa", HasPassword: true, Line: 1},
				{Default: true, Login: "anonymous", Password: "me@example.com", HasPassword: true, Line: 2},
			},
		},
		{
			name: "comments",
			text: "# my servers\n   # indented comment machine evil login x\nmachine a login a password p#ss # not a comment\n#machine b login b\n",
			want: []Entry{{Machine: "a", Login: "a", Password: "p#ss", HasPassword: true, Line: 3}},
		},
		{
			name: "quoted tokens",
			text: `machine a login "alice smith" password "pa ss\"w\\o\trd"` + "\n" + `machine b login "" password "#hash"`,
			want: []Entry{
				{Machine: "a", Login: "alice smith", Password: "pa ss\"w\\o\trd", HasPassword: true, Line: 1},
				{Machine: "b", Login: "", Password: "#hash", HasPassword: true, Line: 2},
			},
		},
		{
			name: "quoted keyword is a value, not a keyword",
			text: `machine a login "password" password "machine"`,
			want: []Entry{{Machine: "a", Login: "password", Password: "machine", HasPassword: true, Line: 1}},
		},
		{
			name: "macdef skipped up to the blank line",
			text: "machine ftp.example login f password pf\nmacdef init\ncd /pub\nmachine evil login e password pe\nbinary\n\nmachine b login b password pb\n",
			want: []Entry{
				{Machine: "ftp.example", Login: "f", Password: "pf", HasPassword: true, Line: 1},
				{Machine: "b", Login: "b", Password: "pb", HasPassword: true, Line: 7},
			},
		},
		{
			name: "macdef ended by a line of blanks",
			text: "machine a login a\nmacdef m\nls\n  \t\npassword pa\n",
			want: []Entry{{Machine: "a", Login: "a", Password: "pa", HasPassword: true, Line: 1}},
		},
		{
			name: "macdef to the end of the file",
			text: "machine a login a password pa\nmacdef m\nmachine b login b password pb",
			want: []Entry{{Machine: "a", Login: "a", Password: "pa", HasPassword: true, Line: 1}},
		},
		{
			name: "crlf line ends",
			text: "machine a\r\nlogin a\r\npassword pa\r\n",
			want: []Entry{{Machine: "a", Login: "a", Password: "pa", HasPassword: true, Line: 1}},
		},
		{
			name: "unknown words skipped",
			text: "machine a port 21 login a password pa",
			want: []Entry{{Machine: "a", Login: "a", Password: "pa", HasPassword: true, Line: 1}},
		},
		{
			name: "empty password and none",
			text: "machine a login a password \"\"\nmachine b login b",
			want: []Entry{
				{Machine: "a", Login: "a", HasPassword: true, Line: 1},
				{Machine: "b", Login: "b", Line: 2},
			},
		},
		{name: "empty", text: "", want: nil},
		{name: "only comments", text: "# nothing\n\n# here\n", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := mustParse(t, tc.text)
			if !reflect.DeepEqual(f.Entries, tc.want) {
				t.Errorf("entries\n got %+v\nwant %+v", f.Entries, tc.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, text string
		line       int
		msg        string
	}{
		{"keyword without value", "machine a login a\npassword", 2, `"password" has no value`},
		{"machine without name", "machine", 1, `"machine" has no value`},
		{"unterminated quote", "machine a\nlogin \"alice\npassword x", 2, "not closed on its line"},
		{"unterminated quote at the end", `machine a login "alice`, 1, "not closed at the end"},
		{"login before any entry", "login a password b\nmachine x", 1, "before any machine"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.text))
			var se *SyntaxError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want a *SyntaxError", err)
			}
			if se.Line != tc.line || !strings.Contains(se.Msg, tc.msg) {
				t.Errorf("err = line %d %q, want line %d containing %q", se.Line, se.Msg, tc.line, tc.msg)
			}
		})
	}
	// The message never holds a value from the file.
	_, err := Parse([]byte(`machine a login "topsecret`))
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Errorf("err = %v, must not show the value", err)
	}
}

func TestLookup(t *testing.T) {
	f := mustParse(t, `
machine API.example.com login first password p1
machine api.example.com login second password p2
machine other.example login o password po
default login anon password pd
`)
	cases := []struct {
		host, login string
		want        string
		ok          bool
	}{
		{"api.example.com", "", "first", true},
		{"Api.Example.Com", "", "first", true},
		{"api.example.com", "second", "second", true},
		{"other.example", "", "o", true},
		{"unknown.example", "", "anon", true},
		{"api.example.com", "nobody", "", false},
		{"unknown.example", "anon", "anon", true},
	}
	for _, tc := range cases {
		e, ok := f.Lookup(tc.host, tc.login)
		if ok != tc.ok || e.Login != tc.want {
			t.Errorf("Lookup(%q, %q) = %q %v, want %q %v", tc.host, tc.login, e.Login, ok, tc.want, tc.ok)
		}
	}
	// Machine entries come before default, wherever it is.
	f = mustParse(t, "default login d password pd\nmachine a login a password pa\n")
	if e, _ := f.Lookup("a", ""); e.Login != "a" || e.Name() != "a" {
		t.Errorf("machine after default: got %+v", e)
	}
	if e, _ := f.Lookup("b", ""); !e.Default || e.Name() != "default" {
		t.Errorf("default: got %+v", e)
	}
	// No default: nothing.
	f = mustParse(t, "machine a login a password pa\n")
	if _, ok := f.Lookup("b", ""); ok {
		t.Error("Lookup found an entry for a host without one")
	}
}

// fakeEnv never touches the process environment or the user's home.
func fakeEnv(goos string, env map[string]string, home string) Env {
	return Env{
		Getenv:  func(k string) string { return env[k] },
		HomeDir: func() (string, error) { return home, nil },
		GOOS:    goos,
		ReadFile: func(string) ([]byte, fs.FileMode, error) {
			panic("Locate must not read")
		},
	}
}

func TestLocate(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "u")
	root := filepath.Join(string(filepath.Separator), "proj")
	cases := []struct {
		name     string
		goos     string
		env      map[string]string
		override string
		want     Location
	}{
		{"home on unix", "linux", nil, "", Location{Path: filepath.Join(home, ".netrc"), Source: SourceHome}},
		{"home on windows", "windows", nil, "", Location{Path: filepath.Join(home, "_netrc"), Fallback: filepath.Join(home, ".netrc"), Source: SourceHome}},
		{"HOME on windows", "windows", map[string]string{"HOME": "/h"}, "", Location{Path: filepath.Join("/h", "_netrc"), Fallback: filepath.Join("/h", ".netrc"), Source: SourceHome}},
		{"NETRC", "darwin", map[string]string{"NETRC": "/etc/my-netrc"}, "", Location{Path: "/etc/my-netrc", Source: SourceEnv}},
		{"override wins over NETRC", "linux", map[string]string{"NETRC": "/etc/my-netrc"}, filepath.Join(root, "n"), Location{Path: filepath.Join(root, "n"), Source: SourceRequest}},
		{"relative override", "linux", nil, "auth/netrc", Location{Path: filepath.Join(root, "auth", "netrc"), Source: SourceRequest}},
		{"home override", "linux", nil, "~/.netrc-work", Location{Path: filepath.Join(home, ".netrc-work"), Source: SourceRequest}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fakeEnv(tc.goos, tc.env, home).Locate(tc.override, root)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	if _, err := fakeEnv("linux", nil, home).Locate("rel", ""); err == nil {
		t.Error("a relative path without a project was accepted")
	}
	e := fakeEnv("linux", nil, "")
	if _, err := e.Locate("", root); !errors.Is(err, ErrNoHome) {
		t.Errorf("no home: err = %v", err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netrc")
	if err := os.WriteFile(path, []byte("machine a login a password pa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := Env{GOOS: "linux"}
	l, err := e.Load(Location{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if l.Path != path || l.Insecure || len(l.File.Entries) != 1 {
		t.Errorf("got %+v", l)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if l, err = e.Load(Location{Path: path}); err != nil || !l.Insecure {
		t.Errorf("0644: insecure = %v, err %v", l.Insecure, err)
	}
	if l, err = (Env{GOOS: "windows"}).Load(Location{Path: path}); err != nil || l.Insecure {
		t.Errorf("windows: insecure = %v, err %v", l.Insecure, err)
	}

	missing := filepath.Join(dir, "missing")
	_, err = e.Load(Location{Path: missing})
	var pe *PathError
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pe) || pe.Path != missing {
		t.Errorf("missing: err = %v", err)
	}

	// The Windows fallback: _netrc missing, .netrc read.
	if l, err = e.Load(Location{Path: missing, Fallback: path}); err != nil || l.Path != path {
		t.Errorf("fallback: %+v, %v", l, err)
	}
	// Both missing: the error names the preferred file.
	_, err = e.Load(Location{Path: missing, Fallback: filepath.Join(dir, "missing2")})
	if !errors.As(err, &pe) || pe.Path != missing {
		t.Errorf("both missing: err = %v", err)
	}

	if _, err = e.Load(Location{Path: dir}); !errors.Is(err, ErrNotFile) {
		t.Errorf("directory: err = %v", err)
	}

	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("machine"), 0o600); err != nil {
		t.Fatal(err)
	}
	var se *SyntaxError
	if _, err = e.Load(Location{Path: bad}); !errors.As(err, &se) || !errors.As(err, &pe) {
		t.Errorf("syntax: err = %v", err)
	}

	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, make([]byte, MaxSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Load(Location{Path: big}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: err = %v", err)
	}
}
