package http

import (
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// Limits of the body ResponseDTO carries (HTTP-31). The rest of a larger
// body is kept by the service and read a page at a time (ReadBody,
// HTTP-37).
const (
	// MaxBodyText is how much of a text body ResponseDTO carries: the
	// first 1 MiB.
	MaxBodyText = 1 << 20
	// MaxPrettyBytes is the largest body Send pretty-prints on its own;
	// larger ones are formatted when the user asks (FormatService).
	MaxPrettyBytes = 256 << 10
	// MaxHexBytes is how much of a body shown as hex ResponseDTO carries.
	MaxHexBytes = 64 << 10
)

// SendInputDTO is what Send takes.
type SendInputDTO struct {
	// RequestID is the HTTP or GraphQL request to send, from the open
	// project.
	RequestID string `json:"requestId"`
	// AllowUnresolved sends {{variables}} that no scope defines as written
	// instead of refusing (the "Send anyway" answer of ENV-11).
	AllowUnresolved bool `json:"allowUnresolved,omitempty"`
	// Prompts are the values typed for the request's {{$prompt LABEL}}
	// references, by label (DYN-07). A send with a label missing refuses
	// with template.prompt (details.prompts lists what to ask). They are
	// used for this send only and never stored.
	Prompts map[string]string `json:"prompts,omitempty"`
}

// ResponseDTO is a response, as the response panel shows it.
type ResponseDTO struct {
	// Method and URL are what was sent: the method as typed and the URL
	// after variables, path parameters and auth, before redirects. URL
	// and FinalURL have their secrets masked, as in the timeline.
	Method string `json:"method"`
	URL    string `json:"url"`
	// FinalURL is the URL of the response, after redirects; Redirects
	// counts them.
	FinalURL  string `json:"finalUrl"`
	Redirects int    `json:"redirects"`
	// Proto is "HTTP/1.1" or "HTTP/2.0"; Protocol the same for labels:
	// "HTTP/1.1", "HTTP/2" (HTTP-30).
	Proto    string `json:"proto"`
	Protocol string `json:"protocol"`
	// StatusCode is the status code; Status the status line after the
	// protocol ("200 OK").
	StatusCode int    `json:"statusCode"`
	Status     string `json:"status"`
	// Headers are the response headers, sorted by name.
	Headers []HeaderDTO `json:"headers"`
	// Cookies are the response's Set-Cookie headers, parsed, in the order
	// received, with what the cookie jar did with each (HTTP-32). Only the
	// final response's: cookies set by redirects are in the timeline.
	Cookies []CookieDTO `json:"cookies"`
	// ContentType is the Content-Type header, or "".
	ContentType string `json:"contentType"`
	// Body is the body as the raw view shows it (HTTP-31): the bytes
	// received decoded with Charset, at most MaxBodyText bytes of them,
	// invalid sequences replaced by U+FFFD. When BodyHex is set it is a
	// hex dump of the first MaxHexBytes bytes instead.
	Body string `json:"body"`
	// BodyLanguage is how the body is highlighted: "json", "xml", "html"
	// or "text", from the Content-Type (or the body's first bytes when
	// there is none).
	BodyLanguage string `json:"bodyLanguage"`
	// BodyPretty is Body pretty-printed when PrettyState is "formatted":
	// JSON beautified, XML indented, two spaces per level; else "".
	BodyPretty string `json:"bodyPretty"`
	// PrettyState says why BodyPretty is set or not: "formatted";
	// "too_large" (over MaxPrettyBytes, the UI offers to format it on
	// request); "incomplete" (clipped or truncated: it cannot be parsed);
	// "invalid" (not valid JSON or XML, PrettyError says why); "" when the
	// language has no pretty-printer (HTML, text) or the body is empty or
	// hex.
	PrettyState string `json:"prettyState"`
	PrettyError string `json:"prettyError"`
	// Charset is the encoding Body was decoded with ("UTF-8", "UTF-16LE",
	// "UTF-16BE", "ISO-8859-1", "US-ASCII", "windows-1252"), "" for an
	// empty or hex body. CharsetSource is where it came from: "bom",
	// "header" (the Content-Type charset), "declaration" (the XML
	// declaration), "meta" (an HTML <meta>) or "default".
	Charset       string `json:"charset"`
	CharsetSource string `json:"charsetSource"`
	// BodyHex is set when Body is a hex dump: the charset is not supported
	// or the body is binary. BodyNote then says which.
	BodyHex  bool   `json:"bodyHex"`
	BodyNote string `json:"bodyNote"`
	// PreviewType is the media type the preview renders the body as
	// (HTTP-35, preview.go): "image/png", "image/jpeg", "image/gif",
	// "image/webp", "image/svg+xml", "image/bmp", "image/x-icon" or
	// "text/html", from the Content-Type or, without one, the body's first
	// bytes; "" when the body has no preview.
	PreviewType string `json:"previewType"`
	// BodyBinary is set when the body had bytes that are not valid in its
	// charset (shown as U+FFFD).
	BodyBinary bool `json:"bodyBinary"`
	// BodyClipped is set when Body shows only the first BodyShown bytes.
	BodyClipped bool `json:"bodyClipped"`
	// BodyShown is the number of body bytes Body shows.
	BodyShown int64 `json:"bodyShown"`
	// BodyTruncated is set when the body was larger than BodyLimit, the
	// limit of the settings (HTTP-37): the body was cut there, and a
	// warning says so. Size then counts what was read.
	BodyTruncated bool `json:"bodyTruncated"`
	// BodyLimit is the largest body kept for this send, in bytes
	// (network.maxResponseBodyMb); 0 when not known.
	BodyLimit int64 `json:"bodyLimit"`
	// Size is the number of body bytes received, as sent (compressed).
	Size int64 `json:"size"`
	// BodySize is the number of bytes of the body as kept: decompressed
	// when ContentEncoding is set, else as received (at most BodyLimit).
	// Body, ReadBody, FilterBody and SaveBody work on these bytes.
	BodySize int64 `json:"bodySize"`
	// ContentEncoding is the content coding removed while receiving
	// ("gzip" or "deflate"), "" when the body is kept as sent.
	ContentEncoding string `json:"contentEncoding"`
	// BodyID names the body's bytes, kept as received for
	// HTTPService.SaveBody (HTTP-34) for a while (MaxKeptBodies,
	// BodyTTL); "" when the service keeps no bodies.
	BodyID string `json:"bodyId"`
	// HistoryID is the request history entry this response is recorded
	// as (docs/history.md), for HistoryService.Example (HTTP-38); "" when
	// it is not recorded (no project open, a run's response, a view of a
	// stored response). The entry is written just after the response is
	// sent.
	HistoryID string `json:"historyId"`
	// FileName is the file name SaveBody suggests: from
	// Content-Disposition, else the URL, with an extension from the
	// Content-Type.
	FileName string `json:"fileName"`
	// HeaderSize is the size of the status line and headers written as
	// HTTP/1.1 text ("HTTP/1.1 200 OK\r\n", "Name: value\r\n" per header,
	// the blank line), whatever the protocol: HTTP/2 sends them
	// compressed, and that size is not known (HTTP-30).
	HeaderSize int64 `json:"headerSize"`
	// DurationMs is the whole send, redirects included.
	DurationMs int64 `json:"durationMs"`
	// TimeoutMs is the timeout the request was sent with (0 for none) and
	// TimeoutSource where it was set: "request", "settings" or "".
	TimeoutMs     int64  `json:"timeoutMs"`
	TimeoutSource string `json:"timeoutSource"`
	// InsecureTLS is set when the request was sent with TLS certificate
	// verification off (HTTP-23), and InsecureTLSSource where that was
	// set: "request" or "settings" ("" when verification was on). The
	// response view shows a warning badge.
	InsecureTLS       bool   `json:"insecureTls"`
	InsecureTLSSource string `json:"insecureTlsSource"`
	// RequestProxy is the request's own proxy setting when it has one
	// (docs/network.md#a-requests-own-proxy): the proxy URL without its
	// password for a custom proxy, "none" when it connects directly
	// whatever proxy is in effect, "" when it inherits the proxy in effect.
	// The connection's Via says where it actually went.
	RequestProxy string `json:"requestProxy"`
	// Warnings are problems that did not stop the send, such as a body
	// dropped for HEAD.
	Warnings []WarningDTO `json:"warnings"`
	// Timeline is what went over the wire, hop by hop, secrets masked
	// (DBG-01).
	Timeline TimelineDTO `json:"timeline"`
	// Tests are the results of the request's enabled assertions, in the
	// editor's order (TEST-01, TEST-06): empty when it has none, and for a
	// response that is not a request's own (a replayed webhook, a body
	// view).
	Tests []TestResultDTO `json:"tests"`
	// Extractions are the results of the request's enabled extraction
	// rules, in the editor's order (TEST-05, extract.go): what each set,
	// or why not. Empty when it has none, and for a response that is not
	// a request's own.
	Extractions []ExtractionResultDTO `json:"extractions"`
	// Incremental is set for a GraphQL request answered with incremental
	// delivery (GQL-08, @defer and @stream: a multipart/mixed or
	// text/event-stream body): its payloads and their merged result. While
	// such a body arrives, Send sends partial responses with
	// Incremental.Receiving set before the final one. Null otherwise.
	Incremental *IncrementalDTO `json:"incremental"`
}

// HeaderDTO is a response header.
type HeaderDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// WarningDTO is a problem that did not stop the send.
type WarningDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// redirectCount counts the redirects among hops; the others are auth
// challenges.
func redirectCount(hops []httpclient.Hop) int {
	n := 0
	for _, h := range hops {
		if h.Kind != httpclient.HopAuth {
			n++
		}
	}
	return n
}

// toResponseDTO turns a response into its DTO. jar is the send's cookie
// jar, nil when there was none; now is when the response arrived; m masks
// the secrets of the timeline, nil only the Authorization fields.
func toResponseDTO(b request.HTTPBuild, r *httpclient.Response, jar *jarRecorder, now time.Time, m httpclient.Masker) ResponseDTO {
	if m == nil {
		m = &httpclient.SecretMasker{}
	}
	out := ResponseDTO{
		Method: b.Request.Method,
		// Masked like the timeline: the URLs carry the secret variables
		// used in them, and the UI shows them (the redirects badge's title).
		URL:               m.MaskText(b.Request.URL),
		FinalURL:          m.MaskText(r.URL),
		Redirects:         redirectCount(r.Redirects),
		Proto:             r.Proto,
		Protocol:          protocolLabel(r.Proto),
		StatusCode:        r.StatusCode,
		Status:            r.Status,
		Headers:           make([]HeaderDTO, 0, len(r.Headers)),
		BodyTruncated:     r.BodyTruncated,
		Size:              r.Size,
		DurationMs:        r.Duration.Milliseconds(),
		TimeoutMs:         b.Request.Settings.Timeout.Milliseconds(),
		TimeoutSource:     b.TimeoutSource,
		InsecureTLS:       b.InsecureSkipVerify,
		InsecureTLSSource: b.TLSSource,
		RequestProxy:      b.Proxy.String(),
		Warnings:          make([]WarningDTO, 0, len(b.Warnings)),
		Tests:             make([]TestResultDTO, 0),
		Extractions:       make([]ExtractionResultDTO, 0),
	}
	out.HeaderSize = int64(len("HTTP/1.1 ") + len(r.Status) + len("\r\n\r\n"))
	for _, h := range r.Headers {
		out.Headers = append(out.Headers, HeaderDTO{Name: h.Name, Value: h.Value})
		out.HeaderSize += int64(len(h.Name) + len(": ") + len(h.Value) + len("\r\n"))
		if out.ContentType == "" && strings.EqualFold(h.Name, "Content-Type") {
			out.ContentType = h.Value
		}
	}
	out.Cookies = responseCookies(r.Headers, jar, now)
	if jar != nil && b.Request.Settings.SkipStoringCookies {
		// The request's storeCookies is off (HTTP-26): the jar never saw
		// them.
		for i := range out.Cookies {
			out.Cookies[i].Jar, out.Cookies[i].JarReason, out.Cookies[i].JarMessage = JarSkipped, "", skippedMessage
		}
	}
	disposition := ""
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, "Content-Disposition") {
			disposition = h.Value
			break
		}
	}
	out.FileName = suggestFileName(disposition, r.URL, out.ContentType)
	for _, w := range b.Warnings {
		out.Warnings = append(out.Warnings, WarningDTO{Code: w.Code, Message: w.Message})
	}

	if m == nil {
		m = &httpclient.SecretMasker{}
	}
	out.Timeline = toTimelineDTO(r, m)

	out.BodySize = max(r.DecodedSize, int64(len(r.Body)))
	out.ContentEncoding = r.Encoding
	setBody(&out, r.Body)
	addNote(&out, encodingNote(r))
	return out
}

// encodingNote says what happened to the body's Content-Encoding when the
// view should know: decompression failed part way, or the coding is not
// one PostSonde removes.
func encodingNote(r *httpclient.Response) string {
	if r.DecodeError != "" {
		return r.DecodeError + " What was received is shown."
	}
	if r.Encoding != "" || len(r.Body) == 0 {
		return ""
	}
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, "Content-Encoding") && !strings.EqualFold(strings.TrimSpace(h.Value), "identity") {
			return "The body is compressed with " + h.Value + ", which PostSonde does not decompress; it is shown as received."
		}
	}
	return ""
}

// addNote puts note before out.BodyNote.
func addNote(out *ResponseDTO, note string) {
	switch {
	case note == "":
	case out.BodyNote == "":
		out.BodyNote = note
	default:
		out.BodyNote = note + " " + out.BodyNote
	}
}

// protocolLabel turns a response's Proto into its usual name: "HTTP/2"
// for "HTTP/2.0", else Proto as it is ("HTTP/1.1", "HTTP/1.0").
func protocolLabel(proto string) string {
	if proto == "HTTP/2.0" {
		return "HTTP/2"
	}
	return proto
}
