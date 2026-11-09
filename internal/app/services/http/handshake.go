package http

// handshake.go builds the auth of the handshakes of websocket, socketio
// and grpc requests (AUTH-31) with what an HTTP send uses: the request's
// own or inherited auth with its local secrets (collection.LoadAuthSource),
// the project folder and allowed directories of a JWT key file, the netrc
// file, and the OAuth 2.0 token store and token requests. See
// docs/auth.md#auth-on-every-protocol-auth-31.

import (
	"context"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// WithAuthLoader loads the auth of a request of any kind
// (collection.LoadAuthSource), for HandshakeAuth and for the Auth tab's
// tools (JWTPreview, NetrcFile, NetrcCheck, OAuth2Token, OAuth2GetToken,
// OAuth2ClearToken) on websocket, socketio and grpc requests. Without it
// they use the HTTP loader, which takes HTTP, GraphQL and SSE requests
// only.
func WithAuthLoader(load Loader) Option {
	return func(s *HTTPService) { s.authLoad = load }
}

// loadAuth loads request id for its auth only.
func (s *HTTPService) loadAuth(ctx context.Context, id string) (collection.HTTPSource, error) {
	if s.authLoad != nil {
		return s.authLoad(ctx, id)
	}
	return s.load(ctx, id)
}

// canLoadAuth reports whether loadAuth can load anything.
func (s *HTTPService) canLoadAuth() bool { return s.authLoad != nil || s.load != nil }

// HandshakeAuth builds the auth that the handshake of request in.RequestID
// sends (request.HandshakeAuthFunc): its own auth or the closest folder's,
// with its local secrets, its fields rendered with in.Expand, checked by
// request.HandshakeAuth for in.Protocol. An oauth2 auth's token is the
// stored one while it is good, else one got from the token endpoint (or
// the browser sign-in) as a send gets it, after in.Ready; the token
// request is the user's (PRV-05) and is masked in errors.
//
// It is not a method, so it is not bound.
func HandshakeAuth(ctx context.Context, s *HTTPService, in request.HandshakeInput) (request.Handshake, error) {
	if err := ctx.Err(); err != nil {
		return request.Handshake{}, errs.Wrap(err, errs.KindOf(err), "Building the auth was interrupted.")
	}
	none := request.Handshake{Type: projectfile.AuthNone}
	if s == nil || !s.canLoadAuth() || in.RequestID == "" {
		return none, nil
	}
	src, err := s.loadAuth(ctx, in.RequestID)
	if err != nil {
		return request.Handshake{}, err
	}
	out := request.Handshake{Type: src.Auth.Type, FolderName: src.AuthFolderName}
	if out.Type == "" || out.Type == projectfile.AuthInherit {
		out.Type = projectfile.AuthNone
	}
	a, err := request.HandshakeAuth(src.Auth, in.Protocol, request.AuthOptions{
		Expand: in.Expand, Root: workspaceRoot(src), AllowedDirs: s.allowed(), URL: in.URL, Netrc: s.netrcOptions(false),
	})
	if err != nil {
		return request.Handshake{}, err
	}
	out.Headers, out.Query, out.Credentials, out.Secrets = a.Headers, a.Query, a.Credentials, a.Secrets
	if in.Ready != nil {
		if err := in.Ready(); err != nil {
			return request.Handshake{}, err
		}
	}
	p := a.OAuth2()
	if p == nil {
		return out, nil
	}
	b := request.HTTPBuild{OAuth2: p, InsecureSkipVerify: s.defaultInsecure(), Proxy: in.Proxy}
	b.Request.Settings = httpclient.DefaultSettings()
	ctx = netx.WithUserOp(ctx, netx.UserOp{Kind: string(in.Protocol) + ".oauth2_token", ID: in.RequestID})
	log, err := s.obtainToken(ctx, &b, tokenAuto, nil)
	if err != nil {
		secrets := append(slices.Clone(out.Secrets), p.Token.Secrets()...)
		return request.Handshake{}, errs.Redact(err, httpclient.NewSecretMasker(append(secrets, log.secrets()...)).MaskText)
	}
	token := log.token.AccessToken
	if p.IDToken {
		if log.token.IDToken == "" {
			return request.Handshake{}, noIDToken()
		}
		token = log.token.IDToken
	}
	out.Secrets = append(append(slices.Clone(out.Secrets), log.secrets()...), token)
	out.Credentials = &request.Credentials{Token: token}
	if p.In == projectfile.APIKeyInQuery {
		out.Query = append(out.Query, request.QueryParam{Name: p.Name, Value: token})
		return out, nil
	}
	value := token
	if p.Prefix != "" {
		value = p.Prefix + " " + token
	}
	out.Headers = append(out.Headers, httpclient.Header{Name: p.Name, Value: value, Enabled: true})
	return out, nil
}

// GRPCAuthURL is the URL a grpc request's auth sees for address (a
// netrc auth looks its host up): https:// with TLS, else http://, and the
// address as typed.
func GRPCAuthURL(address string, tls bool) string {
	if tls {
		return "https://" + strings.TrimSpace(address)
	}
	return "http://" + strings.TrimSpace(address)
}

// StreamChecks returns the checks of a websocket, socketio or grpc request
// (streamcheck.Loader, TEST-08): its assertions as they are now, pending
// edits included, rendered with its variables (the scopes of a send,
// runtime variables included), with its schema files read from its
// project or the allowed directories (PRV-10).
//
// It is not a method, so it is not bound.
func StreamChecks(ctx context.Context, s *HTTPService, id string) (streamcheck.Checks, error) {
	if err := ctx.Err(); err != nil {
		return streamcheck.Checks{}, errs.Wrap(err, errs.KindOf(err), "Loading the assertions was interrupted.")
	}
	if s == nil || !s.canLoadAuth() {
		return streamcheck.Checks{}, streamcheck.Unavailable()
	}
	src, err := s.loadAuth(ctx, id)
	if err != nil {
		return streamcheck.Checks{}, err
	}
	if len(src.Request.Assertions) == 0 {
		return streamcheck.Checks{}, nil
	}
	chain, err := s.chainOf(src)
	if err != nil {
		return streamcheck.Checks{}, err
	}
	r := template.Resolver{Scopes: chain, Files: s.fileReader(src)}
	return streamcheck.Prepare(r, src.Request.Assertions, schemaLoader(s.filePolicy(src))), nil
}
