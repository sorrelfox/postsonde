package http

// oauth2.go gets the token of an oauth2 auth (AUTH-07…12, AUTH-14) and
// shows the exchange in the timeline (AUTH-33). The build
// carries the token request (request.HTTPBuild.OAuth2); a send gets the
// token as part of its operation, from the token store (AUTH-13),
// refreshed with its refresh token once it expires, or from the token
// endpoint through the app's HTTP client (its dialer and user-operation
// guard, TLS trust, client certificates and proxy), after a sign-in in
// the system browser for the authorization code grant, or from the
// redirect of the implicit grant's sign-in; then adds it, or its OpenID
// Connect ID token (UseOAuth2Token). Copies and previews never ask the token endpoint:
// they use a stored token, or a placeholder. See
// docs/auth.md#oauth-20-auth-08-auth-12-auth-33.

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

// Progress messages while a send gets its OAuth 2.0 token.
const (
	StageToken   = "Getting an OAuth 2.0 token"
	StageRefresh = "Refreshing the OAuth 2.0 token"
	StageSignIn  = "Waiting for the sign-in in the browser"
)

// HopToken is the kind of a timeline hop that is an OAuth 2.0 token
// request (AUTH-33), shown before the request's own hops.
const HopToken = "token"

// Timeline events of an oauth2 auth.
const (
	// EventOAuth2Token: the send got a token from the token endpoint, by
	// its grant or by refreshing the stored one. Detail: the grant
	// (refresh_token for a refresh), the token URL, the status and the
	// lifetime.
	EventOAuth2Token = "oauth2_token"
	// EventOAuth2TokenStored: the send used a token obtained earlier.
	// Detail: the grant and when it expires.
	EventOAuth2TokenStored = "oauth2_token_stored"
	// EventOAuth2TokenDropped: the server answered 401 to a stored token,
	// which is dropped, so the next send gets a new one.
	EventOAuth2TokenDropped = "oauth2_token_dropped"
	// EventOAuth2RefreshFailed: refreshing the stored token failed, so the
	// send got a new token by the auth's grant. Detail: the status and
	// the error.
	EventOAuth2RefreshFailed = "oauth2_refresh_failed"
	// EventOAuth2Authorized: the user signed in in the browser and the
	// authorization code (or the implicit grant's token) came back
	// (AUTH-07, AUTH-10). Detail: the redirect URI.
	EventOAuth2Authorized = "oauth2_authorized"
)

// CodeOAuth2NoRefreshToken: a refresh was asked for, but the stored token
// has no refresh token, or there is no stored token (kind
// invalid_argument).
const CodeOAuth2NoRefreshToken = "http.oauth2_no_refresh_token"

// oauth2State is what the service keeps for oauth2 auths besides the
// token store.
type oauth2State struct {
	// running marks the keys whose token is being got (lockToken); the
	// channel closes when it is done.
	running map[string]chan struct{}
	// emit tells the UI a stored token changed (WithOAuth2Events); nil
	// sends nothing.
	emit func(name string, data any)
	// browser opens the authorization URL of the redirect grants
	// (authorization code, implicit) in the system browser
	// (WithOAuth2Browser); nil cannot.
	browser func(url string) error
	// authorizeTimeout is how long a sign-in may take; 0 is
	// oauth2.DefaultAuthorizeTimeout.
	authorizeTimeout time.Duration
}

// WithOAuth2Browser opens the authorization URL of the authorization code
// and implicit grants (AUTH-07, AUTH-10) with open: the app's
// Browser.OpenURL. The user asked
// for the token (a send, Get new token), so nothing asks again. Without
// it the grant fails (oauth2.CodeBrowserFailed). Tests pass a browser
// that follows the redirect itself.
func WithOAuth2Browser(open func(url string) error) Option {
	return func(s *HTTPService) { s.oauth.browser = open }
}

