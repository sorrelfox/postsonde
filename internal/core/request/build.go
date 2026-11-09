package request

import (
	"io"

	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/awssigv4"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/hawk"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/jwt"
	"github.com/sorrelfox/postsonde/internal/core/oauth1"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Error codes of BuildHTTP, besides those of the steps it calls.
const (
	// CodeURLMissing: the URL is empty (after its variables are rendered).
	CodeURLMissing = "request.url_missing"
	// CodeInvalidSetting: a negative timeout or redirect limit in the
	// request's settings. Details: field.
	CodeInvalidSetting = "request.invalid_setting"
)

// Timeout sources, for HTTPBuild.TimeoutSource.
const (
	// TimeoutFromRequest: the request's own settings.timeoutMs.
	TimeoutFromRequest = "request"
	// TimeoutFromSettings: the app's network.defaultTimeoutMs.
	TimeoutFromSettings = "settings"
)

// TLS verification sources, for HTTPBuild.TLSSource.
const (
	// TLSFromRequest: the request's own settings.verifyTls.
	TLSFromRequest = "request"
	// TLSFromSettings: the app's network.verifyTls.
	TLSFromSettings = "settings"
)

// HTTPOptions are what BuildHTTP needs besides the request file.
type HTTPOptions struct {
	// Expand renders {{variables}}; nil sends text as typed.
	Expand Expand
	// Root is the project root that relative body file paths are resolved
	// against.
	Root string
	// AllowedDirs are the allow-listed directories of the settings
	// (privacy.allowedDirs, PRV-10) that a file the request names for its
	// auth (a JWT key file, a netrc file) may be in besides Root; see
	// AuthOptions.AllowedDirs.
	AllowedDirs []string
	// DefaultTimeout applies when the request has no timeout of its own
	// (HTTP-21); zero means none.
	DefaultTimeout time.Duration
	// DefaultInsecure turns TLS verification off for a request without
	// its own verifyTls: the app setting network.verifyTls false
	// (HTTP-23). The zero value verifies.
	DefaultInsecure bool
	// FolderHeaders are the header rows of the request's folders, the
	// top-level folder first (COL-03); see InheritHeaders.
	FolderHeaders []FolderHeaders
	// Now is the clock of a JWT auth's claims (AUTH-16), of an AWS
	// signature's X-Amz-Date (AUTH-17) and of a Hawk timestamp (AUTH-18);
	// nil is time.Now.
	Now func() time.Time
	// Rand is the randomness of OAuth 1.0 and Hawk nonces; nil is
	// crypto/rand.
	Rand io.Reader
	// Netrc says where a netrc auth finds its file (AUTH-19); the zero
	// value reads the user's real file, for that auth type only.
	Netrc NetrcOptions
	// ProxyAsTyped leaves the request's own proxy unbuilt: HTTPBuild.Proxy
	// stays the zero Proxy and ProxySetting holds it as typed. For a
	// templated snippet, whose {{variables}} stay as written and so could
	// not be parsed as a proxy URL.
	ProxyAsTyped bool
}

// HTTPBuild is a request ready to send.
type HTTPBuild struct {
	Request httpclient.Request
	// InsecureSkipVerify is set when the request turns TLS verification off
	// (settings.verifyTls false, or the app setting when the request has
	// none), which needs a client of its own.
	InsecureSkipVerify bool
	// TLSSource says where InsecureSkipVerify came from when it is set:
	// TLSFromRequest or TLSFromSettings; "" when verification is on.
	TLSSource string
	// TimeoutSource says where Request.Settings.Timeout came from:
	// TimeoutFromRequest or TimeoutFromSettings; "" when there is none.
	TimeoutSource string
	// Proxy is the request's own proxy (settings.proxy, BuildProxy); the
	// zero Proxy inherits the proxy in effect. Send with Proxy.Context, and
	// keep the connections of each Proxy apart.
	Proxy Proxy
	// ProxySetting is settings.proxy as typed, its password merged.
	ProxySetting projectfile.RequestProxy
	// Warnings are problems that do not stop the send.
	Warnings []Warning
	// HeaderSources says where each of Request.Headers came from, index
	// for index: HeaderFromRequest, HeaderFromFolder or HeaderFromAuth.
	// Nil when the builder does not record it (BuildGraphQL).
	HeaderSources []string
	// Suppressed are the automatic headers the build left out (HTTP-09):
	// the body's Content-Type and the auth's headers when a header row
	// overrides them or settings.omitHeaders turns them off.
	Suppressed []SuppressedHeader
	// AuthSecrets are the values the auth added or signed with, to mask
	// wherever the request is shown (AuthResult.Secrets).
	AuthSecrets []string
	// JWT is the token a jwt auth built for this request (AUTH-16); nil
	// for other auth types.
	JWT *jwt.Token
	// OAuth1 is the OAuth 1.0 signature of this request (AUTH-15): its
	// protocol parameters and base string; nil for other auth types.
	OAuth1 *oauth1.Result
	// AWSV4 is the AWS Signature Version 4 of this request (AUTH-17): its
	// canonical request, string to sign and signature; nil for other auth
	// types.
	AWSV4 *awssigv4.Result
	// Hawk is what the Hawk MAC of this request covers (AUTH-18); nil for
	// other auth types.
	Hawk *hawk.Artifacts
	// Netrc is what a netrc auth took from its file (AUTH-19), without
	// the password; nil for other auth types.
	Netrc *NetrcMatch
	// OAuth2 is an oauth2 auth's token request and where its token goes
	// (AUTH-08); nil for other auth types, and when the token URL is
	// empty. The build has no token until UseOAuth2Token adds one: the
	// caller gets it, from its token store or the token endpoint.
	OAuth2 *OAuth2Pending

	// oauth1, awsv4 and hawk are the auth to sign the finished request
	// with (sign); nil once signed.
	oauth1 *oauth1Pending
	awsv4  *awsV4Pending
	hawk   *hawkPending
}

// Header sources, for HTTPBuild.HeaderSources and SuppressedHeader.Source.
const (
	// HeaderFromRequest: a row of the request's Headers table.
	HeaderFromRequest = "request"
	// HeaderFromFolder: a folder's header row (COL-03).
	HeaderFromFolder = "folder"
	// HeaderFromAuth: added by the request's auth.
	HeaderFromAuth = "auth"
	// HeaderFromBody: the Content-Type the body implies (HTTP-16).
	HeaderFromBody = "body"
)

// Reasons an automatic header is not sent, for SuppressedHeader.Reason.
const (
	// SuppressedOverridden: an enabled header row of the same name wins.
	SuppressedOverridden = "overridden"
	// SuppressedOmitted: settings.omitHeaders names it.
	SuppressedOmitted = "omitted"
)

// SuppressedHeader is an automatic header a build did not send.
type SuppressedHeader struct {
	// Name and Value are the header it would have sent. For Digest auth
	// the value is DigestPlaceholder: the field is computed from the
	// server's challenge.
	Name  string
	Value string
	// Source is HeaderFromBody or HeaderFromAuth.
	Source string
	// Reason is SuppressedOverridden or SuppressedOmitted.
	Reason string
}

// DigestPlaceholder is the value shown for the Authorization field of
// Digest auth, which is computed from the server's 401 challenge.
const DigestPlaceholder = "Digest …"

// BuildHTTP maps an HTTP request file onto the request httpclient sends
// (HTTP-20). auth is the auth the request sends: resolved (ResolveAuth)
// and with its local secret merged (requestsecrets.Merge).
//
// In order: the method is checked (Method); the path parameters are
// filled in (SubstitutePath), then the URL's own variables rendered, and
// a relative URL ("/users") joined to {{baseUrl}} (ExpandURL, COL-08); the
// auth's headers and query parameters are added (Auth, WithAuthHeaders,
// WithAuthQuery), so that rows typed in the tables win; the header rows
// are mapped (Headers) and the folders' headers added (InheritHeaders:
// the request's rows win, then the nearer folder), and a folder's header
// wins over the auth's too; Digest credentials go to Request.Digest
// unless an Authorization row is enabled; the body is mapped (Body) and dropped for HEAD and
// TRACE with a warning (BodyForMethod). The first error stops the build
// and is returned as it is.
//
// settings.omitHeaders turns automatic headers off (HTTP-09): a
// "Content-Type" entry sends the body without the type it implies, and an
// entry naming an auth header (compared ignoring case) leaves that header
// out, "Authorization" also the Digest answer. What is left out, or
// overridden by a row, is listed in HTTPBuild.Suppressed; where each sent
// header came from is in HTTPBuild.HeaderSources.
//
// Settings: settings.timeoutMs overrides o.DefaultTimeout, and 0 there
// means no timeout, whatever the default; followRedirects, maxRedirects
// (0 follows none), followOriginalMethod and forwardAuthorization override
// httpclient.DefaultSettings (HTTP-22); verifyTls overrides
// o.DefaultInsecure (HTTP-23); urlEncoding (HTTP-24), httpVersion
// (HTTP-25), sendCookies and storeCookies (HTTP-26) map onto the
// httpclient.Settings of the same meaning, an unknown value failing with
// CodeInvalidSetting.
//
// An oauth1, awsv4 or hawk auth signs the request last, as it is then
// (method, URL as sent, headers, body), and adds its header or parameters
// (sign).
func BuildHTTP(f projectfile.HTTPRequest, auth projectfile.Auth, o HTTPOptions) (HTTPBuild, error) {
	b, err := buildHTTP(f, auth, o)
	if err != nil {
		return b, err
	}
	if err := b.sign(); err != nil {
		return HTTPBuild{}, err
	}
	return b, nil
}

// sign signs b's finished request with the auth that signs requests
// (OAuth 1.0, AWS Signature Version 4, Hawk), if it has one. It is the
// last step of every build.
func (b *HTTPBuild) sign() error {
	if err := b.signOAuth1(); err != nil {
		return err
	}
	if err := b.signAWSV4(); err != nil {
		return err
	}
	return b.signHawk()
}

// buildHTTP is BuildHTTP without the signature (sign), for the builders
// that change the request further before it is signed (BuildGraphQL).
func buildHTTP(f projectfile.HTTPRequest, auth projectfile.Auth, o HTTPOptions) (HTTPBuild, error) {
	var out HTTPBuild
	if err := Method(f.Method); err != nil {
		return out, err
	}
	settings, source, err := httpSettings(f.Settings, o.DefaultTimeout)
	if err != nil {
		return out, err
	}

	url, err := SubstitutePath(f.URL, f.PathParams, o.Expand)
	if err != nil {
		return out, err
	}
	// A relative URL ("/users") goes to {{baseUrl}} (COL-08, ExpandURL).
	if url, err = ExpandURL(url, o.Expand); err != nil {
		e, ok := err.(*errs.Error)
		if !ok {
			e = errs.Wrap(err, errs.InvalidArgument, "The URL could not be rendered.").
				WithHint("Check the variables used in the URL.")
		}
		return out, e.WithDetail(DetailField, "url")
	}
	if url == "" {
		return out, errs.New(errs.InvalidArgument, "The request has no URL.").
			WithCode(CodeURLMissing).
			WithHint("Enter the URL to send the request to, e.g. https://example.com/path.")
	}

	a, err := AuthWith(auth, nil, AuthOptions{Expand: o.Expand, Root: o.Root, AllowedDirs: o.AllowedDirs, Now: o.Now, Rand: o.Rand, URL: url, Netrc: o.Netrc})

	if err != nil {
		return out, err
	}
	out.AuthSecrets, out.JWT, out.Netrc = a.Secrets, a.JWT, a.Netrc
	out.Warnings = append(out.Warnings, a.Warnings...)
	if a.oauth1 != nil {
		a.oauth1.omit = f.Settings.OmitHeaders
		out.oauth1 = a.oauth1
	}
	if a.awsv4 != nil {
		a.awsv4.omit = f.Settings.OmitHeaders
		out.awsv4 = a.awsv4
	}
	if a.hawk != nil {
		a.hawk.omit = f.Settings.OmitHeaders
		out.hawk = a.hawk
	}
	if a.oauth2 != nil {
		a.oauth2.omit = f.Settings.OmitHeaders
		out.OAuth2 = a.oauth2
	}

	url = WithAuthQuery(url, a.Query)

	own, err := Headers(f.Headers, o.Expand)
	if err != nil {
		return out, err
	}
	headers, err := InheritHeaders(own, o.FolderHeaders, o.Expand)
	if err != nil {
		return out, err
	}
	sources := make([]string, len(headers))
	for i := range sources {
		sources[i] = HeaderFromRequest
		if i < len(headers)-len(own) {
			sources[i] = HeaderFromFolder
		}
	}
	omit := omitSet(f.Settings.OmitHeaders)
	authHeaders := make([]httpclient.Header, 0, len(a.Headers))
	for _, h := range a.Headers {
		switch {
		case hasHeader(headers, h.Name):
			out.Suppressed = append(out.Suppressed, SuppressedHeader{Name: h.Name, Value: h.Value, Source: HeaderFromAuth, Reason: SuppressedOverridden})
		case omit[strings.ToLower(h.Name)]:
			out.Suppressed = append(out.Suppressed, SuppressedHeader{Name: h.Name, Value: h.Value, Source: HeaderFromAuth, Reason: SuppressedOmitted})
		default:
			authHeaders = append(authHeaders, h)
		}
	}
	headers = WithAuthHeaders(headers, authHeaders)
	for len(sources) < len(headers) {
		sources = append(sources, HeaderFromAuth)
	}
	var digest *httpclient.DigestCredentials
	if a.Digest != nil {
		// An Authorization row wins, as over the other auth types.
		switch {
		case hasHeader(headers, AuthorizationHeader):
			out.Suppressed = append(out.Suppressed, SuppressedHeader{Name: AuthorizationHeader, Value: DigestPlaceholder, Source: HeaderFromAuth, Reason: SuppressedOverridden})
		case omit[strings.ToLower(AuthorizationHeader)]:
			out.Suppressed = append(out.Suppressed, SuppressedHeader{Name: AuthorizationHeader, Value: DigestPlaceholder, Source: HeaderFromAuth, Reason: SuppressedOmitted})
		default:
			digest = a.Digest
		}
	}

	body, err := Body(f.Body, o.Expand, o.Root)
	if err != nil {
		return out, err
	}
	body, warning := BodyForMethod(f.Method, body)
	if warning != nil {
		out.Warnings = append(out.Warnings, *warning)
	}
	if body != nil && body.ContentType() != "" {
		switch {
		case HasContentType(headers):
			out.Suppressed = append(out.Suppressed, SuppressedHeader{Name: "Content-Type", Value: body.ContentType(), Source: HeaderFromBody, Reason: SuppressedOverridden})
		case omit["content-type"]:
			out.Suppressed = append(out.Suppressed, SuppressedHeader{Name: "Content-Type", Value: body.ContentType(), Source: HeaderFromBody, Reason: SuppressedOmitted})
			body = withoutContentType(body)
		}
	}
	out.HeaderSources = sources

	out.Request = httpclient.Request{Method: f.Method, URL: url, Headers: headers, Body: body, Settings: settings, Digest: digest}
	switch {
	case f.Settings.VerifyTLS != nil:
		if !*f.Settings.VerifyTLS {
			out.InsecureSkipVerify, out.TLSSource = true, TLSFromRequest
		}
	case o.DefaultInsecure:
		out.InsecureSkipVerify, out.TLSSource = true, TLSFromSettings
	}
	out.TimeoutSource = source
	// Last, so the undefined variables of every other field are reported
	// with the proxy's.
	out.ProxySetting = f.Settings.Proxy
	if !o.ProxyAsTyped {
		if out.Proxy, err = BuildProxy(f.Settings.Proxy, o.Expand); err != nil {
			return out, err
		}
	}
	return out, nil
}

// httpSettings applies a request's settings over the defaults.
func httpSettings(s projectfile.RequestSettings, defaultTimeout time.Duration) (httpclient.Settings, string, error) {
	out := httpclient.DefaultSettings()
	source := ""
	if defaultTimeout > 0 {
		out.Timeout, source = defaultTimeout, TimeoutFromSettings
	}
	if s.TimeoutMs != nil {
		ms := *s.TimeoutMs
		if ms < 0 {
			return out, "", invalidSetting("timeoutMs", "The request's timeout is negative.", "Set a timeout of 0 (none) or more milliseconds.")
		}
		out.Timeout, source = time.Duration(ms)*time.Millisecond, TimeoutFromRequest
		if ms == 0 {
			source = ""
		}
	}
	if s.FollowRedirects != nil {
		out.FollowRedirects = *s.FollowRedirects
	}
	if s.MaxRedirects != nil {
		if *s.MaxRedirects < 0 {
			return out, "", invalidSetting("maxRedirects", "The request's maximum number of redirects is negative.", "Set 0 or more.")
		}
		out.MaxRedirects = *s.MaxRedirects
	}
	if s.FollowOriginalMethod != nil {
		out.KeepMethod = *s.FollowOriginalMethod
	}
	if s.ForwardAuthorization != nil {
		out.ForwardAuthorization = *s.ForwardAuthorization
	}
	switch s.URLEncoding {
	case "", projectfile.URLEncodingAuto:
		out.URLEncoding = httpclient.URLEncodingWHATWG
	case projectfile.URLEncodingRFC3986:
		out.URLEncoding = httpclient.URLEncodingRFC3986
	case projectfile.URLEncodingNone:
		out.URLEncoding = httpclient.URLEncodingAsTyped
	default:
		return out, "", invalidSetting("urlEncoding", "The request's URL encoding "+strconv.Quote(s.URLEncoding)+" is not known.",
			"Choose automatic, RFC 3986 or no encoding in the request's Settings tab.")
	}
	switch s.HTTPVersion {
	case "", projectfile.HTTPVersionAuto:
		out.HTTPVersion = httpclient.HTTPVersionAuto
	case projectfile.HTTPVersion1:
		out.HTTPVersion = httpclient.HTTPVersion1
	case projectfile.HTTPVersion2:
		out.HTTPVersion = httpclient.HTTPVersion2
	default:
		return out, "", invalidSetting("httpVersion", "The request's HTTP version "+strconv.Quote(s.HTTPVersion)+" is not known.",
			"Choose automatic, HTTP/1.1 or HTTP/2 in the request's Settings tab.")
	}
	out.SkipSendingCookies = s.SendCookies != nil && !*s.SendCookies
	out.SkipStoringCookies = s.StoreCookies != nil && !*s.StoreCookies
	return out, source, nil
}

func invalidSetting(field, msg, hint string) error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidSetting).WithHint(hint).WithDetail("field", field)
}

