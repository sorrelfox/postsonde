package snippet

// template.go: the templated view of a request (GEN-12, GEN-13): its
// {{variable}} references kept as written instead of resolved, and no
// secret value in it. The caller builds the request without rendering its
// references (an Expand that returns the text as it is) and with the auth
// TemplateAuth returns; FromTemplateBuild and MaskedTemplate do the rest.
// See docs/snippets.md.

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// FromTemplateBuild is FromBuild for a request built with its references
// kept as written: a reference the build percent-encoded (in a path
// parameter, a query parameter an auth adds) is written back as
// {{name}}, and a URL that cannot be parsed because a reference is in
// its scheme or host ({{baseUrl}}/items) is taken as it is, without its
// fragment, its userinfo as Basic credentials. No cookie from the jar is
// added: they are not part of the request as written.
func FromTemplateBuild(b request.HTTPBuild) (Request, error) {
	return fromBuild(b, nil, true)
}

// MaskedTemplate is Masked for a templated request: the same values are
// masked, except that a credential made only of {{variable}} references
// (requestsecrets.IsReference: "Bearer {{token}}", a Basic password
// "{{password}}") is kept, since it names the variable, not its value.
// secrets are the values typed in the auth (TypedSecrets).
func (r Request) MaskedTemplate(secrets []string) (Request, bool) {
	return r.masked(secrets, true)
}

// TemplateAuth returns the auth a templated request is built with: a
// without a value typed in a secret member, and without anything made for
// one send.
//
//   - basic, bearer, apikey, digest: each secret member typed as a value
//     (not requestsecrets.IsReference), or stored on another machine, is
//     Mask; references and the other members stay as written.
//   - jwt, oauth2: a token is made (signed, or asked from the token
//     endpoint) for each send, so it is Mask where it goes: the header
//     (Authorization unless the auth names another) with the prefix, or
//     the query parameter (access_token unless named).
//   - oauth1, awsv4, hawk: the request is signed for each send, so the
//     signature is Mask where it goes: "Authorization: OAuth ••••••",
//     "Authorization: AWS4-HMAC-SHA256 ••••••", "Authorization: Hawk
//     ••••••", or the query parameter oauth_signature or X-Amz-Signature
//     for an auth sent in the query. An oauth1 auth sent in the form body
//     adds nothing.
//
// An auth that sends nothing (disabled, or without the member that makes
// it send) is returned unchanged, as are none, netrc (built without
// reading its file, request.NetrcOptions.Defer) and unknown types.
// masked says whether a value was replaced by Mask.
func TemplateAuth(a projectfile.Auth) (_ projectfile.Auth, masked bool) {
	if a.Disabled {
		return a, false
	}
	switch a.Type {
	case projectfile.AuthBasic, projectfile.AuthBearer, projectfile.AuthAPIKey, projectfile.AuthDigest:
		for _, m := range a.SecretMembers() {
			v := a.SecretOf(m)
			if (v != "" && !requestsecrets.IsReference(v)) || (v == "" && a.IsLocal(m)) {
				a, masked = a.WithSecretOf(m, Mask), true
			}
		}
		a.LocalSecrets = nil
		return a, masked
	case projectfile.AuthJWT:
		if a.JWTSecret == "" && a.KeyFile == "" && !a.IsLocal("secret") {
			return a, false
		}
		return tokenStandIn(a, projectfile.DefaultJWTQueryParam), true
	case projectfile.AuthOAuth2:
		if a.TokenURL == "" {
			return a, false
		}
		return tokenStandIn(a, projectfile.DefaultOAuth2QueryParam), true
	case projectfile.AuthOAuth1:
		if a.ConsumerKey == "" {
			return a, false
		}
		switch a.In {
		case projectfile.APIKeyInQuery:
			return standIn(projectfile.APIKeyInQuery, "oauth_signature", Mask), true
		case projectfile.AuthInBody:
			return projectfile.Auth{Type: projectfile.AuthNone}, false
		}
		return standIn(projectfile.APIKeyInHeader, request.AuthorizationHeader, "OAuth "+Mask), true
	case projectfile.AuthAWSV4:
		if a.AccessKey == "" {
			return a, false
		}
		if a.In == projectfile.APIKeyInQuery {
			return standIn(projectfile.APIKeyInQuery, "X-Amz-Signature", Mask), true
		}
		return standIn(projectfile.APIKeyInHeader, request.AuthorizationHeader, "AWS4-HMAC-SHA256 "+Mask), true
	case projectfile.AuthHawk:
		if a.HawkID == "" {
			return a, false
		}
		return standIn(projectfile.APIKeyInHeader, request.AuthorizationHeader, "Hawk "+Mask), true
	}
	return a, false
}

