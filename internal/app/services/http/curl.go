package http

import (
	"context"
	"net/http"
	"net/url"
	"slices"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
)

// CurlInputDTO is what CurlCommand takes.
type CurlInputDTO struct {
	RequestID string `json:"requestId"`
	// IncludeSecrets writes the real values of the secrets into the
	// command; false (the default) masks them (PRV-09).
	IncludeSecrets bool `json:"includeSecrets"`
}

// CurlDTO is a request as a curl command (GEN-01, EXP-01).
type CurlDTO struct {
	// Command is the curl command for a POSIX shell.
	Command string `json:"command"`
	// Masked is set when secrets were replaced by ••••••: the command
	// shows the request but does not authenticate as it is.
	Masked bool `json:"masked"`
	// Unresolved are the names of the undefined {{variables}}, left in
	// the command as written, sorted.
	Unresolved []string `json:"unresolved"`
}

// CurlCommand returns request in.RequestID (HTTP, GraphQL or SSE) as the
// curl command that sends the same request (GEN-01, EXP-01): built as
// Send builds it, with the active environment's variables, dynamic
// values, folder headers, auth, settings and the cookie jar's cookies,
// then written by snippet.Curl. Nothing is sent.
//
// Secrets are masked unless in.IncludeSecrets (docs/privacy.md): the
// values of the secret variables used, the credentials the auth adds and
// those of every Authorization row, as the timeline masks them. Undefined
// variables do not fail it: they stay as written and are listed in
// Unresolved. It fails as Send does for other build errors (an unknown
// ID, a missing path parameter, a body file that does not exist), and
// with the snippet codes for a body a command cannot hold.
func (s *HTTPService) CurlCommand(ctx context.Context, in CurlInputDTO) (CurlDTO, error) {
	if err := ctx.Err(); err != nil {
		return CurlDTO{}, errs.Wrap(err, errs.KindOf(err), "Copying the request was interrupted.")
	}
	_, x, b, err := s.build(ctx, in.RequestID, previewBuild)
	if err != nil {
		return CurlDTO{}, err
	}
	r, err := snippet.FromBuild(b, s.jarCookies(b))
	if err != nil {
		return CurlDTO{}, err
	}
	out := CurlDTO{Unresolved: x.skippedNames()}
	if !in.IncludeSecrets {
		r, out.Masked = r.Masked(secretsOf(x, b))
	}
	if out.Command, err = snippet.Curl(r); err != nil {
		return CurlDTO{}, err
	}
	return out, nil
}

// jarCookies returns the cookie jar's cookies a send of b would add (nil
// for none, or when the request does not send jar cookies), for a
// snippet or an export.
func (s *HTTPService) jarCookies(b request.HTTPBuild) []*http.Cookie {
	if s.jars == nil || b.Request.Settings.SkipSendingCookies {
		return nil
	}
	jar, err := s.jars.Jar()
	if err != nil {
		s.logger.Warn("request copied without cookies", "err", err)
		return nil
	}
	if jar == nil {
		return nil
	}
	// The jar is asked for the URL as sent, as on a send.
	sent, _, err := httpclient.SentURL(b.Request.URL, b.Request.Settings.URLEncoding)
	if err != nil {
		return nil
	}
	u, err := url.Parse(sent)
	if err != nil {
		return nil
	}
	return jar.Cookies(u)
}

// skippedNames returns the names of the undefined variables left as
// written, sorted and without duplicates.
func (x *expansion) skippedNames() []string {
	names := slices.Clone(x.skipped)
	slices.Sort(names)
	names = slices.Compact(names)
	if names == nil {
		names = []string{}
	}
	return names
}
