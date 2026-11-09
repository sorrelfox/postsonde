// Package respdiff compares two HTTP responses (HTTP-40): the status
// line, the header fields by name (letter case ignored) and the body, as
// rows for a side-by-side view. Two JSON bodies are compared by structure,
// member by member and array element by index, and shown pretty-printed;
// other text line by line (a Myers diff); binary bodies only as equal or
// not. Every part is bounded (Limits), and what a limit cut short is said
// in a Notice. Standard library only; see docs/response-diff.md.
package respdiff

import (
	"bytes"
	"strconv"
	"unicode/utf8"
)

// Kind says how a row, header or change differs.
type Kind string

// Kinds.
const (
	Same    Kind = "same"
	Added   Kind = "added"
	Removed Kind = "removed"
	Changed Kind = "changed"
)

// Header is one header field.
type Header struct {
	Name, Value string
}

// Response is one side of a comparison.
type Response struct {
	// StatusCode and Status are the status ("200 OK", or only "OK").
	StatusCode int
	Status     string
	// Headers are the header fields in the order received.
	Headers []Header
	// Body is the body as kept; Clipped says it holds only the start of
	// Size bytes.
	Body    []byte
	Clipped bool
	Size    int64
}

// Limits bound a comparison. Zero fields take the defaults.
type Limits struct {
	// MaxBodyBytes is the largest body compared; a larger one is only
	// said to be equal or not. Default 1 MiB.
	MaxBodyBytes int
	// MaxLines is the most lines of one side in a line diff. Default
	// 20,000.
	MaxLines int
	// MaxEdits is the most lines added and removed a line diff looks
	// for; beyond it the texts are shown as all removed, then all added.
	// Default 1,000.
	MaxEdits int
	// MaxRows is the most rows returned; the rest are left out. Default
	// 20,000.
	MaxRows int
	// MaxNodes and MaxDepth bound one JSON body (values, nesting); a
	// larger one is compared as text. Defaults 200,000 and 200.
	MaxNodes int
	MaxDepth int
	// MaxChanges is the most JSON changes listed. Default 5,000.
	MaxChanges int
}

// Default limits.
const (
	DefaultMaxBodyBytes = 1 << 20
	DefaultMaxLines     = 20000
	DefaultMaxEdits     = 1000
	DefaultMaxRows      = 20000
	DefaultMaxNodes     = 200000
	DefaultMaxDepth     = 200
	DefaultMaxChanges   = 5000
)

func (l Limits) withDefaults() Limits {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&l.MaxBodyBytes, DefaultMaxBodyBytes)
	def(&l.MaxLines, DefaultMaxLines)
	def(&l.MaxEdits, DefaultMaxEdits)
	def(&l.MaxRows, DefaultMaxRows)
	def(&l.MaxNodes, DefaultMaxNodes)
	def(&l.MaxDepth, DefaultMaxDepth)
	def(&l.MaxChanges, DefaultMaxChanges)
	return l
}

// Notice codes: why the body diff is not the full picture.
const (
	// CodeTooLarge: a body is over MaxBodyBytes; only equal or not.
	CodeTooLarge = "respdiff.too_large"
	// CodeClipped: a body was kept only in part; the rest is not compared.
	CodeClipped = "respdiff.clipped"
	// CodeNotJSON: one body is JSON and the other is not; compared as text.
	CodeNotJSON = "respdiff.not_json"
	// CodeJSONLimits: a JSON body is too deep or has too many values;
	// compared as text.
	CodeJSONLimits = "respdiff.json_limits"
	// CodeArraysByIndex: arrays are compared element by element.
	CodeArraysByIndex = "respdiff.arrays_by_index"
	// CodeKeyOrder: members in another order are shown in the left
	// body's order.
	CodeKeyOrder = "respdiff.key_order"
	// CodeTooManyChanges: the line diff gave up; all lines are shown
	// removed, then added.
	CodeTooManyChanges = "respdiff.too_many_changes"
	// CodeTooManyLines: a text has more than MaxLines lines; only the
	// first are compared.
	CodeTooManyLines = "respdiff.too_many_lines"
	// CodeRowsLimited: rows past MaxRows are left out.
	CodeRowsLimited = "respdiff.rows_limited"
	// CodeChangesLimited: JSON changes past MaxChanges are not listed.
	CodeChangesLimited = "respdiff.changes_limited"
	// CodeBinary: a body is not UTF-8 text; only equal or not.
	CodeBinary = "respdiff.binary"
)

