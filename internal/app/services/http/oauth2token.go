package http

// oauth2token.go is the Auth tab's view of an oauth2 auth's stored token
// (AUTH-11, AUTH-13): its status with the token masked, and getting,
// refreshing and clearing it by hand. See docs/auth.md#token-store.

import (
	"context"
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// EventOAuth2TokenChanged is sent when a stored OAuth 2.0 token was stored,
// refreshed or removed (WithOAuth2Events), with OAuth2TokenChangedDTO.
const EventOAuth2TokenChanged = Domain + ":oauth2-token-changed"

func init() {
	application.RegisterEvent[OAuth2TokenChangedDTO](EventOAuth2TokenChanged)
}

// OAuth2TokenChangedDTO says which token changed.
type OAuth2TokenChangedDTO struct {
	// Key is the token's key (OAuth2TokenDTO.Key); "" when every token
	// changed.
	Key string `json:"key"`
}

// Statuses of OAuth2TokenDTO.
const (
	// OAuth2TokenNone: no token is stored for the auth.
	OAuth2TokenNone = "none"
	// OAuth2TokenValid: the stored token has not expired (or has no
	// expiry).
	OAuth2TokenValid = "valid"
	// OAuth2TokenExpired: the stored token has expired; a send refreshes
	// it when it has a refresh token, else gets a new one.
	OAuth2TokenExpired = "expired"
)

// OAuth2TokenInputDTO names the request (HTTP, GraphQL or SSE) whose
// oauth2 auth, its own or inherited, is meant.
type OAuth2TokenInputDTO struct {
	RequestID string `json:"requestId"`
}

// OAuth2GetTokenInputDTO is what OAuth2GetToken takes.
type OAuth2GetTokenInputDTO struct {
	RequestID string `json:"requestId"`
	// Refresh refreshes the stored token with its refresh token, rather
	// than getting a new one by the auth's grant.
	Refresh bool `json:"refresh"`
}

// OAuth2TokenDTO is the stored token of a request's oauth2 auth. The
// token itself never leaves Go: Token shows its last characters only.
type OAuth2TokenDTO struct {
	// Available is false when the request sends no oauth2 auth (another
	// type, a disabled auth, no token URL yet); the rest is then empty.
	Available bool `json:"available"`
	// Key is the token's key in the store: a hash of the auth's fields,
	// never a value of them.
	Key string `json:"key"`
	// Grant is the auth's grant type.
	Grant string `json:"grant"`
	// Status is "none", "valid" or "expired".
	Status string `json:"status"`
	// Token is the access token masked: •••••• and its last four
	// characters (only the mask for a short token); "" without one.
	Token string `json:"token"`
	// TokenType and Scope are as the server sent them.
	TokenType string `json:"tokenType"`
	Scope     string `json:"scope"`
	// ExpiresAt is when the token expires, Unix milliseconds; 0 when the
	// server gave no lifetime. ObtainedAt is when it was got.
	ExpiresAt  int64 `json:"expiresAt"`
	ObtainedAt int64 `json:"obtainedAt"`
	// Refreshable is set when the token has a refresh token.
	Refreshable bool `json:"refreshable"`
	// FolderName names the folder whose auth it is when the request
	// inherits it; "" for the request's own.
	FolderName string `json:"folderName"`
	// SendsIDToken is set when the auth sends the ID token instead of the
	// access token (AUTH-14): Status is then the ID token's too.
	SendsIDToken bool `json:"sendsIdToken"`
	// IDToken is the stored token's OpenID Connect ID token, nil without
	// one.
	IDToken *OAuth2IDTokenDTO `json:"idToken"`
}

// OAuth2IDTokenDTO is an OpenID Connect ID token's claims (AUTH-14), as
// the Auth tab shows them. The token itself never leaves Go: Token shows
// its last characters only. Its claims were checked when it arrived (the
// issuer, audience, expiry and nonce); its signature is not verified
// (docs/auth.md#oidc-id-token).
type OAuth2IDTokenDTO struct {
	// Token is the ID token masked, as OAuth2TokenDTO.Token.
	Token string `json:"token"`
	// Algorithm is the header's alg.
	Algorithm string `json:"algorithm"`
	// Issuer, Subject and Audience are iss, sub and aud.
	Issuer   string   `json:"issuer"`
	Subject  string   `json:"subject"`
	Audience []string `json:"audience"`
	// ExpiresAt and IssuedAt are exp and iat, Unix milliseconds; 0 when
	// absent.
	ExpiresAt int64 `json:"expiresAt"`
	IssuedAt  int64 `json:"issuedAt"`
	// Name and Email are the profile claims, "" when absent.
	Name  string `json:"name"`
	Email string `json:"email"`
	// Expired is set once exp has passed.
	Expired bool `json:"expired"`
	// Error says why the claims cannot be read; "" when they can.
	Error string `json:"error"`
}

// OAuth2Token returns the status of the stored token of request
// in.RequestID's oauth2 auth. Only the auth is built, with undefined
// variables left as written, and nothing is sent.
func (s *HTTPService) OAuth2Token(ctx context.Context, in OAuth2TokenInputDTO) (OAuth2TokenDTO, error) {
	p, folder, err := s.oauth2Pending(ctx, in.RequestID)
	if err != nil || p == nil {
		return OAuth2TokenDTO{}, err
	}
	return s.tokenDTO(p, folder), nil
}

// OAuth2GetToken gets a token for request in.RequestID's oauth2 auth now,
// as a send would, and stores it: with in.Refresh the stored token
// refreshed with its refresh token (CodeOAuth2NoRefreshToken without
// one), else a new token by the auth's grant, whatever is stored. The
// request is built as a send builds it, so it fails as Send does when it
// cannot be. It is a user action: the token request goes through the
// app's dialer marked with it (PRV-05). Cancelling the call stops it.
func (s *HTTPService) OAuth2GetToken(ctx context.Context, in OAuth2GetTokenInputDTO) (OAuth2TokenDTO, error) {
	if err := ctx.Err(); err != nil {
		return OAuth2TokenDTO{}, errs.Wrap(err, errs.KindOf(err), "Getting the OAuth 2.0 token was interrupted.")
	}
	src, x, b, err := s.tokenBuild(ctx, in.RequestID)
	if err != nil {
		return OAuth2TokenDTO{}, err
	}
	if b.OAuth2 == nil {
		return OAuth2TokenDTO{}, noOAuth2()
	}
	mode := tokenGrant
	if in.Refresh {
		mode = tokenRefresh
	}
	ctx = netx.WithUserOp(ctx, netx.UserOp{Kind: Domain + ".oauth2_token", ID: in.RequestID})
	log, err := s.obtainToken(ctx, &b, mode, nil)
	if err != nil {
		// Masked like a send's error (docs/privacy.md#secrets-in-errors).
		secrets := append(secretsOf(x, b), b.OAuth2.Token.Secrets()...)
		return OAuth2TokenDTO{}, errs.Redact(err, httpclient.NewSecretMasker(append(secrets, log.secrets()...)).MaskText)
	}
	return s.tokenDTO(b.OAuth2, src.AuthFolderName), nil
}

// OAuth2ClearToken removes the stored token of request in.RequestID's
// oauth2 auth, and returns the status after.
func (s *HTTPService) OAuth2ClearToken(ctx context.Context, in OAuth2TokenInputDTO) (OAuth2TokenDTO, error) {
	p, folder, err := s.oauth2Pending(ctx, in.RequestID)
	if err != nil {
		return OAuth2TokenDTO{}, err
	}
	if p == nil {
		return OAuth2TokenDTO{}, noOAuth2()
	}
	if _, ok := s.tokenStore().Get(p.Key); ok {
		s.deleteToken(p.Key)
	}
	return s.tokenDTO(p, folder), nil
}

// CodeNoOAuth2: the request sends no oauth2 auth (another type, a disabled
// auth, or no token URL yet), so it has no token (kind invalid_argument).
const CodeNoOAuth2 = "http.no_oauth2"

func noOAuth2() error {
	return errs.New(errs.InvalidArgument, "The request's auth gets no OAuth 2.0 token.").
		WithCode(CodeNoOAuth2).
		WithHint("Choose the OAuth 2.0 auth type, enter the token URL, and make sure “Send this auth” is on.")
}

// tokenBuild builds request id as a send would, for its oauth2 auth's
// token request: an HTTP, GraphQL or SSE request whole, a websocket,
// socketio or grpc request only its auth (AUTH-31), with the app's TLS
// verification for the token request.
func (s *HTTPService) tokenBuild(ctx context.Context, id string) (collection.HTTPSource, *expansion, request.HTTPBuild, error) {
	if s.authLoad == nil {
		return s.build(ctx, id, buildOptions{})
	}
	src, err := s.loadAuth(ctx, id)
	if err != nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, err
	}
	if src.Handshake == "" {
		return s.build(ctx, id, buildOptions{})
	}
	chain, err := s.chainOf(src)
	if err != nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, err
	}
	x := &expansion{resolver: template.Resolver{Scopes: chain}}
	b := request.HTTPBuild{InsecureSkipVerify: s.defaultInsecure()}
	b.Request.Settings = httpclient.DefaultSettings()
	if src.Auth.Disabled {
		return src, x, b, nil
	}
	a, err := request.AuthWith(src.Auth, nil, request.AuthOptions{Expand: x.expand, Root: workspaceRoot(src), AllowedDirs: s.allowed()})
	if err != nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, err
	}
	if err := x.unresolvedErr(); err != nil {
		return collection.HTTPSource{}, nil, request.HTTPBuild{}, err
	}
	b.OAuth2, b.AuthSecrets = a.OAuth2(), a.Secrets
	return src, x, b, nil
}

