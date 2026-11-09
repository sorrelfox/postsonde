// Package mockexample turns a response a request got, a response example
// (HTTP-38) or a history entry made into one, into a mock route
// (MOCK-03): the route's method and path come from the request's URL as
// typed, with a path parameter wherever the request used a {{variable}}
// or a :param, and its status, headers and body from the response, so the
// mock server answers as the real server did. Merge puts the route into a
// mock server's configuration, as a new route, as a named response of the
// route already answering that method and path, or in place of that
// route's own response. Standard library only; see
// docs/mock-routes.md#routes-from-a-response.
package mockexample

import (
	"encoding/base64"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
)

// Example is the response to mock, as a response example keeps it.
type Example struct {
	// Name is the example's name ("200 OK"); a named response gets it.
	Name string
	// StatusCode and StatusText are the status ("Created").
	StatusCode int
	StatusText string
	// Headers are the response's header fields in the order received.
	Headers []Header
	// Body is the body as text, or its bytes in standard Base64 when
	// Base64 is set.
	Body   string
	Base64 bool
	// Clipped says Body holds only the first bytes of the body, of Size
	// bytes.
	Clipped bool
	Size    int64
	// SentMethod and SentURL are the request as sent (variables resolved,
	// secrets masked); "" when not known.
	SentMethod string
	SentURL    string
}

// Header is one header field.
type Header struct {
	Name  string
	Value string
}

// Input is what Build takes.
type Input struct {
	Example Example
	// Method and URL are the request's, as typed: "{{baseUrl}}/users/:id".
	// An empty URL uses the URL sent; an empty method the method sent,
	// else GET.
	Method string
	URL    string
	// Path, when not nil, is the route's path pattern instead of the one
	// made from the URL.
	Path *string
	// Masker masks the secrets PostSonde knows now, again: every header
	// value (Authorization and Proxy-Authorization by name) and the body.
	// nil masks those two headers only.
	Masker httpclient.Masker
}

// Severity of a Note.
type Severity string

