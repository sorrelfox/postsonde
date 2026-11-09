package request

// handshake.go applies a request's auth to the handshake of a connection
// that is not an HTTP request of its own (AUTH-31): the upgrade request of
// a WebSocket, the transport requests of a Socket.IO connection and the
// metadata of a gRPC call. The auth is the same as for HTTP (ResolveAuth,
// AuthWith); only the types that need the HTTP request itself are left
// out. See docs/auth.md#auth-on-every-protocol-auth-31.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// CodeAuthNotApplicable: the auth type cannot be used on the protocol's
// handshake (kind unsupported): Digest, OAuth 1.0, AWS Signature and Hawk
// on any handshake, and an auth sent in the query string on gRPC.
// Details: type, protocol, and field "in" for a location.
const CodeAuthNotApplicable = "request.auth_not_applicable"

// DetailAuthProtocol is the detail of CodeAuthNotApplicable naming the
// protocol.
const DetailAuthProtocol = "protocol"

// HandshakeProtocol is a protocol whose handshake HandshakeAuth fills.
type HandshakeProtocol string

// The handshake protocols.
const (
	HandshakeWebSocket HandshakeProtocol = "websocket"
	HandshakeSocketIO  HandshakeProtocol = "socketio"
	HandshakeGRPC      HandshakeProtocol = "grpc"
)

// label names p in messages.
func (p HandshakeProtocol) label() string {
	switch p {
	case HandshakeWebSocket:
		return "WebSocket"
	case HandshakeSocketIO:
		return "Socket.IO"
	case HandshakeGRPC:
		return "gRPC"
	}
	return string(p)
}

// HandshakeApplies reports whether an auth of type typ can be sent on the
// handshake of p: every type but Digest (it answers an HTTP challenge),
// OAuth 1.0, AWS Signature and Hawk (they sign the HTTP request: method,
// URL and body). The location is checked by HandshakeAuth.
func HandshakeApplies(typ string) bool {
	switch typ {
	case projectfile.AuthDigest, projectfile.AuthOAuth1, projectfile.AuthAWSV4, projectfile.AuthHawk:
		return false
	}
	return true
}

// HandshakeAuth is AuthWith for the handshake of protocol p. a must be
// resolved (ResolveAuth), with its local secrets merged.
//
// A disabled auth, and "none", add nothing. A type that cannot be sent on
// a handshake (HandshakeApplies) is refused with CodeAuthNotApplicable
// rather than left out, so a connection never goes without the
// credentials the Auth tab shows; so is a query parameter (apikey, jwt or
// oauth2 with in "query") on gRPC, which has no URL. The result is that of
// AuthWith: Headers for the handshake (gRPC: the metadata), Query for its
// URL, and for oauth2 the pending token request (AuthResult.OAuth2) the
// caller gets the token for. o.URL is the connection's URL as rendered (a
// netrc auth looks its host up).
func HandshakeAuth(a projectfile.Auth, p HandshakeProtocol, o AuthOptions) (AuthResult, error) {
	if a.Disabled || a.Type == projectfile.AuthNone {
		return AuthResult{}, nil
	}
	if !HandshakeApplies(a.Type) {
		return AuthResult{}, notApplicable(a.Type, p, "",
			authName(a.Type)+" cannot be sent on a "+p.label()+" handshake: it "+whyNot(a.Type)+".",
			"Choose another auth type for this request in its Auth tab (No auth, Basic, Bearer, API key, JWT, OAuth 2.0 or Netrc), or set its headers yourself.")
	}
	if p == HandshakeGRPC && inQuery(a) {
		return AuthResult{}, notApplicable(a.Type, p, "in",
			"gRPC has no query string, so the "+authName(a.Type)+" cannot be sent in one.",
			"Send it in a header (gRPC metadata) instead: set “Add to” to Header in the Auth tab.")
	}
	r, err := AuthWith(a, nil, o)
	if err != nil {
		return AuthResult{}, err
	}
	if c := r.Credentials; c != nil {
		// As they are, for a Socket.IO auth payload: the header values in
		// Secrets carry them with a prefix or in Base64.
		r.Secrets = append(r.Secrets, c.Token, c.Password, c.Value)
	}
	return r, nil
}

func notApplicable(typ string, p HandshakeProtocol, field, msg, hint string) error {
	e := errs.New(errs.Unsupported, msg).
		WithCode(CodeAuthNotApplicable).
		WithHint(hint).
		WithDetail(DetailAuthType, typ).
		WithDetail(DetailAuthProtocol, string(p))
	if field != "" {
		e = e.WithDetail(DetailAuthField, field)
	}
	return e
}

// inQuery reports whether a sends its credentials in the query string.
func inQuery(a projectfile.Auth) bool {
	switch a.Type {
	case projectfile.AuthAPIKey, projectfile.AuthJWT, projectfile.AuthOAuth2:
		return a.In == projectfile.APIKeyInQuery
	}
	return false
}

func authName(typ string) string {
	switch typ {
	case projectfile.AuthDigest:
		return "Digest auth"
	case projectfile.AuthOAuth1:
		return "OAuth 1.0"
	case projectfile.AuthAWSV4:
		return "AWS Signature"
	case projectfile.AuthHawk:
		return "Hawk auth"
	case projectfile.AuthAPIKey:
		return "API key"
	case projectfile.AuthJWT:
		return "JWT"
	case projectfile.AuthOAuth2:
		return "OAuth 2.0 token"
	}
	return typ + " auth"
}

