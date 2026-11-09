package history

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/har"
	"github.com/sorrelfox/postsonde/internal/core/history"
)

// HAR export of the history (EXP-04, docs/har.md#export): entries are
// written as they are kept, masked when they were recorded, with the
// timings of their final hop. Nothing is unmasked: the history never
// holds a secret to include.

// Error codes of ExportHAR.
const (
	// CodeExportEmpty: no entry to export (kind invalid_argument).
	CodeExportEmpty = "history.export_empty"
	// CodeExportFailed: the file could not be written (kind io,
	// not_found or permission_denied). Details: path.
	CodeExportFailed = "history.export_failed"
)

// SaveDialog describes the native save dialog ExportHAR shows.
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

// Option configures a HistoryService.
type Option func(*HistoryService)

// WithHARExport lets the user export entries as HAR: pick shows the save
// dialog, version is the app's version written in log.creator.
func WithHARExport(version string, pick SavePicker) Option {
	return func(s *HistoryService) { s.version, s.pickSave = version, pick }
}

// ExportHARInputDTO is what ExportHAR takes.
type ExportHARInputDTO struct {
	// IDs are the entries to export; empty exports those of RequestID,
	// or every entry when RequestID is "" too.
	IDs []string `json:"ids"`
	// RequestID limits the export to one collection request's entries.
	RequestID string `json:"requestId"`
	// Title names the HAR page and the suggested file; blank is "PostSonde
	// history".
	Title string `json:"title"`
}

// ExportHARResultDTO is what ExportHAR returns.
type ExportHARResultDTO struct {
	// Saved is false when the user cancelled the dialog.
	Saved bool   `json:"saved"`
	Path  string `json:"path"`
	// Entries counts the entries written; Masked is set when one of them
	// had a secret hidden when it was recorded.
	Entries int  `json:"entries"`
	Masked  bool `json:"masked"`
}

// ExportHAR writes history entries as a HAR 1.2 file the user picks in
// the native save dialog (EXP-04): oldest first, under one page, each with
// the request as sent, the response snapshot and the recorded timings.
// Entries are masked as the history keeps them. Nothing is written when
// the user cancels.
//
// Errors: history.export_empty when there is no entry to export,
// history.not_found for an unknown ID, history.export_failed when the file
// cannot be written, history.unavailable without a save dialog.
func (s *HistoryService) ExportHAR(ctx context.Context, in ExportHARInputDTO) (ExportHARResultDTO, error) {
	if s.pickSave == nil {
		return ExportHARResultDTO{}, errs.New(errs.Unsupported, "Exporting the history is not available.").WithCode("history.unavailable")
	}
	st, _, err := s.current(ctx)
	if err != nil {
		return ExportHARResultDTO{}, err
	}
	entries, err := selectEntries(st, in)
	if err != nil {
		return ExportHARResultDTO{}, err
	}
	if len(entries) == 0 {
		return ExportHARResultDTO{}, errs.New(errs.InvalidArgument, "There is no history entry to export.").WithCode(CodeExportEmpty).
			WithHint("Send the request first: every send is recorded in the history.")
	}
	title := in.Title
	if title == "" {
		title = "PostSonde history"
	}
	f, masked := BuildHAR(s.version, title, entries)
	data, err := har.Encode(f)
	if err != nil {
		return ExportHARResultDTO{}, errs.Wrap(err, errs.Internal, "The HAR file could not be made.")
	}
	path, err := s.pickSave(ctx, SaveDialog{
		Title:      "Export as HAR",
		Message:    "Export " + plural(len(entries), "history entry", "history entries") + " as an HTTP Archive, secrets masked.",
		ButtonText: "Export",
		Filename:   har.FileName(title),
	})
	if err != nil {
		return ExportHARResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return ExportHARResultDTO{}, nil
	}
	if err := har.WriteFile(path, data); err != nil {
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO
		}
		return ExportHARResultDTO{}, errs.Wrap(err, kind, "The HAR file could not be saved to "+path+".").WithCode(CodeExportFailed).
			WithDetail("path", path).WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	s.logger.Info("history exported as HAR", "entries", len(entries))
	return ExportHARResultDTO{Saved: true, Path: path, Entries: len(entries), Masked: masked}, nil
}

// BuildHAR returns the HAR file of entries (any order; written oldest
// first) under one page titled title, and whether an entry was masked.
func BuildHAR(version, title string, entries []history.Entry) (*har.File, bool) {
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b history.Entry) int { return a.Time.Compare(b.Time) })
	f := har.NewFile(version)
	started := time.Now()
	if len(sorted) > 0 {
		started = sorted[0].Time
	}
	page := f.AddPage(title, started)
	masked := false
	for _, e := range sorted {
		he := har.FromHistory(e)
		he.Pageref = page
		f.Log.Entries = append(f.Log.Entries, he)
		masked = masked || e.Masked
	}
	return f, masked
}

// selectEntries reads the entries an export names.
func selectEntries(st *history.Store, in ExportHARInputDTO) ([]history.Entry, error) {
	ids := in.IDs
	if len(ids) == 0 {
		for offset := 0; ; {
			p := st.List(history.Query{RequestID: in.RequestID, Offset: offset, Limit: history.MaxPageSize})
			for _, e := range p.Entries {
				ids = append(ids, e.ID)
			}
			offset += len(p.Entries)
			if len(p.Entries) == 0 || offset >= p.Total {
				break
			}
		}
	}
	out := make([]history.Entry, 0, len(ids))
	for _, id := range ids {
		e, err := st.Get(id)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