// Severities.
const (
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Note codes.
const (
	// CodeClipped: the example keeps only the start of the body.
	CodeClipped = "mockexample.clipped"
	// CodeBinary: the body is not UTF-8 and is sent from Base64.
	CodeBinary = "mockexample.binary"
	// CodeInvalidBase64: the example's Base64 body does not decode; the
	// route has no body.
	CodeInvalidBase64 = "mockexample.invalid_base64"
	// CodeNoBodyStatus: 204 and 304 are sent without a body.
	CodeNoBodyStatus = "mockexample.no_body_status"
	// CodeNoContentType: the response had no Content-Type; the route
	// sends its body type's.
	CodeNoContentType = "mockexample.no_content_type"
	// CodeReasonPhrase: the response's reason phrase is not the standard
	// one, which the mock server sends.
	CodeReasonPhrase = "mockexample.reason_phrase"
	// CodeMasked: secrets were hidden.
	CodeMasked = "mockexample.masked"
	// CodePath: a note about the path made from the URL.
	CodePath = "mockexample.path"
)

// Note is something the user should know about the route made.
type Note struct {
	Severity Severity
	Code     string
	Message  string
}

// Reasons a header field is left out of the route.
const (
	// DropHopByHop: a hop-by-hop field (Connection, Keep-Alive,
	// Transfer-Encoding…, or named in Connection) that belongs to one
	// connection, not to the response.
	DropHopByHop = "hop_by_hop"
	// DropContentLength: the mock server sets Content-Length from the
	// body it sends.
	DropContentLength = "content_length"
	// DropContentEncoding: PostSonde decompressed the body (gzip,
	// deflate), so it is not encoded any more.
	DropContentEncoding = "content_encoding"
	// DropDate: the mock server sends the current date.
	DropDate = "date"
	// DropInvalid: the name is not a token or the value has a control
	// character; the route would not be served with it.
	DropInvalid = "invalid"
)

// Dropped is a header field left out of the route, and why.
type Dropped struct {
	Name, Value string
	// Reason is a Drop constant.
	Reason string
}

// Result is the route Build made.
type Result struct {
	// Method is the route's method; Path its pattern (Input.Path when
	// given); DerivedPath the pattern made from the URL.
	Method      string
	Path        string
	DerivedPath string
	// Reply is what the route sends: status, headers, body; named after
	// the example, no rules, not templated.
	Reply mockhttp.Response
	// Dropped are the header fields left out, in the order received.
	Dropped []Dropped
	Notes   []Note
	// Masked says a secret was hidden in a header value or the body.
	Masked bool
}

// hopByHop are the connection-specific fields of RFC 9110 section 7.6.1,
// and those HTTP/1.0 and proxies still use.
var hopByHop = map[string]bool{
	"connection":        true,
	"keep-alive":        true,
	"proxy-connection":  true,
	"te":                true,
	"trailer":           true,
	"transfer-encoding": true,
	"upgrade":           true,
}

// Build makes the route of in. It never fails: what cannot be kept is in
// Dropped and Notes, and a status the mock server refuses shows in
// mockhttp.Check.
func Build(in Input) Result {
	ex := in.Example
	m := in.Masker
	if m == nil {
		m = &httpclient.SecretMasker{}
	}
	out := Result{Method: strings.TrimSpace(in.Method)}
	if out.Method == "" {
		out.Method = strings.TrimSpace(ex.SentMethod)
	}
	if out.Method == "" {
		out.Method = http.MethodGet
	}
	var pathNotes []string
	out.DerivedPath, pathNotes = RoutePath(in.URL, ex.SentURL)
	out.Path = out.DerivedPath
	if in.Path != nil {
		out.Path = strings.TrimSpace(*in.Path)
	} else {
		for _, n := range pathNotes {
			out.Notes = append(out.Notes, Note{Severity: SeverityInfo, Code: CodePath, Message: n})
		}
	}

	name := strings.TrimSpace(ex.Name)
	out.Reply = mockhttp.Response{Name: name, Status: ex.StatusCode, Headers: []mockhttp.Header{}}
	if std := http.StatusText(ex.StatusCode); ex.StatusText != "" && std != "" && !strings.EqualFold(strings.TrimSpace(ex.StatusText), std) {
		out.Notes = append(out.Notes, Note{Severity: SeverityInfo, Code: CodeReasonPhrase,
			Message: "The response's reason phrase \"" + strings.TrimSpace(ex.StatusText) + "\" is not sent: the mock server sends the standard one, \"" + std + "\"."})
	}

	out.Reply.Headers, out.Dropped, out.Masked = headers(ex.Headers, m)
	contentType := ""
	for _, h := range out.Reply.Headers {
		if strings.EqualFold(h.Name, "Content-Type") {
			contentType = h.Value
			break
		}
	}

	body, masked, notes := buildBody(ex, contentType, m)
	out.Masked = out.Masked || masked
	out.Notes = append(out.Notes, notes...)
	if body.Mode == mockhttp.BodyRaw && (ex.StatusCode == http.StatusNoContent || ex.StatusCode == http.StatusNotModified) {
		out.Notes = append(out.Notes, Note{Severity: SeverityInfo, Code: CodeNoBodyStatus,
			Message: "A " + strconv.Itoa(ex.StatusCode) + " response has no body; the body is left out."})
		body = mockhttp.Body{Mode: mockhttp.BodyNone}
	}
	if body.Mode == mockhttp.BodyRaw && contentType == "" {
		ct := "application/octet-stream"
		if body.Type != mockhttp.BodyTypeBase64 {
			ct = "text/plain; charset=utf-8"
		}
		out.Notes = append(out.Notes, Note{Severity: SeverityInfo, Code: CodeNoContentType,
			Message: "The response had no Content-Type; the mock server sends " + ct + ". Add a Content-Type header to the route to change it."})
	}
	out.Reply.Body = body
	if out.Masked {
		out.Notes = append(out.Notes, Note{Severity: SeverityInfo, Code: CodeMasked,
			Message: "Secrets in the response are hidden (" + httpclient.Mask + "); the route sends them hidden."})
	}
	return out
}

// headers returns the fields of hs the route sends, masked with m, and
// those left out.
func headers(hs []Header, m httpclient.Masker) (kept []mockhttp.Header, dropped []Dropped, masked bool) {
	kept = []mockhttp.Header{}
	// Fields named in Connection are hop-by-hop too.
	named := map[string]bool{}
	var encodings []string
	for _, h := range hs {
		switch strings.ToLower(strings.TrimSpace(h.Name)) {
		case "connection":
			for _, tok := range strings.Split(h.Value, ",") {
				if tok = strings.ToLower(strings.TrimSpace(tok)); tok != "" {
					named[tok] = true
				}
			}
		case "content-encoding":
			encodings = append(encodings, h.Value)
		}
	}
	// PostSonde decompresses a body with one gzip or deflate coding
	// (httpclient's SendTo): the example keeps the decoded body.
	decoded := len(encodings) == 1 && decodedCoding(encodings[0])
	for _, h := range hs {
		name := strings.TrimSpace(h.Name)
		lower := strings.ToLower(name)
		if strings.HasPrefix(name, ":") {
			continue
		}
		v := m.MaskHeader(name, h.Value)
		masked = masked || v != h.Value
		reason := ""
		switch {
		case hopByHop[lower] || named[lower]:
			reason = DropHopByHop
		case lower == "content-length":
			reason = DropContentLength
		case lower == "content-encoding" && decoded:
			reason = DropContentEncoding
		case lower == "date":
			reason = DropDate
		case !isToken(name) || !validValue(v):
			reason = DropInvalid
		}
		if reason != "" {
			dropped = append(dropped, Dropped{Name: name, Value: v, Reason: reason})
			continue
		}
		kept = append(kept, mockhttp.Header{Enabled: true, Name: name, Value: v})
	}
	return kept, dropped, masked
}

// decodedCoding reports whether PostSonde removes the content coding v
// while receiving (httpclient: gzip, x-gzip, deflate), or v is identity.
func decodedCoding(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "gzip", "x-gzip", "deflate", "identity":
		return true
	}
	return false
}

