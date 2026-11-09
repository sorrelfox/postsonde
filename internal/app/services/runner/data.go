package runner

import (
	"context"
	"errors"
	"path/filepath"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	corerunner "github.com/sorrelfox/postsonde/internal/core/runner"
)

// Data-driven runs (RUN-05): the data file is chosen in the native open
// dialog (PickDataFile), previewed, and named by path in Start, which
// makes one iteration per row. Only a file the user picked in this
// session is read: the file-access allowlist (PRV-10) does not apply, as
// for a file body, because the user chose it (docs/privacy.md).

// CodeDataNotPicked: Start or PreviewDataFile named a data file that was
// not chosen in the open dialog in this session (kind permission_denied).
const CodeDataNotPicked = "runner.data_not_picked"

// CodeInvalidDelimiter: the delimiter is not one a data file may use
// (kind invalid_argument). Details: delimiter.
const CodeInvalidDelimiter = "runner.invalid_delimiter"

// MaxPreviewRows is how many rows a DataPreviewDTO has.
const MaxPreviewRows = 5

// FileDialog describes the native open dialog for a data file.
type FileDialog struct {
	Title         string
	Message       string
	ButtonText    string
	FilterName    string
	FilterPattern string
}

// FilePicker shows the native open dialog and returns the chosen path, or
// "" when the user cancelled.
type FilePicker func(ctx context.Context, d FileDialog) (string, error)

// WithDataFilePicker lets PickDataFile show the native open dialog.
func WithDataFilePicker(pick FilePicker) Option {
	return func(s *RunnerService) { s.pickData = pick }
}

// PickDataFile shows the native open dialog for a data file (a CSV file
// with a header row, or a JSON array of objects), reads it with delimiter
// ("" to detect it; ",", ";", "\t" or "|"), and returns its preview: the
// columns, the first MaxPreviewRows rows and the number of rows. A
// cancelled dialog returns a preview with an empty path. The file may
// then be named in RunInputDTO.DataFile and PreviewDataFile for the rest
// of the session.
//
// Errors: runner.invalid_delimiter, runner.unavailable without a dialog,
// and those of PreviewDataFile's reading (runner.data_invalid,
// runner.data_too_large, runner.data_empty, runner.data_unreadable),
// whose details.path is the file chosen so the UI can read it again with
// another delimiter.
func (s *RunnerService) PickDataFile(ctx context.Context, delimiter string) (DataPreviewDTO, error) {
	delim, err := parseDelimiter(delimiter)
	if err != nil {
		return DataPreviewDTO{}, err
	}
	if s.pickData == nil {
		return DataPreviewDTO{}, unavailable()
	}
	path, err := s.pickData(ctx, FileDialog{
		Title:         "Choose a data file",
		Message:       "Choose a CSV file with a header row, or a JSON array of objects. The run makes one iteration per row.",
		ButtonText:    "Choose",
		FilterName:    "CSV and JSON files",
		FilterPattern: "*.csv;*.tsv;*.txt;*.json",
	})
	if err != nil {
		return DataPreviewDTO{}, errs.Wrap(err, errs.Internal, "The open dialog could not be shown.")
	}
	if path == "" {
		return DataPreviewDTO{}, nil
	}
	path = filepath.Clean(path)
	s.mu.Lock()
	s.picked[path] = true
	s.mu.Unlock()
	return s.preview(path, delim)
}

// PreviewDataFile reads data file path again with delimiter (another
// delimiter, or the file changed) and returns its preview, as
// PickDataFile does. path must have been chosen with PickDataFile in this
// session (runner.data_not_picked otherwise).
func (s *RunnerService) PreviewDataFile(ctx context.Context, path, delimiter string) (DataPreviewDTO, error) {
	delim, err := parseDelimiter(delimiter)
	if err != nil {
		return DataPreviewDTO{}, err
	}
	path, err = s.pickedPath(path)
	if err != nil {
		return DataPreviewDTO{}, err
	}
	return s.preview(path, delim)
}

func (s *RunnerService) preview(path string, delim rune) (DataPreviewDTO, error) {
	d, err := corerunner.ReadDataFile(path, delim)
	if err != nil {
		var e *errs.Error
		if errors.As(err, &e) {
			return DataPreviewDTO{}, e.WithDetail("path", path)
		}
		return DataPreviewDTO{}, err
	}
	return toDataPreviewDTO(path, d), nil
}

// pickedPath is path, cleaned, when the user chose it in this session.
func (s *RunnerService) pickedPath(path string) (string, error) {
	path = filepath.Clean(path)
	s.mu.Lock()
	ok := s.picked[path]
	s.mu.Unlock()
	if !ok {
		return "", errs.New(errs.PermissionDenied, "The data file was not chosen in this session.").
			WithCode(CodeDataNotPicked).
			WithHint("Choose the data file again.").
			WithDetail("name", filepath.Base(path))
	}
	return path, nil
}

// readData reads the rows of data file path for a run.
func (s *RunnerService) readData(path, delimiter string) (corerunner.Data, error) {
	delim, err := parseDelimiter(delimiter)
	if err != nil {
		return corerunner.Data{}, err
	}
	if path, err = s.pickedPath(path); err != nil {
		return corerunner.Data{}, err
	}
	return corerunner.ReadDataFile(path, delim)
}

// parseDelimiter is the delimiter of a DTO: "" to detect it.
func parseDelimiter(s string) (rune, error) {
	if s == "" {
		return 0, nil
	}
	r, size := utf8.DecodeRuneInString(s)
	if size != len(s) || !corerunner.ValidDelimiter(r) {
		return 0, errs.New(errs.InvalidArgument, "The delimiter of a data file is a comma, a semicolon, a tab or a pipe.").
			WithCode(CodeInvalidDelimiter).
			WithDetail("delimiter", s)
	}
	return r, nil
}

func toDataPreviewDTO(path string, d corerunner.Data) DataPreviewDTO {
	out := DataPreviewDTO{Path: path, Name: filepath.Base(path), Format: string(d.Format), Columns: d.Columns, Rows: [][]string{}, RowCount: len(d.Rows)}
	if out.Columns == nil {
		out.Columns = []string{}
	}
	if d.Delimiter != 0 {
		out.Delimiter = string(d.Delimiter)
	}
	for _, row := range d.Rows[:min(len(d.Rows), MaxPreviewRows)] {
		values := make([]string, len(d.Columns))
		for i, c := range d.Columns {
			values[i] = row[c]
		}
		out.Rows = append(out.Rows, values)
	}
	return out
}