// Notice says what a limit or a choice left out.
type Notice struct {
	Code, Message string
}

// Line is one line of one side: N is its number (1-based) in that side's
// text as shown.
type Line struct {
	N    int
	Text string
}

// Row is one row of the side-by-side view. Left or Right is nil where
// that side has no line. Path is the JSON path the row belongs to ("" for
// text).
type Row struct {
	Kind        Kind
	Left, Right *Line
	Path        string
}

// Change is one difference between two JSON bodies, at a path: a member
// or element added or removed (its value on one side), or a value changed
// (both, with their types when the type changed).
type Change struct {
	Path string
	Kind Kind
	// Left and Right are the values as compact JSON, cut at 200
	// characters; "" on the side that has none.
	Left, Right         string
	LeftType, RightType string
}

// Body modes.
const (
	ModeEmpty    = "empty"
	ModeJSON     = "json"
	ModeText     = "text"
	ModeBinary   = "binary"
	ModeTooLarge = "too_large"
)

// BodyDiff is the comparison of the bodies.
type BodyDiff struct {
	// Mode is how they were compared: "json", "text", "binary",
	// "too_large", or "empty" when both are.
	Mode string
	// Identical says the bodies are the same bytes (JSON: the same
	// values, whatever the formatting and member order).
	Identical bool
	Rows      []Row
	// Changes are the JSON differences by path, in document order.
	Changes []Change
	// Added, Removed and Changed count the changes (JSON) or the lines
	// (text).
	Added, Removed, Changed int
	Notices                 []Notice
	LeftSize, RightSize     int64
}

// StatusDiff is the comparison of the status lines.
type StatusDiff struct {
	Left, Right string
	Changed     bool
}

// HeaderDiff is one header name: its values on each side, in order.
type HeaderDiff struct {
	// Name is the name as the left response writes it (else the
	// right's).
	Name        string
	Kind        Kind
	Left, Right []string
}

// Result is a comparison.
type Result struct {
	Status  StatusDiff
	Headers []HeaderDiff
	// HeadersAdded, HeadersRemoved and HeadersChanged count the names.
	HeadersAdded, HeadersRemoved, HeadersChanged int
	Body                                         BodyDiff
}

// Compare compares left and right within lim.
func Compare(left, right Response, lim Limits) Result {
	lim = lim.withDefaults()
	out := Result{Status: StatusDiff{Left: statusLine(left), Right: statusLine(right)}}
	out.Status.Changed = out.Status.Left != out.Status.Right
	out.Headers = Headers(left.Headers, right.Headers)
	for _, h := range out.Headers {
		switch h.Kind {
		case Added:
			out.HeadersAdded++
		case Removed:
			out.HeadersRemoved++
		case Changed:
			out.HeadersChanged++
		}
	}
	out.Body = Body(left, right, lim)
	return out
}

// statusLine is "200 OK" of r.
func statusLine(r Response) string {
	code := strconv.Itoa(r.StatusCode)
	s := r.Status
	if s == "" {
		return code
	}
	if len(s) >= len(code) && s[:len(code)] == code {
		return s
	}
	return code + " " + s
}

