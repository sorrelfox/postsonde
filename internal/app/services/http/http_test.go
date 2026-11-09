package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// recorder collects the events of one registry.
type recorder struct {
	mu       sync.Mutex
	items    map[string][]ResponseDTO
	progress map[string][]string
	// counts are the progress events whole, with bytes done and total.
	counts map[string][]operation.ProgressEventDTO
	done   map[string]chan operation.DoneEventDTO
	// results are the items of repeat runs (repeat_test.go); resultAdded
	// gets a value, without blocking, whenever some arrive.
	results     map[string][]RepeatResultDTO
	resultAdded chan struct{}
	// itemAdded gets a value, without blocking, whenever responses arrive
	// (incremental_test.go).
	itemAdded chan struct{}
}

func newRecorder() *recorder {
	return &recorder{items: map[string][]ResponseDTO{}, progress: map[string][]string{}, done: map[string]chan operation.DoneEventDTO{},
		results: map[string][]RepeatResultDTO{}, resultAdded: make(chan struct{}, 1), itemAdded: make(chan struct{}, 1)}
}

func (r *recorder) doneChan(id string) chan operation.DoneEventDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.done[id]
	if !ok {
		ch = make(chan operation.DoneEventDTO, 1)
		r.done[id] = ch
	}
	return ch
}

func (r *recorder) emit(name string, data any) {
	switch d := data.(type) {
	case operation.DataEventDTO[ResponseDTO]:
		if name != EventData {
			panic("data event named " + name)
		}
		r.mu.Lock()
		r.items[d.OpID] = append(r.items[d.OpID], d.Items...)
		r.mu.Unlock()
		select {
		case r.itemAdded <- struct{}{}:
		default:
		}
	case operation.DataEventDTO[RepeatResultDTO]:
		if name != EventRepeatData {
			panic("repeat data event named " + name)
		}
		r.mu.Lock()
		r.results[d.OpID] = append(r.results[d.OpID], d.Items...)
		r.mu.Unlock()
		select {
		case r.resultAdded <- struct{}{}:
		default:
		}
	case operation.ProgressEventDTO:
		if name != EventProgress && name != EventRepeatProgress {
			panic("progress event named " + name)
		}
		r.mu.Lock()
		r.progress[d.OpID] = append(r.progress[d.OpID], d.Message)
		if r.counts == nil {
			r.counts = map[string][]operation.ProgressEventDTO{}
		}
		r.counts[d.OpID] = append(r.counts[d.OpID], d)
		r.mu.Unlock()
	case operation.DoneEventDTO:
		if name != EventDone && name != EventRepeatDone {
			panic("done event named " + name)
		}
		r.doneChan(d.OpID) <- d
	}
}

func (r *recorder) wait(t *testing.T, id string) operation.DoneEventDTO {
	t.Helper()
	return r.waitWithin(t, id, 10*time.Second)
}

// waitWithin is wait with its own limit, for sends that are slow by
// design, such as a large body under -race on a loaded machine.
func (r *recorder) waitWithin(t *testing.T, id string, limit time.Duration) operation.DoneEventDTO {
	t.Helper()
	select {
	case d := <-r.doneChan(id):
		return d
	case <-time.After(limit):
		t.Fatalf("operation %s did not finish", id)
		return operation.DoneEventDTO{}
	}
}

func (r *recorder) response(t *testing.T, id string) ResponseDTO {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items[id]) != 1 {
		t.Fatalf("items = %d, want one response", len(r.items[id]))
	}
	return r.items[id][0]
}

type fixture struct {
	svc  *HTTPService
	ops  *operation.Registry
	rec  *recorder
	src  collection.HTTPSource
	mu   sync.Mutex
	def  time.Duration
	jars CookieJars
}

func (f *fixture) setDefault(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.def = d
}

