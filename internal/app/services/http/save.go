package http

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes of SaveBody.
const (
	// CodeBodyGone: the body is no longer kept (MaxKeptBodies, BodyTTL,
	// or the app restarted).
	CodeBodyGone = "http.body_gone"
	// CodeSaveFailed: the file could not be written.
	CodeSaveFailed = "http.save_failed"
)

// SaveDialog describes the native save dialog SaveBody shows.
type SaveDialog struct {
	Title      string
	Message    string
	ButtonText string
	// Filename is the suggested file name.
	Filename string
}

// SavePicker shows the native save dialog and returns the path the user
// chose, or "" when they cancelled.
type SavePicker func(ctx context.Context, d SaveDialog) (string, error)

// Option configures an HTTPService.
type Option func(*HTTPService)

// WithSaveBody lets the user save response bodies (HTTP-34): bodies over
// SpoolThreshold are spooled to spoolDir (under the app data directory,
// emptied at start and shutdown; "" keeps them in memory), and pick shows
// the save dialog.
func WithSaveBody(spoolDir string, pick SavePicker) Option {
	return func(s *HTTPService) {
		s.bodies = newBodyStore(spoolDir)
		s.pickSave = pick
	}
}

// SaveBodyInputDTO is what SaveBody takes.
type SaveBodyInputDTO struct {
	// BodyID is ResponseDTO.BodyID.
	BodyID string `json:"bodyId"`
}

// SaveBodyResultDTO is what SaveBody returns.
type SaveBodyResultDTO struct {
	// Saved is false when the user cancelled the dialog.
	Saved bool `json:"saved"`
	// Path is the file written; Size the bytes written.
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// SaveBody asks where to save the body of a response (ResponseDTO.BodyID)
// with the native save dialog, suggesting ResponseDTO.FileName, and
// writes the bytes received there (HTTP-34): not charset-decoded, not
// clipped like the view's Body, with a gzip or deflate Content-Encoding
// removed while receiving (ResponseDTO.ContentEncoding, HTTP-37). A body
// over the limit was cut while receiving (ResponseDTO.BodyTruncated);
// what was received is saved. Nothing is written when the user cancels.
//
// Errors: http.body_gone (not_found) when the body is no longer kept,
// http.save_failed (io, not_found or permission_denied) when the file
// cannot be written, http.unavailable without a save dialog.
func (s *HTTPService) SaveBody(ctx context.Context, in SaveBodyInputDTO) (SaveBodyResultDTO, error) {
	if s.bodies == nil || s.pickSave == nil {
		return SaveBodyResultDTO{}, errs.New(errs.Unsupported, "Saving response bodies is not available.").WithCode(CodeUnavailable)
	}
	kb, r, ok, err := s.bodies.open(in.BodyID)
	if err != nil {
		return SaveBodyResultDTO{}, errs.Wrap(err, errs.IO, "The response body could not be read.").WithCode(CodeSaveFailed)
	}
	if !ok {
		return SaveBodyResultDTO{}, bodyGone()
	}
	defer r.Close()

	path, err := s.pickSave(ctx, SaveDialog{
		Title:      "Save Response Body",
		Message:    "Save the response body as received.",
		ButtonText: "Save",
		Filename:   kb.name,
	})
	if err != nil {
		return SaveBodyResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return SaveBodyResultDTO{}, nil
	}
	n, err := writeFileAtomic(path, r)
	if err != nil {
		s.logger.Warn("saving a response body failed", "err", err)
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO // a full disk, a read-only volume, …
		}
		return SaveBodyResultDTO{}, errs.Wrap(err, kind, "The response body could not be saved to "+path+".").
			WithCode(CodeSaveFailed).WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	s.logger.Info("response body saved", "bytes", n)
	return SaveBodyResultDTO{Saved: true, Path: path, Size: n}, nil
}

func bodyGone() error {
	return errs.New(errs.NotFound, "This response's body is no longer kept.").
		WithCode(CodeBodyGone).
		WithHint("PostSonde keeps the bodies of the last 8 responses for an hour. Send the request again to save its body.")
}

// writeFileAtomic writes r to path through a temporary file in the same
// directory, renamed over path once complete, so a failed save never
// leaves a partial file under the chosen name.
func writeFileAtomic(path string, r io.Reader) (int64, error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.part")
	if err != nil {
		return 0, err
	}
	tmp := f.Name()
	n, err := io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// CreateTemp makes the file 0600; a saved body is an ordinary
		// user file.
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return n, nil
}
