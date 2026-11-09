package history

import (
	"encoding/base64"
	"mime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// CodeNoResponse: a response example was asked of an entry without a
// response (a failed or cancelled send; kind invalid_argument). Details:
// id.
const CodeNoResponse = "history.no_response"

// Example returns entry e as a response example (HTTP-38) named name
// (the status, "201 Created", when blank), saved at savedAt: the response
// and the request as sent, as the entry keeps them (bodies of at most
// MaxBody bytes, which is projectfile.ResponseExampleMaxBody).
//
// The entry was masked when it was recorded; m masks it again, so a
// secret known now but not then (a secret variable added since, an entry
// recorded by an older PostSonde) is not saved either: the URL, every
// header value (Authorization and Proxy-Authorization by name) and both
// bodies. masked reports whether m hid anything. An entry without a
// response is history.no_response.
func Example(e Entry, name string, savedAt time.Time, m httpclient.Masker) (ex projectfile.ResponseExample, masked bool, err error) {
	r := e.Response
	if r == nil {
		return ex, false, errs.New(errs.InvalidArgument, "This send got no response to save as an example.").
			WithCode(CodeNoResponse).WithDetail("id", e.ID)
	}
	if m == nil {
		m = &httpclient.SecretMasker{}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(r.Status)
		if name == "" {
			name = strconv.Itoa(r.StatusCode)
		}
	}
	ex = projectfile.ResponseExample{
		Name:       name,
		StatusCode: r.StatusCode,
		StatusText: StatusText(r.StatusCode, r.Status),
	}
	if !savedAt.IsZero() {
		ex.SavedAt = savedAt.UTC().Format(time.RFC3339)
	}
	var hidden bool
	ex.Headers, hidden = exampleHeaders(r.Headers, m)
	masked = masked || hidden
	ex.Body, ex.BodyEncoding, hidden = maskedBody(r.Body, m)
	masked = masked || hidden
	if r.Body.Clipped || r.Truncated {
		ex.BodyClipped, ex.BodySize = true, r.Body.Size
	}

	q := e.Request
	req := &projectfile.ExampleRequest{Method: q.Method, URL: m.MaskText(q.URL)}
	masked = masked || req.URL != q.URL
	req.Headers, hidden = exampleHeaders(q.Headers, m)
	masked = masked || hidden
	if q.Body.Text != "" && !q.Body.Base64 {
		// A body that is not text (Base64 in the entry) is left out: a raw
		// body is text.
		text := m.MaskText(q.Body.Text)
		masked = masked || text != q.Body.Text
		req.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: rawType(headerValue(q.Headers, "Content-Type")), Text: text}
	}
	ex.Request = req
	return ex, masked, nil
}

// StatusText returns the reason phrase of a status line after the
// protocol ("201 Created" gives "Created").
func StatusText(code int, status string) string {
	status = strings.TrimSpace(status)
	if rest, ok := strings.CutPrefix(status, strconv.Itoa(code)); ok {
		return strings.TrimSpace(rest)
	}
	return status
}

// exampleHeaders converts header fields to example rows, masked with m.
// HTTP/2 pseudo-header fields (":path") are left out: they are not
// headers of the request or response.
func exampleHeaders(hs []Header, m httpclient.Masker) (out []projectfile.KeyValue, masked bool) {
	for _, h := range hs {
		if strings.HasPrefix(h.Name, ":") {
			continue
		}
		v := m.MaskHeader(h.Name, h.Value)
		masked = masked || v != h.Value
		out = append(out, projectfile.KeyValue{Name: h.Name, Value: v})
	}
	return out, masked
}

// maskedBody returns a body snapshot's text masked with m, and "base64"
// as its encoding when the snapshot holds Base64. A Base64 body is masked
// in its decoded bytes.
func maskedBody(b Body, m httpclient.Masker) (text, encoding string, masked bool) {
	if !b.Base64 {
		text = m.MaskText(b.Text)
		return text, "", text != b.Text
	}
	raw, err := base64.StdEncoding.DecodeString(b.Text)
	if err != nil {
		return b.Text, projectfile.ExampleBodyBase64, false
	}
	s := m.MaskText(string(raw))
	if s == string(raw) {
		return b.Text, projectfile.ExampleBodyBase64, false
	}
	if utf8.ValidString(s) {
		return s, "", true
	}
	return base64.StdEncoding.EncodeToString([]byte(s)), projectfile.ExampleBodyBase64, true
}

// headerValue returns the first value of header name, or "".
func headerValue(hs []Header, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// rawType is the raw body type of a Content-Type.
func rawType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(contentType)
	}
	switch {
	case strings.Contains(mt, "json"):
		return projectfile.RawJSON
	case strings.Contains(mt, "xml"):
		return projectfile.RawXML
	case strings.Contains(mt, "html"):
		return projectfile.RawHTML
	case strings.Contains(mt, "yaml"):
		return projectfile.RawYAML
	}
	return projectfile.RawText
}