// WithOAuth2AuthorizeTimeout sets how long a sign-in in the browser may
// take; 0 is oauth2.DefaultAuthorizeTimeout (5 minutes).
func WithOAuth2AuthorizeTimeout(d time.Duration) Option {
	return func(s *HTTPService) { s.oauth.authorizeTimeout = d }
}

// WithOAuth2Store keeps the tokens of oauth2 auths in store. Without it
// they are kept in memory for the session (oauth2.MemoryStore); the app
// keeps them in the workspace's local directory (package tokenstore).
func WithOAuth2Store(store oauth2.Store) Option {
	return func(s *HTTPService) { s.tokens = store }
}

// WithOAuth2Events sends EventOAuth2TokenChanged through emit whenever the
// service stores or removes a token, so the Auth tab shows it.
func WithOAuth2Events(emit func(name string, data any)) Option {
	return func(s *HTTPService) { s.oauth.emit = emit }
}

// tokenStore returns the service's token store.
func (s *HTTPService) tokenStore() oauth2.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = &oauth2.MemoryStore{}
	}
	return s.tokens
}

// putToken stores t under key and says so.
func (s *HTTPService) putToken(key string, t oauth2.Token) {
	s.tokenStore().Put(key, t)
	s.tokenChanged(key)
}

// deleteToken removes the token under key and says so.
func (s *HTTPService) deleteToken(key string) {
	s.tokenStore().Delete(key)
	s.tokenChanged(key)
}

func (s *HTTPService) tokenChanged(key string) {
	if s.oauth.emit != nil {
		s.oauth.emit(EventOAuth2TokenChanged, OAuth2TokenChangedDTO{Key: key})
	}
}