// oauth2Pending builds only the auth of request id, its own or inherited,
// with undefined variables left as written: its oauth2 token request, nil
// when it sends none. A problem elsewhere in the request does not stop it.
func (s *HTTPService) oauth2Pending(ctx context.Context, id string) (*request.OAuth2Pending, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", errs.Wrap(err, errs.KindOf(err), "Reading the OAuth 2.0 token was interrupted.")
	}
	if !s.canLoadAuth() {
		return nil, "", errs.New(errs.Unsupported, "Sending HTTP requests is not available.").WithCode(CodeUnavailable)
	}
	src, err := s.loadAuth(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if src.Auth.Disabled {
		return nil, "", nil
	}
	chain, err := scopeChain(src)
	if err != nil {
		return nil, "", err
	}
	x := &expansion{resolver: template.Resolver{Scopes: chain}, allowUnresolved: true}
	a, err := request.AuthWith(src.Auth, nil, request.AuthOptions{Expand: x.expand, Root: workspaceRoot(src), AllowedDirs: s.allowed()})
	if err != nil {
		return nil, "", err
	}
	return a.OAuth2(), src.AuthFolderName, nil
}

// tokenDTO returns the status of p's stored token.
func (s *HTTPService) tokenDTO(p *request.OAuth2Pending, folder string) OAuth2TokenDTO {
	out := OAuth2TokenDTO{Available: true, Key: p.Key, Grant: p.Token.Config.GrantType(), Status: OAuth2TokenNone, FolderName: folder, SendsIDToken: p.IDToken}
	t, ok := s.tokenStore().Get(p.Key)
	if !ok || t.AccessToken == "" {
		return out
	}
	out.Status = OAuth2TokenValid
	if !s.usable(p, t) {
		out.Status = OAuth2TokenExpired
	}
	out.IDToken = s.idTokenDTO(t)
	out.Token = maskToken(t.AccessToken)
	out.TokenType, out.Scope = t.TokenType, t.Scope
	if !t.Expiry.IsZero() {
		out.ExpiresAt = t.Expiry.UnixMilli()
	}
	if !t.Obtained.IsZero() {
		out.ObtainedAt = t.Obtained.UnixMilli()
	}
	out.Refreshable = t.RefreshToken != ""
	return out
}

