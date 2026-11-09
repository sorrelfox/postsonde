package fileaccess

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// fixture lays out, under one temp dir:
//
//	project/        the project root (implicitly allowed)
//	  data.txt
//	allowed/        an allow-listed directory
//	  in.txt
//	  sub/deep.txt
//	outside/        not allowed
//	  secret.txt
type fixture struct {
	base, project, allowed, outside string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	f := fixture{
		base:    base,
		project: filepath.Join(base, "project"),
		allowed: filepath.Join(base, "allowed"),
		outside: filepath.Join(base, "outside"),
	}
	write(t, filepath.Join(f.project, "data.txt"), "project data")
	write(t, filepath.Join(f.allowed, "in.txt"), "allowed data")
	write(t, filepath.Join(f.allowed, "sub", "deep.txt"), "deep data")
	write(t, filepath.Join(f.outside, "secret.txt"), "secret data")
	return f
}

func (f fixture) policy() Policy {
	return Policy{Allowed: []string{f.allowed}, ProjectRoot: f.project, HomeDir: func() (string, error) { return f.allowed, nil }}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links need privileges on Windows:", err)
		}
		t.Fatal(err)
	}
}

func code(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestReadFileAllowed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := f.policy()
	tests := []struct {
		path, want string
	}{
		{filepath.Join(f.allowed, "in.txt"), "allowed data"},
		{filepath.Join(f.allowed, "sub", "deep.txt"), "deep data"},
		{"data.txt", "project data"},                                      // relative to the project
		{"./data.txt", "project data"},                                    // same
		{filepath.Join(f.project, "data.txt"), "project data"},            // the project is allowed
		{"~/in.txt", "allowed data"},                                      // the home folder
		{filepath.Join(f.allowed, "sub", "..", "in.txt"), "allowed data"}, // ".." that stays inside
		{"../allowed/in.txt", "allowed data"},                             // ".." into another allowed dir
	}
	for _, tt := range tests {
		got, err := p.ReadFile(tt.path)
		if err != nil || string(got) != tt.want {
			t.Errorf("ReadFile(%q) = %q, %v; want %q", tt.path, got, err, tt.want)
		}
	}
}

func TestReadFileRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := f.policy()
	tests := []struct {
		name, path, code string
	}{
		{"outside", filepath.Join(f.outside, "secret.txt"), CodeNotAllowed},
		{"dot-dot out of an allowed dir", f.allowed + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside" + string(filepath.Separator) + "secret.txt", CodeNotAllowed},
		{"dot-dot out of the project", "../outside/secret.txt", CodeNotAllowed},
		{"missing inside", filepath.Join(f.allowed, "nope.txt"), CodeNotFound},
		{"missing outside says not allowed", filepath.Join(f.outside, "nope.txt"), CodeNotAllowed},
		{"a folder", f.allowed, CodeUnreadable},
		{"empty", "  ", CodeInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.ReadFile(tt.path)
			if err == nil || code(err) != tt.code {
				t.Fatalf("ReadFile(%q) = %q, %v; want code %s", tt.path, got, err, tt.code)
			}
			if tt.code == CodeNotAllowed {
				if !errors.Is(err, &errs.Error{Kind: errs.PermissionDenied}) {
					t.Errorf("kind of %v, want permission_denied", err)
				}
				if !strings.Contains(err.Error(), "outside the directories allowed") {
					t.Errorf("message %q", err.Error())
				}
			}
		})
	}
}

func TestSymlinks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// A link inside the allowed dir that points out of it.
	symlink(t, filepath.Join(f.outside, "secret.txt"), filepath.Join(f.allowed, "escape.txt"))
	// A link to a folder outside, used as a path component.
	symlink(t, f.outside, filepath.Join(f.allowed, "outdir"))
	// A link outside that points into the allowed dir.
	symlink(t, filepath.Join(f.allowed, "in.txt"), filepath.Join(f.outside, "pointer.txt"))
	// A link that goes out and ".." that would come back lexically:
	// allowed/outdir/../in.txt is outside/../in.txt on disk.
	write(t, filepath.Join(f.base, "in.txt"), "base data")
	// A broken link inside.
	symlink(t, filepath.Join(f.outside, "gone.txt"), filepath.Join(f.allowed, "broken.txt"))
	p := f.policy()
	const sep = string(filepath.Separator)

	for _, tt := range []struct{ name, path, code string }{
		{"file link escaping", filepath.Join(f.allowed, "escape.txt"), CodeNotAllowed},
		{"folder link escaping", filepath.Join(f.allowed, "outdir", "secret.txt"), CodeNotAllowed},
		{"dot-dot after a link is physical", f.allowed + sep + "outdir" + sep + ".." + sep + "in.txt", CodeNotAllowed},
		{"missing under a link out", f.allowed + sep + "outdir" + sep + "nope.txt", CodeNotAllowed},
		{"missing then dot-dot out", f.allowed + sep + "nope" + sep + ".." + sep + ".." + sep + "outside" + sep + "x", CodeNotAllowed},
		{"broken link out", filepath.Join(f.allowed, "broken.txt"), CodeNotFound},
	} {
		if got, err := p.ReadFile(tt.path); code(err) != tt.code {
			t.Errorf("%s: ReadFile(%q) = %q, %v; want code %s", tt.name, tt.path, got, err, tt.code)
		}
	}
	// A link from outside into the allowed dir reads the real file, which
	// is allowed.
	got, err := p.ReadFile(filepath.Join(f.outside, "pointer.txt"))
	if err != nil || string(got) != "allowed data" {
		t.Errorf("link pointing in = %q, %v", got, err)
	}
}