// clock returns the time for token expiry.
func (s *HTTPService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// tokenLog is how a send got its token, for the timeline.
type tokenLog struct {
	// exchanges are the requests to the token endpoint, in order: a
	// refresh; a refresh that failed, then a new grant; or a new grant.
	// None when a stored token was used (stored).
	exchanges []tokenExchange
	stored    bool
	// token is the token used; key its key in the store; grant the
	// auth's grant.
	token oauth2.Token
	key   string
	grant string
	// old is the stored token that was refreshed or replaced: its
	// refresh token was sent, so it is masked too.
	old oauth2.Token
	// code are the authorization code and PKCE verifier the token
	// request redeemed, masked too.
	code []string
	// rejected is a token the server sent that was refused (a bad ID
	// token): masked too.
	rejected oauth2.Token
}

// tokenExchange is one request to the token endpoint.
type tokenExchange struct {
	result *oauth2.Result
	// grant is the grant_type sent: the auth's grant or refresh_token.
	grant string
	err   error
	// authorized is the redirect URI the browser brought the code back
	// to, before this exchange; "" for other grants.
	authorized string
	// started is when the exchange started: the token request's start,
	// or for the implicit grant (no token request, result nil) when its
	// token came.
	started time.Time
}

// secrets returns the values of the tokens to mask: the token used and
// the one it replaced.
func (l *tokenLog) secrets() []string {
	if l == nil {
		return nil
	}
	out := append(append(l.token.Secrets(), l.old.Secrets()...), l.code...)
	return append(out, l.rejected.Secrets()...)
}

// started returns when the first exchange started; false without one.
func (l *tokenLog) started() (time.Time, bool) {
	if l == nil || len(l.exchanges) == 0 {
		return time.Time{}, false
	}
	if x := l.exchanges[0]; x.result != nil {
		return x.result.Started, true
	} else if !x.started.IsZero() {
		return x.started, true
	}
	return time.Time{}, false
}

// tokenMode says which token obtainToken gets.
type tokenMode int

const (
	// tokenAuto: the stored token while it has not expired; else the
	// stored token refreshed when it has a refresh token; else, or when
	// the refresh fails, a new grant. What a send does.
	tokenAuto tokenMode = iota
	// tokenRefresh: the stored token refreshed with its refresh token,
	// nothing else ("Refresh" in the Auth tab).
	tokenRefresh
	// tokenGrant: a new grant, whatever is stored ("Get new token").
	tokenGrant
)

// authorizeOAuth2 adds the token of b's oauth2 auth, if it has one that is
// not added yet: the stored token while it has not expired, else the
// stored token refreshed with its refresh token, else (or when the refresh
// fails) a new one from the token endpoint; the token got is stored. ctx
// must be an operation's context: the dialer refuses connections without
// one (PRV-05). The log is returned with an error too when the token
// endpoint was asked.
func (s *HTTPService) authorizeOAuth2(ctx context.Context, b *request.HTTPBuild) (*tokenLog, error) {
	return s.authorizeOAuth2Stage(ctx, b, nil)
}

// authorizeOAuth2Stage is authorizeOAuth2 reporting its stages to stage
// (nil for none).
func (s *HTTPService) authorizeOAuth2Stage(ctx context.Context, b *request.HTTPBuild, stage func(string)) (*tokenLog, error) {
	p := b.OAuth2
	if p == nil || p.Applied {
		return nil, nil
	}
	log, err := s.obtainToken(ctx, b, tokenAuto, stage)
	if err != nil {
		return log, err
	}
	token := log.token.AccessToken
	if p.IDToken {
		// AUTH-14: the OpenID Connect ID token instead.
		if log.token.IDToken == "" {
			return log, noIDToken()
		}
		token = log.token.IDToken
	}
	b.UseOAuth2Token(token)
	return log, nil
}

// noIDToken is the error of an auth that sends the ID token when the
// server sent none.
func noIDToken() error {
	return errs.New(errs.Protocol, "The authorization server sent no ID token, which this auth sends instead of the access token.").
		WithCode(oauth2.CodeNoIDToken).
		WithHint("Add openid to the scope, or send the access token. Only OpenID Connect providers send ID tokens.")
}

// usable reports whether t can be sent for p at now: it has not expired,
// nor has its ID token when p sends that (oauth2.Token.ExpiredFor).
func (s *HTTPService) usable(p *request.OAuth2Pending, t oauth2.Token) bool {
	return t.AccessToken != "" && !t.ExpiredFor(s.clock(), p.IDToken)
}

// obtainToken gets the token of b's oauth2 auth as mode says, one at a
// time per key: a second send of the same auth waits for the first, then
// uses the token it stored, so a token is got once.
func (s *HTTPService) obtainToken(ctx context.Context, b *request.HTTPBuild, mode tokenMode, stage func(string)) (*tokenLog, error) {
	p := b.OAuth2
	grant := p.Token.Config.GrantType()
	stored := func() (*tokenLog, bool) {
		if mode != tokenAuto {
			return nil, false
		}
		if t, ok := s.tokenStore().Get(p.Key); ok && s.usable(p, t) {
			return &tokenLog{stored: true, token: t, key: p.Key, grant: grant}, true
		}
		return nil, false
	}
	if log, ok := stored(); ok {
		return log, nil
	}
	unlock, err := s.lockToken(ctx, p.Key)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if log, ok := stored(); ok {
		return log, nil
	}
	return s.fetchToken(ctx, b, mode, stage)
}

// lockToken waits until no other token request of key runs, then marks
// one running until unlock is called. It fails when ctx ends first.
func (s *HTTPService) lockToken(ctx context.Context, key string) (func(), error) {
	for {
		s.mu.Lock()
		busy, ok := s.oauth.running[key]
		if !ok {
			if s.oauth.running == nil {
				s.oauth.running = map[string]chan struct{}{}
			}
			done := make(chan struct{})
			s.oauth.running[key] = done
			s.mu.Unlock()
			return func() {
				s.mu.Lock()
				delete(s.oauth.running, key)
				s.mu.Unlock()
				close(done)
			}, nil
		}
		s.mu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return nil, errs.Wrap(ctx.Err(), errs.KindOf(ctx.Err()), "Waiting for the OAuth 2.0 token was interrupted.")
		}
	}
}