// buildBody returns the route body of ex, masked with m.
func buildBody(ex Example, contentType string, m httpclient.Masker) (body mockhttp.Body, masked bool, notes []Note) {
	if ex.Clipped {
		size := ex.Size
		notes = append(notes, Note{Severity: SeverityWarning, Code: CodeClipped,
			Message: "The example keeps only the first " + formatSize(int64(bodyLen(ex))) + " of the " + formatSize(size) +
				" body; the route sends that part."})
	}
	if ex.Body == "" {
		return mockhttp.Body{Mode: mockhttp.BodyNone}, false, notes
	}
	if !ex.Base64 {
		text := m.MaskText(ex.Body)
		return mockhttp.Body{Mode: mockhttp.BodyRaw, Type: rawType(contentType), Text: text}, text != ex.Body, notes
	}
	raw, err := base64.StdEncoding.DecodeString(ex.Body)
	if err != nil {
		notes = append(notes, Note{Severity: SeverityWarning, Code: CodeInvalidBase64,
			Message: "The example's body is not valid Base64; the route has no body."})
		return mockhttp.Body{Mode: mockhttp.BodyNone}, false, notes
	}
	s := m.MaskText(string(raw))
	masked = s != string(raw)
	if masked && utf8.ValidString(s) {
		return mockhttp.Body{Mode: mockhttp.BodyRaw, Type: rawType(contentType), Text: s}, true, notes
	}
	notes = append(notes, Note{Severity: SeverityInfo, Code: CodeBinary,
		Message: "The body is not text; the route keeps it in Base64 and sends the bytes."})
	return mockhttp.Body{Mode: mockhttp.BodyRaw, Type: mockhttp.BodyTypeBase64, Text: base64.StdEncoding.EncodeToString([]byte(s))}, masked, notes
}

// bodyLen is the number of bytes ex.Body holds.
func bodyLen(ex Example) int {
	if ex.Base64 {
		return base64.StdEncoding.DecodedLen(len(ex.Body))
	}
	return len(ex.Body)
}

// rawType is the route body type of a Content-Type: its language, else
// text.
func rawType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(contentType)
	}
	switch {
	case strings.Contains(mt, "json"):
		return "json"
	case strings.Contains(mt, "xml"):
		return "xml"
	case strings.Contains(mt, "html"):
		return "html"
	case strings.Contains(mt, "yaml"):
		return "yaml"
	}
	return "text"
}

func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func validValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
