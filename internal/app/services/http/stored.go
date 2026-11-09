package http

import (
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// A stored response (HTTP-38, HTTP-39) is one PostSonde kept rather than
// just received: a request history entry or a response example saved with
// a request. StoredResponse turns it into the ResponseDTO the response
// view shows, so an earlier response or an example is shown like the
// latest one. It keeps what was stored: at most 64 KiB of the body, the
// request as sent, and no timing, connection or trace.

// CodeInvalidStored: a stored response's body is not valid Base64 (kind
// invalid_argument).
const CodeInvalidStored = "http.invalid_stored_response"

// StoredResponseInputDTO is a stored response: what the history or a
// response example keeps of a send.
type StoredResponseInputDTO struct {
	// Method and URL are the request's, as sent; RequestHeaders every
	// header field written, RequestBody the body sent (Base64 when
	// RequestBodyBase64).
	Method            string      `json:"method"`
	URL               string      `json:"url"`
	RequestHeaders    []HeaderDTO `json:"requestHeaders"`
	RequestBody       string      `json:"requestBody"`
	RequestBodyBase64 bool        `json:"requestBodyBase64"`
	// Proto is "HTTP/1.1" or "HTTP/2.0", "" when not known. FinalURL is
	// the URL after Redirects redirects, "" for URL.
	Proto     string `json:"proto"`
	FinalURL  string `json:"finalUrl"`
	Redirects int    `json:"redirects"`
	// StatusCode is the status code; Status the status line after the
	// protocol ("200 OK"), or only the reason phrase ("OK").
	StatusCode int         `json:"statusCode"`
	Status     string      `json:"status"`
	Headers    []HeaderDTO `json:"headers"`
	// Body is the body kept, Base64 when BodyBase64. BodySize counts the
	// whole body; BodyClipped says Body holds only its start.
	Body        string `json:"body"`
	BodyBase64  bool   `json:"bodyBase64"`
	BodySize    int64  `json:"bodySize"`
	BodyClipped bool   `json:"bodyClipped"`
	DurationMs  int64  `json:"durationMs"`
}

// StoredResponse returns the view of a stored response (HTTP-38, HTTP-39):
// the status line, headers, cookies and body as a sent response's DTO
// shows them (the body decoded, highlighted and pretty-printed the same
// way), and a timeline of one exchange with the request as sent and the
// response head. The cookies are parsed without a jar; nothing is kept
// for SaveBody (BodyID is ""). Secrets were masked when it was stored.
//
// Errors: http.invalid_stored_response when a Base64 body does not
// decode.
func (s *HTTPService) StoredResponse(ctx context.Context, in StoredResponseInputDTO) (ResponseDTO, error) {
	if err := ctx.Err(); err != nil {
		return ResponseDTO{}, errs.Wrap(err, errs.KindOf(err), "Showing the response was interrupted.")
	}
	body, err := storedBytes(in.Body, in.BodyBase64, "response body")
	if err != nil {
		return ResponseDTO{}, err
	}
	reqBody, err := storedBytes(in.RequestBody, in.RequestBodyBase64, "request body")
	if err != nil {
		return ResponseDTO{}, err
	}
	return storedResponseDTO(in, body, reqBody, time.Now()), nil
}

func storedBytes(text string, b64 bool, what string) ([]byte, error) {
	if !b64 {
		return []byte(text), nil
	}
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, errs.Wrap(err, errs.InvalidArgument, "The stored "+what+" is not valid Base64.").WithCode(CodeInvalidStored)
	}
	return raw, nil
}

// storedResponseDTO builds the view of in, whose bodies are body and
// reqBody.
func storedResponseDTO(in StoredResponseInputDTO, body, reqBody []byte, now time.Time) ResponseDTO {
	status := strings.TrimSpace(in.Status)
	if code := strconv.Itoa(in.StatusCode); !strings.HasPrefix(status, code) {
		status = strings.TrimSpace(code + " " + status)
	}
	final := in.FinalURL
	if final == "" {
		final = in.URL
	}
	headers := make([]httpclient.Header, 0, len(in.Headers))
	for _, h := range in.Headers {
		headers = append(headers, httpclient.Header{Name: h.Name, Value: h.Value, Enabled: true})
	}
	out := ResponseDTO{
		Method:      in.Method,
		URL:         in.URL,
		FinalURL:    final,
		Redirects:   in.Redirects,
		Proto:       in.Proto,
		Protocol:    protocolLabel(in.Proto),
		StatusCode:  in.StatusCode,
		Status:      status,
		Headers:     make([]HeaderDTO, 0, len(in.Headers)),
		Size:        max(in.BodySize, int64(len(body))),
		DurationMs:  in.DurationMs,
		Warnings:    make([]WarningDTO, 0),
		Tests:       make([]TestResultDTO, 0),
		Extractions: make([]ExtractionResultDTO, 0),
	}
	out.BodySize = out.Size
	out.HeaderSize = int64(len("HTTP/1.1 ") + len(status) + len("\r\n\r\n"))
	for _, h := range headers {
		out.Headers = append(out.Headers, HeaderDTO{Name: h.Name, Value: h.Value})
		out.HeaderSize += int64(len(h.Name) + len(": ") + len(h.Value) + len("\r\n"))
		if out.ContentType == "" && strings.EqualFold(h.Name, "Content-Type") {
			out.ContentType = h.Value
		}
	}
	out.Cookies = responseCookies(headers, nil, now)
	out.FileName = suggestFileName(headerOf(headers, "Content-Disposition"), final, out.ContentType)

	// A clipped body is shown as the start it is, never pretty-printed
	// (it would not parse).
	clipped := in.BodyClipped || int64(len(body)) < out.Size
	out.BodyClipped = clipped
	setBody(&out, body)
	out.BodyClipped = out.BodyClipped || clipped

	sent := &httpclient.SentRequest{Method: in.Method, URL: in.URL, Proto: in.Proto, Target: target(in.URL), Body: reqBody, BodySize: int64(len(reqBody))}
	for _, h := range in.RequestHeaders {
		sent.Headers = append(sent.Headers, httpclient.Header{Name: h.Name, Value: h.Value, Enabled: true})
	}
	m := &httpclient.SecretMasker{}
	req, _ := sentRequestDTO(sent, m)
	out.Timeline = TimelineDTO{
		Hops: []TimelineHopDTO{{
			Kind:          HopFinal,
			Request:       req,
			ResponseProto: in.Proto,
			StatusCode:    in.StatusCode,
			Status:        status,
			ResponseHead:  responseHead(in.Proto, status, headers, m),
		}},
		Events: make([]TimelineEventDTO, 0),
	}
	return out
}

// headerOf returns the first value of header name, or "".
func headerOf(hs []httpclient.Header, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// target is the request-target of raw ("/a?b=1"), "/" when it has none.
func target(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "/"
	}
	return u.RequestURI()
}