// fetchToken asks the token endpoint for the token of b's oauth2 auth, as
// mode says, and stores it.
func (s *HTTPService) fetchToken(ctx context.Context, b *request.HTTPBuild, mode tokenMode, stage func(string)) (*tokenLog, error) {
	if stage == nil {
		stage = func(string) {}
	}
	p := b.OAuth2
	grant := p.Token.Config.GrantType()
	log := &tokenLog{key: p.Key, grant: grant}
	old, has := s.tokenStore().Get(p.Key)
	if has {
		log.old = old
	}
	canRefresh := has && old.RefreshToken != ""
	if mode == tokenRefresh && !canRefresh {
		return nil, errs.New(errs.InvalidArgument, "There is no refresh token for this OAuth 2.0 auth.").
			WithCode(CodeOAuth2NoRefreshToken).
			WithHint("Get a new token instead. Authorization servers send a refresh token only with some grants.")
	}
	if grant == oauth2.GrantImplicit {
		if has && mode == tokenAuto {
			// Expired: the implicit grant has no refresh token.
			s.deleteToken(p.Key)
		}
		return s.implicitToken(ctx, p, log, stage)
	}
	client, err := s.client(b.InsecureSkipVerify, p.Token.Config.TokenURL, b.Proxy)
	if err != nil {
		return nil, err
	}
	settings := b.OAuth2Settings()
	if canRefresh && (mode == tokenRefresh || (mode == tokenAuto && !s.usable(p, old))) {
		stage(StageRefresh)
		res, err := oauth2.Exchange(ctx, client, oauth2.RefreshRequest(p.Token.Config, old.RefreshToken), settings, s.clock)
		log.exchanges = append(log.exchanges, tokenExchange{result: res, grant: oauth2.GrantRefreshToken, err: err})
		log.reject(res, err)
		if err == nil {
			t := res.Token
			if t.RefreshToken == "" {
				// The server may keep the refresh token (RFC 6749
				// section 6): the old one stays good.
				t.RefreshToken = old.RefreshToken
			}
			s.putToken(p.Key, t)
			log.token = t
			return log, nil
		}
		if mode == tokenRefresh || ctx.Err() != nil {
			return log, err
		}
		// The refresh token was refused, or the refresh failed: a new
		// grant, as if there had been no token.
		s.logger.Info("oauth2 token refresh failed, getting a new token", "err", errs.Redact(err, httpclient.NewSecretMasker(log.secrets()).MaskText))
		s.deleteToken(p.Key)
	} else if has && mode == tokenAuto {
		// Expired, without a refresh token.
		s.deleteToken(p.Key)
	}
	req, authorized := p.Token, ""
	if grant == oauth2.GrantAuthorizationCode {
		// The sign-in in the system browser (AUTH-07): the code comes
		// back to a loopback listener, then is redeemed below.
		stage(StageSignIn)
		code, err := oauth2.Authorize(ctx, req.Config, oauth2.AuthorizeOptions{Open: s.oauth.browser, Timeout: s.oauth.authorizeTimeout})
		if err != nil {
			return log, err
		}
		log.code = []string{code.Code, code.Verifier}
		req, authorized = req.WithCode(code), code.RedirectURI
	}
	stage(StageToken)
	res, err := oauth2.Exchange(ctx, client, req, settings, s.clock)
	log.exchanges = append(log.exchanges, tokenExchange{result: res, grant: grant, err: err, authorized: authorized})
	if err != nil {
		log.reject(res, err)
		return log, err
	}
	s.putToken(p.Key, res.Token)
	log.token = res.Token
	return log, nil
}

// reject keeps the token of res, an exchange that failed with err after
// the server sent a token (its ID token was refused), to be masked.
func (l *tokenLog) reject(res *oauth2.Result, err error) {
	if err != nil && res != nil && res.Token.AccessToken != "" {
		l.rejected = res.Token
	}
}

