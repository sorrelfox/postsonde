package http

import (
	"context"
	nethttp "net/http"
	"net/url"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// HeaderPreviewInputDTO is what HeaderPreview takes.
type HeaderPreviewInputDTO struct {
	RequestID string `json:"requestId"`
}

// Sources of a previewed header (PreviewHeaderDTO.Source, HTTP-09).
const (
	// PreviewFromRequest, PreviewFromFolder and PreviewFromAuth: a header
	// row of the request, of a folder (COL-03), or the auth's header.
	PreviewFromRequest = request.HeaderFromRequest
	PreviewFromFolder  = request.HeaderFromFolder
	PreviewFromAuth    = request.HeaderFromAuth
	// PreviewFromBody: Content-Type, Content-Length or Transfer-Encoding
	// taken from the body.
	PreviewFromBody = request.HeaderFromBody
	// PreviewFromURL: Host from the URL, or Authorization from the URL's
	// userinfo.
	PreviewFromURL = "url"
	// PreviewFromCookies: the cookie jar's cookies (alone, or appended to
	// a Cookie row).
	PreviewFromCookies = "cookies"
	// PreviewFromClient: another field the client writes itself.
	PreviewFromClient = "client"
)

// PreviewHeaderDTO is one header field of a HeaderPreview.
type PreviewHeaderDTO struct {
	Name string `json:"name"`
	// Value is the value, secrets masked as the timeline masks them
	// (Masked set when something was).
	Value  string `json:"value"`
	Masked bool   `json:"masked"`
	// Source says where the field comes from: one of the Preview*
	// sources.
	Source string `json:"source"`
	// Reason is why a field in HeaderPreviewDTO.Suppressed is not sent:
	// "overridden" (a header row of the same name wins) or "omitted"
	// (settings.omitHeaders, or settings.sendCookies false for the
	// jar's cookies). "" for a sent field.
	Reason string `json:"reason"`
}

// HeaderPreviewDTO are the headers a send of the request would write
// (HTTP-09), as HeaderPreview computes them.
type HeaderPreviewDTO struct {
	// Headers are the fields written, in the order HTTP/1.1 writes them.
	Headers []PreviewHeaderDTO `json:"headers"`
	// Suppressed are the automatic headers not sent, with their Reason.
	Suppressed []PreviewHeaderDTO `json:"suppressed"`
	// Digest is set when the Authorization field is computed from the
	// server's Digest challenge, after a first send without it.
	Digest bool `json:"digest"`
	// HTTPVersion is the request's HTTP version setting: "auto", "http1"
	// or "http2". Over HTTP/2 the names go out lower-cased, and Host as
	// the :authority pseudo-header field.
	HTTPVersion string `json:"httpVersion"`
	// Unresolved are the undefined {{variables}}, shown as written.
	Unresolved []string `json:"unresolved"`
}

// HeaderPreview returns the header fields a send of request in.RequestID
// would write, without sending anything (HTTP-09): the request is built
// as Send builds it (request.BuildHTTP or BuildGraphQL, with variables,
// folder headers, auth, settings and the cookie jar's cookies), then its
// head is written by httpclient.Preview, the client's own code. Each field
// says where it comes from, so the editor can show the ones the user did
// not type, and the automatic headers the build left out are listed with
// the reason. Secrets are masked as in the timeline.
//
// Undefined variables do not fail it: they stay as written and are listed
// in Unresolved. It fails as Send does for other build errors.
func (s *HTTPService) HeaderPreview(ctx context.Context, in HeaderPreviewInputDTO) (HeaderPreviewDTO, error) {
	if err := ctx.Err(); err != nil {
		return HeaderPreviewDTO{}, errs.Wrap(err, errs.KindOf(err), "Computing the request's headers was interrupted.")
	}
	_, x, b, err := s.build(ctx, in.RequestID, headerPreviewBuild)
	if err != nil {
		return HeaderPreviewDTO{}, err
	}
	var jar httpclient.CookieJar
	var jarCookies []*nethttp.Cookie
	sentURL, _, urlErr := httpclient.SentURL(b.Request.URL, b.Request.Settings.URLEncoding)
	if j := s.cookieJar(); j != nil {
		jar = j
		if urlErr == nil {
			if u, err := url.Parse(sentURL); err == nil {
				jarCookies = j.Cookies(u)
			}
		}
	}
	written, err := httpclient.Preview(b.Request, jar)
	if err != nil {
		return HeaderPreviewDTO{}, err
	}

	masker := httpclient.NewSecretMasker(secretsOf(x, b))
	dto := func(name, value, source, reason string) PreviewHeaderDTO {
		shown := masker.MaskHeader(name, value)
		return PreviewHeaderDTO{Name: name, Value: shown, Masked: shown != value, Source: source, Reason: reason}
	}

	// The configured headers, by lower-case name: where the first one
	// comes from, and its value.
	type row struct{ source, value string }
	rows := map[string]row{}
	for i, h := range b.Request.Headers {
		if !h.Enabled {
			continue
		}
		key := strings.ToLower(h.Name)
		if _, ok := rows[key]; ok {
			continue
		}
		source := PreviewFromRequest
		if i < len(b.HeaderSources) {
			source = b.HeaderSources[i]
		}
		rows[key] = row{source: source, value: h.Value}
	}

	out := HeaderPreviewDTO{
		Headers:     make([]PreviewHeaderDTO, 0, len(written)),
		Suppressed:  []PreviewHeaderDTO{},
		Digest:      b.Request.Digest != nil,
		HTTPVersion: string(b.Request.Settings.HTTPVersion),
		Unresolved:  x.skippedNames(),
	}
	if out.HTTPVersion == "" {
		out.HTTPVersion = "auto"
	}
	for _, h := range written {
		key := strings.ToLower(h.Name)
		r, typed := rows[key]
		source := r.source
		switch {
		case key == "cookie" && (!typed || r.value != h.Value):
			source = PreviewFromCookies
		case typed:
		case key == "host":
			source = PreviewFromURL
		case key == "authorization":
			source = PreviewFromURL
		case key == "content-type" || key == "content-length" || key == "transfer-encoding":
			source = PreviewFromBody
		default:
			source = PreviewFromClient
		}
		out.Headers = append(out.Headers, dto(h.Name, h.Value, source, ""))
	}

	for _, sh := range b.Suppressed {
		out.Suppressed = append(out.Suppressed, dto(sh.Name, sh.Value, sh.Source, sh.Reason))
	}
	if _, ok := rows["host"]; ok && urlErr == nil {
		if u, err := url.Parse(sentURL); err == nil {
			out.Suppressed = append(out.Suppressed, dto("Host", u.Host, PreviewFromURL, request.SuppressedOverridden))
		}
	}
	if b.Request.Settings.SkipSendingCookies && len(jarCookies) > 0 {
		out.Suppressed = append(out.Suppressed, dto("Cookie", cookieValue(jarCookies), PreviewFromCookies, request.SuppressedOmitted))
	}
	return out, nil
}

// cookieJar returns the project's cookie jar, nil when there is none or
// it cannot be opened.
func (s *HTTPService) cookieJar() httpclient.CookieJar {
	if s.jars == nil {
		return nil
	}
	jar, err := s.jars.Jar()
	if err != nil {
		s.logger.Warn("header preview made without cookies", "err", err)
		return nil
	}
	if jar == nil {
		return nil
	}
	return &jarRecorder{jar: jar}
}

// cookieValue writes cookies as a Cookie header value.
func cookieValue(cookies []*nethttp.Cookie) string {
	parts := make([]string, len(cookies))
	for i, c := range cookies {
		parts[i] = c.Name + "=" + c.Value
	}
	return strings.Join(parts, "; ")
}
