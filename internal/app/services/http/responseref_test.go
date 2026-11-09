package http

import (
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/extract"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// refServer hands out a new token at every POST /login (tok-1, tok-2…)
// and echoes the Authorization header of every other request.
type refServer struct {
	*httptest.Server
	logins atomic.Int32
}

func newRefServer(t *testing.T) *refServer {
	t.Helper()
	s := &refServer{}
	s.Server = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/login" {
			n := s.logins.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Session", fmt.Sprintf("sess-%d", n))
			fmt.Fprintf(w, `{"access_token": "tok-%d", "user": {"id": 7}}`, n)
			return
		}
		fmt.Fprintf(w, "%s|%s", r.URL.Path, r.Header.Get("Authorization"))
	}))
	t.Cleanup(s.Close)
	return s
}

// refProject writes a login request (req_login, "Log in") and a request
// req_me whose Authorization header is auth.
func refProject(t *testing.T, f *projectFixture, base, auth string, extractions string) {
	t.Helper()
	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{{Name: "base", Value: base}}); err != nil {
		t.Fatal(err)
	}
	f.write(t, "login.json", `{"kind": "http", "id": "req_login", "name": "Log in", "method": "POST", "url": "{{base}}/login", "auth": {"type": "none"}, "extractions": [`+extractions+`]}`)
	f.write(t, "me.json", `{"kind": "http", "id": "req_me", "name": "Me", "method": "GET", "url": "{{base}}/me", "auth": {"type": "none"},
		"headers": [{"name": "Authorization", "value": `+jsonString(auth)+`}]}`)
	f.reload(t)
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func newRefFixture(t *testing.T, opts ...Option) (*projectFixture, *testClock) {
	t.Helper()
	f := newProjectFixture(t, opts...)
	clock := newClock()
	f.svc.now = clock.Now
	return f, clock
}

// sendErr sends request id and returns the error it is refused with at
// once, or the error the operation ends with.
func (f *projectFixture) sendErr(t *testing.T, id string) (*errs.Error, *operation.DoneEventDTO) {
	t.Helper()
	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: id})
	if err != nil {
		var e *errs.Error
		if !errors.As(err, &e) {
			t.Fatalf("Send = %v", err)
		}
		return e, nil
	}
	done := f.rec.wait(t, started.OpID)
	return nil, &done
}

// TestResponseReferenceLoginToken (DYN-06): a
// request takes the token from the login request's last response, by
// JSONPath; a header and the status read the same way. Before the login
// was ever sent the send is refused, saying so.
func TestResponseReferenceLoginToken(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t)
	f, _ := newRefFixture(t)
	refProject(t, f, srv.URL, "Bearer {{$response req_login jsonpath $.access_token}} {{$response req_login header X-Session}} {{$response req_login status}}", "")

	e, _ := f.sendErr(t, "req_me")
	if e == nil || e.Code != CodeResponseNotSent || e.Details["reference"] != "$response req_login jsonpath $.access_token" ||
		e.Message != `The request "Log in" has no response yet.` || !strings.Contains(e.Hint, "send=missing") {
		t.Fatalf("before the login: %+v", e)
	}
	if srv.logins.Load() != 0 {
		t.Fatal("the login was sent")
	}

	f.sendBody(t, "req_login")
	if got := f.sendBody(t, "req_me"); got != "/me|Bearer tok-1 sess-1 200" {
		t.Errorf("body = %q", got)
	}
	// A new login: the next send reads the new token.
	f.sendBody(t, "req_login")
	if got := f.sendBody(t, "req_me"); got != "/me|Bearer tok-2 sess-2 200" {
		t.Errorf("body = %q", got)
	}

	// The hover: the request's name, the value, never a send.
	v, err := f.svc.ResponseValue(t.Context(), "req_me", "$response req_login jsonpath $.access_token")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Resolved || v.Value != "tok-2" || v.RequestName != "Log in" || v.RequestID != "req_login" || v.Status != 200 || v.SendFirst || v.Send != "never" || v.ReceivedAt == "" {
		t.Errorf("ResponseValue = %+v", v)
	}
	if srv.logins.Load() != 2 {
		t.Errorf("logins = %d", srv.logins.Load())
	}
}