func newFixture(t *testing.T, src collection.HTTPSource, jars CookieJars, opts ...Option) *fixture {
	t.Helper()
	f := &fixture{rec: newRecorder(), src: src, jars: jars}
	f.ops = operation.NewRegistry(f.rec.emit, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.ops.Shutdown(ctx)
	})
	load := func(ctx context.Context, id string) (collection.HTTPSource, error) {
		srcID := f.src.Request.ID
		if f.src.GraphQL != nil {
			srcID = f.src.GraphQL.ID
		}
		if id != srcID {
			return collection.HTTPSource{}, errs.New(errs.NotFound, "no such request").WithCode("collection.not_found")
		}
		return f.src, nil
	}
	defaults := func() time.Duration {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.def
	}
	f.svc = New(load, defaults, f.ops, netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil))), jars, nil, opts...)
	t.Cleanup(func() { _ = f.svc.ServiceShutdown() })
	return f
}

func httpSource(url string) collection.HTTPSource {
	return collection.HTTPSource{
		Request: projectfile.HTTPRequest{ID: "req_1", Method: "GET", URL: url},
		Auth:    projectfile.Auth{Type: projectfile.AuthNone},
	}
}

func (f *fixture) send(t *testing.T) string {
	t.Helper()
	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatalf("Send = %v", err)
	}
	return started.OpID
}

func checkErr(t *testing.T, e *errs.Error, kind errs.Kind, code string) {
	t.Helper()
	if e == nil || e.Kind != kind || e.Code != code {
		t.Fatalf("err = %#v, want %s %s", e, kind, code)
	}
}

func TestSend(t *testing.T) {
	t.Parallel()
	var got nethttp.Header
	var gotPath string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		got, gotPath = r.Header.Clone(), r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Server", "test")
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":7,"tags":["a"]}`)
	}))
	defer srv.Close()

	src := httpSource(srv.URL + "/users/:id")
	src.Request.Method = "POST"
	src.Request.PathParams = []projectfile.KeyValue{{Name: "id", Value: "7"}}
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Folder", Value: "{{f}}"}}
	src.Auth = projectfile.Auth{Type: projectfile.AuthBearer, Token: "tok"}
	src.FolderVariables = [][]projectfile.Variable{{{Name: "f", Value: "outer"}}, {{Name: "f", Value: "inner"}}}
	f := newFixture(t, src, nil)

	id := f.send(t)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusOK || done.Items != 1 {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, id)
	if resp.StatusCode != 201 || resp.Status != "201 Created" || resp.Method != "POST" || resp.URL != srv.URL+"/users/7" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Body != `{"id":7,"tags":["a"]}` || resp.BodyPretty != "{\n  \"id\": 7,\n  \"tags\": [\n    \"a\"\n  ]\n}" || resp.Size != int64(len(resp.Body)) {
		t.Errorf("body = %q, pretty = %q, size %d", resp.Body, resp.BodyPretty, resp.Size)
	}
	if resp.ContentType != "application/json" || !slices.Contains(resp.Headers, HeaderDTO{Name: "X-Server", Value: "test"}) {
		t.Errorf("headers = %+v", resp.Headers)
	}
	if gotPath != "/users/7" || got.Get("Authorization") != "Bearer tok" || got.Get("X-Folder") != "inner" {
		t.Errorf("server got %s %v", gotPath, got)
	}
	f.rec.mu.Lock()
	progress := slices.Clone(f.rec.progress[id])
	f.rec.mu.Unlock()
	if len(progress) == 0 || progress[0] != StageConnecting {
		t.Errorf("progress = %q, want %q first", progress, StageConnecting)
	}
}