// FormatTimeout writes a timeout for messages: "500 ms", "30 s", "1.5 s",
// "2 min", "1 h 30 min".
func FormatTimeout(d time.Duration) string {
	ms := d.Milliseconds()
	switch {
	case ms < 1000:
		return strconv.FormatInt(ms, 10) + " ms"
	case ms < 60_000 || ms%1000 != 0:
		return strconv.FormatFloat(float64(ms)/1000, 'f', -1, 64) + " s"
	}
	s := ms / 1000
	h, m, sec := s/3600, s%3600/60, s%60
	var parts []string
	if h > 0 {
		parts = append(parts, strconv.FormatInt(h, 10)+" h")
	}
	if m > 0 {
		parts = append(parts, strconv.FormatInt(m, 10)+" min")
	}
	if sec > 0 {
		parts = append(parts, strconv.FormatInt(sec, 10)+" s")
	}
	return strings.Join(parts, " ")
}

// omitSet returns the names of settings.omitHeaders, lower-cased.
func omitSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[strings.ToLower(strings.TrimSpace(n))] = true
	}
	return out
}

// withoutContentType returns b sending no Content-Type of its own
// (settings.omitHeaders "Content-Type", HTTP-09). The body's bytes are
// unchanged; a multipart body keeps its boundary.
func withoutContentType(b httpclient.Body) httpclient.Body {
	switch b := b.(type) {
	case httpclient.RawBody:
		b.Type = ""
		return b
	case *FileBody:
		c := *b
		c.typ = ""
		return &c
	case *MultipartBody:
		c := *b
		c.omitType = true
		return &c
	}
	return untypedBody{b}
}

// untypedBody is a body without a Content-Type.
type untypedBody struct{ httpclient.Body }

func (untypedBody) ContentType() string { return "" }