// TestResponseReferenceMissingValues: a value the response does not have,
// a request that does not exist, an expression that does not parse and a
// reference to a request of another kind are clear errors naming the
// request.
func TestResponseReferenceMissingValues(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t)
	f, _ := newRefFixture(t)
	refProject(t, f, srv.URL, "", "")
	f.write(t, "ws.json", `{"kind": "websocket", "id": "req_ws", "name": "Socket", "url": "ws://localhost/"}`)
	f.reload(t)
	f.sendBody(t, "req_login")

	tests := []struct {
		ref, code, msg string
	}{
		{"$response req_login jsonpath $.nope", extract.CodeNotFound, `JSONPath $.nope selects nothing in the body of the last response of "Log in" (status 200).`},
		{"$response req_login header X-Nope", extract.CodeNotFound, `Header X-Nope is not in the last response of "Log in" (status 200).`},
		{"$response req_login xpath /a", extract.CodeUnreadableBody, `XPath /a cannot be read: the body is not valid XML, in the last response of "Log in" (status 200).`},
		{"$response req_login jsonpath $[", "jsonpath.invalid_query", ""},
		{"$response req_gone status", CodeResponseRequestNotFound, `The referenced request "req_gone" is not an HTTP or GraphQL request of this project.`},
		{"$response req_ws status", CodeResponseRequestNotFound, `The referenced request "req_ws" is not an HTTP or GraphQL request of this project.`},
		{"$response req_login nope", template.CodeInvalidDynamic, ""},
	}
	for _, tt := range tests {
		f.write(t, "me.json", `{"kind": "http", "id": "req_me", "name": "Me", "method": "GET", "url": "{{base}}/me", "auth": {"type": "none"},
			"headers": [{"name": "Authorization", "value": `+jsonString("{{"+tt.ref+"}}")+`}]}`)
		f.reload(t)
		e, done := f.sendErr(t, "req_me")
		if e == nil {
			t.Errorf("%s: sent (%+v)", tt.ref, done)
			continue
		}
		if e.Code != tt.code || (tt.msg != "" && e.Message != tt.msg) || (tt.code != template.CodeInvalidDynamic && e.Details["reference"] != tt.ref) {
			t.Errorf("%s: %s %q %v", tt.ref, e.Code, e.Message, e.Details)
		}
		v, err := f.svc.ResponseValue(t.Context(), "req_me", tt.ref)
		if err != nil || v.Resolved || v.Problem == "" || v.Code != tt.code {
			t.Errorf("%s: ResponseValue = %+v, %v", tt.ref, v, err)
		}
	}
}