// implicitToken runs the implicit grant (AUTH-10) for p: the sign-in in
// the system browser, whose redirect brings the token itself (in its
// fragment, posted back by the redirect page); there is no token request.
// The token is stored.
func (s *HTTPService) implicitToken(ctx context.Context, p *request.OAuth2Pending, log *tokenLog, stage func(string)) (*tokenLog, error) {
	stage(StageSignIn)
	got, err := oauth2.AuthorizeImplicit(ctx, p.Token.Config, oauth2.AuthorizeOptions{Open: s.oauth.browser, Timeout: s.oauth.authorizeTimeout, Now: s.clock})
	if err != nil {
		return log, err
	}
	log.exchanges = append(log.exchanges, tokenExchange{grant: oauth2.GrantImplicit, authorized: got.RedirectURI, started: time.Now()})
	s.putToken(p.Key, got.Token)
	log.token = got.Token
	return log, nil
}

// authorize adds the token of the send's oauth2 auth (authorizeOAuth2),
// with stage saying so, and masks the tokens from then on: in the
// timeline, the history and the assertions' results.
func (x *send) authorize(ctx context.Context, stage func(string)) (*tokenLog, error) {
	if x.svc == nil || x.build.OAuth2 == nil || x.build.OAuth2.Applied {
		return nil, nil
	}
	log, err := x.svc.authorizeOAuth2Stage(ctx, &x.build, stage)
	if secrets := log.secrets(); len(secrets) > 0 {
		x.secrets = append(slices.Clip(x.secrets), secrets...)
		x.masker = httpclient.NewSecretMasker(x.secrets)
		x.checkMasker = httpclient.NewSecretMasker(append(slices.Clone(x.secrets), x.checkSecrets...))
	}
	return log, err
}

// previewOAuth2 adds the stored token of b's oauth2 auth for a copy or a
// preview, or the placeholder when there is none: nothing is sent.
func (s *HTTPService) previewOAuth2(b *request.HTTPBuild) {
	p := b.OAuth2
	if p == nil || p.Applied {
		return
	}
	if t, ok := s.tokenStore().Get(p.Key); ok && s.usable(p, t) {
		if p.IDToken {
			b.UseOAuth2Token(t.IDToken)
		} else {
			b.UseOAuth2Token(t.AccessToken)
		}
		return
	}
	b.UseOAuth2Placeholder()
}

// droppedOnUnauthorized drops the stored token of log when the server
// answered 401 to it, and says so (an event), so the next send gets a new
// token.
func (s *HTTPService) droppedOnUnauthorized(log *tokenLog, status int) bool {
	if log == nil || !log.stored || status != 401 {
		return false
	}
	s.deleteToken(log.key)
	return true
}

