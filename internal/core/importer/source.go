package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes of reading the file to import.
const (
	// CodeReadFailed: the file could not be read (the kind comes from the
	// cause: not_found, permission_denied, io). Details: path.
	CodeReadFailed = "import.read_failed"
	// CodeTooLarge: the file is larger than MaxFileSize (kind
	// invalid_argument). Details: path, limit.
	CodeTooLarge = "import.too_large"
	// CodeSourceChanged: the file changed between its preview and the
	// import (kind invalid_argument). Details: path.
	CodeSourceChanged = "import.source_changed"
)

// MaxFileSize is the largest file ReadFile reads, in bytes.
const MaxFileSize = 64 << 20

// ReadFile reads the file at path as a Source named by its base name, and
// returns the SHA-256 of its content (hex) so a later import can check the
// file is still the one that was previewed. Only regular files are read.
func ReadFile(path string) (Source, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return Source{}, "", readFailed(err, path)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Source{}, "", readFailed(err, path)
	}
	if !info.Mode().IsRegular() {
		return Source{}, "", errs.New(errs.InvalidArgument, "Only a file can be imported.").
			WithCode(CodeReadFailed).
			WithHint("Choose a file, not a folder or a device.").
			WithDetail("path", path)
	}
	if info.Size() > MaxFileSize {
		return Source{}, "", tooLarge(path)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return Source{}, "", readFailed(err, path)
	}
	if len(data) > MaxFileSize {
		return Source{}, "", tooLarge(path)
	}
	sum := sha256.Sum256(data)
	return Source{Name: filepath.Base(path), Data: data}, hex.EncodeToString(sum[:]), nil
}

// SourceChanged is the error of an import whose file no longer has the
// digest of its preview.
func SourceChanged(path string) *errs.Error {
	return errs.New(errs.InvalidArgument, "The file changed after it was previewed.").
		WithCode(CodeSourceChanged).
		WithHint("Look at the new preview, then import again.").
		WithDetail("path", path)
}

func readFailed(err error, path string) *errs.Error {
	kind := errs.KindOf(err)
	msg := "The file could not be read."
	switch {
	case errors.Is(err, fs.ErrNotExist):
		kind, msg = errs.NotFound, "The file no longer exists."
	case errors.Is(err, fs.ErrPermission):
		kind, msg = errs.PermissionDenied, "PostSonde is not allowed to read the file."
	}
	return errs.Wrap(err, kind, msg).
		WithCode(CodeReadFailed).
		WithHint("Check the file and try again.").
		WithDetail("path", path)
}

func tooLarge(path string) *errs.Error {
	return errs.New(errs.InvalidArgument, "The file is too large to import.").
		WithCode(CodeTooLarge).
		WithHint("Files of up to "+strconv.Itoa(MaxFileSize>>20)+" MiB can be imported.").
		WithDetail("path", path).
		WithDetail("limit", strconv.Itoa(MaxFileSize))
}
