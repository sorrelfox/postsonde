package postman

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// Saved examples (HTTP-38): a Postman item's "response" list and a
// PostSonde request's responseExamples map onto each other. A Postman
// example is a name, the original request (method, headers, body, URL),
// the status (code and reason phrase), the response headers and the body
// as text; so is a response example (docs/file-format.md#response-examples).
// Both ways, the credentials of Authorization and Proxy-Authorization
// headers are masked, bodies are at most projectfile.ResponseExampleMaxBody
// bytes, and the example's text is taken as it is: {{…}} in it is not a
// variable.

// maskedHeader masks the credentials of an Authorization field; other
// values are kept.
var maskedHeader = (&httpclient.SecretMasker{}).MaskHeader

// examples maps an item's "response" list to response examples. It
// reports, in one info, what was shortened.
func (c *collector) examples(responses *yaml.Node, loc importer.Location) ([]projectfile.ResponseExample, []importer.Diagnostic) {
	var out []projectfile.ResponseExample
	clipped := 0
	for _, r := range items(responses) {
		if !isMap(r) {
			continue
		}
		name := strings.TrimSpace(sget(r, "name"))
		if name == "" {
			name = "Example " + strconv.Itoa(len(out)+1)
		}
		ex := projectfile.ResponseExample{Name: name, StatusText: strings.TrimSpace(sget(r, "status"))}
		ex.StatusCode, _ = strconv.Atoi(strings.TrimSpace(sget(r, "code")))
		ex.Headers = exampleRows(get(r, "header"))
		body := sget(r, "body")
		if len(body) > projectfile.ResponseExampleMaxBody {
			ex.BodyClipped, ex.BodySize = true, int64(len(body))
			body = truncate(body, projectfile.ResponseExampleMaxBody)
			clipped++
		}
		ex.Body = body
		if orig := get(r, "originalRequest"); isMap(orig) {
			ex.Request = c.exampleRequest(orig, loc)
		}
		out = append(out, ex)
	}
	if clipped == 0 {
		return out, nil
	}
	return out, []importer.Diagnostic{{Code: CodeExamples, Severity: importer.SeverityInfo, Source: loc,
		Message: "The body of " + plural(clipped, "a saved example was", strconv.Itoa(clipped)+" saved examples were") + " longer than 64 KiB and was shortened."}}
}

// exampleRequest maps an example's original request.
func (c *collector) exampleRequest(n *yaml.Node, loc importer.Location) *projectfile.ExampleRequest {
	t := &texts{}
	out := &projectfile.ExampleRequest{Method: strings.ToUpper(strings.TrimSpace(sget(n, "method")))}
	if out.Method == "" {
		out.Method = "GET"
	}
	u := get(n, "url")
	if isMap(u) {
		out.URL = strings.TrimSpace(sget(u, "raw"))
		if out.URL == "" {
			out.URL = buildURL(u)
		}
	} else {
		out.URL = strings.TrimSpace(str(u))
	}
	out.Headers = exampleRows(get(n, "header"))
	if b := get(n, "body"); isMap(b) && sget(b, "mode") != "graphql" {
		out.Body, _, _ = mapBody(b, nil, t, loc)
		if out.Body.Mode == projectfile.BodyNone {
			out.Body = projectfile.Body{}
		}
	}
	return out
}

// exampleRows maps an example's header list as it is: no variables, the
// credentials of Authorization masked.
func exampleRows(n *yaml.Node) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	for _, h := range items(n) {
		key, value := sget(h, "key"), str(get(h, "value"))
		if key == "" && value == "" {
			continue
		}
		out = append(out, projectfile.KeyValue{Name: key, Value: maskedHeader(key, value), Disabled: isTrue(get(h, "disabled"))})
	}
	return out
}

// pmResponse is a saved example of an item.
type pmResponse struct {
	Name            string     `json:"name"`
	OriginalRequest *pmRequest `json:"originalRequest,omitempty"`
	Status          string     `json:"status,omitempty"`
	Code            int        `json:"code,omitempty"`
	PreviewLanguage string     `json:"_postman_previewlanguage,omitempty"`
	Header          []pmHeader `json:"header"`
	Cookie          []any      `json:"cookie"`
	Body            string     `json:"body"`
}

// responses writes a request's response examples as its saved examples.
// A Base64 (binary) body has no text form in Postman and is left out, a
// clipped one is exported as kept; both are reported.
func responses(list []projectfile.ResponseExample, add func(string, importer.Severity, string)) *[]any {
	out := make([]any, 0, len(list))
	var binary, clipped []string
	for _, ex := range list {
		r := pmResponse{Name: displayName(ex.Name, "Example"), Status: ex.StatusText, Code: ex.StatusCode, Header: exampleHeaders(ex.Headers), Cookie: []any{}}
		switch {
		case ex.BodyEncoding != "":
			binary = append(binary, r.Name)
		default:
			r.Body = ex.Body
			if ex.BodyClipped {
				clipped = append(clipped, r.Name)
			}
		}
		r.PreviewLanguage = previewLanguage(ex.Headers, r.Body)
		if q := ex.Request; q != nil {
			// The text is as sent: nothing in it is a template, so the scan
			// and its notes are not kept.
			quiet := func(string, importer.Severity, string) {}
			req := &pmRequest{Method: displayName(q.Method, "GET"), Header: exampleHeaders(q.Headers), URL: urlObject(q.URL, nil, nil, &scan{}, quiet)}
			req.Body, _ = body(q.Body, nil, &scan{}, quiet)
			r.OriginalRequest = req
		}
		out = append(out, r)
	}
	if len(binary) > 0 {
		add(CodeExportChanged, importer.SeverityWarning, pick(len(binary) == 1, "The body of the response example ", "The bodies of the response examples ")+quoteList(binary)+
			pick(len(binary) == 1, " is", " are")+" binary and not exported: Postman keeps an example's body as text.")
	}
	if len(clipped) > 0 {
		add(CodeExportChanged, importer.SeverityInfo, pick(len(clipped) == 1, "The response example ", "The response examples ")+quoteList(clipped)+
			pick(len(clipped) == 1, " has", " have")+" only the start of the body, as PostSonde kept it.")
	}
	return &out
}

// exampleHeaders writes an example's header rows, Authorization masked.
func exampleHeaders(rows []projectfile.KeyValue) []pmHeader {
	out := []pmHeader{}
	for _, h := range rows {
		out = append(out, pmHeader{Key: h.Name, Value: maskedHeader(h.Name, h.Value), Type: "text", Disabled: h.Disabled})
	}
	return out
}

// previewLanguage is the language Postman shows an example's body in,
// from its Content-Type.
func previewLanguage(rows []projectfile.KeyValue, body string) string {
	if body == "" || !utf8.ValidString(body) {
		return ""
	}
	for _, h := range rows {
		if !strings.EqualFold(h.Name, "Content-Type") {
			continue
		}
		switch v := strings.ToLower(h.Value); {
		case strings.Contains(v, "json"):
			return "json"
		case strings.Contains(v, "xml"):
			return "xml"
		case strings.Contains(v, "html"):
			return "html"
		case strings.Contains(v, "javascript"):
			return "javascript"
		}
	}
	return "text"
}