// TestResponseReferenceSendsFirst: with
// send=missing the login is sent first when it has no response; with an
// age when its response is older; with always at every send. However
// many references read it, a request is sent once per send, and the
// operation says it sends it.
func TestResponseReferenceSendsFirst(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t)
	st, rec := historyStore(t)
	f, clock := newRefFixture(t, rec)
	refProject(t, f, srv.URL, "{{$response req_login jsonpath $.access_token send=10m}} {{$response req_login header X-Session send=10m}}", "")

	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_me"})
	if err != nil {
		t.Fatal(err)
	}
	if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done.Error)
	}
	if got := f.rec.response(t, started.OpID).Body; got != "/me|tok-1 sess-1" {
		t.Errorf("body = %q", got)
	}
	if !slices.Contains(f.rec.progressOf(started.OpID), StageSendingFirst) {
		t.Errorf("progress = %v", f.rec.progressOf(started.OpID))
	}
	if n := srv.logins.Load(); n != 1 {
		t.Errorf("logins = %d, want 1", n)
	}
	// The login sent first is in the history, before the request.
	page := st.List(history.Query{})
	if len(page.Entries) != 2 || page.Entries[1].RequestID != "req_login" || page.Entries[0].RequestID != "req_me" {
		t.Errorf("history = %+v", page.Entries)
	}

	// Fresh: not sent again.
	clock.add(9 * time.Minute)
	if got := f.sendBody(t, "req_me"); got != "/me|tok-1 sess-1" || srv.logins.Load() != 1 {
		t.Errorf("fresh: %q, logins %d", got, srv.logins.Load())
	}
	v, _ := f.svc.ResponseValue(t.Context(), "req_me", "$response req_login jsonpath $.access_token send=10m")
	if v.SendFirst || v.Value != "tok-1" {
		t.Errorf("fresh hover = %+v", v)
	}
	// Stale: sent again, once for both references.
	clock.add(2 * time.Minute)
	v, _ = f.svc.ResponseValue(t.Context(), "req_me", "$response req_login jsonpath $.access_token send=10m")
	if !v.SendFirst || v.Value != "tok-1" || v.MaxAgeMs != (10*time.Minute).Milliseconds() || v.Send != "stale" {
		t.Errorf("stale hover = %+v", v)
	}
	if got := f.sendBody(t, "req_me"); got != "/me|tok-2 sess-2" || srv.logins.Load() != 2 {
		t.Errorf("stale: %q, logins %d", got, srv.logins.Load())
	}

	// send=always: at every send.
	f.write(t, "me.json", `{"kind": "http", "id": "req_me", "name": "Me", "method": "GET", "url": "{{base}}/me", "auth": {"type": "none"},
		"headers": [{"name": "Authorization", "value": "{{$response req_login jsonpath $.access_token send=always}}|{{$response req_login status send=always}}"}]}`)
	f.reload(t)
	if got := f.sendBody(t, "req_me"); got != "/me|tok-3|200" {
		t.Errorf("always: %q", got)
	}
	if got := f.sendBody(t, "req_me"); got != "/me|tok-4|200" || srv.logins.Load() != 4 {
		t.Errorf("always: %q, logins %d", got, srv.logins.Load())
	}

	// send=missing with a response: read as it is.
	f.write(t, "me.json", `{"kind": "http", "id": "req_me", "name": "Me", "method": "GET", "url": "{{base}}/me", "auth": {"type": "none"},
		"headers": [{"name": "Authorization", "value": "{{$response req_login jsonpath $.access_token send=missing}}"}]}`)
	f.reload(t)
	clock.add(48 * time.Hour)
	if got := f.sendBody(t, "req_me"); got != "/me|tok-4" || srv.logins.Load() != 4 {
		t.Errorf("missing: %q, logins %d", got, srv.logins.Load())
	}
}

// TestResponseReferenceChain: a request sent first may itself need
// another one sent first; the send-first of a request that fails is the
// send's error, naming it.
func TestResponseReferenceChain(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t)
	f, _ := newRefFixture(t)
	refProject(t, f, srv.URL, "{{$response req_mid body send=missing}}", "")
	f.write(t, "mid.json", `{"kind": "http", "id": "req_mid", "name": "Middle", "method": "GET", "url": "{{base}}/mid", "auth": {"type": "none"},
		"headers": [{"name": "Authorization", "value": "{{$response req_login jsonpath $.access_token send=missing}}"}]}`)
	f.reload(t)
	if got := f.sendBody(t, "req_me"); got != "/me|/mid|tok-1" {
		t.Errorf("body = %q", got)
	}

	// A request sent first that cannot be built.
	f.write(t, "mid.json", `{"kind": "http", "id": "req_mid", "name": "Middle", "method": "GET", "url": "{{base}}/mid", "auth": {"type": "none"},
		"headers": [{"name": "Authorization", "value": "{{undefinedName}}"}]}`)
	f.reload(t)
	f.svc.lastResponses.forget("req_mid")
	e, done := f.sendErr(t, "req_me")
	if e != nil || done.Status != operation.StatusError || done.Error.Code != CodeResponseSendFailed || done.Error.Details["cause"] != template.CodeUnresolved ||
		!strings.HasPrefix(done.Error.Message, `The request "Middle" was sent first to read its response, but the variable {{undefinedName}} is not defined.`) {
		t.Errorf("failed send-first: %+v %+v", e, done)
	}
}

