package http

import (
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/oauth2/tokenstore"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// refreshIDP is a test identity provider that sends refresh tokens: the
// client credentials grant issues "access-<n>" with "refresh-<n>", the
// refresh token grant exchanges a refresh token it issued for
// "access-<n>" and, unless keepRefresh, a new "refresh-<n>". Every token
// lives an hour. refuseRefresh answers refreshes with invalid_grant.
type refreshIDP struct {
	*httptest.Server
	mu            sync.Mutex
	n             int
	grants        []string
	refreshes     map[string]bool
	access        map[string]bool
	refuseRefresh bool
	keepRefresh   bool
	delay         time.Duration
}

func newRefreshIDP(t *testing.T) *refreshIDP {
	idp := &refreshIDP{refreshes: map[string]bool{}, access: map[string]bool{}}
	idp.Server = httptest.NewServer(nethttp.HandlerFunc(idp.serve))
	t.Cleanup(idp.Close)
	return idp
}

func (idp *refreshIDP) serve(w nethttp.ResponseWriter, r *nethttp.Request) {
	_ = r.ParseForm()
	idp.mu.Lock()
	delay := idp.delay
	idp.mu.Unlock()
	time.Sleep(delay)
	idp.mu.Lock()
	defer idp.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	if id, secret, ok := r.BasicAuth(); !ok || id != idpClientID || secret != idpSecret {
		fail(401, "invalid_client")
		return
	}
	grant := r.PostForm.Get("grant_type")
	idp.grants = append(idp.grants, grant)
	switch grant {
	case "client_credentials":
	case "refresh_token":
		if idp.refuseRefresh || !idp.refreshes[r.PostForm.Get("refresh_token")] {
			fail(400, "invalid_grant")
			return
		}
		if r.PostForm.Get("scope") != "" {
			fail(400, "invalid_scope")
			return
		}
	default:
		fail(400, "unsupported_grant_type")
		return
	}
	idp.n++
	access := fmt.Sprintf("access-%d-%s", idp.n, strings.Repeat("x", 12))
	idp.access[access] = true
	resp := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600}
	if grant == "client_credentials" || !idp.keepRefresh {
		refresh := fmt.Sprintf("refresh-%d", idp.n)
		idp.refreshes[refresh] = true
		resp["refresh_token"] = refresh
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (idp *refreshIDP) grantsSent() []string {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return slices.Clone(idp.grants)
}

// echoAPI answers "ok|<token>" to a token the IdP issued, 401 otherwise.
func echoAPI(t *testing.T, idp *refreshIDP) *httptest.Server {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		idp.mu.Lock()
		ok := idp.access[tok]
		idp.mu.Unlock()
		if !ok {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, "ok|"+tok)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// testClock is a clock tests move by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClock() *testClock {
	return &testClock{now: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)}
}

// A token about to expire is refreshed with its refresh
// token before the request, without a new grant; the refresh is in the
// timeline with the refresh tokens masked. A server that keeps the refresh
// token (sends none back) keeps it good for the next refresh.
func TestSendOAuth2RefreshesBeforeExpiry(t *testing.T) {
	t.Parallel()
	idp := newRefreshIDP(t)
	api := echoAPI(t, idp)
	clock := newClock()
	f := newFixture(t, oauth2Source(api.URL, idp.URL), nil)
	f.svc.now = clock.Now

	done, id := sendOp(t, f)
	if resp := f.rec.response(t, id); done.Status != operation.StatusOK || !strings.HasPrefix(resp.Body, "ok|access-1-") {
		t.Fatalf("first send: %+v %s", done, resp.Body)
	}

	// 20 seconds before the expiry: inside oauth2.ExpiryLeeway, so the
	// token is refreshed before it is sent.
	clock.add(time.Hour - 20*time.Second)
	done, id = sendOp(t, f)
	resp := f.rec.response(t, id)
	if done.Status != operation.StatusOK || !strings.HasPrefix(resp.Body, "ok|access-2-") {
		t.Fatalf("refreshing send: %+v %s", done, resp.Body)
	}
	if got := idp.grantsSent(); !slices.Equal(got, []string{"client_credentials", "refresh_token"}) {
		t.Errorf("grants = %v", got)
	}
	tl := resp.Timeline
	if len(tl.Hops) != 2 || tl.Hops[0].Kind != HopToken ||
		tl.Hops[0].Request.Body != "grant_type=refresh_token&refresh_token="+httpclient.Mask {
		t.Fatalf("hops = %+v", tl.Hops)
	}
	if !hasEvent(tl, EventOAuth2Token, "refresh_token from "+idp.URL+"/oauth/token: 200, expires in 3600 s") {
		t.Errorf("events = %+v", tl.Events)
	}
	noSecrets(t, "timeline", tl, "refresh-1", "refresh-2", "access-2-")
	if !slices.Contains(f.rec.progressOf(id), StageRefresh) {
		t.Errorf("progress = %v", f.rec.progressOf(id))
	}

	// The server keeps the refresh token: the next refresh sends it again.
	idp.mu.Lock()
	idp.keepRefresh = true
	idp.mu.Unlock()
	clock.add(time.Hour)
	if done, id = sendOp(t, f); !strings.HasPrefix(f.rec.response(t, id).Body, "ok|access-3-") {
		t.Fatalf("second refresh: %+v", done)
	}
	clock.add(time.Hour)
	if done, id = sendOp(t, f); !strings.HasPrefix(f.rec.response(t, id).Body, "ok|access-4-") {
		t.Fatalf("third refresh: %+v", done)
	}
	if got := idp.grantsSent(); len(got) != 4 || got[3] != "refresh_token" {
		t.Errorf("grants = %v", got)
	}
}

// When the refresh fails the send gets a new token by the grant, as if
// there had been none; both requests are in the timeline.
func TestSendOAuth2RefreshFailsThenNewGrant(t *testing.T) {
	t.Parallel()
	idp := newRefreshIDP(t)
	api := echoAPI(t, idp)
	clock := newClock()
	f := newFixture(t, oauth2Source(api.URL, idp.URL), nil)
	f.svc.now = clock.Now
	if done, _ := sendOp(t, f); done.Status != operation.StatusOK {
		t.Fatalf("first send: %+v", done)
	}
	idp.mu.Lock()
	idp.refuseRefresh = true
	idp.mu.Unlock()
	clock.add(2 * time.Hour)

	done, id := sendOp(t, f)
	resp := f.rec.response(t, id)
	if done.Status != operation.StatusOK || !strings.HasPrefix(resp.Body, "ok|access-2-") {
		t.Fatalf("send: %+v %s", done, resp.Body)
	}
	if got := idp.grantsSent(); !slices.Equal(got, []string{"client_credentials", "refresh_token", "client_credentials"}) {
		t.Errorf("grants = %v", got)
	}
	tl := resp.Timeline
	if len(tl.Hops) != 3 || tl.Hops[0].Kind != HopToken || tl.Hops[1].Kind != HopToken || tl.Hops[0].StatusCode != 400 ||
		!strings.Contains(tl.Hops[1].Request.Body, "grant_type=client_credentials") || tl.Hops[1].StartMs < tl.Hops[0].StartMs {
		t.Fatalf("hops = %+v", tl.Hops)
	}
	if !hasEvent(tl, EventOAuth2RefreshFailed, "") || !hasEvent(tl, EventOAuth2Token, "") {
		t.Errorf("events = %+v", tl.Events)
	}
	noSecrets(t, "timeline", tl, "refresh-1", "refresh-2")

	// A refresh by hand that fails says so, and keeps the token.
	_, err := f.svc.OAuth2GetToken(t.Context(), OAuth2GetTokenInputDTO{RequestID: "req_1", Refresh: true})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != oauth2.CodeTokenError || e.Details["error"] != "invalid_grant" {
		t.Fatalf("manual refresh = %v", err)
	}
	if st, _ := f.svc.OAuth2Token(t.Context(), OAuth2TokenInputDTO{RequestID: "req_1"}); st.Status != OAuth2TokenValid {
		t.Errorf("status after a failed refresh = %+v", st)
	}
}

// Tokens survive a restart (a new service and store on
// the same local directory), and are kept in the local side file only.
func TestOAuth2TokensPersistAcrossRestart(t *testing.T) {
	t.Parallel()
	idp := newRefreshIDP(t)
	api := echoAPI(t, idp)
	local := t.TempDir()
	open := func() *fixture {
		store := tokenstore.New(func() (string, error) { return local, nil })
		return newFixture(t, oauth2Source(api.URL, idp.URL), nil, WithOAuth2Store(store))
	}
	f := open()
	done, id := sendOp(t, f)
	token := strings.TrimPrefix(f.rec.response(t, id).Body, "ok|")
	if done.Status != operation.StatusOK || !strings.HasPrefix(token, "access-1-") {
		t.Fatalf("first send: %+v %s", done, token)
	}

	restarted := open()
	done, id = sendOp(t, restarted)
	resp := restarted.rec.response(t, id)
	if done.Status != operation.StatusOK || resp.Body != "ok|"+token || len(idp.grantsSent()) != 1 {
		t.Fatalf("after the restart: %s, grants %v", resp.Body, idp.grantsSent())
	}
	if !hasEvent(resp.Timeline, EventOAuth2TokenStored, "") {
		t.Errorf("events = %+v", resp.Timeline.Events)
	}
	data, err := os.ReadFile(filepath.Join(local, filepath.FromSlash(tokenstore.FileName)))
	if err != nil || !strings.Contains(string(data), token) || !strings.Contains(string(data), "refresh-1") {
		t.Errorf("token file: %s, %v", data, err)
	}
}

// The Auth tab's token: its status with the token masked, a new token, a
// refresh and Clear by hand; each change is announced.
func TestOAuth2TokenStatusAndActions(t *testing.T) {
	t.Parallel()
	idp := newRefreshIDP(t)
	clock := newClock()
	var mu sync.Mutex
	var changed []string
	emit := func(name string, data any) {
		mu.Lock()
		defer mu.Unlock()
		if d, ok := data.(OAuth2TokenChangedDTO); ok && name == EventOAuth2TokenChanged {
			changed = append(changed, d.Key)
		}
	}
	f := newFixture(t, oauth2Source("https://api.test", idp.URL), nil, WithOAuth2Events(emit))
	f.svc.now = clock.Now
	ctx := t.Context()
	in := OAuth2TokenInputDTO{RequestID: "req_1"}

	st, err := f.svc.OAuth2Token(ctx, in)
	if err != nil || !st.Available || st.Status != OAuth2TokenNone || st.Key == "" || st.Grant != "client_credentials" || st.Token != "" {
		t.Fatalf("before = %+v, %v", st, err)
	}
	key := st.Key

	// Refresh without a token has nothing to refresh.
	_, err = f.svc.OAuth2GetToken(ctx, OAuth2GetTokenInputDTO{RequestID: "req_1", Refresh: true})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeOAuth2NoRefreshToken || len(idp.grantsSent()) != 0 {
		t.Fatalf("refresh without a token = %v", err)
	}

	st, err = f.svc.OAuth2GetToken(ctx, OAuth2GetTokenInputDTO{RequestID: "req_1"})
	if err != nil || st.Status != OAuth2TokenValid || st.Key != key || !st.Refreshable || st.TokenType != "Bearer" ||
		st.ExpiresAt != clock.Now().Add(time.Hour).UnixMilli() || st.ObtainedAt != clock.Now().UnixMilli() {
		t.Fatalf("got = %+v, %v", st, err)
	}
	// Masked: the mask and the last four characters, never the token.
	if st.Token != httpclient.Mask+"xxxx" {
		t.Errorf("token shown as %q", st.Token)
	}
	noSecrets(t, "status", st, "access-1", "refresh-1")

	clock.add(time.Hour)
	if st, _ = f.svc.OAuth2Token(ctx, in); st.Status != OAuth2TokenExpired {
		t.Errorf("after the expiry = %+v", st)
	}
	st, err = f.svc.OAuth2GetToken(ctx, OAuth2GetTokenInputDTO{RequestID: "req_1", Refresh: true})
	if err != nil || st.Status != OAuth2TokenValid {
		t.Fatalf("refresh = %+v, %v", st, err)
	}
	// A new token by hand is a new grant, even with a valid one stored.
	if _, err = f.svc.OAuth2GetToken(ctx, OAuth2GetTokenInputDTO{RequestID: "req_1"}); err != nil {
		t.Fatal(err)
	}
	if got := idp.grantsSent(); !slices.Equal(got, []string{"client_credentials", "refresh_token", "client_credentials"}) {
		t.Errorf("grants = %v", got)
	}

	st, err = f.svc.OAuth2ClearToken(ctx, in)
	if err != nil || st.Status != OAuth2TokenNone || st.Token != "" {
		t.Fatalf("clear = %+v, %v", st, err)
	}
	mu.Lock()
	if len(changed) != 4 || changed[0] != key {
		t.Errorf("changes = %v", changed)
	}
	mu.Unlock()

	// Another auth type has no token.
	f.mu.Lock()
	f.src.Auth = projectfile.Auth{Type: projectfile.AuthBearer, Token: "t"}
	f.mu.Unlock()
	if st, err = f.svc.OAuth2Token(ctx, in); err != nil || st.Available {
		t.Errorf("bearer = %+v, %v", st, err)
	}
	if _, err = f.svc.OAuth2ClearToken(ctx, in); !errors.As(err, &e) || e.Code != CodeNoOAuth2 {
		t.Errorf("clear for bearer = %v", err)
	}
}

// Sends of one auth at the same time get one token: the others wait and
// use it (run with -race).
func TestSendOAuth2OneTokenForConcurrentSends(t *testing.T) {
	t.Parallel()
	idp := newRefreshIDP(t)
	idp.delay = 50 * time.Millisecond
	api := echoAPI(t, idp)
	f := newFixture(t, oauth2Source(api.URL, idp.URL), nil)
	ids := make([]string, 4)
	for i := range ids {
		ids[i] = f.send(t)
	}
	for _, id := range ids {
		if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
			t.Fatalf("done = %+v", done)
		}
		if body := f.rec.response(t, id).Body; !strings.HasPrefix(body, "ok|access-1-") {
			t.Errorf("body = %s", body)
		}
	}
	if got := idp.grantsSent(); len(got) != 1 {
		t.Errorf("grants = %v", got)
	}
}

// progressOf returns the progress messages of operation id.
func (r *recorder) progressOf(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.progress[id])
}
