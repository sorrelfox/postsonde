package httpclient

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// EventRequestOptions is the timeline event of the request options that
// differ from DefaultSettings (HTTP-24, HTTP-25, HTTP-26), recorded once
// at the start of a send: "HTTP/1.1 only, URL encoding RFC 3986, jar
// cookies not sent". Nothing is recorded when all are the defaults.
const EventRequestOptions = "request_options"

// CodeHTTP2Unsupported: the request was restricted to HTTP/2
// (HTTPVersion2) and the server did not negotiate it: no h2 in ALPN on
// https, or no h2c on plain http.
const CodeHTTP2Unsupported = "httpclient.http2_unsupported"

// optionsDetail describes the options of s that differ from
// DefaultSettings, for EventRequestOptions; "" when none do. The cookie
// switches only count with a cookie jar.
func optionsDetail(s Settings, jar bool) string {
	var parts []string
	switch s.HTTPVersion {
	case HTTPVersion1:
		parts = append(parts, "HTTP/1.1 only")
	case HTTPVersion2:
		parts = append(parts, "HTTP/2 only")
	}
	switch s.URLEncoding {
	case URLEncodingAsTyped:
		parts = append(parts, "URL not encoded")
	case URLEncodingRFC3986:
		parts = append(parts, "URL encoding RFC 3986")
	}
	if jar && s.SkipSendingCookies {
		parts = append(parts, "jar cookies not sent")
	}
	if jar && s.SkipStoringCookies {
		parts = append(parts, "response cookies not stored")
	}
	return strings.Join(parts, ", ")
}

// noHTTP2 reports whether err, a failed send restricted to HTTP/2 and
// mapped to e, failed because the server does not speak HTTP/2: it
// refused the h2 ALPN offer, or answered the h2c connection preface in
// HTTP/1.x.
func noHTTP2(err error, e *errs.Error) bool {
	// crypto/tls reports the no_application_protocol alert (RFC 7301
	// section 3.2) with an unexported type, so it is matched by its text.
	msg := err.Error()
	if strings.Contains(msg, "no application protocol") || strings.Contains(msg, "unsupported protocol") ||
		strings.Contains(msg, "looked like an HTTP/1.1 header") {
		return true
	}
	switch e.Code {
	case CodeMalformedResponse, CodeConnectionClosed:
		// Plain http: a server that does not know the preface answers in
		// HTTP/1.x or hangs up.
		return true
	}
	return false
}