func TestAllowedDirThroughLink(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// The allowlist names a link to the real directory: files are allowed
	// through either path.
	link := filepath.Join(f.base, "allowed-link")
	symlink(t, f.allowed, link)
	p := Policy{Allowed: []string{link}}
	for _, path := range []string{filepath.Join(link, "in.txt"), filepath.Join(f.allowed, "in.txt")} {
		if got, err := p.ReadFile(path); err != nil || string(got) != "allowed data" {
			t.Errorf("ReadFile(%q) = %q, %v", path, got, err)
		}
	}
}

func TestSizeLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	write(t, filepath.Join(f.allowed, "big.txt"), strings.Repeat("x", 101))
	write(t, filepath.Join(f.allowed, "fits.txt"), strings.Repeat("x", 100))
	p := f.policy()
	p.MaxSize = 100
	if _, err := p.ReadFile(filepath.Join(f.allowed, "fits.txt")); err != nil {
		t.Errorf("100 bytes: %v", err)
	}
	_, err := p.ReadFile(filepath.Join(f.allowed, "big.txt"))
	if code(err) != CodeTooLarge || !strings.Contains(err.Error(), "larger than 100 bytes") {
		t.Errorf("101 bytes: %v", err)
	}
	if got := formatSize(DefaultMaxSize); got != "10 MiB" {
		t.Errorf("formatSize = %q", got)
	}
}

func TestNoProjectAndNoAllowlist(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := (Policy{}).ReadFile("data.txt"); code(err) != CodeInvalidPath {
		t.Errorf("relative without a project: %v", err)
	}
	if _, err := (Policy{}).ReadFile(filepath.Join(f.allowed, "in.txt")); code(err) != CodeNotAllowed {
		t.Errorf("zero policy: %v", err)
	}
	// A missing allowed directory allows nothing.
	p := Policy{Allowed: []string{filepath.Join(f.base, "missing"), "relative/dir"}}
	if _, err := p.ReadFile(filepath.Join(f.allowed, "in.txt")); code(err) != CodeNotAllowed {
		t.Errorf("missing allowed dir: %v", err)
	}
}

func TestAbs(t *testing.T) {
	t.Parallel()
	home := filepath.FromSlash("/home/u")
	root := filepath.FromSlash("/p")
	if runtime.GOOS == "windows" {
		home, root = `C:\home\u`, `C:\p`
	}
	p := Policy{ProjectRoot: root, HomeDir: func() (string, error) { return home, nil }}
	for in, want := range map[string]string{
		"~":         home,
		"~/a/b.txt": filepath.Join(home, "a", "b.txt"),
		"a/b.txt":   filepath.Join(root, "a", "b.txt"),
		"~user/x":   filepath.Join(root, "~user", "x"),
	} {
		got, err := p.Abs(in)
		if err != nil || filepath.Clean(got) != want {
			t.Errorf("Abs(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	p.HomeDir = func() (string, error) { return "", errors.New("no home") }
	if _, err := p.Abs("~/x"); code(err) != CodeInvalidPath {
		t.Errorf("no home: %v", err)
	}
}

func TestStat(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	symlink(t, f.outside, filepath.Join(f.allowed, "outdir"))
	p := f.policy()
	for _, tt := range []struct {
		name, path, code string
		dir              bool
	}{
		{"allowed file", filepath.Join(f.allowed, "in.txt"), "", false},
		{"allowed folder", filepath.Join(f.allowed, "sub"), "", true},
		{"the project folder", f.project, "", true},
		{"relative", "data.txt", "", false},
		{"outside", filepath.Join(f.outside, "secret.txt"), CodeNotAllowed, false},
		{"outside folder", f.outside, CodeNotAllowed, false},
		{"folder link out", filepath.Join(f.allowed, "outdir"), CodeNotAllowed, false},
		{"missing inside", filepath.Join(f.allowed, "nope"), CodeNotFound, false},
	} {
		info, err := p.Stat(tt.path)
		if code(err) != tt.code {
			t.Errorf("%s: Stat(%q) = %v; want code %q", tt.name, tt.path, err, tt.code)
			continue
		}
		if err == nil && info.IsDir() != tt.dir {
			t.Errorf("%s: IsDir = %v", tt.name, info.IsDir())
		}
	}
}

func TestOpenAndSubject(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := f.policy()
	file, info, err := p.Open("data.txt")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if info.Size() != int64(len("project data")) {
		t.Errorf("size = %d", info.Size())
	}
	p.Subject = "The JWT key file"
	_, _, err = p.Open(filepath.Join(f.outside, "secret.txt"))
	if !IsNotAllowed(err) {
		t.Fatalf("err = %v", err)
	}
	var e *errs.Error
	errors.As(err, &e)
	if !strings.HasPrefix(e.Message, `The JWT key file "`) || e.Hint != HintAllow {
		t.Errorf("message %q, hint %q", e.Message, e.Hint)
	}
	if _, _, err := p.Open(f.allowed); code(err) != CodeUnreadable {
		t.Errorf("folder: %v", err)
	}
	if IsNotAllowed(errors.New("x")) || IsNotAllowed(nil) {
		t.Error("IsNotAllowed of other errors")
	}
}