// withTokenExchange returns tl with how the send got its token first: the
// token requests' hops (kind HopToken, with the token responses' bodies)
// and events, the request's own shifted by offset, the time from the
// start of the first token request to the start of the send; or, for a
// stored token, an event. m masks both.
func withTokenExchange(tl TimelineDTO, log *tokenLog, m httpclient.Masker, offset time.Duration, now time.Time) TimelineDTO {
	if log == nil {
		return tl
	}
	if log.stored {
		detail := log.grant + ", " + expiryText(log.token, now)
		tl.Events = append([]TimelineEventDTO{{AtMs: 0, Name: EventOAuth2TokenStored, Detail: detail}}, tl.Events...)
		return tl
	}
	first, ok := log.started()
	if !ok {
		return tl
	}
	var hops []TimelineHopDTO
	var events []TimelineEventDTO
	masked := false
	for _, x := range log.exchanges {
		if x.result == nil && x.grant == oauth2.GrantImplicit {
			// The implicit grant: no token request, the token came with
			// the redirect.
			shift := ms(x.started.Sub(first))
			events = append(events,
				TimelineEventDTO{AtMs: shift, Name: EventOAuth2Authorized, Detail: "signed in in the browser, redirected to " + x.authorized},
				TimelineEventDTO{AtMs: shift, Name: EventOAuth2Token, Detail: "implicit from the redirect, " + expiryText(log.token, log.token.Obtained)})
			continue
		}
		if x.result == nil {
			continue
		}
		shift := ms(x.result.Started.Sub(first))
		url := m.MaskText(withoutQuery(x.result.Request.URL))
		r := x.result.Response
		if r == nil {
			// No answer: only a failed refresh gets here, followed by a
			// new grant.
			events = append(events, TimelineEventDTO{AtMs: shift, Name: EventOAuth2RefreshFailed,
				Detail: x.grant + " to " + url + " failed: " + m.MaskText(errText(x.err)) + "; getting a new token"})
			continue
		}
		if x.authorized != "" {
			events = append(events, TimelineEventDTO{AtMs: shift, Name: EventOAuth2Authorized,
				Detail: "signed in in the browser, redirected to " + x.authorized})
		}
		tok := toTimelineDTO(r, m)
		for i := range tok.Hops {
			tok.Hops[i].Kind = HopToken
			tok.Hops[i].StartMs += shift
		}
		for i := range tok.Events {
			tok.Events[i].AtMs += shift
		}
		body, bodyMasked := tokenBody(r.Body, m)
		tok.Hops[len(tok.Hops)-1].ResponseBody = body
		masked = masked || tok.Masked || bodyMasked
		hops = append(hops, tok.Hops...)
		events = append(events, tok.Events...)
		at := shift + ms(r.Duration)
		detail := x.grant + " from " + url + ": " + strconv.Itoa(r.StatusCode)
		switch {
		case x.err == nil:
			if log.token.AccessToken != "" {
				detail += ", " + expiryText(log.token, log.token.Obtained)
			}
			events = append(events, TimelineEventDTO{AtMs: at, Name: EventOAuth2Token, Detail: detail})
		case x.grant == oauth2.GrantRefreshToken:
			events = append(events, TimelineEventDTO{AtMs: at, Name: EventOAuth2RefreshFailed,
				Detail: detail + ", " + m.MaskText(errText(x.err)) + "; getting a new token"})
		default:
			events = append(events, TimelineEventDTO{AtMs: at, Name: EventOAuth2Token, Detail: detail})
		}
	}
	if len(hops) == 0 && len(events) == 0 {
		return tl
	}
	shift := ms(offset)
	for i := range tl.Hops {
		tl.Hops[i].StartMs += shift
	}
	for i := range tl.Events {
		tl.Events[i].AtMs += shift
	}
	out := TimelineDTO{
		Hops:   append(hops, tl.Hops...),
		Events: append(events, tl.Events...),
		Masked: tl.Masked || masked,
	}
	if len(out.Events) > maxTimelineEvents {
		out.Events = out.Events[:maxTimelineEvents]
	}
	return out
}

// errText is the message of err for an event detail.
func errText(err error) string {
	if err == nil {
		return ""
	}
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}

// maxTimelineEvents is the most trace events a timeline carries.
const maxTimelineEvents = 1000

// tokenBody is a token response's body for the timeline, masked, clipped
// like a request body; masked reports whether anything was hidden.
func tokenBody(body []byte, m httpclient.Masker) (string, bool) {
	shown := clipUTF8(body, MaxTimelineBody)
	if !utf8.Valid(shown) {
		// Not a token response, and not maskable by value: left out.
		return "", false
	}
	text := m.MaskText(string(shown))
	return text, text != string(shown)
}

// expiryText says how long a token lives: "expires in 3600 s", or "no
// expiry".
func expiryText(t oauth2.Token, now time.Time) string {
	if t.Expiry.IsZero() {
		return "no expiry"
	}
	left := max(t.Expiry.Sub(now), 0).Round(time.Second)
	return "expires in " + strconv.FormatInt(int64(left/time.Second), 10) + " s"
}

// withoutQuery drops the query and fragment of a URL for an event detail.
func withoutQuery(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}