// TestSendGraphQL sends a GraphQL request to a test server in both modes
// (GQL-02): POST with a JSON body, and GET with the query in the URL.
func TestSendGraphQL(t *testing.T) {
	t.Parallel()
	type seen struct {
		method, contentType, accept string
		query                       url.Values
		body                        map[string]any
	}
	got := make(chan seen, 1)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		s := seen{method: r.Method, contentType: r.Header.Get("Content-Type"), accept: r.Header.Get("Accept"), query: r.URL.Query()}
		if r.Method == nethttp.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&s.body); err != nil {
				t.Errorf("body: %v", err)
			}
		}
		got <- s
		w.Header().Set("Content-Type", "application/graphql-response+json")
		fmt.Fprint(w, `{"data":{"user":{"name":"Ada"}}}`)
	}))
	defer srv.Close()

	gql := &projectfile.GraphQLRequest{
		ID:        "req_1",
		URL:       "{{base}}/graphql",
		Query:     "query User($id: ID!) { user(id: $id) { name } }",
		Variables: `{"id": "{{user}}"}`,
		// GQL-06: sent as the body's extensions, and in GET's query string.
		Extensions: `{"trace": "{{user}}"}`,
	}
	src := collection.HTTPSource{
		GraphQL:         gql,
		Auth:            projectfile.Auth{Type: projectfile.AuthNone},
		FolderVariables: [][]projectfile.Variable{{{Name: "base", Value: srv.URL}, {Name: "user", Value: "7"}}},
	}
	f := newFixture(t, src, nil)

	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("POST done = %+v", done)
	}
	s := <-got
	if s.method != "POST" || s.contentType != "application/json" || s.accept != request.GraphQLAccept {
		t.Errorf("POST: %s, Content-Type %q, Accept %q", s.method, s.contentType, s.accept)
	}
	want := map[string]any{"query": gql.Query, "variables": map[string]any{"id": "7"}, "operationName": "User", "extensions": map[string]any{"trace": "7"}}
	if !reflect.DeepEqual(s.body, want) {
		t.Errorf("POST body = %v, want %v", s.body, want)
	}
	resp := f.rec.response(t, id)
	if resp.Method != "POST" || resp.BodyPretty == "" {
		t.Errorf("POST response = %+v", resp)
	}

	gql.Method = "GET"
	id = f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("GET done = %+v", done)
	}
	s = <-got
	if s.method != "GET" || s.query.Get("query") != gql.Query || s.query.Get("variables") != `{"id":"7"}` || s.query.Get("operationName") != "User" || s.query.Get("extensions") != `{"trace":"7"}` {
		t.Errorf("GET: %s %v", s.method, s.query)
	}
}

func TestSendEnvironmentVariables(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		fmt.Fprint(w, r.URL.RequestURI())
	}))
	defer srv.Close()

	manager := coreworkspace.New(t.TempDir())
	ws, err := manager.Create(filepath.Join(t.TempDir(), "p"), "P")
	if err != nil {
		t.Fatal(err)
	}
	ws.File.Variables = []projectfile.Variable{{Name: "path", Value: "global"}, {Name: "base", Value: "http://wrong"}}
	store := environments.New(ws.Root, ws.LocalDir, false)
	env, err := store.Create("Local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveVariables(env.ID, []environments.Variable{{Name: "base", Value: srv.URL}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActive(env.ID); err != nil {
		t.Fatal(err)
	}
	src := httpSource("{{base}}/{{path}}")
	src.Workspace = ws
	f := newFixture(t, src, nil)

	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if resp := f.rec.response(t, id); resp.Body != "/global" {
		t.Errorf("body = %q, want the path the server got", resp.Body)
	}

	// A variable that is defined nowhere refuses to send.
	f.src.Request.URL = "{{nope}}/x"
	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	var e *errs.Error
	errors.As(err, &e)
	checkErr(t, e, errs.InvalidArgument, template.CodeUnresolved)
}

// slowServer answers after release is closed or the request is gone.
func slowServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv
}

func TestSendTimeouts(t *testing.T) {
	t.Parallel()
	srv := slowServer(t)
	ms := func(v int64) *int64 { return &v }
	tests := []struct {
		name       string
		own        *int64
		def        time.Duration
		wantSource string
		wantMsg    string
	}{
		{"request timeout", ms(150), 0, request.TimeoutFromRequest, "The request's timeout is 150 ms."},
		{"request timeout overrides the default", ms(150), time.Hour, request.TimeoutFromRequest, "The request's timeout is 150 ms."},
		{"default timeout", nil, 150 * time.Millisecond, request.TimeoutFromSettings, "The default timeout is 150 ms."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := httpSource(srv.URL)
			src.Request.Settings.TimeoutMs = tt.own
			f := newFixture(t, src, nil)
			f.setDefault(tt.def)

			start := time.Now()
			id := f.send(t)
			done := f.rec.wait(t, id)
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("took %v", elapsed)
			}
			if done.Status != operation.StatusError || done.Error == nil {
				t.Fatalf("done = %+v", done)
			}
			e := done.Error
			if e.Kind != string(errs.Timeout) || e.Code != httpclient.CodeTimeout {
				t.Fatalf("error = %+v", e)
			}
			if !strings.HasSuffix(e.Message, tt.wantMsg) || e.Details["timeoutSource"] != tt.wantSource || e.Details["timeoutMs"] != "150" {
				t.Errorf("error = %q, details %v", e.Message, e.Details)
			}
			if !strings.Contains(e.Hint, "timeout") {
				t.Errorf("hint = %q", e.Hint)
			}
		})
	}
}