// TestResponseReferenceCycles: requests that would send each other first
// in a loop, a request that would send itself, and a chain longer than
// MaxResponseDepth are refused. A request reading its own stored
// response is no loop.
func TestResponseReferenceCycles(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t)
	f, _ := newRefFixture(t)
	refProject(t, f, srv.URL, "", "")
	write := func(id, name, auth string) {
		f.write(t, id+".json", `{"kind": "http", "id": "`+id+`", "name": "`+name+`", "method": "GET", "url": "{{base}}/`+id+`", "auth": {"type": "none"},
			"headers": [{"name": "Authorization", "value": `+jsonString(auth)+`}]}`)
	}

	// A → B → A.
	write("req_a", "A", "{{$response req_b body send=missing}}")
	write("req_b", "B", "{{$response req_a body send=missing}}")
	// Itself.
	write("req_self", "Self", "{{$response req_self status send=missing}}")
	f.reload(t)
	e, done := f.sendErr(t, "req_a")
	if e != nil || done.Error == nil || done.Error.Code != CodeResponseCycle ||
		!strings.Contains(done.Error.Message, "refer to each other's responses in a loop: A → B → A") {
		t.Errorf("A → B → A: %+v %+v", e, done)
	}
	e, _ = f.sendErr(t, "req_self")
	if e == nil || e.Code != CodeResponseCycle || e.Details["requests"] != "Self → Self" {
		t.Errorf("itself: %+v", e)
	}

	// Its own stored response, without sending: no loop.
	write("req_self", "Self", "{{$response req_self status}}")
	f.reload(t)
	if e, _ := f.sendErr(t, "req_self"); e == nil || e.Code != CodeResponseNotSent {
		t.Errorf("own response before a send: %+v", e)
	}
	write("req_self", "Self", "x")
	f.reload(t)
	f.sendBody(t, "req_self")
	write("req_self", "Self", "{{$response req_self status}}")
	f.reload(t)
	if got := f.sendBody(t, "req_self"); got != "/req_self|200" {
		t.Errorf("own response: %q", got)
	}

	// r0 → r1 → … → r6: deeper than MaxResponseDepth.
	for i := 0; i <= MaxResponseDepth+1; i++ {
		write(fmt.Sprintf("req_r%d", i), fmt.Sprintf("R%d", i), fmt.Sprintf("{{$response req_r%d status send=missing}}", i+1))
	}
	write(fmt.Sprintf("req_r%d", MaxResponseDepth+2), "End", "end")
	f.reload(t)
	e, done = f.sendErr(t, "req_r0")
	if e != nil || done.Error == nil || done.Error.Code != CodeResponseTooDeep || !strings.Contains(done.Error.Message, "Too many requests would be sent first") {
		t.Errorf("too deep: %+v %+v", e, done)
	}
}

// TestResponseReferenceSecrets: a value the referenced send masked (a
// secret extraction) is secret where it is used: masked in the hover
// and in the timeline; the secret flag masks any value.
func TestResponseReferenceSecrets(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t)
	f, _ := newRefFixture(t)
	refProject(t, f, srv.URL, "X {{$response req_login jsonpath $.access_token}} {{$response req_login jsonpath $.user.id secret}}",
		`{"source": "jsonpath", "expr": "$.access_token", "variable": "token", "secret": true}`)
	f.sendBody(t, "req_login")
	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_me"})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.wait(t, started.OpID)
	resp := f.rec.response(t, started.OpID)
	if resp.Body != "/me|X tok-1 7" {
		t.Errorf("body = %q", resp.Body)
	}
	timeline := fmt.Sprint(resp.Timeline)
	if strings.Contains(timeline, "tok-1") || !strings.Contains(timeline, httpclient.Mask) {
		t.Errorf("the token is not masked in the timeline: %s", timeline)
	}
	for ref, want := range map[string]bool{
		"$response req_login jsonpath $.access_token":   true,
		"$response req_login jsonpath $.user.id":        false,
		"$response req_login jsonpath $.user.id secret": true,
	} {
		v, _ := f.svc.ResponseValue(t.Context(), "req_me", ref)
		if v.Secret != want || (want && v.Value != "") || !v.Resolved {
			t.Errorf("%s: %+v", ref, v)
		}
	}
}

