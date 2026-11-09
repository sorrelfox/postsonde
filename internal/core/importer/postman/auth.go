package postman

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/oauth1"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// auth maps a request's, folder's or collection's auth. A missing (or
// null) auth, and type "inherit", inherit the parent's, as in Postman;
// "noauth" is none; a type PostSonde lacks becomes none with a warning.
// The type's parameters are a list of {key, value} (v2.1) or an object
// (v2.0).
func (c *collector) auth(n *yaml.Node, t *texts, loc importer.Location) (projectfile.Auth, []importer.Diagnostic) {
	typ := strings.ToLower(strings.TrimSpace(sget(n, "type")))
	params := authParams(get(n, typ))
	p := func(key string) string { return t.conv(params[key], "the auth") }
	switch typ {
	case "", "inherit":
		return projectfile.Auth{Type: projectfile.AuthInherit}, nil
	case "noauth":
		return projectfile.Auth{Type: projectfile.AuthNone}, nil
	case "basic":
		return projectfile.Auth{Type: projectfile.AuthBasic, Username: p("username"), Password: p("password")}, nil
	case "digest":
		return projectfile.Auth{Type: projectfile.AuthDigest, Username: p("username"), Password: p("password")}, nil
	case "bearer":
		return projectfile.Auth{Type: projectfile.AuthBearer, Token: p("token")}, nil
	case "apikey":
		a := projectfile.Auth{Type: projectfile.AuthAPIKey, Key: p("key"), Value: p("value"), In: projectfile.APIKeyInHeader}
		if strings.EqualFold(params["in"], "query") {
			a.In = projectfile.APIKeyInQuery
		}
		return a, nil
	case "jwt":
		return jwtAuth(params, p), nil
	case "oauth1":
		return oauth1Auth(params, p, loc)
	case "awsv4":
		a := projectfile.Auth{
			Type: projectfile.AuthAWSV4, AccessKey: p("accessKey"), SecretKey: p("secretKey"), SessionToken: p("sessionToken"),
			Region: p("region"), Service: p("service"),
		}
		if strings.EqualFold(strings.TrimSpace(params["addAuthDataToQuery"]), "true") {
			a.In = projectfile.APIKeyInQuery
		}
		return a, nil
	case "hawk":
		return hawkAuth(params, p, loc)
	case "oauth2":
		return oauth2Auth(get(n, typ), params, func(v string) string { return t.conv(v, "the auth") }, loc)
	}
	return projectfile.Auth{Type: projectfile.AuthNone}, []importer.Diagnostic{{
		Code: CodeAuthUnsupported, Severity: importer.SeverityWarning, Source: loc,
		Message: "PostSonde has no " + authName(typ) + " auth: the request sends no auth. Set up the credentials by hand, e.g. as a header.",
	}}
}

// jwtAuth maps Postman's "JWT Bearer" auth (AUTH-16): the algorithm, the
// secret (HS*) or private key (the others), whether the secret is base64,
// the payload and extra header JSON, and where the token goes: the
// Authorization header with headerPrefix (Postman's default "Bearer"), or
// the query parameter queryParamKey (Postman's default "token"). Postman
// has no claim helpers: its payload uses {{$timestamp}}, which PostSonde
// renders too.
func jwtAuth(params map[string]string, p func(string) string) projectfile.Auth {
	a := projectfile.Auth{
		Type:         projectfile.AuthJWT,
		Algorithm:    strings.ToUpper(strings.TrimSpace(params["algorithm"])),
		SecretBase64: strings.EqualFold(strings.TrimSpace(params["isSecretBase64Encoded"]), "true"),
		JWTHeader:    p("header"),
		Payload:      p("payload"),
	}
	if a.Algorithm == "EDDSA" {
		a.Algorithm = "EdDSA"
	}
	if strings.HasPrefix(a.Algorithm, "HS") || a.Algorithm == "" {
		a.JWTSecret = p("secret")
	} else {
		a.JWTSecret = p("privateKey")
	}
	if strings.TrimSpace(a.JWTHeader) == "{}" {
		a.JWTHeader = ""
	}
	if strings.EqualFold(params["addTokenTo"], "queryParam") {
		a.In = projectfile.APIKeyInQuery
		a.Key = p("queryParamKey")
		if a.Key == "" {
			a.Key = "token"
		}
		return a
	}
	if prefix, ok := params["headerPrefix"]; ok && prefix != projectfile.DefaultBearerPrefix {
		v := p("headerPrefix")
		a.Prefix = &v
	}
	return a
}

