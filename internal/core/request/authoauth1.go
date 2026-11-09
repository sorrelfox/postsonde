package request

// authoauth1.go maps an oauth1 auth (AUTH-15): the fields are rendered
// with the rest of the auth, and the request is signed by package oauth1
// once it is complete (HTTPBuild.signOAuth1), since the signature covers
// the method, the URL and a form body. See
// docs/auth.md#oauth-10-auth-15.

import (
	"crypto/rsa"
	"io"
	"mime"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/jwt"
	"github.com/sorrelfox/postsonde/internal/core/oauth1"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// FormContentType is the media type of a URL-encoded form body, whose
// parameters an OAuth 1.0 signature covers.
const FormContentType = "application/x-www-form-urlencoded"

// maxSignedForm is the largest form body read to sign it.
const maxSignedForm = 16 << 20

// oauth1Pending is an oauth1 auth rendered and checked, waiting for the
// finished request to sign.
type oauth1Pending struct {
	// in is "header", "query" or "body".
	in       string
	realm    string
	bodyHash bool
	// input holds the auth's values; Method, URL, Form and BodyHash are
	// filled from the request.
	input oauth1.Input
	// omit are the request's settings.omitHeaders.
	omit []string
}

func (m authMapper) oauth1() (AuthResult, error) {
	a := m.auth
	in := a.In
	if in == "" {
		in = projectfile.APIKeyInHeader
	}
	if in != projectfile.APIKeyInHeader && in != projectfile.APIKeyInQuery && in != projectfile.AuthInBody {
		return AuthResult{}, m.detail(errs.New(errs.Unsupported, "This version of PostSonde cannot send OAuth 1.0 parameters there.").
			WithCode(CodeAuthUnsupported).
			WithHint("Send them in the Authorization header, the query string or a form body."), "in")
	}
	v, err := m.fields(
		"consumerKey", a.ConsumerKey, "consumerSecret", a.ConsumerSecret,
		"accessToken", a.AccessToken, "tokenSecret", a.TokenSecret,
		"signatureMethod", a.SignatureMethod, "callback", a.Callback, "verifier", a.Verifier,
		"realm", a.Realm, "nonce", a.Nonce, "timestamp", a.Timestamp,
	)
	if err != nil {
		return AuthResult{}, err
	}
	consumerKey, consumerSecret, token, tokenSecret := v[0], v[1], v[2], v[3]
	method, callback, verifier, realm, nonce, timestamp := v[4], v[5], v[6], v[7], v[8], v[9]
	if consumerKey == "" {
		return AuthResult{}, nil
	}
	if method == "" {
		method = oauth1.DefaultMethod
	}
	if !oauth1.Supported(method) {
		return AuthResult{}, m.detail(errs.New(errs.Unsupported, "PostSonde cannot sign OAuth 1.0 requests with this signature method.").
			WithCode(CodeAuthUnsupported).
			WithHint("Choose HMAC-SHA1, HMAC-SHA256, RSA-SHA1 or PLAINTEXT."), "signatureMethod")
	}
	if hasControl(realm) {
		return AuthResult{}, m.invalid("realm", "The OAuth 1.0 realm contains a line break or another control character.",
			"Remove control characters from the realm, or from the variables it uses.")
	}
	keys := oauth1.Keys{ConsumerSecret: consumerSecret, TokenSecret: tokenSecret}
	secrets := []string{consumerSecret, tokenSecret}
	if method == oauth1.RSASHA1 {
		if strings.TrimSpace(consumerSecret) == "" {
			return AuthResult{}, m.invalid("consumerSecret", "RSA-SHA1 needs the client's RSA private key.",
				"Paste the PEM private key in the consumer secret field.")
		}
		key, err := jwt.ParseKey(jwt.RS256, []byte(consumerSecret), false)
		if err != nil {
			e, ok := err.(*errs.Error)
			if !ok {
				e = errs.Wrap(err, errs.InvalidArgument, "The RSA private key could not be read.")
			}
			return AuthResult{}, m.detail(e.WithCode(CodeAuthInvalid), "consumerSecret")
		}
		rk, ok := key.(*rsa.PrivateKey)
		if !ok {
			return AuthResult{}, m.invalid("consumerSecret", "RSA-SHA1 needs an RSA private key.",
				"Paste an unencrypted PEM RSA private key (PKCS#8 or PKCS#1).")
		}
		keys = oauth1.Keys{RSA: rk}
		secrets = []string{consumerSecret}
	}
	version := oauth1.Version
	if a.OmitVersion {
		version = ""
	}
	p := &oauth1Pending{
		in: in, realm: realm, bodyHash: a.BodyHash,
		input: oauth1.Input{
			ConsumerKey: consumerKey, Token: token, SignatureMethod: method,
			Callback: callback, Verifier: verifier, Version: version,
			Nonce: nonce, Timestamp: timestamp, Keys: keys,
			Now: m.opts.Now, Rand: m.opts.Rand,
		},
	}
	return AuthResult{oauth1: p, Secrets: nonEmpty(secrets)}, nil
}

func nonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// signOAuth1 signs b's request as it is now (method, URL, headers, body)
// with the pending oauth1 auth, if there is one, and adds the protocol
// parameters to the Authorization header, the query string or the form
// body. It runs once, as the last step of a build.
func (b *HTTPBuild) signOAuth1() error {
	p := b.oauth1
	if p == nil {
		return nil
	}
	b.oauth1 = nil
	r := &b.Request
	sent, _, err := httpclient.SentURL(r.URL, r.Settings.URLEncoding)
	if err != nil {
		return err
	}
	in := p.input
	in.Method, in.URL = r.Method, sent

	form := isFormType(requestContentType(r.Headers, r.Body))
	var formData []byte
	switch {
	case form && r.Body != nil:
		if formData, err = readBody(r.Body, maxSignedForm); err != nil {
			return oauth1Failed(err, "The form body could not be read to sign it.")
		}
		in.Form = oauth1.FormParams(string(formData))
	case p.in == projectfile.AuthInBody && r.Body == nil:
		form = true
	case p.in == projectfile.AuthInBody:
		return errs.New(errs.InvalidArgument, "OAuth 1.0 parameters can be sent in the body only with a URL-encoded form body.").
			WithCode(CodeAuthInvalid).
			WithHint("Choose the form (URL-encoded) body type, or send the parameters in the header or the query string.").
			WithDetail(DetailAuthType, projectfile.AuthOAuth1).
			WithDetail(DetailAuthField, "in")
	}
	if p.bodyHash && !form {
		var body io.Reader
		if r.Body != nil {
			rc, _, err := r.Body.Open()
			if err != nil {
				return oauth1Failed(err, "The body could not be read to hash it.")
			}
			defer rc.Close()
			body = rc
		}
		if in.BodyHash, err = oauth1.BodyHash(in.SignatureMethod, body); err != nil {
			return oauth1Failed(err, "The body could not be read to hash it.")
		}
	}

	res, err := oauth1.SignRequest(in)
	if err != nil {
		e, ok := err.(*errs.Error)
		if !ok {
			e = errs.Wrap(err, errs.Internal, "The request could not be signed.")
		}
		return e.WithDetail(DetailAuthType, projectfile.AuthOAuth1)
	}
	b.OAuth1 = &res
	b.AuthSecrets = append(b.AuthSecrets, res.Signature, oauth1.Encode(res.Signature))

	switch p.in {
	case projectfile.APIKeyInQuery:
		params := make([]QueryParam, len(res.Params))
		for i, q := range res.Params {
			params[i] = QueryParam{Name: q.Name, Value: q.Value}
		}
		r.URL = WithAuthQuery(r.URL, params)
	case projectfile.AuthInBody:
		data := formData
		if len(data) > 0 {
			data = append(data, '&')
		}
		data = append(data, oauth1.Pairs(res.Params)...)
		typ := FormContentType
		if r.Body != nil {
			typ = r.Body.ContentType()
		}
		r.Body = httpclient.RawBody{Data: data, Type: typ}
	default:
		value := oauth1.Header(p.realm, res.Params)
		omit := omitSet(p.omit)
		switch {
		case hasHeader(r.Headers, AuthorizationHeader):
			b.Suppressed = append(b.Suppressed, SuppressedHeader{Name: AuthorizationHeader, Value: value, Source: HeaderFromAuth, Reason: SuppressedOverridden})
		case omit[strings.ToLower(AuthorizationHeader)]:
			b.Suppressed = append(b.Suppressed, SuppressedHeader{Name: AuthorizationHeader, Value: value, Source: HeaderFromAuth, Reason: SuppressedOmitted})
		default:
			if b.HeaderSources != nil && len(b.HeaderSources) == len(r.Headers) {
				b.HeaderSources = append(b.HeaderSources, HeaderFromAuth)
			}
			r.Headers = append(r.Headers, httpclient.Header{Name: AuthorizationHeader, Value: value, Enabled: true})
		}
	}
	return nil
}

func oauth1Failed(err error, msg string) error {
	return errs.Wrap(err, errs.KindOf(err), msg).
		WithHint("Check the body, or turn the body hash off.").
		WithDetail(DetailAuthType, projectfile.AuthOAuth1)
}

// requestContentType is the Content-Type a request is sent with: an
// enabled header row's, else the body's own.
func requestContentType(headers []httpclient.Header, body httpclient.Body) string {
	for _, h := range headers {
		if h.Enabled && strings.EqualFold(h.Name, "Content-Type") {
			return h.Value
		}
	}
	if body != nil {
		return body.ContentType()
	}
	return ""
}

func isFormType(contentType string) bool {
	t, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		t, _, _ = strings.Cut(contentType, ";")
		t = strings.ToLower(strings.TrimSpace(t))
	}
	return t == FormContentType
}

// readBody reads all of body, at most limit bytes.
func readBody(body httpclient.Body, limit int64) ([]byte, error) {
	rc, _, err := body.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errs.New(errs.InvalidArgument, "The form body is larger than 16 MiB.")
	}
	return data, nil
}