func TestSendCancel(t *testing.T) {
	t.Parallel()
	srv := slowServer(t)
	f := newFixture(t, httpSource(srv.URL), nil)
	id := f.send(t)
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if !f.ops.Cancel(id) {
		t.Fatal("Cancel = false, want the send to be running")
	}
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusCancelled || done.Error != nil || done.Items != 0 {
		t.Errorf("done = %+v", done)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancel took %v", elapsed)
	}
}

type fixedJar struct{ jar *cookies.Jar }

func (j fixedJar) Jar() (*cookies.Jar, error) { return j.jar, nil }

func TestSendCookies(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if c, err := r.Cookie("session"); err == nil {
			fmt.Fprint(w, c.Value)
			return
		}
		nethttp.SetCookie(w, &nethttp.Cookie{Name: "session", Value: "s1", Path: "/"})
	}))
	defer srv.Close()
	f := newFixture(t, httpSource(srv.URL+"/"), fixedJar{cookies.New(cookies.Options{})})

	first := f.send(t)
	f.rec.wait(t, first)
	second := f.send(t)
	f.rec.wait(t, second)
	if got := f.rec.response(t, second).Body; got != "s1" {
		t.Errorf("second body = %q, want the cookie the first response set", got)
	}
}

func TestSendRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t, httpSource(""), nil)
	var e *errs.Error

	_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	errors.As(err, &e)
	checkErr(t, e, errs.InvalidArgument, request.CodeURLMissing)

	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_other"})
	errors.As(err, &e)
	checkErr(t, e, errs.NotFound, "collection.not_found")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.svc.Send(ctx, SendInputDTO{RequestID: "req_1"}); errs.KindOf(err) != errs.Cancelled {
		t.Errorf("cancelled: err = %v", err)
	}

	_, err = New(nil, nil, nil, nil, nil, nil).Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	errors.As(err, &e)
	checkErr(t, e, errs.Unsupported, CodeUnavailable)
	if f.ops.Running() != 0 {
		t.Errorf("running = %d, want nothing started", f.ops.Running())
	}
}