func whyNot(typ string) string {
	if typ == projectfile.AuthDigest {
		return "answers the challenge of an HTTP 401 response, which a handshake does not get answered"
	}
	return "signs an HTTP request's method, URL and body"
}

// HandshakeHeaders returns the auth headers that no enabled row of rows
// overrides (WithAuthHeaders), for a handshake. For gRPC (lower) the
// names are in lower case, as metadata keys are.
func HandshakeHeaders(rows []httpclient.Header, auth []httpclient.Header, lower bool) []httpclient.Header {
	var out []httpclient.Header
	for _, h := range WithAuthHeaders(rows, auth)[len(rows):] {
		if lower {
			h.Name = strings.ToLower(h.Name)
		}
		out = append(out, h)
	}
	return out
}

// CodeAuthPayloadInvalid: the Socket.IO auth payload the credentials go in
// is not a JSON object (kind invalid_argument). Details: field
// "authPayload".
const CodeAuthPayloadInvalid = "request.auth_payload_invalid"

// payloadMembers are the names and values the credentials c become in a
// Socket.IO auth payload, in order; none for nil c.
func payloadMembers(c *Credentials) [][2]string {
	switch {
	case c == nil:
		return nil
	case c.Token != "":
		return [][2]string{{"token", c.Token}}
	case c.Key != "":
		return [][2]string{{c.Key, c.Value}}
	case c.Username != "" || c.Password != "":
		return [][2]string{{"username", c.Username}, {"password", c.Password}}
	}
	return nil
}

// PayloadQuery returns the credentials c as the query parameters a
// socket.io v2 server reads instead of an auth payload
// (socket.handshake.query, which v2 clients fill from the URL's query):
// the members WithPayloadCredentials would add, under the same names.
// WithAuthQuery leaves out a name the URL already has.
func PayloadQuery(c *Credentials) []QueryParam {
	var out []QueryParam
	for _, kv := range payloadMembers(c) {
		out = append(out, QueryParam{Name: kv[0], Value: kv[1]})
	}
	return out
}

// WithPayloadCredentials returns the Socket.IO auth payload text payload
// (rendered; "" for none) with the credentials c added as members
// (AUTH-31): "token" for a bearer, JWT or OAuth 2.0 token, "username" and
// "password" for Basic and netrc, the key's name for an API key. A member
// the payload already has is kept as typed, as a typed header wins over
// the Auth tab. Nothing changes for nil c.
func WithPayloadCredentials(payload string, c *Credentials) (string, error) {
	add := payloadMembers(c)
	if len(add) == 0 {
		return payload, nil
	}
	body := strings.TrimSpace(payload)
	if body == "" {
		body = "{}"
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &members); err != nil || members == nil {
		return "", errs.New(errs.InvalidArgument, "The auth payload is not a JSON object, so the credentials of the Auth tab cannot be added to it.").
			WithCode(CodeAuthPayloadInvalid).
			WithHint("Make the auth payload a JSON object such as {}, or send the credentials in the handshake instead.").
			WithDetail(DetailAuthField, "authPayload")
	}
	// The payload's own members as typed, then the credentials it does not
	// have, so the text stays as the user wrote it.
	var b bytes.Buffer
	inner := strings.TrimSpace(body[1 : len(body)-1])
	b.WriteByte('{')
	b.WriteString(inner)
	n := len(members)
	for _, kv := range add {
		if _, ok := members[kv[0]]; ok {
			continue
		}
		k, _ := json.Marshal(kv[0])
		v, _ := json.Marshal(kv[1])
		if n > 0 {
			b.WriteByte(',')
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
		n++
	}
	b.WriteByte('}')
	return b.String(), nil
}

// HandshakeInput is what a HandshakeAuthFunc builds the auth of one
// handshake from.
type HandshakeInput struct {
	// RequestID is the request whose auth (own or inherited) is sent.
	RequestID string
	// Protocol is the connection's protocol.
	Protocol HandshakeProtocol
	// URL is the connection's URL, rendered: what a netrc auth looks up.
	// For gRPC, http:// or https:// and the address.
	URL string
	// Expand renders the {{variables}} of the auth fields with the
	// request's scopes, as the connection renders its other fields.
	Expand Expand
	// Ready is called after the fields are rendered and before anything
	// goes on the network (an OAuth 2.0 token request): an error from it
	// (the connection's undefined variables) stops there. nil is ready.
	Ready func() error
	// Proxy is the request's own proxy (settings.proxy), which an OAuth
	// 2.0 token request made for the handshake goes through too; the zero
	// Proxy inherits the proxy in effect.
	Proxy Proxy
}

// Handshake is the auth a handshake sends.
type Handshake struct {
	// Type is the auth type sent ("none" for none), FolderName the folder
	// it is inherited from ("" for the request's own).
	Type, FolderName string
	// Headers go after the handshake's own header rows (HandshakeHeaders);
	// for gRPC they are metadata. Query goes at the end of its URL
	// (WithAuthQuery).
	Headers []httpclient.Header
	Query   []QueryParam
	// Credentials are the values sent, for a Socket.IO auth payload
	// (WithPayloadCredentials); nil when there are none.
	Credentials *Credentials
	// Secrets are the values to mask wherever the handshake is shown.
	Secrets []string
}

// HandshakeAuthFunc builds the auth of a handshake (AUTH-31): the app's
// is the HTTP service's (internal/app/services/http, HandshakeAuth), which
// resolves the request's auth with its folders and local secrets, applies
// HandshakeAuth and gets an OAuth 2.0 token.
type HandshakeAuthFunc func(ctx context.Context, in HandshakeInput) (Handshake, error)