// oauth1Auth maps Postman's OAuth 1.0 auth (AUTH-15): the consumer key and
// secret, the access token and token secret, the signature method (the RSA
// methods sign with privateKey, which goes in the consumer secret member),
// the fixed timestamp and nonce, realm, callback, verifier and
// includeBodyHash. The parameters go in the Authorization header with
// addParamsToHeader; without it Postman puts them in the URL, or in the
// body of a POST or PUT with a URL-encoded body (oauth1BodyIn). An empty
// or missing version leaves oauth_version out, as Postman does. What
// PostSonde does differently is one warning: another version,
// addEmptyParamsToSign, disableHeaderEncoding, and a signature method
// PostSonde lacks (kept, so the send says what to choose instead).
func oauth1Auth(params map[string]string, p func(string) string, loc importer.Location) (projectfile.Auth, []importer.Diagnostic) {
	on := func(key string) bool { return strings.EqualFold(strings.TrimSpace(params[key]), "true") }
	a := projectfile.Auth{
		Type:            projectfile.AuthOAuth1,
		ConsumerKey:     p("consumerKey"),
		ConsumerSecret:  p("consumerSecret"),
		AccessToken:     p("token"),
		TokenSecret:     p("tokenSecret"),
		SignatureMethod: strings.ToUpper(strings.TrimSpace(params["signatureMethod"])),
		Callback:        p("callback"),
		Verifier:        p("verifier"),
		Realm:           p("realm"),
		Nonce:           p("nonce"),
		Timestamp:       p("timestamp"),
		BodyHash:        on("includeBodyHash"),
		In:              projectfile.APIKeyInQuery,
	}
	if on("addParamsToHeader") {
		a.In = projectfile.APIKeyInHeader
	}
	if strings.HasPrefix(a.SignatureMethod, "RSA-") {
		// The token secret is not used by RSA signatures.
		a.ConsumerSecret, a.TokenSecret = p("privateKey"), ""
	}
	var changed []string
	switch v := strings.TrimSpace(params["version"]); v {
	case "":
		a.OmitVersion = true
	case oauth1.Version:
	default:
		changed = append(changed, "It sent oauth_version "+strconv.Quote(clip(v))+"; PostSonde sends 1.0.")
	}
	if a.SignatureMethod != "" && !oauth1.Supported(a.SignatureMethod) {
		changed = append(changed, "PostSonde cannot sign with "+clip(a.SignatureMethod)+": choose HMAC-SHA1, HMAC-SHA256, RSA-SHA1 or PLAINTEXT before sending.")
	}
	if on("addEmptyParamsToSign") {
		changed = append(changed, "It signed empty parameters; PostSonde leaves an empty token, callback or verifier out.")
	}
	if on("disableHeaderEncoding") {
		changed = append(changed, "It did not encode the header's values; PostSonde encodes them.")
	}
	if len(changed) == 0 {
		return a, nil
	}
	return a, []importer.Diagnostic{{
		Code: CodeAuthChanged, Severity: importer.SeverityWarning, Source: loc,
		Message: "The OAuth 1.0 auth was imported with a change. " + strings.Join(changed, " "),
	}}
}

// hawkAuth maps Postman's Hawk auth (AUTH-18): the ID, key and algorithm,
// ext (extraData), app, dlg (delegation) and includePayloadHash. Postman's
// user and its fixed nonce and timestamp have no PostSonde equivalent
// (PostSonde generates the nonce and timestamp on every send): one
// warning.
func hawkAuth(params map[string]string, p func(string) string, loc importer.Location) (projectfile.Auth, []importer.Diagnostic) {
	a := projectfile.Auth{
		Type: projectfile.AuthHawk, HawkID: p("authId"), HawkKey: p("authKey"),
		HawkAlgorithm: strings.ToLower(strings.TrimSpace(params["algorithm"])),
		Ext:           p("extraData"), App: p("app"), Dlg: p("delegation"),
		PayloadHash: strings.EqualFold(strings.TrimSpace(params["includePayloadHash"]), "true"),
	}
	var lost []string
	for _, k := range []string{"user", "nonce", "timestamp"} {
		if strings.TrimSpace(params[k]) != "" {
			lost = append(lost, k)
		}
	}
	if len(lost) == 0 {
		return a, nil
	}
	return a, []importer.Diagnostic{{
		Code: CodeAuthChanged, Severity: importer.SeverityWarning, Source: loc,
		Message: "The Hawk auth was imported with a change: PostSonde has no " + strings.Join(lost, ", ") +
			" setting (it makes a new nonce and timestamp for every send).",
	}}
}

