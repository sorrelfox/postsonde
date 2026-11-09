package http

import (
	"context"
	"errors"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/har"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
)

// HAR export of collection requests (EXP-04, docs/har.md#export): each
// request is built as Send builds it and written, not sent, as a HAR
// entry without a response. Secrets are masked unless the user asks for
// them (PRV-09), as for Copy as cURL.

// Error codes of ExportHAR.
const (
	// CodeExportEmpty: none of the requests could be exported (kind
	// invalid_argument).
	CodeExportEmpty = "http.export_empty"
	// CodeExportFailed: the file could not be written (kind io,
	// not_found or permission_denied). Details: path.
	CodeExportFailed = "http.export_failed"
)

// WithVersion sets the app's version, written in exported files
// (log.creator of a HAR file).
func WithVersion(version string) Option {
	return func(s *HTTPService) { s.version = version }
}

// ExportHARInputDTO is what ExportHAR takes.
type ExportHARInputDTO struct {
	// Name names the HAR page and the suggested file: the folder's name.
	Name string `json:"name"`
	// RequestIDs are the HTTP, GraphQL and SSE requests to export, in
	// order (a folder's, depth first).
	RequestIDs []string `json:"requestIds"`
	// IncludeSecrets writes the real values of the secrets; false (the
	// default) masks them (PRV-09).
	IncludeSecrets bool `json:"includeSecrets"`
}

// ExportSkippedDTO is a request ExportHAR left out, and why.
type ExportSkippedDTO struct {
	RequestID string `json:"requestId"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// ExportHARResultDTO is what ExportHAR returns.
type ExportHARResultDTO struct {
	// Saved is false when the user cancelled the dialog.
	Saved bool   `json:"saved"`
	Path  string `json:"path"`
	// Entries counts the requests written.
	Entries int `json:"entries"`
	// Masked is set when secrets were replaced by ••••••.
	Masked bool `json:"masked"`
	// Skipped are the requests that could not be built.
	Skipped []ExportSkippedDTO `json:"skipped"`
}

// ExportHAR writes requests in.RequestIDs as a HAR 1.2 file the user picks
// in the native save dialog (EXP-04): each built as Send builds it (the
// active environment, folder headers, auth, the cookie jar's cookies),
// under one page named in.Name, with no response (status 0) since nothing
// is sent. Secrets are masked unless in.IncludeSecrets, as CurlCommand
// masks them. Undefined variables stay as written. A request that cannot
// be built (not an HTTP request, a missing body file…) is left out and
// listed in Skipped. Nothing is written when the user cancels.
//
// Errors: http.export_empty when no request could be built,
// http.export_failed when the file cannot be written, http.unavailable
// without a save dialog.
func (s *HTTPService) ExportHAR(ctx context.Context, in ExportHARInputDTO) (ExportHARResultDTO, error) {
	if s.pickSave == nil {
		return ExportHARResultDTO{}, errs.New(errs.Unsupported, "Exporting requests is not available.").WithCode(CodeUnavailable)
	}
	f, out, err := s.buildHAR(ctx, in, time.Now())
	if err != nil {
		return ExportHARResultDTO{}, err
	}
	data, err := har.Encode(f)
	if err != nil {
		return ExportHARResultDTO{}, errs.Wrap(err, errs.Internal, "The HAR file could not be made.")
	}
	msg := "Export the requests as an HTTP Archive, secrets masked."
	if in.IncludeSecrets {
		msg = "Export the requests as an HTTP Archive, with their secrets."
	}
	path, err := s.pickSave(ctx, SaveDialog{Title: "Export as HAR", Message: msg, ButtonText: "Export", Filename: har.FileName(in.Name)})
	if err != nil {
		return ExportHARResultDTO{}, errs.Wrap(err, errs.Internal, "The save dialog could not be shown.")
	}
	if path == "" {
		return ExportHARResultDTO{Skipped: out.Skipped, Entries: out.Entries, Masked: out.Masked}, nil
	}
	if err := har.WriteFile(path, data); err != nil {
		kind := errs.KindOf(err)
		if kind == errs.Internal {
			kind = errs.IO
		}
		return ExportHARResultDTO{}, errs.Wrap(err, kind, "The HAR file could not be saved to "+path+".").WithCode(CodeExportFailed).
			WithDetail("path", path).WithHint("Check that the folder exists and can be written to, and that the disk is not full.")
	}
	s.logger.Info("requests exported as HAR", "entries", out.Entries, "masked", out.Masked)
	out.Saved, out.Path = true, path
	return out, nil
}

// buildHAR builds the HAR file of an export, started at now.
func (s *HTTPService) buildHAR(ctx context.Context, in ExportHARInputDTO, now time.Time) (*har.File, ExportHARResultDTO, error) {
	out := ExportHARResultDTO{Skipped: []ExportSkippedDTO{}}
	f := har.NewFile(s.version)
	name := in.Name
	if name == "" {
		name = "Requests"
	}
	page := f.AddPage(name, now)
	for _, id := range in.RequestIDs {
		if err := ctx.Err(); err != nil {
			return nil, out, errs.Wrap(err, errs.KindOf(err), "Exporting the requests was interrupted.")
		}
		src, x, b, err := s.build(ctx, id, previewBuild)
		if err != nil {
			out.Skipped = append(out.Skipped, skippedDTO(id, err))
			continue
		}
		r, err := snippet.FromBuild(b, s.jarCookies(b))
		if err != nil {
			out.Skipped = append(out.Skipped, skippedDTO(id, err))
			continue
		}
		if !in.IncludeSecrets {
			var masked bool
			r, masked = r.Masked(secretsOf(x, b))
			out.Masked = out.Masked || masked
		}
		reqName := src.Request.Name
		if src.GraphQL != nil {
			reqName = src.GraphQL.Name
		}
		e := har.FromRequest(r, now, reqName)
		e.Pageref = page
		f.Log.Entries = append(f.Log.Entries, e)
	}
	out.Entries = len(f.Log.Entries)
	if out.Entries == 0 {
		return nil, out, errs.New(errs.InvalidArgument, "None of the requests could be exported.").WithCode(CodeExportEmpty).
			WithHint("HTTP, GraphQL and SSE requests are exported; check that they can be sent.")
	}
	return f, out, nil
}

func skippedDTO(id string, err error) ExportSkippedDTO {
	d := ExportSkippedDTO{RequestID: id, Message: err.Error()}
	var e *errs.Error
	if errors.As(err, &e) {
		d.Code = e.Code
		if e.Message != "" {
			d.Message = e.Message
		}
	}
	return d
}