// TestResponseReferenceFromHistory: after a restart (nothing kept in
// memory), the history's copy of the last response is read; a value
// masked there is not used, and send=missing sends the request again
// for it. Forgetting the responses (the history cleared) leaves nothing.
func TestResponseReferenceFromHistory(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t)
	st, rec := historyStore(t)
	f, _ := newRefFixture(t, rec, WithResponseHistory(nil))
	refProject(t, f, srv.URL, "{{$response req_login jsonpath $.user.id}}",
		`{"source": "jsonpath", "expr": "$.access_token", "variable": "token", "secret": true}`)
	f.sendBody(t, "req_login")
	// As after a restart: the history only.
	f.svc.lastResponses.forget("")
	f.svc.responseHistory = func() (*history.Store, error) { return st, nil }

	if got := f.sendBody(t, "req_me"); got != "/me|7" {
		t.Errorf("from the history: %q", got)
	}
	v, _ := f.svc.ResponseValue(t.Context(), "req_me", "$response req_login jsonpath $.user.id")
	if !v.FromHistory || v.Value != "7" {
		t.Errorf("hover = %+v", v)
	}

	// The token was masked when recorded.
	f.write(t, "me.json", `{"kind": "http", "id": "req_me", "name": "Me", "method": "GET", "url": "{{base}}/me", "auth": {"type": "none"},
		"headers": [{"name": "Authorization", "value": "{{$response req_login jsonpath $.access_token}}"}]}`)
	f.reload(t)
	if e, _ := f.sendErr(t, "req_me"); e == nil || e.Code != CodeResponseMasked {
		t.Errorf("masked: %+v", e)
	}
	f.write(t, "me.json", `{"kind": "http", "id": "req_me", "name": "Me", "method": "GET", "url": "{{base}}/me", "auth": {"type": "none"},
		"headers": [{"name": "Authorization", "value": "{{$response req_login jsonpath $.access_token send=missing}}"}]}`)
	f.reload(t)
	if got := f.sendBody(t, "req_me"); got != "/me|tok-2" {
		t.Errorf("sent again: %q", got)
	}

	// Cleared.
	ForgetResponses(f.svc, "req_login")
	if err := st.Clear(); err != nil {
		t.Fatal(err)
	}
	v, _ = f.svc.ResponseValue(t.Context(), "req_me", "$response req_login jsonpath $.access_token")
	if v.Resolved || v.Code != CodeResponseNotSent || !strings.Contains(v.Problem, "no response yet") {
		t.Errorf("cleared: %+v", v)
	}
}

// TestResponseReferenceResolution: the resolution view lists a response
// reference with the stored value and the request's name, and one
// without a response as not sent yet, without failing or sending.
func TestResponseReferenceResolution(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t)
	f, _ := newRefFixture(t)
	refProject(t, f, srv.URL, "{{$response req_login jsonpath $.access_token send=missing}}", "")
	find := func() ResolvedVariableDTO {
		t.Helper()
		res, err := f.svc.Resolution(t.Context(), "req_me")
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range res.Variables {
			if v.Kind == ResolutionResponse {
				return v
			}
		}
		t.Fatalf("not listed: %+v", res.Variables)
		return ResolvedVariableDTO{}
	}
	v := find()
	if v.Resolved || v.Problem != string(template.ProblemResponse) || !strings.Contains(v.Reason, "it is sent first when this request is sent") ||
		!strings.Contains(v.Description, "sent first when it has none") {
		t.Errorf("before the login: %+v", v)
	}
	if srv.logins.Load() != 0 {
		t.Error("the resolution sent the login")
	}
	f.sendBody(t, "req_login")
	v = find()
	if !v.Resolved || v.Value != "tok-1" || v.Scope != "Response: Log in" || v.Source != SourceResponse || v.Generated {
		t.Errorf("after the login: %+v", v)
	}
}

// TestResponseReferenceRepeatAndRun: a repeat run and a folder run send
// the request first like a single send.
func TestResponseReferenceRepeatAndRun(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t)
	f, _ := newRefFixture(t)
	refProject(t, f, srv.URL, "{{$response req_login jsonpath $.access_token send=always}}", "")

	started, err := f.svc.Repeat(t.Context(), RepeatInputDTO{RequestID: "req_me", Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
		t.Fatalf("repeat: %+v", done.Error)
	}
	if n := srv.logins.Load(); n != 2 {
		t.Errorf("logins after the repeat = %d, want 2", n)
	}

	run := NewRunSession(f.svc)
	defer run.Close()
	res := run.Send(t.Context(), "req_me", RunOptions{}, nil)
	if res.Error != nil || res.Response == nil || res.Response.Body != "/me|tok-3" {
		t.Errorf("run: %+v %+v", res.Error, res.Response)
	}
	if err := run.MissingPrompts(t.Context(), []string{"req_me"}, RunOptions{}); err != nil {
		t.Errorf("MissingPrompts = %v", err)
	}
}
