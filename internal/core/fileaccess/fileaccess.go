// Package fileaccess limits the local files that features driven by
// project data may read to allow-listed directories (PRV-10). A project
// can come from anyone, so a path written in it ({{$file ~/.ssh/id_rsa}},
// a JWT key file, a .proto file) must not reach a file the user did not
// agree to share. The allowlist is an app setting (privacy.allowedDirs),
// never a project file, and the open project's own folder is always
// allowed.
//
// A path is resolved before it is checked: "~" is the home folder, a
// relative path is relative to the project root, and symbolic links and
// ".." are followed on disk (filepath.EvalSymlinks), so neither can lead
// out of an allowed directory. The file is then opened through os.Root,
// which refuses to leave the directory even if a link changes between the
// check and the open. See docs/privacy.md#local-file-access-prv-10.
package fileaccess

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// DefaultMaxSize is the largest file Policy.ReadFile reads when
// Policy.MaxSize is 0: 10 MiB.
const DefaultMaxSize = 10 << 20

// Error codes of ReadFile.
const (
	// CodeNotAllowed: the file is outside every allowed directory (kind
	// permission_denied). Details: path.
	CodeNotAllowed = "fileaccess.not_allowed"
	// CodeNotFound: the file does not exist (kind not_found). Details:
	// path.
	CodeNotFound = "fileaccess.not_found"
	// CodeTooLarge: the file is larger than the limit (kind
	// invalid_argument). Details: path, size, limit.
	CodeTooLarge = "fileaccess.too_large"
	// CodeUnreadable: the path is a folder or a device, or reading failed
	// (kind invalid_argument, permission_denied or io). Details: path.
	CodeUnreadable = "fileaccess.unreadable"
	// CodeInvalidPath: the path is empty, or relative without a project
	// (kind invalid_argument). Details: path.
	CodeInvalidPath = "fileaccess.invalid_path"
)

// HintAllow says how to allow a directory.
const HintAllow = "Add its folder under Settings, Privacy, \"Allowed directories for file access\", or move the file into the project."

// Policy says which files may be read. The zero Policy allows nothing.
type Policy struct {
	// Allowed are the allow-listed directories (absolute paths). A
	// directory that does not exist allows nothing.
	Allowed []string
	// ProjectRoot is the open project's folder: always allowed, and the
	// base of relative paths. "" when no project is open; relative paths
	// are then refused.
	ProjectRoot string
	// HomeDir returns the home folder for "~"; nil is os.UserHomeDir.
	HomeDir func() (string, error)
	// MaxSize is the largest file read, in bytes; 0 is DefaultMaxSize.
	MaxSize int64
	// Subject names the file in messages, capitalised: "The JWT key
	// file" gives "The JWT key file "k.pem" is outside the directories
	// allowed for file access."; "" is "The file".
	Subject string
}

func (p Policy) subject() string {
	if p.Subject == "" {
		return "The file"
	}
	return p.Subject
}

// IsNotAllowed reports whether err is a refusal of a Policy: a file
// outside every allowed directory (CodeNotAllowed).
func IsNotAllowed(err error) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Code == CodeNotAllowed
}