// tokenStandIn is where a jwt or oauth2 auth puts its token, with Mask for
// the token; defaultQuery is the query parameter when a names none.
func tokenStandIn(a projectfile.Auth, defaultQuery string) projectfile.Auth {
	if a.In == projectfile.APIKeyInQuery {
		name := a.Key
		if name == "" {
			name = defaultQuery
		}
		return standIn(projectfile.APIKeyInQuery, name, Mask)
	}
	name := a.Key
	if name == "" {
		name = request.AuthorizationHeader
	}
	value := Mask
	if p := a.BearerPrefix(); p != "" {
		value = p + " " + Mask
	}
	return standIn(projectfile.APIKeyInHeader, name, value)
}

// standIn is an API key auth that adds name: value in a header, or
// name=value to the query.
func standIn(in, name, value string) projectfile.Auth {
	return projectfile.Auth{Type: projectfile.AuthAPIKey, Key: name, Value: value, In: in}
}

// TypedSecrets returns the values typed in a's secret members (not made
// of {{variable}} references), which a templated request masks wherever
// they appear.
func TypedSecrets(a projectfile.Auth) []string {
	var out []string
	for _, m := range a.SecretMembers() {
		if v := a.SecretOf(m); v != "" && !requestsecrets.IsReference(v) {
			out = append(out, v)
		}
	}
	return out
}

// KeepReferences is the Expand of a templated build: the text as written.
func KeepReferences(text string) (string, error) { return text, nil }

// encodedReference matches a {{reference}} whose braces, or whose
// content, a URL encoding changed.
var encodedReference = regexp.MustCompile(`(?i)(\{\{|%7B%7B)(.*?)(\}\}|%7D%7D)`)

// restoreReferences writes the {{references}} of u back as written, and
// Mask where it was percent-encoded.
func restoreReferences(u string) string {
	u = encodedReference.ReplaceAllStringFunc(u, func(m string) string {
		sub := encodedReference.FindStringSubmatch(m)
		inner := sub[2]
		if dec, err := url.PathUnescape(inner); err == nil {
			inner = dec
		}
		return "{{" + inner + "}}"
	})
	return strings.ReplaceAll(u, url.QueryEscape(Mask), Mask)
}

// templateURL returns raw, a URL with a reference in its scheme or host,
// without its fragment and userinfo, and the userinfo (nil for none).
func templateURL(raw string) (string, *url.Userinfo) {
	raw, _, _ = strings.Cut(raw, "#")
	var user *url.Userinfo
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		end := strings.IndexAny(rest, "/?")
		if end < 0 {
			end = len(rest)
		}
		if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
			name, pass, hasPass := strings.Cut(rest[:at], ":")
			if hasPass {
				user = url.UserPassword(name, pass)
			} else {
				user = url.User(name)
			}
			raw = raw[:i+3] + rest[at+1:]
		}
	}
	return restoreReferences(raw), user
}

// isCredentialHeader reports whether a header field carries credentials:
// Authorization or Proxy-Authorization, in any spelling.
func isCredentialHeader(name string) bool {
	return strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization")
}

// credentialOf returns the credentials of an Authorization value: what
// follows the scheme, or the whole value without one.
func credentialOf(value string) string {
	if _, rest, ok := strings.Cut(value, " "); ok && strings.TrimSpace(rest) != "" {
		return rest
	}
	return value
}