func TestResponseDTO(t *testing.T) {
	t.Parallel()
	b := request.HTTPBuild{
		Request: httpclient.Request{
			Method:   "HEAD",
			URL:      "https://example.com/a",
			Settings: httpclient.Settings{Timeout: 30 * time.Second},
		},
		TimeoutSource: request.TimeoutFromSettings,
		Warnings:      []request.Warning{{Code: request.WarnBodyOmitted, Message: "The body was not sent: HEAD requests cannot have one."}},
	}
	r := &httpclient.Response{
		Proto:      "HTTP/1.1",
		StatusCode: 200,
		Status:     "200 OK",
		Headers: []httpclient.Header{
			{Name: "Content-Type", Value: "application/problem+json; charset=utf-8", Enabled: true},
			{Name: "Set-Cookie", Value: "a=1", Enabled: true},
			{Name: "Set-Cookie", Value: "b=2", Enabled: true},
		},
		Body:     []byte(`{"title":"ok"}`),
		Size:     14,
		URL:      "https://example.com/b",
		Duration: 1234 * time.Millisecond,
		Redirects: []httpclient.Hop{{
			URL: "https://example.com/a", StatusCode: 302, Status: "302 Found", Proto: "HTTP/1.1",
			Headers: []httpclient.Header{{Name: "Location", Value: "/b", Enabled: true}},
			Timing: httpclient.Timing{
				DNS: time.Millisecond, Connect: 2 * time.Millisecond, RequestWrite: 250 * time.Microsecond, Wait: 3 * time.Millisecond,
				Total:    10 * time.Millisecond,
				Observed: httpclient.PhaseDNS | httpclient.PhaseConnect | httpclient.PhaseRequestWrite | httpclient.PhaseWait,
				Offsets:  httpclient.PhaseOffsets{DNS: 100 * time.Microsecond, Connect: 1100 * time.Microsecond, RequestWrite: 3200 * time.Microsecond, Wait: 3450 * time.Microsecond},
			},
			Conn: httpclient.Conn{LocalAddr: "192.0.2.10:50000", RemoteAddr: "93.184.216.34:80", Protocol: "HTTP/1.1"},
			Request: &httpclient.SentRequest{
				Method: "HEAD", URL: "https://example.com/a", Proto: "HTTP/1.1", Target: "/a",
				Headers: []httpclient.Header{{Name: "Host", Value: "example.com", Enabled: true}, {Name: "Authorization", Value: "Bearer tok", Enabled: true}},
				Body:    []byte{},
			},
		}},
		Request: &httpclient.SentRequest{
			Method: "HEAD", URL: "https://example.com/b", Proto: "HTTP/1.1", Target: "/b",
			Headers: []httpclient.Header{{Name: "Host", Value: "example.com", Enabled: true}},
			Body:    []byte{},
		},
		Events: []httpclient.TimelineEvent{{At: 2 * time.Millisecond, Name: httpclient.EventGetConn, Detail: "example.com:443"}},
		Timing: httpclient.Timing{
			Connect: 2 * time.Millisecond, TLS: 5 * time.Millisecond, RequestWrite: 100 * time.Microsecond, Wait: 20 * time.Millisecond, Download: time.Millisecond,
			Total:    1234 * time.Millisecond,
			Observed: httpclient.PhaseConnect | httpclient.PhaseTLS | httpclient.PhaseRequestWrite | httpclient.PhaseWait | httpclient.PhaseDownload,
			Offsets:  httpclient.PhaseOffsets{Connect: 50 * time.Microsecond, TLS: 2050 * time.Microsecond, RequestWrite: 7100 * time.Microsecond, Wait: 7200 * time.Microsecond, Download: 27200 * time.Microsecond},
		},
		Conn: httpclient.Conn{
			LocalAddr: "192.0.2.10:50001", RemoteAddr: "93.184.216.34:443", Protocol: "h2",
			TLS: &httpclient.TLSInfo{
				Version: "TLS 1.3", CipherSuite: "TLS_AES_128_GCM_SHA256", ServerName: "example.com", NegotiatedProtocol: "h2",
				PeerCertificates: []httpclient.CertInfo{{
					Subject: "CN=example.com", Issuer: "CN=Test CA,O=Test",
					NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
					DNSNames: []string{"example.com", "www.example.com"},
				}},
			},
		},
	}
	dto := toResponseDTO(b, r, nil, time.Time{}, nil)
	wire := "HTTP/1.1 200 OK\r\nContent-Type: application/problem+json; charset=utf-8\r\nSet-Cookie: a=1\r\nSet-Cookie: b=2\r\n\r\n"
	if dto.HeaderSize != int64(len(wire)) {
		t.Errorf("HeaderSize = %d, want %d", dto.HeaderSize, len(wire))
	}
	if h2 := toResponseDTO(b, &httpclient.Response{Proto: "HTTP/2.0"}, nil, time.Time{}, nil); h2.Protocol != "HTTP/2" {
		t.Errorf("Protocol = %q, want HTTP/2", h2.Protocol)
	}
	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/response", append(data, '\n'))

	empty, err := json.Marshal(toResponseDTO(request.HTTPBuild{}, &httpclient.Response{Body: []byte{}}, nil, time.Time{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), `"headers":[]`) || !strings.Contains(string(empty), `"warnings":[]`) {
		t.Errorf("empty = %s, want [] for the lists", empty)
	}
}
