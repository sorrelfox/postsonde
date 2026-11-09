package request

// auth.go maps a request's auth (projectfile.Auth, AUTH-01…06, AUTH-08,
// AUTH-12, AUTH-15…19)
// onto the headers and query parameters that carry it, or the Digest
// credentials httpclient answers a challenge with. See docs/auth.md.

import (
	"encoding/base64"
	"io"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/jwt"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Error codes of Auth and ResolveAuth.
const (
	// CodeAuthInvalid: an auth field cannot be sent (a colon in a Basic
	// username, a control character, an API key name that is not a header
	// name), after expansion. Details: type, field.
	CodeAuthInvalid = "request.auth_invalid"
	// CodeAuthSecretMissing: the secret was typed on another machine and
	// is not stored on this one (kind invalid_argument). Details: type,
	// field.
	CodeAuthSecretMissing = "request.auth_secret_missing"
	// CodeAuthUnsupported: an auth type or API key location this version
	// cannot send (kind unsupported). Details: type, and field "in" for a
	// location.
	CodeAuthUnsupported = "request.auth_unsupported"
	// CodeAuthInheritFailed: a folder the request inherits auth from could
	// not be read (kind invalid_argument). Details: folder.
	CodeAuthInheritFailed = "request.auth_inherit_failed"
	// CodeAuthKeyFileFailed: the key file of a JWT auth could not be read
	// (kind not_found, permission_denied or io). Details: type, field
	// "keyFile", path (as stored, variables rendered).
	CodeAuthKeyFileFailed = "request.auth_key_file_failed"
)

// DetailAuthType and DetailAuthField are the details of auth errors: the
// auth type and the member ("username", "password", "prefix", "token",
// "key", "value", "in"; for jwt also "algorithm", "secret", "keyFile",
// "jwtHeader", "payload"; for oauth1 "consumerKey", "consumerSecret",
// "accessToken", "tokenSecret", "signatureMethod", "callback", "verifier",
// "realm", "nonce", "timestamp"; for awsv4 "accessKey", "secretKey",
// "sessionToken", "region", "service", "expires"; for hawk "hawkId",
// "hawkKey", "hawkAlgorithm", "ext", "app", "dlg"; for netrc "netrcFile"
// and "url"; for oauth2 "grantType", "tokenUrl", "clientId",
// "clientSecret", "clientAuth", "scope", "audience", "resource", "state",
// "tokenParams").
const (
	DetailAuthType   = "type"
	DetailAuthField  = "field"
	DetailAuthFolder = "folder"
)

// AuthorizationHeader is the header Basic and Bearer auth set.
const AuthorizationHeader = "Authorization"

// QueryParam is a query parameter to add to the URL, not encoded.
type QueryParam struct {
	Name  string
	Value string
}

// AuthResult is what an auth adds to a request.
type AuthResult struct {
	// Headers go after the request's own headers (WithAuthHeaders).
	Headers []httpclient.Header
	// Query goes at the end of the URL's query (WithAuthQuery).
	Query []QueryParam
	// Digest are the credentials httpclient answers a Digest challenge
	// with (httpclient.Request.Digest), nil for other types.
	Digest *httpclient.DigestCredentials
	// JWT is the token a jwt auth built, with its header and payload
	// JSON; nil for other types.
	JWT *jwt.Token
	// oauth1 is an oauth1 auth rendered and checked: BuildHTTP signs the
	// finished request with it (HTTPBuild.signOAuth1).
	oauth1 *oauth1Pending
	// awsv4 and hawk are an awsv4 or hawk auth rendered and checked:
	// BuildHTTP signs the finished request with it (HTTPBuild.signAWSV4,
	// HTTPBuild.signHawk).
	awsv4 *awsV4Pending
	hawk  *hawkPending
	// oauth2 is an oauth2 auth rendered and checked: the token request
	// and where the token goes (HTTPBuild.OAuth2).
	oauth2 *OAuth2Pending
	// Netrc is what a netrc auth took from its file (no password); nil
	// for other types, and when the file was not read (NetrcOptions.Defer).
	Netrc *NetrcMatch
	// Credentials are the rendered values a basic, bearer, apikey, jwt or
	// netrc auth sends, as they are rather than in a header: what a
	// Socket.IO auth payload carries (HandshakeAuth, AUTH-31). nil for the
	// other types and an auth that sends nothing.
	Credentials *Credentials
	// Warnings are problems that do not stop the send: a netrc file
	// others can read.
	Warnings []Warning
	// Secrets are the values to mask wherever the request is shown
	// (httpclient.SecretMasker): the header and query values added, the
	// Digest password, a JWT's signing secret, OAuth 1.0 secrets, the AWS
	// secret key and session token, the Hawk key, a netrc password, an
	// OAuth 2.0 client secret (the token joins HTTPBuild.AuthSecrets when
	// it is added).
	Secrets []string
}

// Credentials are the values an auth sends, rendered: Token for bearer
// and jwt (without the prefix), Username and Password for basic and
// netrc, Key and Value for apikey.
type Credentials struct {
	Token              string
	Username, Password string
	Key, Value         string
}

// AuthOptions are what AuthWith needs besides the auth.
type AuthOptions struct {
	// Expand, when not nil, renders {{variables}} in every field first.
	Expand Expand
	// Root is the project root a relative JWT key file is read from.
	Root string
	// AllowedDirs are the allow-listed directories of the settings
	// (privacy.allowedDirs). A JWT key file, and a netrc file the auth
	// names, is read only from Root or one of them, links and ".."
	// followed first (PRV-10, package fileaccess): the auth comes from a
	// project, which anyone may have written. The netrc file of NETRC or
	// the home directory is the user's choice and is not limited.
	AllowedDirs []string
	// Now is the time of a JWT's iat, nbf and exp claims; nil is
	// time.Now.
	Now func() time.Time
	// Rand is the randomness of a JWT's jti, of PS and ES signatures and
	// of OAuth 1.0 and Hawk nonces; nil is crypto/rand.
	Rand io.Reader
	// URL is the request's URL, rendered: a netrc auth looks its host up.
	URL string
	// Netrc says where a netrc auth finds its file. It is read only for
	// that type.
	Netrc NetrcOptions
}

// Auth returns the headers and query parameters that carry a. a must be
// resolved (ResolveAuth): an "inherit" auth is an internal error.
//
// secrets are the request's locally stored secret values
// (requestsecrets.Store.Read); they fill a secret member marked local.
// expand, when not nil, renders {{variables}} in every field first.
//
//   - none, and a disabled basic, bearer or apikey auth: nothing.
//   - basic: Authorization: Basic base64(username ":" password), the
//     UTF-8 bytes as typed (RFC 7617). Nothing when both are empty. A
//     username may not contain ":"; neither may contain control
//     characters.
//   - bearer: Authorization: <prefix> <token>, or <token> alone when the
//     prefix is "". Nothing when the token is empty.
//   - apikey: a header <key>: <value>, or with in "query" the query
//     parameter key=value. Nothing when the key is empty.
//   - digest: no header; Digest holds the username and password, which
//     httpclient uses to answer the server's 401 challenge (RFC 7616).
//     Nothing when both are empty. Neither may contain control
//     characters.
//   - jwt: a JWT built and signed now (package jwt) from the algorithm,
//     header and payload JSON (variables rendered first) and the key,
//     typed or read from keyFile; sent as <key or Authorization>:
//     <prefix> <jwt>, or with in "query" as <key or access_token>=<jwt>.
//     Nothing when there is no key (secret and keyFile empty).
//   - oauth1: nothing yet; the fields are rendered and checked, and
//     BuildHTTP signs the finished request (OAuth 1.0, RFC 5849) and adds
//     the parameters to the Authorization header, the query string or the
//     form body. Nothing when the consumer key is empty.
//   - awsv4: nothing yet; BuildHTTP signs the finished request with AWS
//     Signature Version 4 and adds X-Amz-Date (X-Amz-Security-Token,
//     X-Amz-Content-Sha256) and Authorization, or with in "query" the
//     X-Amz-* parameters of a presigned URL. Nothing when the access key
//     is empty.
//   - hawk: nothing yet; BuildHTTP signs the finished request with Hawk
//     and adds Authorization: Hawk …. Nothing when the Hawk ID is empty.
//   - netrc: Authorization: Basic with the login and password of the
//     entry for the host of AuthOptions.URL in the netrc file (package
//     netrc), read now. Nothing when the URL's userinfo has a password.
//     The file is read for this type only.
//   - oauth2: nothing yet; the fields are rendered and checked into the
//     token request (package oauth2), and the build carries it
//     (HTTPBuild.OAuth2) until the caller adds the token it got
//     (HTTPBuild.UseOAuth2Token). Nothing when the token URL is empty
//     (for the implicit grant, the authorization URL).
//
// Errors are *errs.Error with DetailAuthType and DetailAuthField; values
// never appear in them. An expand error keeps its kind and code.
func Auth(a projectfile.Auth, secrets map[string]string, expand Expand) (AuthResult, error) {
	return AuthWith(a, secrets, AuthOptions{Expand: expand})
}

// AuthWith is Auth with the options a JWT needs (the project root of a
// relative key file, and the clock and randomness of its claims) and
// those of a netrc auth (the URL, and where its file is).
func AuthWith(a projectfile.Auth, secrets map[string]string, o AuthOptions) (AuthResult, error) {
	a = requestsecrets.Merge(a, secrets)
	switch a.Type {
	case projectfile.AuthNone:
		return AuthResult{}, nil
	case "", projectfile.AuthInherit:
		return AuthResult{}, errs.New(errs.Internal, "The inherited auth was not resolved before sending.").
			WithHint("This is a bug in PostSonde.")
	case projectfile.AuthBasic, projectfile.AuthBearer, projectfile.AuthAPIKey, projectfile.AuthDigest, projectfile.AuthJWT, projectfile.AuthOAuth1,
		projectfile.AuthAWSV4, projectfile.AuthHawk, projectfile.AuthNetrc, projectfile.AuthOAuth2:
	default:
		return AuthResult{}, errs.New(errs.Unsupported, "This version of PostSonde cannot send this type of auth.").
			WithCode(CodeAuthUnsupported).
			WithHint("Choose another auth type, or send the request from a newer PostSonde.").
			WithDetail(DetailAuthType, a.Type)
	}
	if a.Disabled {
		return AuthResult{}, nil
	}
	// A JWT key read from a file does not need the typed secret.
	if missing := requestsecrets.MissingMember(a); missing != "" && (a.Type != projectfile.AuthJWT || a.KeyFile == "") {
		return AuthResult{}, errs.New(errs.InvalidArgument, "The "+secretLabel(a.Type, missing)+" was saved on another computer and is not stored on this one.").
			WithCode(CodeAuthSecretMissing).
			WithHint("Type it again in the Auth tab, or use a {{variable}} from a secret environment variable.").
			WithDetail(DetailAuthType, a.Type).
			WithDetail(DetailAuthField, missing)
	}
	m := authMapper{auth: a, expand: o.Expand, opts: o}
	var out AuthResult
	var err error
	switch a.Type {
	case projectfile.AuthBasic:
		out, err = m.basic()
	case projectfile.AuthBearer:
		out, err = m.bearer()
	case projectfile.AuthDigest:
		out, err = m.digest()
	case projectfile.AuthJWT:
		out, err = m.jwt()
	case projectfile.AuthOAuth1:
		out, err = m.oauth1()
	case projectfile.AuthAWSV4:
		out, err = m.awsv4()
	case projectfile.AuthHawk:
		out, err = m.hawk()
	case projectfile.AuthNetrc:
		out, err = m.netrc()
	case projectfile.AuthOAuth2:
		out, err = m.oauth2()
	default:
		out, err = m.apiKey()
	}
	if err != nil {
		return AuthResult{}, err
	}
	for _, h := range out.Headers {
		out.Secrets = append(out.Secrets, h.Value)
	}
	for _, q := range out.Query {
		out.Secrets = append(out.Secrets, q.Value)
	}
	if out.Digest != nil {
		// Never sent as it is; masked should it appear in a body or URL.
		out.Secrets = append(out.Secrets, out.Digest.Password)
	}
	return out, nil
}

func secretLabel(typ, member string) string {
	switch typ {
	case projectfile.AuthOAuth1:
		if member == "tokenSecret" {
			return "OAuth 1.0 token secret"
		}
		return "OAuth 1.0 consumer secret"
	case projectfile.AuthAWSV4:
		if member == "sessionToken" {
			return "AWS session token"
		}
		return "AWS secret access key"
	case projectfile.AuthHawk:
		return "Hawk key"
	case projectfile.AuthOAuth2:
		return "OAuth 2.0 client secret"

	case projectfile.AuthBasic:
		return "Basic auth password"
	case projectfile.AuthBearer:
		return "bearer token"
	case projectfile.AuthDigest:
		return "Digest auth password"
	case projectfile.AuthJWT:
		return "JWT signing secret"
	}
	return "API key"
}

type authMapper struct {
	auth   projectfile.Auth
	expand Expand
	opts   AuthOptions
}

// field renders a field's {{variables}}.
func (m authMapper) field(name, text string) (string, error) {
	if m.expand == nil {
		return text, nil
	}
	out, err := m.expand(text)
	if err != nil {
		e, ok := err.(*errs.Error)
		if !ok {
			e = errs.Wrap(err, errs.InvalidArgument, "An auth field could not be rendered.").
				WithHint("Check the variables used in the Auth tab.")
		}
		return "", m.detail(e, name)
	}
	return out, nil
}

func (m authMapper) detail(e *errs.Error, field string) *errs.Error {
	return e.WithDetail(DetailAuthType, m.auth.Type).WithDetail(DetailAuthField, field)
}

func (m authMapper) invalid(field, msg, hint string) error {
	return m.detail(errs.New(errs.InvalidArgument, msg).WithCode(CodeAuthInvalid).WithHint(hint), field)
}

// fields renders the named fields in order.
func (m authMapper) fields(pairs ...string) ([]string, error) {
	out := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		v, err := m.field(pairs[i], pairs[i+1])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (m authMapper) basic() (AuthResult, error) {
	v, err := m.fields("username", m.auth.Username, "password", m.auth.Password)
	if err != nil {
		return AuthResult{}, err
	}
	user, pass := v[0], v[1]
	if user == "" && pass == "" {
		return AuthResult{}, nil
	}
	if strings.Contains(user, ":") {
		return AuthResult{}, m.invalid("username", "The Basic auth username contains a colon.",
			"Basic auth cannot send a username with \":\" (RFC 7617). Use another auth type or an Authorization header.")
	}
	for _, f := range []struct{ name, label, value string }{{"username", "username", user}, {"password", "password", pass}} {
		if hasControl(f.value) {
			return AuthResult{}, m.invalid(f.name, "The Basic auth "+f.label+" contains a line break or another control character.",
				"Remove control characters from the "+f.label+", or from the variables it uses.")
		}
	}
	token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	return AuthResult{Headers: []httpclient.Header{{Name: AuthorizationHeader, Value: "Basic " + token, Enabled: true}},
		Credentials: &Credentials{Username: user, Password: pass}}, nil
}

func (m authMapper) digest() (AuthResult, error) {
	v, err := m.fields("username", m.auth.Username, "password", m.auth.Password)
	if err != nil {
		return AuthResult{}, err
	}
	user, pass := v[0], v[1]
	if user == "" && pass == "" {
		return AuthResult{}, nil
	}
	for _, f := range []struct{ name, value string }{{"username", user}, {"password", pass}} {
		if hasControl(f.value) {
			return AuthResult{}, m.invalid(f.name, "The Digest auth "+f.name+" contains a line break or another control character.",
				"Remove control characters from the "+f.name+", or from the variables it uses.")
		}
	}
	return AuthResult{Digest: &httpclient.DigestCredentials{Username: user, Password: pass}}, nil
}

func (m authMapper) bearer() (AuthResult, error) {
	v, err := m.fields("prefix", m.auth.BearerPrefix(), "token", m.auth.Token)
	if err != nil {
		return AuthResult{}, err
	}
	prefix, token := v[0], v[1]
	if token == "" {
		return AuthResult{}, nil
	}
	if hasHeaderBreak(prefix) {
		return AuthResult{}, m.invalid("prefix", "The bearer token prefix contains a line break or another control character.",
			"Remove control characters from the prefix, or from the variables it uses.")
	}
	if hasHeaderBreak(token) {
		return AuthResult{}, m.invalid("token", "The bearer token contains a line break or another control character.",
			"Remove control characters from the token, or from the variables it uses.")
	}
	value := token
	if prefix != "" {
		value = prefix + " " + token
	}
	return AuthResult{Headers: []httpclient.Header{{Name: AuthorizationHeader, Value: value, Enabled: true}},
		Credentials: &Credentials{Token: token}}, nil
}

func (m authMapper) apiKey() (AuthResult, error) {
	in := m.auth.In
	if in == "" {
		in = projectfile.APIKeyInHeader
	}
	if in != projectfile.APIKeyInHeader && in != projectfile.APIKeyInQuery {
		return AuthResult{}, m.detail(errs.New(errs.Unsupported, "This version of PostSonde cannot send an API key there.").
			WithCode(CodeAuthUnsupported).
			WithHint("Send the API key in a header or the query string."), "in")
	}
	v, err := m.fields("key", m.auth.Key, "value", m.auth.Value)
	if err != nil {
		return AuthResult{}, err
	}
	key, value := v[0], v[1]
	if m.auth.Key == "" {
		return AuthResult{}, nil
	}
	if in == projectfile.APIKeyInQuery {
		if key == "" {
			return AuthResult{}, m.invalid("key", "The API key's parameter name is empty once its variables are filled in.",
				"Check the variables used in the key name.")
		}
		return AuthResult{Query: []QueryParam{{Name: key, Value: value}}, Credentials: &Credentials{Key: key, Value: value}}, nil
	}
	if !isToken(key) {
		return AuthResult{}, m.invalid("key", "The API key's header name is not a valid header name.",
			"Header names may contain only letters, digits and !#$%&'*+-.^_`|~, with no spaces.")
	}
	if hasHeaderBreak(value) {
		return AuthResult{}, m.invalid("value", "The API key contains a line break or another control character.",
			"Remove control characters from the API key, or from the variables it uses.")
	}
	return AuthResult{Headers: []httpclient.Header{{Name: key, Value: value, Enabled: true}}, Credentials: &Credentials{Key: key, Value: value}}, nil
}

// hasControl reports a C0 control character or DEL, tab included.
func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// hasHeaderBreak reports what a header value may not contain: control
// characters other than tab (the rule of Headers).
func hasHeaderBreak(s string) bool {
	for i := 0; i < len(s); i++ {
		if (s[i] < ' ' && s[i] != '\t') || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// WithAuthHeaders returns headers followed by the auth headers that no
// enabled header of the same name (ignoring case) overrides: a header the
// user typed in the Headers table wins over the Auth tab.
func WithAuthHeaders(headers, auth []httpclient.Header) []httpclient.Header {
	out := append(make([]httpclient.Header, 0, len(headers)+len(auth)), headers...)
	for _, h := range auth {
		overridden := false
		for _, row := range headers {
			if row.Enabled && strings.EqualFold(row.Name, h.Name) {
				overridden = true
				break
			}
		}
		if !overridden {
			out = append(out, h)
		}
	}
	return out
}

// WithAuthQuery appends params to the query of rawURL, a URL whose
// variables are rendered, before any fragment. A parameter is left out
// when the query already has one of that name, compared as encoded, so a
// parameter typed in the Params table wins over the Auth tab.
//
// Names and values are credentials, not URL text, so they are encoded
// completely: every byte except the RFC 3986 unreserved characters
// (letters, digits, "-", ".", "_", "~") becomes %XX, space included. The
// rest of the URL is left as it is.
func WithAuthQuery(rawURL string, params []QueryParam) string {
	if len(params) == 0 {
		return rawURL
	}
	base, fragment := rawURL, ""
	if i := strings.IndexByte(rawURL, '#'); i >= 0 {
		base, fragment = rawURL[:i], rawURL[i:]
	}
	path, query, hasQuery := strings.Cut(base, "?")
	existing := map[string]bool{}
	if hasQuery {
		for entry := range strings.SplitSeq(query, "&") {
			name, _, _ := strings.Cut(entry, "=")
			existing[name] = true
		}
	}
	var b strings.Builder
	b.WriteString(query)
	for _, p := range params {
		name := encodeComponent(p.Name)
		if existing[name] {
			continue
		}
		existing[name] = true
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "&") {
			b.WriteByte('&')
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(encodeComponent(p.Value))
	}
	if b.Len() == 0 && !hasQuery {
		return rawURL
	}
	return path + "?" + b.String() + fragment
}

// encodeComponent percent-encodes all but the RFC 3986 unreserved
// characters, with upper-case hex digits.
func encodeComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// AuthSource is a folder a request can inherit auth from.
type AuthSource struct {
	// ID and Name identify the folder.
	ID   string
	Name string
	// Auth is the folder's own auth.
	Auth projectfile.Auth
	// Unreadable is set when the folder's _folder.json did not load, so
	// its auth is not known.
	Unreadable bool
}

// ResolveAuth returns the auth a request with auth own sends (AUTH-01).
// parents are the request's folders from the closest one up to the top
// level; the project root has no auth.
//
// An auth other than "inherit" (or "") is its own. Otherwise the first
// folder whose auth is not "inherit" supplies it, and from points at that
// folder in parents. When no folder does, the result is "none" and from is
// nil. A folder that could not be read stops the search with
// CodeAuthInheritFailed, rather than skipping it and sending the auth of a
// folder further up.
//
// The secrets of a folder's auth are that folder's: load them by
// from.ID.
func ResolveAuth(own projectfile.Auth, parents []AuthSource) (resolved projectfile.Auth, from *AuthSource, err error) {
	if own.Type != "" && own.Type != projectfile.AuthInherit {
		return own, nil, nil
	}
	for i := range parents {
		p := &parents[i]
		if p.Unreadable {
			return projectfile.Auth{}, nil, errs.New(errs.InvalidArgument, "The request inherits its auth from a folder whose settings could not be read.").
				WithCode(CodeAuthInheritFailed).
				WithHint("Fix the folder's _folder.json, or choose an auth type for the request.").
				WithDetail(DetailAuthFolder, p.ID)
		}
		if p.Auth.Type != "" && p.Auth.Type != projectfile.AuthInherit {
			return p.Auth, p, nil
		}
	}
	return projectfile.Auth{Type: projectfile.AuthNone}, nil, nil
}
