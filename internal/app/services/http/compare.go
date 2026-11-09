package http

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/respdiff"
)

// Comparing two responses (HTTP-40): CompareResponses takes two
// stored responses (the latest send or an earlier one from the history,
// or a response example; the UI reads them as for StoredResponse) and
// returns their differences with internal/core/respdiff: the status, the
// header fields by name and the body, JSON by structure and other text by
// line, as rows for a side-by-side view. See docs/response-diff.md.

// CompareInputDTO is the two responses to compare.
type CompareInputDTO struct {
	Left  StoredResponseInputDTO `json:"left"`
	Right StoredResponseInputDTO `json:"right"`
}

// ResponseDiffDTO is a comparison.
type ResponseDiffDTO struct {
	Status StatusDiffDTO `json:"status"`
	// Headers are the header names, the left response's order first.
	Headers []HeaderDiffDTO `json:"headers"`
	// HeadersAdded, HeadersRemoved and HeadersChanged count the names.
	HeadersAdded   int         `json:"headersAdded"`
	HeadersRemoved int         `json:"headersRemoved"`
	HeadersChanged int         `json:"headersChanged"`
	Body           BodyDiffDTO `json:"body"`
}

// StatusDiffDTO is the two status lines ("200 OK").
type StatusDiffDTO struct {
	Left    string `json:"left"`
	Right   string `json:"right"`
	Changed bool   `json:"changed"`
}

// HeaderDiffDTO is one header name and its values on each side.
type HeaderDiffDTO struct {
	Name string `json:"name"`
	// Kind is "same", "added", "removed" or "changed".
	Kind  string   `json:"kind"`
	Left  []string `json:"left"`
	Right []string `json:"right"`
}

// BodyDiffDTO is the comparison of the bodies.
type BodyDiffDTO struct {
	// Mode is "json", "text", "binary", "too_large" or "empty".
	Mode      string `json:"mode"`
	Identical bool   `json:"identical"`
	// Rows are the side-by-side rows: JSON pretty-printed, text as is.
	Rows []DiffRowDTO `json:"rows"`
	// Changes are the JSON differences by path.
	Changes []JSONChangeDTO `json:"changes"`
	// Added, Removed and Changed count the JSON changes or the lines.
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
	// Notices say what a limit or a choice left out (respdiff.* codes).
	Notices   []DiffNoticeDTO `json:"notices"`
	LeftSize  int64           `json:"leftSize"`
	RightSize int64           `json:"rightSize"`
}

// DiffRowDTO is one row: a line of each side, null where a side has none.
type DiffRowDTO struct {
	// Kind is "same", "added", "removed" or "changed".
	Kind  string       `json:"kind"`
	Left  *DiffLineDTO `json:"left"`
	Right *DiffLineDTO `json:"right"`
	// Path is the JSON path of the value the row belongs to, "" for text.
	Path string `json:"path"`
}

// DiffLineDTO is a line and its number (1-based) on its side.
type DiffLineDTO struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

// JSONChangeDTO is one JSON difference.
type JSONChangeDTO struct {
	Path string `json:"path"`
	// Kind is "added", "removed" or "changed".
	Kind string `json:"kind"`
	// Left and Right are the values as compact JSON (at most 200
	// characters), "" on the side without one; the types are set when a
	// value was added, removed, or changed its type.
	Left      string `json:"left"`
	Right     string `json:"right"`
	LeftType  string `json:"leftType"`
	RightType string `json:"rightType"`
}

// DiffNoticeDTO is a notice about the body comparison.
type DiffNoticeDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CompareResponses compares two stored responses (HTTP-40). Bodies of
// more than 1 MiB are only said to be equal or not; the other limits of
// respdiff apply, each with a notice.
//
// Errors: http.invalid_stored_response when a Base64 body does not
// decode.
func (s *HTTPService) CompareResponses(ctx context.Context, in CompareInputDTO) (ResponseDiffDTO, error) {
	if err := ctx.Err(); err != nil {
		return ResponseDiffDTO{}, errs.Wrap(err, errs.KindOf(err), "Comparing the responses was interrupted.")
	}
	left, err := diffSide(in.Left)
	if err != nil {
		return ResponseDiffDTO{}, err
	}
	right, err := diffSide(in.Right)
	if err != nil {
		return ResponseDiffDTO{}, err
	}
	return toResponseDiffDTO(respdiff.Compare(left, right, respdiff.Limits{})), nil
}

func diffSide(in StoredResponseInputDTO) (respdiff.Response, error) {
	body, err := storedBytes(in.Body, in.BodyBase64, "response body")
	if err != nil {
		return respdiff.Response{}, err
	}
	out := respdiff.Response{StatusCode: in.StatusCode, Status: in.Status, Body: body, Clipped: in.BodyClipped, Size: in.BodySize}
	for _, h := range in.Headers {
		out.Headers = append(out.Headers, respdiff.Header{Name: h.Name, Value: h.Value})
	}
	return out, nil
}

func toResponseDiffDTO(r respdiff.Result) ResponseDiffDTO {
	out := ResponseDiffDTO{
		Status:       StatusDiffDTO{Left: r.Status.Left, Right: r.Status.Right, Changed: r.Status.Changed},
		Headers:      make([]HeaderDiffDTO, 0, len(r.Headers)),
		HeadersAdded: r.HeadersAdded, HeadersRemoved: r.HeadersRemoved, HeadersChanged: r.HeadersChanged,
	}
	nonNil := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	for _, h := range r.Headers {
		out.Headers = append(out.Headers, HeaderDiffDTO{Name: h.Name, Kind: string(h.Kind), Left: nonNil(h.Left), Right: nonNil(h.Right)})
	}
	b := r.Body
	out.Body = BodyDiffDTO{
		Mode: b.Mode, Identical: b.Identical,
		Rows:    make([]DiffRowDTO, 0, len(b.Rows)),
		Changes: make([]JSONChangeDTO, 0, len(b.Changes)),
		Added:   b.Added, Removed: b.Removed, Changed: b.Changed,
		Notices:  make([]DiffNoticeDTO, 0, len(b.Notices)),
		LeftSize: b.LeftSize, RightSize: b.RightSize,
	}
	line := func(l *respdiff.Line) *DiffLineDTO {
		if l == nil {
			return nil
		}
		return &DiffLineDTO{N: l.N, Text: l.Text}
	}
	for _, row := range b.Rows {
		out.Body.Rows = append(out.Body.Rows, DiffRowDTO{Kind: string(row.Kind), Left: line(row.Left), Right: line(row.Right), Path: row.Path})
	}
	for _, c := range b.Changes {
		out.Body.Changes = append(out.Body.Changes, JSONChangeDTO{Path: c.Path, Kind: string(c.Kind), Left: c.Left, Right: c.Right,
			LeftType: c.LeftType, RightType: c.RightType})
	}
	for _, n := range b.Notices {
		out.Body.Notices = append(out.Body.Notices, DiffNoticeDTO{Code: n.Code, Message: n.Message})
	}
	return out
}