// maskToken shows a token as the mask and its last four characters, enough
// to tell two tokens apart; a token of fewer than 16 bytes as the mask
// only.
func maskToken(t string) string {
	if len(t) < 16 {
		return httpclient.Mask
	}
	return httpclient.Mask + t[len(t)-4:]
}

// idTokenDTO returns the claims of t's ID token for the Auth tab, nil
// without one.
func (s *HTTPService) idTokenDTO(t oauth2.Token) *OAuth2IDTokenDTO {
	if t.IDToken == "" {
		return nil
	}
	out := &OAuth2IDTokenDTO{Token: maskToken(t.IDToken), Audience: []string{}}
	id, err := oauth2.DecodeIDToken(t.IDToken)
	if err != nil {
		var e *errs.Error
		out.Error = err.Error()
		if errors.As(err, &e) {
			out.Error = e.Message
		}
		return out
	}
	out.Algorithm, out.Issuer, out.Subject, out.Name, out.Email = id.Algorithm, id.Issuer, id.Subject, id.Name, id.Email
	out.Audience = append(out.Audience, id.Audience...)
	out.ExpiresAt = id.Expiry.UnixMilli()
	if !id.IssuedAt.IsZero() {
		out.IssuedAt = id.IssuedAt.UnixMilli()
	}
	out.Expired = !s.clock().Before(id.Expiry)
	return out
}
