package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

const (
	idpClientID = "shop-app"
	idpSecret   = "idp-cl1ent-s3cret"
)

// tokenIDP is a test identity provider: a token endpoint for the client
// credentials grant (RFC 6749 section 4.4) that checks the client (in the
// Authorization header or the body) and the scopes, and issues
// "issued-token-<n>" tokens valid for an hour. reply replaces its answer.
type tokenIDP struct {
	*httptest.Server
	mu     sync.Mutex
	issued []string
	reply  func() (int, string)
}

func newTokenIDP(t *testing.T) *tokenIDP {
	idp := &tokenIDP{}
	idp.Server = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		idp.mu.Lock()
		reply := idp.reply
		idp.mu.Unlock()
		if reply != nil {
			status, body := reply()
			w.WriteHeader(status)
			fmt.Fprint(w, body)
			return
		}
		fail := func(status int, code string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "refused by the test IdP"})
		}
		id, secret, ok := r.BasicAuth()
		if ok {
			id, _ = url.QueryUnescape(id)
			secret, _ = url.QueryUnescape(secret)
		} else {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		switch {
		case id != idpClientID || secret != idpSecret:
			fail(401, "invalid_client")
			return
		case r.PostForm.Get("grant_type") != "client_credentials":
			fail(400, "unsupported_grant_type")
			return
		case r.PostForm.Get("scope") != "" && r.PostForm.Get("scope") != "orders:read":
			fail(400, "invalid_scope")
			return
		}
		idp.mu.Lock()
		tok := fmt.Sprintf("issued-token-%d", len(idp.issued)+1)
		idp.issued = append(idp.issued, tok)
		idp.mu.Unlock()
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600,"scope":"orders:read"}`, tok)
	}))
	t.Cleanup(idp.Close)
	return idp
}

func (idp *tokenIDP) count() int {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	return len(idp.issued)
}

// valid reports whether tok was issued, and not revoked.
func (idp *tokenIDP) valid(tok string) bool {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	for _, t := range idp.issued {
		if t == tok && !strings.HasPrefix(t, "revoked:") {
			return true
		}
	}
	return false
}

// revokeAll makes every issued token invalid.
func (idp *tokenIDP) revokeAll() {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	for i, t := range idp.issued {
		idp.issued[i] = "revoked:" + t
	}
}

// apiServer accepts the IdP's tokens in the Authorization header or the
// access_token query parameter, and answers "ok|<where>|<token>"; 401
// without a valid one.
func apiServer(t *testing.T, idp *tokenIDP) *httptest.Server {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		where, tok := "header", strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if q := r.URL.Query().Get("access_token"); q != "" {
			where, tok = "query", q
		}
		if !idp.valid(tok) {
			w.WriteHeader(401)
			fmt.Fprint(w, "unauthorized")
			return
		}
		fmt.Fprintf(w, "ok|%s|%s", where, tok)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func oauth2Source(api, idp string) collection.HTTPSource {
	src := httpSource(api + "/orders")
	src.Auth = projectfile.Auth{Type: projectfile.AuthOAuth2, TokenURL: idp + "/oauth/token", ClientID: idpClientID, ClientSecret: idpSecret, Scope: "orders:read"}
	return src
}

// sendOp sends req_1 and waits for the operation.
func sendOp(t *testing.T, f *fixture) (operation.DoneEventDTO, string) {
	t.Helper()
	id := f.send(t)
	return f.rec.wait(t, id), id
}

// A token is got from a test identity provider with the
// client credentials grant and used on the request; the exchange is in
// the timeline, masked; the token is stored for the session and got again
// once it expires.
func TestSendOAuth2ClientCredentials(t *testing.T) {
	t.Parallel()
	idp := newTokenIDP(t)
	api := apiServer(t, idp)
	st, record := historyStore(t)
	f := newFixture(t, oauth2Source(api.URL, idp.URL), nil, record)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	f.svc.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}

	done, id := sendOp(t, f)
	if done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, id)
	if resp.Body != "ok|header|issued-token-1" {
		t.Fatalf("server said %s", resp.Body)
	}
	tl := resp.Timeline
	if len(tl.Hops) != 2 || tl.Hops[0].Kind != HopToken || tl.Hops[1].Kind != HopFinal || !tl.Masked {
		t.Fatalf("hops = %+v", tl.Hops)
	}
	tokenHop := tl.Hops[0]
	if tokenHop.StatusCode != 200 || tokenHop.Request.Method != "POST" || !strings.HasSuffix(tokenHop.Request.URL, "/oauth/token") ||
		!strings.Contains(tokenHop.Request.Head, "Authorization: Basic "+httpclient.Mask) ||
		!strings.Contains(tokenHop.Request.Body, "grant_type=client_credentials") ||
		!strings.Contains(tokenHop.ResponseBody, `"access_token":"`+httpclient.Mask+`"`) {
		t.Errorf("token hop = %+v", tokenHop)
	}
	if !strings.Contains(tl.Hops[1].Request.Head, "Authorization: Bearer "+httpclient.Mask) || tl.Hops[1].StartMs < tokenHop.StartMs {
		t.Errorf("final hop = %+v", tl.Hops[1])
	}
	if !hasEvent(tl, oauth2Event, "client_credentials from "+idp.URL+"/oauth/token: 200, expires in 3600 s") {
		t.Errorf("events = %+v", tl.Events)
	}
	// The API echoes the token in its body, which is shown as received.
	noSecrets(t, "timeline", resp.Timeline, "issued-token-1")

	// Stored: the next send asks the IdP nothing.
	done, id = sendOp(t, f)
	resp = f.rec.response(t, id)
	if done.Status != operation.StatusOK || resp.Body != "ok|header|issued-token-1" || idp.count() != 1 {
		t.Fatalf("second send: %+v %s, issued %d", done, resp.Body, idp.count())
	}
	if len(resp.Timeline.Hops) != 1 || !hasEvent(resp.Timeline, EventOAuth2TokenStored, "client_credentials, expires in 3600 s") {
		t.Errorf("stored timeline = %+v", resp.Timeline)
	}

	// Copies use the stored token, masked unless asked.
	c, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1"})
	if err != nil || !c.Masked || strings.Contains(c.Command, "issued-token") || !strings.Contains(c.Command, "Bearer "+httpclient.Mask) {
		t.Errorf("curl = %+v, %v", c, err)
	}
	c, err = f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1", IncludeSecrets: true})
	if err != nil || !strings.Contains(c.Command, "Bearer issued-token-1") {
		t.Errorf("curl with secrets = %+v, %v", c, err)
	}
	path := filepath.Join(t.TempDir(), "out.har")
	f.svc.pickSave = func(context.Context, SaveDialog) (string, error) { return path, nil }
	if res, err := f.svc.ExportHAR(t.Context(), ExportHARInputDTO{Name: "x", RequestIDs: []string{"req_1"}}); err != nil || !res.Masked {
		t.Fatalf("HAR = %+v, %v", res, err)
	}
	if data := readFile(t, path); strings.Contains(data, "issued-token") || strings.Contains(data, idpSecret) || !strings.Contains(data, "Bearer "+httpclient.Mask) {
		t.Errorf("HAR = %s", data)
	}

	// Expired: a new token.
	clockMu.Lock()
	now = now.Add(time.Hour)
	clockMu.Unlock()
	done, id = sendOp(t, f)
	if resp = f.rec.response(t, id); done.Status != operation.StatusOK || resp.Body != "ok|header|issued-token-2" || idp.count() != 2 {
		t.Fatalf("after expiry: %s, issued %d", resp.Body, idp.count())
	}

	// The history holds neither the tokens nor the client secret.
	waitEntries(t, st, 3)
	if data := historyFiles(t, st); strings.Contains(data, "issued-token") || strings.Contains(data, idpSecret) {
		t.Errorf("history:\n%s", data)
	}
}

const oauth2Event = EventOAuth2Token

func hasEvent(tl TimelineDTO, name, detail string) bool {
	for _, e := range tl.Events {
		if e.Name == name && (detail == "" || e.Detail == detail) {
			return true
		}
	}
	return false
}

// noSecrets fails when v, as JSON, holds the client secret or one of more.
func noSecrets(t *testing.T, what string, v any, more ...string) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append(more, idpSecret, url.QueryEscape(idpSecret)) {
		if strings.Contains(string(data), s) {
			t.Errorf("%s holds %q:\n%s", what, s, data)
		}
	}
}

// The client credentials in the body (client_secret_post) and the token in
// the query string: both masked in the timeline.
func TestSendOAuth2BodyAndQuery(t *testing.T) {
	t.Parallel()
	idp := newTokenIDP(t)
	api := apiServer(t, idp)
	src := oauth2Source(api.URL, idp.URL)
	src.Auth.ClientAuth, src.Auth.In = projectfile.OAuth2ClientAuthBody, projectfile.APIKeyInQuery
	f := newFixture(t, src, nil)
	done, id := sendOp(t, f)
	resp := f.rec.response(t, id)
	if done.Status != operation.StatusOK || resp.Body != "ok|query|issued-token-1" {
		t.Fatalf("done %+v, server said %s", done, resp.Body)
	}
	hops := resp.Timeline.Hops
	if !strings.Contains(hops[0].Request.Body, "client_id="+idpClientID+"&client_secret="+httpclient.Mask) ||
		strings.Contains(hops[0].Request.Head, "Authorization") {
		t.Errorf("token request = %+v", hops[0].Request)
	}
	if !strings.HasSuffix(hops[1].Request.URL, "?access_token="+httpclient.Mask) {
		t.Errorf("final URL = %s", hops[1].Request.URL)
	}
	noSecrets(t, "timeline", resp.Timeline, "issued-token-1")
}

// Failed token requests fail the send with the IdP's error, and nothing is
// sent to the API.
func TestSendOAuth2Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		edit   func(*projectfile.Auth)
		reply  func() (int, string)
		code   string
		detail string
	}{
		{name: "invalid_client", edit: func(a *projectfile.Auth) { a.ClientSecret = "wrong" }, code: oauth2.CodeTokenError, detail: "invalid_client"},
		{name: "invalid_scope", edit: func(a *projectfile.Auth) { a.Scope = "orders:write" }, code: oauth2.CodeTokenError, detail: "invalid_scope"},
		{name: "malformed JSON", reply: func() (int, string) { return 200, `{"access_token": "x"` }, code: oauth2.CodeMalformedResponse},
		{name: "server error", reply: func() (int, string) { return 502, `bad gateway` }, code: oauth2.CodeTokenHTTPStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			idp := newTokenIDP(t)
			idp.reply = tt.reply
			var apiCalls int
			var mu sync.Mutex
			api := httptest.NewServer(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {
				mu.Lock()
				apiCalls++
				mu.Unlock()
			}))
			t.Cleanup(api.Close)
			src := oauth2Source(api.URL, idp.URL)
			if tt.edit != nil {
				tt.edit(&src.Auth)
			}
			st, record := historyStore(t)
			f := newFixture(t, src, nil, record)
			done, _ := sendOp(t, f)
			if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != tt.code || done.Error.Details["error"] != tt.detail {
				t.Fatalf("done = %+v, error %+v", done, done.Error)
			}
			mu.Lock()
			defer mu.Unlock()
			if apiCalls != 0 {
				t.Errorf("the API was called %d times", apiCalls)
			}
			noSecrets(t, "error", done.Error, "wrong")
			waitEntries(t, st, 1)
			if data := historyFiles(t, st); strings.Contains(data, idpSecret) || strings.Contains(data, "wrong") {
				t.Errorf("history:\n%s", data)
			}
		})
	}
}

// A stored token the API refuses with 401 is dropped: the next send gets
// a new one.
func TestSendOAuth2DropsRefusedToken(t *testing.T) {
	t.Parallel()
	idp := newTokenIDP(t)
	api := apiServer(t, idp)
	f := newFixture(t, oauth2Source(api.URL, idp.URL), nil)
	if done, _ := sendOp(t, f); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	idp.revokeAll()
	done, id := sendOp(t, f)
	resp := f.rec.response(t, id)
	if done.Status != operation.StatusOK || resp.StatusCode != 401 || !hasEvent(resp.Timeline, EventOAuth2TokenDropped, "") {
		t.Fatalf("refused: %d %+v", resp.StatusCode, resp.Timeline.Events)
	}
	done, id = sendOp(t, f)
	if resp = f.rec.response(t, id); done.Status != operation.StatusOK || resp.Body != "ok|header|issued-token-2" {
		t.Fatalf("after drop: %s", resp.Body)
	}
}

// Copies and previews never ask the token endpoint: without a stored
// token they show a placeholder.
func TestOAuth2CopyWithoutToken(t *testing.T) {
	t.Parallel()
	idp := newTokenIDP(t)
	f := newFixture(t, oauth2Source("https://api.test", idp.URL), nil)
	c, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1", IncludeSecrets: true})
	if err != nil || !strings.Contains(c.Command, "Bearer "+request.OAuth2Placeholder) || idp.count() != 0 {
		t.Errorf("curl = %+v, %v, issued %d", c, err, idp.count())
	}
	h, err := f.svc.HeaderPreview(t.Context(), HeaderPreviewInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	noSecrets(t, "header preview", h)
	if idp.count() != 0 {
		t.Errorf("preview asked the IdP")
	}
}

// An SSE stream gets its token when it connects, like a send.
func TestStreamOAuth2(t *testing.T) {
	t.Parallel()
	idp := newTokenIDP(t)
	api := apiServer(t, idp)
	src := oauth2Source(api.URL, idp.URL)
	src.SSE = true
	f := newFixture(t, src, nil)
	st, err := NewStream(t.Context(), f.svc, "req_1", false)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "test", ID: "1"})
	resp, err := st.Open(ctx, "", func(_ *httpclient.Response, r io.Reader) error {
		b, err := io.ReadAll(r)
		got = string(b)
		return err
	})
	if err != nil || resp.StatusCode != 200 || got != "ok|header|issued-token-1" {
		t.Fatalf("stream: %v %s", err, got)
	}
}