// Abs returns path as an absolute path, not yet checked: "~" and "~/…"
// are in the home folder, a relative path is relative to ProjectRoot.
// ".." and links are left for Resolve to follow on disk.
func (p Policy) Abs(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		hint := "Write a path after $file."
		if p.Subject != "" {
			hint = "Choose the file again."
		}
		return "", invalidPath(path, "The file path is empty.", hint)
	}
	if rest, ok := homeRelative(path); ok {
		home, err := p.home()
		if err != nil || home == "" {
			return "", invalidPath(path, "The home folder is not known, so "+strconv.Quote(path)+" cannot be read.", "Use an absolute path.")
		}
		if rest == "" {
			return home, nil
		}
		return strings.TrimRight(home, `/\`) + string(filepath.Separator) + rest, nil
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	if p.ProjectRoot == "" {
		return "", invalidPath(path, "The relative path "+strconv.Quote(path)+" needs a project to be open.",
			"Open the project the request belongs to, or use an absolute path.")
	}
	return strings.TrimRight(p.ProjectRoot, `/\`) + string(filepath.Separator) + filepath.FromSlash(path), nil
}

// homeRelative splits "~" or "~/rest" ("~\rest" on Windows).
func homeRelative(path string) (rest string, ok bool) {
	if path == "~" {
		return "", true
	}
	if len(path) >= 2 && path[0] == '~' && (path[1] == '/' || (runtime.GOOS == "windows" && path[1] == '\\')) {
		return filepath.FromSlash(path[2:]), true
	}
	return "", false
}

func (p Policy) home() (string, error) {
	if p.HomeDir != nil {
		return p.HomeDir()
	}
	return os.UserHomeDir()
}

// Resolved is a file path checked against a Policy.
type Resolved struct {
	// Path is the file's real path: absolute, with every link and ".."
	// followed.
	Path string
	// Root is the real path of the allowed directory it is in, and Rel
	// its path inside Root.
	Root, Rel string
}

// Resolve checks that path names a file inside an allowed directory, once
// links and ".." are followed. A missing file inside an allowed directory
// is CodeNotFound; outside, CodeNotAllowed, so the answer says nothing
// about files the policy does not allow.
func (p Policy) Resolve(path string) (Resolved, error) {
	abs, err := p.Abs(path)
	if err != nil {
		return Resolved{}, err
	}
	roots := p.roots()
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Not on disk (or a link is broken): answer "not found" only for
		// a path that would be allowed as written.
		if _, _, inside := within(roots, resolveMissing(abs)); !inside {
			return Resolved{}, p.notAllowed(path)
		}
		if errors.Is(err, fs.ErrNotExist) {
			return Resolved{}, errs.Wrap(err, errs.NotFound, p.subject()+" "+strconv.Quote(path)+" was not found.").
				WithCode(CodeNotFound).
				WithHint("Check the path: a relative path is relative to the project folder.").
				WithDetail("path", path)
		}
		return Resolved{}, p.unreadable(err, path)
	}
	root, rel, inside := within(roots, real)
	if !inside {
		return Resolved{}, p.notAllowed(path)
	}
	return Resolved{Path: real, Root: root, Rel: rel}, nil
}

// resolveMissing is abs with its longest existing leading part resolved
// on disk (links and ".." followed) and the rest joined to it: the path
// abs would have if the missing part were created.
func resolveMissing(abs string) string {
	vol := filepath.VolumeName(abs)
	parts := strings.FieldsFunc(abs[len(vol):], func(r rune) bool { return r == '/' || r == filepath.Separator })
	for n := len(parts) - 1; n >= 0; n-- {
		prefix := vol + string(filepath.Separator) + strings.Join(parts[:n], string(filepath.Separator))
		real, err := filepath.EvalSymlinks(prefix)
		if err != nil {
			continue
		}
		// The rest starts with a missing name, which cannot be a link:
		// cleaning it lexically is what the file system would do.
		return filepath.Join(append([]string{real}, parts[n:]...)...)
	}
	return filepath.Clean(abs)
}

// roots are the real paths of the allowed directories and the project.
func (p Policy) roots() []string {
	dirs := make([]string, 0, len(p.Allowed)+1)
	if p.ProjectRoot != "" {
		dirs = append(dirs, p.ProjectRoot)
	}
	dirs = append(dirs, p.Allowed...)
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue
		}
		real, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue // a missing directory allows nothing
		}
		if info, err := os.Stat(real); err != nil || !info.IsDir() {
			continue
		}
		out = append(out, real)
	}
	return out
}

// within finds the root that contains path (both cleaned and absolute) and
// returns path relative to it. Case is ignored where the file systems
// usually ignore it (macOS, Windows): os.Root still keeps the open inside
// the root.
func within(roots []string, path string) (root, rel string, ok bool) {
	for _, r := range roots {
		prefix := r
		if !strings.HasSuffix(prefix, string(filepath.Separator)) {
			prefix += string(filepath.Separator)
		}
		switch {
		case samePath(path, r):
			return r, ".", true
		case len(path) > len(prefix) && samePath(path[:len(prefix)], prefix):
			return r, path[len(prefix):], true
		}
	}
	return "", "", false
}

func samePath(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Open opens the regular file at path if the policy allows it, through
// os.Root on its allowed directory, and returns it with its file info;
// the caller closes it. Errors are those of ReadFile, without the size
// check.
func (p Policy) Open(path string) (*os.File, fs.FileInfo, error) {
	r, err := p.Resolve(path)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.OpenInRoot(r.Root, r.Rel)
	if err != nil {
		return nil, nil, p.openError(err, path)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, p.unreadable(err, path)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, errs.New(errs.InvalidArgument, p.subject()+" "+strconv.Quote(path)+" is not a regular file.").
			WithCode(CodeUnreadable).
			WithHint("Name a file, not a folder or a device.").
			WithDetail("path", path)
	}
	return f, info, nil
}

// Stat returns the file info of path, a file or a folder, if the policy
// allows it, following links through os.Root on its allowed directory.
// Errors are those of Resolve, and CodeUnreadable.
func (p Policy) Stat(path string) (fs.FileInfo, error) {
	r, err := p.Resolve(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(r.Root)
	if err != nil {
		return nil, p.openError(err, path)
	}
	defer root.Close()
	info, err := root.Stat(r.Rel)
	if err != nil {
		return nil, p.openError(err, path)
	}
	return info, nil
}

// openError maps an error of opening a resolved path in its root.
func (p Policy) openError(err error, path string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errs.Wrap(err, errs.NotFound, p.subject()+" "+strconv.Quote(path)+" was not found.").
			WithCode(CodeNotFound).WithDetail("path", path)
	}
	if strings.Contains(err.Error(), "escapes") {
		return p.notAllowed(path) // a link changed after the check
	}
	return p.unreadable(err, path)
}

// ReadFile reads the regular file at path if the policy allows it and it
// is at most MaxSize bytes. Errors are *errs.Error with the codes above;
// their messages name the path as written.
func (p Policy) ReadFile(path string) ([]byte, error) {
	f, info, err := p.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	limit := p.MaxSize
	if limit <= 0 {
		limit = DefaultMaxSize
	}
	if info.Size() > limit {
		return nil, p.tooLarge(path, info.Size(), limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, p.unreadable(err, path)
	}
	if int64(len(data)) > limit {
		return nil, p.tooLarge(path, int64(len(data)), limit)
	}
	return data, nil
}

func (p Policy) notAllowed(path string) *errs.Error {
	return errs.New(errs.PermissionDenied, p.subject()+" "+strconv.Quote(path)+" is outside the directories allowed for file access.").
		WithCode(CodeNotAllowed).
		WithHint(HintAllow).
		WithDetail("path", path)
}

func invalidPath(path, msg, hint string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidPath).WithHint(hint).WithDetail("path", path)
}

func (p Policy) unreadable(err error, path string) *errs.Error {
	kind := errs.IO
	if errors.Is(err, fs.ErrPermission) {
		kind = errs.PermissionDenied
	}
	return errs.Wrap(err, kind, p.subject()+" "+strconv.Quote(path)+" could not be read.").
		WithCode(CodeUnreadable).
		WithHint("Check that the file can be read.").
		WithDetail("path", path)
}

func (p Policy) tooLarge(path string, size, limit int64) *errs.Error {
	hint := "Send a large file as a file body instead."
	if p.Subject != "" {
		hint = "Choose a smaller file."
	}
	return errs.New(errs.InvalidArgument, p.subject()+" "+strconv.Quote(path)+" is larger than "+formatSize(limit)+".").
		WithCode(CodeTooLarge).
		WithHint(hint).
		WithDetail("path", path).
		WithDetail("size", strconv.FormatInt(size, 10)).
		WithDetail("limit", strconv.FormatInt(limit, 10))
}

// formatSize is "10 MiB", "512 KiB" or "100 bytes".
func formatSize(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}