// Body compares the bodies of left and right.
func Body(left, right Response, lim Limits) BodyDiff {
	lim = lim.withDefaults()
	a, b := left.Body, right.Body
	out := BodyDiff{LeftSize: max(left.Size, int64(len(a))), RightSize: max(right.Size, int64(len(b)))}
	if left.Clipped || right.Clipped {
		out.Notices = append(out.Notices, Notice{Code: CodeClipped,
			Message: "Only the start of a body was kept (" + clippedSides(left, right) + "); the rest is not compared."})
	}
	switch {
	case len(a) == 0 && len(b) == 0:
		out.Mode = ModeEmpty
		out.Identical = true
		return out
	case len(a) > lim.MaxBodyBytes || len(b) > lim.MaxBodyBytes:
		out.Mode = ModeTooLarge
		out.Identical = bytes.Equal(a, b)
		out.Notices = append(out.Notices, Notice{Code: CodeTooLarge,
			Message: "A body is larger than " + formatSize(int64(lim.MaxBodyBytes)) + " (" + formatSize(int64(len(a))) + " and " +
				formatSize(int64(len(b))) + "); the bodies are only said to be " + sameOrNot(out.Identical) + "."})
		return out
	case !utf8.Valid(a) || !utf8.Valid(b):
		out.Mode = ModeBinary
		out.Identical = bytes.Equal(a, b)
		msg := "A body is not UTF-8 text; the bodies are only said to be " + sameOrNot(out.Identical)
		if !out.Identical {
			msg += ": they first differ at byte " + strconv.Itoa(firstDiff(a, b))
		}
		out.Notices = append(out.Notices, Notice{Code: CodeBinary, Message: msg + "."})
		return out
	}

	ja, errA := parseJSON(a, lim)
	jb, errB := parseJSON(b, lim)
	switch {
	case errA == nil && errB == nil:
		out.Mode = ModeJSON
		jsonDiff(&out, ja, jb, lim)
		return out
	case errA == errLimits || errB == errLimits:
		out.Notices = append(out.Notices, Notice{Code: CodeJSONLimits,
			Message: "A JSON body is nested deeper than " + strconv.Itoa(lim.MaxDepth) + " levels or has more than " +
				strconv.Itoa(lim.MaxNodes) + " values; the bodies are compared line by line."})
	case (errA == nil) != (errB == nil) && len(a) > 0 && len(b) > 0:
		side := "right"
		if errA != nil {
			side = "left"
		}
		out.Notices = append(out.Notices, Notice{Code: CodeNotJSON,
			Message: "The " + side + " body is not valid JSON; the bodies are compared line by line."})
	}
	out.Mode = ModeText
	textDiff(&out, string(a), string(b), lim)
	return out
}

func sameOrNot(same bool) string {
	if same {
		return "the same"
	}
	return "different"
}

func clippedSides(l, r Response) string {
	switch {
	case l.Clipped && r.Clipped:
		return formatSize(int64(len(l.Body))) + " of " + formatSize(l.Size) + " on the left, " +
			formatSize(int64(len(r.Body))) + " of " + formatSize(r.Size) + " on the right"
	case l.Clipped:
		return formatSize(int64(len(l.Body))) + " of " + formatSize(l.Size) + " on the left"
	}
	return formatSize(int64(len(r.Body))) + " of " + formatSize(r.Size) + " on the right"
}

// firstDiff is the offset of the first byte that differs.
func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KiB"
	case n == 1:
		return "1 byte"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// rows collects rows up to a limit.
type rows struct {
	list    []Row
	max     int
	dropped bool
	// n are the line numbers of each side so far.
	nl, nr int
}

func (r *rows) add(kind Kind, left, right *string, path string) {
	row := Row{Kind: kind, Path: path}
	if left != nil {
		r.nl++
		row.Left = &Line{N: r.nl, Text: *left}
	}
	if right != nil {
		r.nr++
		row.Right = &Line{N: r.nr, Text: *right}
	}
	if len(r.list) >= r.max {
		r.dropped = true
		return
	}
	r.list = append(r.list, row)
}

func (r *rows) finish(out *BodyDiff) {
	out.Rows = r.list
	if r.dropped {
		out.Notices = append(out.Notices, Notice{Code: CodeRowsLimited,
			Message: "Only the first " + strconv.Itoa(r.max) + " lines are shown."})
	}
}
