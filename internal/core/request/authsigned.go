package request

// authsigned.go holds what the auth types that sign the finished request
// share (AWS Signature Version 4 and Hawk; OAuth 1.0 has its own in
// authoauth1.go): the URL as it goes on the wire, split, and the
// Authorization header added unless a row overrides it or
// settings.omitHeaders leaves it out.

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// sentTarget is a request's URL as it goes on the wire
// (httpclient.SentURL), split.
type sentTarget struct {
	// Scheme is lower case; Authority the host and port as sent (no
	// userinfo).
	Scheme    string
	Authority string
	// Path is the path as sent ("/" when empty); Query the query as sent
	// without "?", HasQuery whether there is a "?".
	Path     string
	Query    string
	HasQuery bool
}

// Target is the request target: the path and "?query".
func (t sentTarget) Target() string {
	if t.HasQuery {
		return t.Path + "?" + t.Query
	}
	return t.Path
}

// sentURL returns r's URL as it is sent, split.
func sentURL(r *httpclient.Request) (sentTarget, error) {
	sent, _, err := httpclient.SentURL(r.URL, r.Settings.URLEncoding)
	if err != nil {
		return sentTarget{}, err
	}
	var t sentTarget
	scheme, rest, _ := strings.Cut(sent, "://")
	t.Scheme = strings.ToLower(scheme)
	end := strings.IndexAny(rest, "/?")
	if end < 0 {
		end = len(rest)
	}
	t.Authority, rest = rest[:end], rest[end:]
	if i := strings.LastIndexByte(t.Authority, '@'); i >= 0 {
		t.Authority = t.Authority[i+1:]
	}
	t.Path, t.Query, t.HasQuery = strings.Cut(rest, "?")
	if t.Path == "" {
		t.Path = "/"
	}
	return t, nil
}

// headerRow returns the value of the first enabled header named name
// (ignoring case).
func headerRow(headers []httpclient.Header, name string) (string, bool) {
	for _, h := range headers {
		if h.Enabled && strings.EqualFold(h.Name, name) {
			return h.Value, true
		}
	}
	return "", false
}

// authorizationWins reports whether the auth's Authorization header is
// not sent: an enabled Authorization row overrides it, or omit (the
// request's settings.omitHeaders) names it. It is then listed in
// b.Suppressed with value.
func (b *HTTPBuild) authorizationWins(value string, omit []string) bool {
	r := &b.Request
	switch {
	case hasHeader(r.Headers, AuthorizationHeader):
		b.Suppressed = append(b.Suppressed, SuppressedHeader{Name: AuthorizationHeader, Value: value, Source: HeaderFromAuth, Reason: SuppressedOverridden})
	case omitSet(omit)[strings.ToLower(AuthorizationHeader)]:
		b.Suppressed = append(b.Suppressed, SuppressedHeader{Name: AuthorizationHeader, Value: value, Source: HeaderFromAuth, Reason: SuppressedOmitted})
	default:
		return false
	}
	return true
}

// addAuthHeader appends a header the auth adds, recording its source.
func (b *HTTPBuild) addAuthHeader(name, value string) {
	r := &b.Request
	if b.HeaderSources != nil && len(b.HeaderSources) == len(r.Headers) {
		b.HeaderSources = append(b.HeaderSources, HeaderFromAuth)
	}
	r.Headers = append(r.Headers, httpclient.Header{Name: name, Value: value, Enabled: true})
}
