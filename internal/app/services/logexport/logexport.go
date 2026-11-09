// Package logexport is the bound service behind copying and saving
// timeline and log entries (DBG-07): the UI formats an entry or a whole log
// as text or JSON, and every copy and save goes through Go, which masks the
// secrets PostSonde knows before the text reaches the clipboard or a file
// the user picked in the native save dialog (docs/privacy.md).
package logexport

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/logexport"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// Error codes.
const (
	// CodeInvalidFormat: the format is not "text" or "json" (kind
	// invalid_argument).
	CodeInvalidFormat = "logexport.invalid_format"
	// CodeInvalidJSON: JSON input does not parse (kind invalid_argument).
	CodeInvalidJSON = "logexport.invalid_json"
	// CodeSaveFailed: the file could not be written (kind io, not_found or
	// permission_denied). Details: path.
	CodeSaveFailed = "logexport.save_failed"
	// CodeUnavailable: there is no save dialog (kind unsupported).
	CodeUnavailable = "logexport.unavailable"
)

// DefaultFileName is the suggested file name when the UI gives none.
const DefaultFileName = "postsonde-log"

// Secrets returns the secret values to mask: the open project's and the
// app's. Values may come with an error for the ones that could not be
// read; they are used, and the error is logged.
type Secrets func(ctx context.Context) ([]string, error)

// SaveDialog describes the native save dialog Save shows.
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

// LogExportService masks and saves the logs the UI shows.
type LogExportService struct {
	secrets Secrets
	pick    SavePicker
	logger  *slog.Logger
}

// New returns a LogExportService. A nil secrets masks only the credentials
// of Authorization lines; a nil pick makes Save fail with
// logexport.unavailable; a nil logger means slog.Default().
func New(secrets Secrets, pick SavePicker, logger *slog.Logger) *LogExportService {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogExportService{secrets: secrets, pick: pick, logger: logger.With("service", "logexport")}
}

// ServiceName names the service in Wails logs.
func (s *LogExportService) ServiceName() string { return "logexport" }

// Mask returns an entry or log with the known secrets masked, for the
// clipboard (DBG-07). Nothing is written or logged.
//
// Errors: logexport.invalid_format, logexport.invalid_json.
func (s *LogExportService) Mask(ctx context.Context, in MaskInputDTO) (MaskResultDTO, error) {
	f, err := format(in.Format)
	if err != nil {
		return MaskResultDTO{}, err
	}
	text, masked, err := s.mask(ctx, in.Text, f)
	if err != nil {
		return MaskResultDTO{}, err
	}
	return MaskResultDTO{Text: text, Masked: masked}, nil
}

// Save masks a whole log as Mask does, asks where to save it with the
// native save dialog, suggesting FileName with the format's extension,
// and writes it there (DBG-07). Nothing is written when the user cancels.
//
// Errors: logexport.invalid_format, logexport.invalid_json,
// logexport.save_failed when the file cannot be written,
// logexport.unavailable without a save dialog.
func (s *LogExportService) Save(ctx context.Context, in SaveInputDTO) (SaveResultDTO, error) {
	if s.pick == nil {
		return SaveResultDTO{}, errs.New(errs.Unsupported, "Saving logs is not available.").WithCode(CodeUnavailable)
	}
	f, err := format(in.Format)
	if err != nil {
		return SaveResultDTO{}, err
	}
	text, masked, err := s.mask(ctx, in.Content, f)
	if err != nil {
		return SaveResultDTO{}, err
	}
	if f == core.Text && text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Log"
	}
	path, err := s.pick(ctx, SaveDialog{
		Title:      "Save " + title,
		Message:    "Save the " + inSentence(title) + " as " + formatName(f) + ", secrets masked.",
		ButtonText: "Save",
		Filename:   FileName(in.FileName, f),
	})
	if err != nil {
		return SaveResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return SaveResultDTO{}, nil
	}
	if err := atomicfile.WriteFile(path, []byte(text), 0o644); err != nil {
		s.logger.Warn("saving a log failed", "err", err)
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO // a full disk, a read-only volume, …
		}
		return SaveResultDTO{}, errs.Wrap(err, kind, "The log could not be saved to "+path+".").
			WithCode(CodeSaveFailed).WithDetail("path", path).
			WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	s.logger.Info("log saved", "format", string(f), "bytes", len(text), "masked", masked)
	return SaveResultDTO{Saved: true, Path: path, Size: int64(len(text)), Masked: masked}, nil
}

func (s *LogExportService) mask(ctx context.Context, text string, f core.Format) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, errs.Wrap(err, errs.KindOf(err), "Masking the log was cancelled.")
	}
	var secrets []string
	if s.secrets != nil {
		var err error
		secrets, err = s.secrets(ctx)
		if secretstore.IsLocked(err) {
			// Locked secrets may be in entries logged while they
			// were unlocked: nothing leaves unmasked, unlock first.
			return "", false, err
		}
		if err != nil {
			// Unreadable side files: their secrets cannot be sent either.
			s.logger.Warn("some secrets could not be read for masking", "err", err)
		}
	}
	out, masked, err := core.NewMasker(secrets).Mask(text, f)
	if err != nil {
		return "", false, errs.Wrap(err, errs.InvalidArgument, "The log is not valid JSON.").WithCode(CodeInvalidJSON)
	}
	return out, masked, nil
}

func format(s string) (core.Format, error) {
	f := core.Format(s)
	if !f.Valid() {
		return "", errs.New(errs.InvalidArgument, "Logs are copied and saved as text or JSON.").
			WithCode(CodeInvalidFormat).WithDetail("format", s)
	}
	return f, nil
}

// inSentence returns a log's title for use inside a sentence: the first
// letter in lower case ("Console" → "console"), unless the first word has
// another capital ("WebSocket messages", "TCP messages").
func inSentence(title string) string {
	r := []rune(title)
	if len(r) == 0 {
		return title
	}
	for _, c := range r[1:] {
		if c == ' ' {
			break
		}
		if unicode.IsUpper(c) {
			return title
		}
	}
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func formatName(f core.Format) string {
	if f == core.JSON {
		return "JSON"
	}
	return "plain text"
}

// FileName returns the suggested file name for name and f: characters a
// file name cannot hold replaced by "-", at most 100 characters, and the
// format's extension (added unless name already has it).
func FileName(name string, f core.Format) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r < 0x20, strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .-")
	if strings.EqualFold(filepath.Ext(out), f.Ext()) {
		out = strings.TrimSuffix(out, filepath.Ext(out))
	}
	if out == "" {
		out = DefaultFileName
	}
	if r := []rune(out); len(r) > 100 {
		out = string(r[:100])
	}
	return out + f.Ext()
}
