package request

import (
	"errors"
	"io"
	"os"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// FileBody is a binary file body (HTTP-15): the file's bytes, streamed
// from disk each time the body is opened, never held in memory.
type FileBody struct {
	path   string // on disk
	stored string // as stored, for errors
	typ    string
}

var _ httpclient.Body = (*FileBody)(nil)

// fileBodyWhat names a file body's file in messages.
const fileBodyWhat = "The body file"

// Open opens the file and returns a reader of exactly its current size,
// which is the length: a file that shrinks or grows while it is sent
// fails the send with CodeBodyFileChanged instead of sending a body that
// does not match the Content-Length.
func (b *FileBody) Open() (io.ReadCloser, int64, error) {
	f, err := os.Open(b.path)
	if err != nil {
		return nil, 0, bodyFileError(err, b.stored, fileBodyWhat)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, bodyFileError(err, b.stored, fileBodyWhat)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		_, err := statBodyFile(b.path, b.stored, fileBodyWhat)
		return nil, 0, err
	}
	return &sizedFile{f: f, remaining: info.Size(), stored: b.stored, what: fileBodyWhat}, info.Size(), nil
}

// Path returns the absolute path of the file on disk.
func (b *FileBody) Path() string { return b.path }

// ContentType returns the file body's Content-Type (ContentType of the
// saved body).
func (b *FileBody) ContentType() string { return b.typ }

// sizedFile reads exactly remaining bytes of f: an early end of file and
// data past the size are both changedFileError.
type sizedFile struct {
	f         *os.File
	remaining int64
	stored    string
	what      string
}

func (s *sizedFile) Read(p []byte) (int, error) {
	if s.remaining <= 0 {
		// Past the size: the file must end here.
		var one [1]byte
		n, err := s.f.Read(one[:])
		if n > 0 {
			return 0, changedFileError(s.stored, s.what)
		}
		if err == nil || errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		return 0, bodyFileError(err, s.stored, s.what)
	}
	if int64(len(p)) > s.remaining {
		p = p[:s.remaining]
	}
	n, err := s.f.Read(p)
	s.remaining -= int64(n)
	switch {
	case errors.Is(err, io.EOF):
		if s.remaining > 0 {
			return n, changedFileError(s.stored, s.what)
		}
		return n, nil
	case err != nil:
		return n, bodyFileError(err, s.stored, s.what)
	}
	return n, nil
}

func (s *sizedFile) Close() error { return s.f.Close() }

// fileBody maps a "file" body.
func fileBody(b projectfile.Body, root string) (httpclient.Body, error) {
	if b.Path == "" {
		return nil, errs.New(errs.InvalidArgument, "No file is chosen for the binary body.").
			WithCode(CodeBodyFileUnset).
			WithHint("Choose a file in the Body tab.")
	}
	abs, _, err := bodyFile(root, b.Path, fileBodyWhat)
	if err != nil {
		return nil, err
	}
	return &FileBody{path: abs, stored: b.Path, typ: ContentType(b)}, nil
}
