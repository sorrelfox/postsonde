package snippet

// rawhttp.go: the request as an HTTP/1.1 message (GEN-04).
// docs/snippets.md#raw-http.

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// rawBoundary is the multipart boundary of the raw message: the client
// that sends it makes up its own.
const rawBoundary = "PostSondeFormBoundary"

// RawHTTP returns r as the HTTP/1.1 message that sends it (GEN-04): the
// request line in origin form ("POST /items?x=1 HTTP/1.1") and a Host
// field, or in absolute form without Host when the URL has no scheme (a
// templated {{baseUrl}}/items); the header rows, the jar's cookies, Basic
// credentials and the Content-Type the body implies; Content-Length for a
// body held in memory; then the body as it is. Lines end in LF as shown.
//
// A file body, and each file part of a multipart body (with a fixed
// boundary), is written "< /path/to/file", as .http files write a file's
// contents. Basic credentials holding a mask or a {{reference}} cannot be
// encoded and are written "Basic user:password". Digest credentials, TLS,
// redirect, timeout and HTTP version settings are not part of a message
// and are left out. It never fails.
func RawHTTP(r Request) (string, error) {
	target, host := r.URL, ""
	if s := urlScheme(r.URL); s != "" {
		rest := r.URL[len(s)+3:]
		i := strings.IndexAny(rest, "/?")
		switch {
		case i < 0:
			host, target = rest, "/"
		case rest[i] == '?':
			host, target = rest[:i], "/"+rest[i:]
		default:
			host, target = rest[:i], rest[i:]
		}
	}
	var b strings.Builder
	b.WriteString(r.Method + " " + target + " HTTP/1.1\n")
	field := func(name, value string) { b.WriteString(name + ": " + value + "\n") }
	if host != "" && !hasHeader(r.Headers, "Host") {
		field("Host", host)
	}
	for _, h := range sentHeaders(r) {
		if !noUserAgent(h) {
			field(h.Name, h.Value)
		}
	}
	if c := r.Basic; c != nil {
		cred := c.Username + ":" + c.Password
		if !strings.Contains(cred, Mask) && !strings.Contains(cred, "{{") {
			cred = base64.StdEncoding.EncodeToString([]byte(cred))
		}
		field("Authorization", "Basic "+cred)
	}

	var body string
	switch r.Body.Kind {
	case BodyData:
		body = string(r.Body.Data)
		if !hasHeader(r.Headers, "Content-Length") {
			field("Content-Length", strconv.Itoa(len(r.Body.Data)))
		}
	case BodyFile:
		body = "< " + r.Body.File
	case BodyMultipart:
		if !hasHeader(r.Headers, "Content-Type") {
			field("Content-Type", "multipart/form-data; boundary="+rawBoundary)
		}
		var m strings.Builder
		for _, p := range r.Body.Parts {
			m.WriteString("--" + rawBoundary + "\n")
			m.WriteString("Content-Disposition: " + partDisposition(p) + "\n")
			if p.ContentType != "" {
				m.WriteString("Content-Type: " + p.ContentType + "\n")
			}
			m.WriteString("\n")
			if p.File != "" {
				m.WriteString("< " + p.File + "\n")
			} else {
				m.WriteString(p.Value + "\n")
			}
		}
		m.WriteString("--" + rawBoundary + "--")
		body = m.String()
	}
	out := strings.TrimSuffix(b.String(), "\n")
	if r.Body.Kind != BodyNone {
		out += "\n\n" + body
	}
	return out, nil
}
