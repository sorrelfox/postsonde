package request

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Codes of the errors about the files of file bodies and multipart parts.
const (
	// CodeBodyFileMissing: the file does not exist (kind not_found).
	CodeBodyFileMissing = "request.body_file_missing"
	// CodeBodyFileUnreadable: the file is a directory or another
	// non-regular file, or cannot be read (permission, I/O).
	CodeBodyFileUnreadable = "request.body_file_unreadable"
	// CodeBodyFileUnset: a file body has no file chosen (kind
	// invalid_argument).
	CodeBodyFileUnset = "request.body_file_unset"
	// CodeBodyFileChanged: the file's size changed while it was being sent
	// (kind io).
	CodeBodyFileChanged = "request.body_file_changed"
)

// DetailPath is the detail holding the file's path as stored (relative to
// the project root, or absolute), on the errors about body files.
const DetailPath = "path"

// StoredFilePath returns how a file chosen at abs (an absolute path) is
// stored in a project file whose project is at root: relative to root with
// "/" separators when the file is inside the project, so the project can
// be moved or shared with its fixtures, else abs unchanged. An empty root
// (no project) keeps abs.
func StoredFilePath(root, abs string) string {
	if root == "" || !filepath.IsAbs(abs) {
		return abs
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(abs))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return abs
	}
	return filepath.ToSlash(rel)
}

// ResolveFilePath returns the path on disk of a file stored as stored: an
// absolute path is kept (cleaned), a relative one ("/" separators) is
// joined to the project root. A relative path without a root is returned
// relative; the body mapping reports it as a missing file rather than
// reading it from the working directory.
func ResolveFilePath(root, stored string) string {
	p := filepath.FromSlash(stored)
	if filepath.IsAbs(p) || root == "" {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// bodyFile resolves and stats the file stored as stored, returning its
// path on disk and size. what names the file in messages ("The file of
// multipart part 2 (\"avatar\")").
func bodyFile(root, stored, what string) (abs string, size int64, err error) {
	abs = ResolveFilePath(root, stored)
	if !filepath.IsAbs(abs) {
		return "", 0, bodyFileError(os.ErrNotExist, stored, what).
			WithHint("Open the project the request belongs to, or choose the file again.")
	}
	size, err = statBodyFile(abs, stored, what)
	return abs, size, err
}

// statBodyFile returns the size of the regular file at abs. what names the
// file in messages ("The file of part “avatar”"); stored is the path as
// stored, for the DetailPath detail.
func statBodyFile(abs, stored, what string) (int64, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return 0, bodyFileError(err, stored, what)
	}
	if !info.Mode().IsRegular() {
		return 0, errs.New(errs.InvalidArgument, what+" is not a regular file.").
			WithCode(CodeBodyFileUnreadable).
			WithHint("Choose a file, not a folder or a device.").
			WithDetail(DetailPath, stored)
	}
	return info.Size(), nil
}

// bodyFileError maps an error opening or reading a body file.
func bodyFileError(err error, stored, what string) *errs.Error {
	if os.IsNotExist(err) {
		return errs.Wrap(err, errs.NotFound, what+" was not found: "+stored+".").
			WithCode(CodeBodyFileMissing).
			WithHint("Choose the file again, or restore it at that path.").
			WithDetail(DetailPath, stored)
	}
	kind := errs.IO
	if os.IsPermission(err) {
		kind = errs.PermissionDenied
	}
	return errs.Wrap(err, kind, what+" could not be read: "+stored+".").
		WithCode(CodeBodyFileUnreadable).
		WithHint("Check that the file can be read, or choose another one.").
		WithDetail(DetailPath, stored)
}

// changedFileError is returned when a file sent with a precomputed length
// turns out shorter or longer while it is sent.
func changedFileError(stored, what string) *errs.Error {
	return errs.New(errs.IO, what+" changed while it was being sent: "+stored+".").
		WithCode(CodeBodyFileChanged).
		WithHint("Send the request again once the file is no longer being written.").
		WithDetail(DetailPath, stored)
}