// oauth1BodyIn sends the parameters of a request's own OAuth 1.0 auth
// that Postman does not put in the header in the body instead of the URL
// when the request is a POST or PUT with a URL-encoded body, as Postman
// does. An auth inherited from a folder keeps the URL.
func oauth1BodyIn(h *projectfile.HTTPRequest) {
	if h.Auth.Type == projectfile.AuthOAuth1 && h.Auth.In == projectfile.APIKeyInQuery &&
		(h.Method == "POST" || h.Method == "PUT") && h.Body.Mode == projectfile.BodyURLEncoded {
		h.Auth.In = projectfile.AuthInBody
	}
}

// authParams reads an auth type's parameters: v2.1's list of {key, value}
// or v2.0's object.
func authParams(n *yaml.Node) map[string]string {
	out := map[string]string{}
	switch {
	case n == nil:
	case n.Kind == yaml.SequenceNode:
		for _, it := range n.Items {
			if k := sget(it, "key"); k != "" {
				if _, seen := out[k]; !seen {
					out[k] = str(get(it, "value"))
				}
			}
		}
	case isMap(n):
		for _, pr := range n.Pairs {
			if k := str(pr.Key); k != "" {
				if _, seen := out[k]; !seen {
					out[k] = str(pr.Value)
				}
			}
		}
	}
	return out
}

// authName names a Postman auth type for messages.
func authName(typ string) string {
	switch typ {
	case "oauth1":
		return "OAuth 1.0"
	case "oauth2":
		return "OAuth 2.0"
	case "awsv4":
		return "AWS Signature"
	case "hawk":
		return "Hawk"
	case "ntlm":
		return "NTLM"
	case "edgegrid", "akamai":
		return "Akamai EdgeGrid"
	case "asap":
		return "ASAP (Atlassian)"
	}
	return "“" + clip(typ) + "”"
}

// behavior is the protocolProfileBehavior in effect for a request: the
// collection's, then each folder's, then the request's own, each key
// overriding the one above.
type behavior map[string]*yaml.Node

// with returns b with the members of n over it.
func (b behavior) with(n *yaml.Node) behavior {
	if !isMap(n) || len(n.Pairs) == 0 {
		return b
	}
	out := make(behavior, len(b)+len(n.Pairs))
	for k, v := range b {
		out[k] = v
	}
	for _, p := range n.Pairs {
		if k := str(p.Key); k != "" {
			out[k] = p.Value.Resolve()
		}
	}
	return out
}

// settings returns the request settings b maps to, and the keys of b that
// PostSonde has no setting for.
func (b behavior) settings() (projectfile.RequestSettings, []string) {
	var s projectfile.RequestSettings
	var lost []string
	boolean := func(n *yaml.Node) *bool {
		if v, ok := flag(n); ok {
			return &v
		}
		return nil
	}
	for k, n := range b {
		switch k {
		case "followRedirects":
			s.FollowRedirects = boolean(n)
		case "maxRedirects":
			if v, err := strconv.Atoi(str(n)); err == nil && v >= 0 {
				s.MaxRedirects = &v
			}
		case "followOriginalHttpMethod":
			s.FollowOriginalMethod = boolean(n)
		case "followAuthorizationHeader":
			s.ForwardAuthorization = boolean(n)
		case "strictSSL":
			s.VerifyTLS = boolean(n)
		case "disableUrlEncoding":
			if isTrue(n) {
				s.URLEncoding = projectfile.URLEncodingNone
			}
		case "disableCookies":
			if isTrue(n) {
				no := false
				s.SendCookies, s.StoreCookies = &no, &no
			}
		case "protocolVersion":
			switch strings.ToLower(str(n)) {
			case "http1":
				s.HTTPVersion = projectfile.HTTPVersion1
			case "http2":
				s.HTTPVersion = projectfile.HTTPVersion2
			case "auto", "":
			default:
				lost = append(lost, k)
			}
		case "disableBodyPruning":
			// PostSonde sends the body of GET, DELETE and custom methods.
		default:
			if n != nil && !isFalse(n) && !isEmpty(n) {
				lost = append(lost, clip(k))
			}
		}
	}
	slices.Sort(lost)
	return s, lost
}

// isEmpty reports whether n is null, an empty text, list or object.
func isEmpty(n *yaml.Node) bool {
	n = n.Resolve()
	switch {
	case n == nil:
		return true
	case n.Kind == yaml.ScalarNode:
		return n.Tag == yaml.TagNull || n.Value == ""
	case n.Kind == yaml.SequenceNode:
		return len(n.Items) == 0
	case n.Kind == yaml.MappingNode:
		return len(n.Pairs) == 0
	}
	return false
}
