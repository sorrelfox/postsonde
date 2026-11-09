package mockhttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockroute"
)

type events struct {
	mu   sync.Mutex
	list []servers.Event
}

func (e *events) add(ev servers.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) all() []servers.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

func (e *events) ofKind(kind servers.EventKind) []servers.Event {
	var out []servers.Event
	for _, ev := range e.all() {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// waitFor polls until n events of kind have been recorded and returns them.
func (e *events) waitFor(t *testing.T, kind servers.EventKind, n int) []servers.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := e.ofKind(kind)
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("saw %d %s events, want %d", len(got), kind, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fakeSleep records the delays asked for and returns at once, unless
// block is set: then it waits for the context.
type fakeSleep struct {
	mu     sync.Mutex
	delays []time.Duration
	block  bool
}

func (f *fakeSleep) sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.delays = append(f.delays, d)
	block := f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *fakeSleep) got() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.delays)
}

type fixture struct {
	m   *servers.Manager
	srv servers.Server
	rec *events
	url string
}

func start(t *testing.T, opts mockhttp.Options, cfg mockhttp.Config) *fixture {
	t.Helper()
	rec := &events{}
	m := servers.NewManager(servers.Options{
		Types:       []servers.Type{mockhttp.New(opts)},
		OnTraffic:   rec.add,
		StopTimeout: 2 * time.Second,
		Logger:      slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: mockhttp.TypeName, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if s.Spec.Name != "Mock HTTP" {
		t.Errorf("default name = %q", s.Spec.Name)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{m: m, srv: s, rec: rec, url: "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port()))}
}

type reply struct {
	status int
	header http.Header
	body   string
}

func (f *fixture) do(t *testing.T, method, path string) reply {
	t.Helper()
	req, err := http.NewRequest(method, f.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, string(b)}
}

func route(id, method, path string, status int, body string) mockhttp.Route {
	r := mockhttp.Route{ID: id, Enabled: true, Method: method, Path: path, Status: status}
	if body != "" {
		r.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: "text", Text: body}
	}
	return r
}

func TestResponsesPerMethodAndSpecificity(t *testing.T) {
	t.Parallel()
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{
		route("list", "GET", "/users", 200, "list"),
		route("create", "POST", "/users", 201, "created"),
		route("me", "GET", "/users/me", 200, "me"),
		route("num", "GET", "/users/{id:[0-9]+}", 200, "number"),
		route("user", "GET", "/users/:id", 200, "user"),
		route("del", "DELETE", "/users/:id", 204, "ignored"),
		route("files", "ANY", "/files/*path", 200, "file"),
		route("all", "GET", "/**", 200, "fallback"),
	}})

	tests := []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/users", 200, "list"},
		{"POST", "/users", 201, "created"},
		{"GET", "/users/me", 200, "me"},
		{"GET", "/users/42", 200, "number"},
		{"GET", "/users/bob", 200, "user"},
		{"DELETE", "/users/bob", 204, ""},
		{"PUT", "/files/a/b.txt", 200, "file"},
		{"GET", "/other/thing", 200, "fallback"},
	}
	for _, tt := range tests {
		got := f.do(t, tt.method, tt.path)
		if got.status != tt.status || got.body != tt.body {
			t.Errorf("%s %s = %d %q, want %d %q", tt.method, tt.path, got.status, got.body, tt.status, tt.body)
		}
	}

	f.rec.waitFor(t, servers.KindResponse, len(tests))
	reqs := f.rec.ofKind(servers.KindRequest)
	userReq := reqs[4]
	if r := userReq.HTTP.Route; r == nil || r.ID != "user" || r.Pattern != "/users/:id" ||
		!slices.Equal(r.Params, []servers.Header{{Name: "id", Value: "bob"}}) {
		t.Errorf("GET /users/bob route = %+v", userReq.HTTP.Route)
	}
	fileResp := f.rec.ofKind(servers.KindResponse)[6]
	if r := fileResp.HTTP.Route; r == nil || r.ID != "files" || r.Wildcard != "a/b.txt" ||
		!slices.Equal(r.Params, []servers.Header{{Name: "path", Value: "a/b.txt"}}) {
		t.Errorf("PUT /files route = %+v", fileResp.HTTP.Route)
	}
	if del := f.rec.ofKind(servers.KindResponse)[5]; len(del.Content) != 0 || del.Summary != "204 No Content" {
		t.Errorf("204 response event = %+v", del)
	}
}

func TestNotFoundMethodNotAllowedAndHead(t *testing.T) {
	t.Parallel()
	get := route("get", "GET", "/items/:id", 200, "item")
	get.Headers = []mockhttp.Header{{Enabled: true, Name: "X-Item", Value: "1"}}
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{
		get,
		route("put", "PUT", "/items/:id", 200, "put"),
		route("head", "HEAD", "/special", 200, "special"),
	}})

	nf := f.do(t, "GET", "/nothing")
	var body map[string]any
	if err := json.Unmarshal([]byte(nf.body), &body); err != nil || nf.status != 404 ||
		body["error"] != "No route matches GET /nothing." || nf.header.Get("Content-Type") != "application/json" {
		t.Errorf("404 = %+v (%v)", nf, err)
	}
	if strings.Contains(nf.body, "/items") {
		t.Errorf("404 body lists routes: %s", nf.body)
	}

	na := f.do(t, "POST", "/items/7")
	if na.status != 405 || na.header.Get("Allow") != "GET, HEAD, PUT" || !strings.Contains(na.body, `"allowed"`) {
		t.Errorf("405 = %+v", na)
	}

	head := f.do(t, "HEAD", "/items/7")
	if head.status != 200 || head.body != "" || head.header.Get("X-Item") != "1" || head.header.Get("Content-Length") != "4" {
		t.Errorf("HEAD = %+v", head)
	}
	if got := f.do(t, "GET", "/items/7"); got.body != "item" || got.header.Get("X-Item") != "1" {
		t.Errorf("GET = %+v", got)
	}

	resps := f.rec.waitFor(t, servers.KindResponse, 4)
	if resps[0].Summary != "404 Not Found (no route matches)" || resps[0].HTTP.Route != nil {
		t.Errorf("404 event = %+v", resps[0])
	}
	if resps[1].Summary != "405 Method Not Allowed (allowed: GET, HEAD, PUT)" {
		t.Errorf("405 event = %+v", resps[1])
	}
	if len(resps[2].Content) != 0 || resps[2].HTTP.Method != "HEAD" || resps[2].HTTP.Route.ID != "get" {
		t.Errorf("HEAD event = %+v", resps[2])
	}
	if string(resps[3].Content) != "item" || !slices.Contains(resps[3].HTTP.Headers, servers.Header{Name: "X-Item", Value: "1"}) {
		t.Errorf("GET event = %+v", resps[3])
	}
}

func TestContentType(t *testing.T) {
	t.Parallel()
	jsonRoute := route("json", "GET", "/json", 200, "")
	jsonRoute.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: "json", Text: `{"a":1}`}
	html := route("html", "GET", "/html", 200, "")
	html.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: "html", Text: "<p>hi</p>"}
	override := route("override", "GET", "/override", 200, "")
	override.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: "json", Text: `{}`}
	override.Headers = []mockhttp.Header{
		{Enabled: false, Name: "Content-Type", Value: "text/disabled"},
		{Enabled: true, Name: "content-type", Value: "application/vnd.api+json"},
		{Enabled: true, Name: "Content-Length", Value: "999"},
	}
	none := route("none", "GET", "/none", 200, "")
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{jsonRoute, html, override, none}})

	for path, want := range map[string]string{
		"/json":     "application/json",
		"/html":     "text/html; charset=utf-8",
		"/override": "application/vnd.api+json",
		"/none":     "",
	} {
		got := f.do(t, "GET", path)
		if got.header.Get("Content-Type") != want {
			t.Errorf("%s Content-Type = %q, want %q", path, got.header.Get("Content-Type"), want)
		}
		if path == "/override" && (got.body != "{}" || got.header.Get("Content-Length") != "2") {
			t.Errorf("override = %+v", got)
		}
		if path == "/none" && (got.body != "" || got.header.Get("Content-Length") != "0") {
			t.Errorf("none = %+v", got)
		}
	}
}

func TestBase64Body(t *testing.T) {
	t.Parallel()
	bin := route("bin", "GET", "/bin", 200, "")
	// Wrapped over lines, as typed in the editor.
	bin.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: mockhttp.BodyTypeBase64, Text: "iVBO\nRw0K GgoA/w=="}
	png := route("png", "GET", "/png", 200, "")
	png.Body = bin.Body
	png.Headers = []mockhttp.Header{{Enabled: true, Name: "Content-Type", Value: "image/png"}}
	bad := route("bad", "GET", "/bad", 200, "")
	bad.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: mockhttp.BodyTypeBase64, Text: "not base64!"}
	tmpl := route("tmpl", "GET", "/tmpl", 200, "")
	tmpl.Body = bin.Body
	tmpl.Templated = true
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{bin, png, bad, tmpl}})

	want := "\x89PNG\r\n\x1a\n\x00\xff"
	for path, ct := range map[string]string{"/bin": "application/octet-stream", "/png": "image/png"} {
		got := f.do(t, "GET", path)
		if got.status != 200 || got.body != want || got.header.Get("Content-Type") != ct || got.header.Get("Content-Length") != strconv.Itoa(len(want)) {
			t.Errorf("%s = %d %q %v", path, got.status, got.body, got.header)
		}
	}
	for _, path := range []string{"/bad", "/tmpl"} {
		if got := f.do(t, "GET", path); got.status != 404 {
			t.Errorf("%s served: %d", path, got.status)
		}
	}
	var codes []string
	for _, d := range mockhttp.Check(mockhttp.Config{Routes: []mockhttp.Route{bin, bad, tmpl}}) {
		codes = append(codes, d.RouteID+" "+d.Code)
	}
	if want := []string{"bad " + mockhttp.CodeInvalidBody, "tmpl " + mockhttp.CodeInvalidBody}; !slices.Equal(codes, want) {
		t.Errorf("diagnostics %q, want %q", codes, want)
	}
}

func TestDelays(t *testing.T) {
	t.Parallel()
	fixed := route("fixed", "GET", "/fixed", 200, "slow")
	fixed.Delay = mockhttp.Delay{Mode: mockhttp.DelayFixed, Ms: 1500}
	random := route("random", "GET", "/random", 200, "rnd")
	random.Delay = mockhttp.Delay{Mode: mockhttp.DelayRandom, MinMs: 100, MaxMs: 300}
	sl := &fakeSleep{}
	var spans []int64
	var mu sync.Mutex
	f := start(t, mockhttp.Options{Sleep: sl.sleep, RandN: func(n int64) int64 {
		mu.Lock()
		spans = append(spans, n)
		mu.Unlock()
		return n - 1
	}}, mockhttp.Config{Routes: []mockhttp.Route{fixed, random, route("fast", "GET", "/fast", 200, "fast")}})

	for _, p := range []string{"/fixed", "/random", "/fast"} {
		if got := f.do(t, "GET", p); got.status != 200 {
			t.Errorf("%s = %+v", p, got)
		}
	}
	if got := sl.got(); !slices.Equal(got, []time.Duration{1500 * time.Millisecond, 300 * time.Millisecond}) {
		t.Errorf("delays = %v", got)
	}
	mu.Lock()
	if !slices.Equal(spans, []int64{201}) {
		t.Errorf("random spans = %v, want [201] (min and max included)", spans)
	}
	mu.Unlock()
	resps := f.rec.waitFor(t, servers.KindResponse, 3)
	if resps[0].HTTP.Delay != 1500*time.Millisecond || resps[1].HTTP.Delay != 300*time.Millisecond || resps[2].HTTP.Delay != 0 {
		t.Errorf("event delays = %v %v %v", resps[0].HTTP.Delay, resps[1].HTTP.Delay, resps[2].HTTP.Delay)
	}
}

func TestRealDelay(t *testing.T) {
	t.Parallel()
	slow := route("slow", "GET", "/slow", 200, "done")
	slow.Delay = mockhttp.Delay{Mode: mockhttp.DelayFixed, Ms: 80}
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{slow}})
	began := time.Now()
	got := f.do(t, "GET", "/slow")
	if elapsed := time.Since(began); elapsed < 80*time.Millisecond || got.body != "done" {
		t.Errorf("got %+v after %v, want the body after at least 80ms", got, elapsed)
	}
}

func TestStopCancelsDelay(t *testing.T) {
	t.Parallel()
	slow := route("slow", "GET", "/slow", 200, "never")
	slow.Delay = mockhttp.Delay{Mode: mockhttp.DelayFixed, Ms: int(mockhttp.MaxDelay / time.Millisecond)}
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{slow}})

	errc := make(chan error, 1)
	go func() {
		resp, err := http.Get(f.url + "/slow")
		if err == nil {
			resp.Body.Close()
			err = fmt.Errorf("got a response: %d", resp.StatusCode)
		}
		errc <- err
	}()
	f.rec.waitFor(t, servers.KindRequest, 1)

	began := time.Now()
	if _, err := f.m.Stop(t.Context(), f.srv.ID); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Errorf("Stop took %v with a delayed response in progress", elapsed)
	}
	if err := <-errc; err == nil || strings.Contains(err.Error(), "got a response") {
		t.Errorf("client = %v, want a closed connection", err)
	}
	evs := f.rec.ofKind(servers.KindError)
	if len(evs) != 1 || !strings.Contains(evs[0].Summary, "the server is stopping") {
		t.Errorf("error events = %+v", evs)
	}
	if len(f.rec.ofKind(servers.KindResponse)) != 0 {
		t.Error("a response was logged")
	}
}

func TestClientLeavesDuringDelay(t *testing.T) {
	t.Parallel()
	slow := route("slow", "GET", "/slow", 200, "never")
	slow.Delay = mockhttp.Delay{Mode: mockhttp.DelayFixed, Ms: 10_000}
	sl := &fakeSleep{block: true}
	f := start(t, mockhttp.Options{Sleep: sl.sleep}, mockhttp.Config{Routes: []mockhttp.Route{slow}})

	c, err := net.Dial("tcp", strings.TrimPrefix(f.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n")
	f.rec.waitFor(t, servers.KindRequest, 1)
	c.Close()
	evs := f.rec.waitFor(t, servers.KindError, 1)
	if !strings.Contains(evs[0].Summary, "the client closed the connection") {
		t.Errorf("error event = %+v", evs[0])
	}
}

func TestUpdateConfigWhileRunning(t *testing.T) {
	t.Parallel()
	slow := route("slow", "GET", "/slow", 200, "old")
	slow.Delay = mockhttp.Delay{Mode: mockhttp.DelayFixed, Ms: 200}
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{slow, route("a", "GET", "/a", 200, "old a")}})

	// A request in progress finishes with the routes it matched.
	done := make(chan reply, 1)
	go func() { done <- f.do(t, "GET", "/slow") }()
	f.rec.waitFor(t, servers.KindRequest, 1)

	updated := route("a", "GET", "/a", 202, "new a")
	s, err := f.m.UpdateConfig(f.srv.ID, mockhttp.Config{Routes: []mockhttp.Route{updated, route("b", "POST", "/b", 201, "b")}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg, ok := s.Spec.Config.(mockhttp.Config); !ok || len(cfg.Routes) != 2 || s.State != servers.StateRunning {
		t.Errorf("server after update = %+v", s)
	}
	if got := f.do(t, "GET", "/a"); got.status != 202 || got.body != "new a" {
		t.Errorf("GET /a = %+v", got)
	}
	if got := f.do(t, "POST", "/b"); got.status != 201 {
		t.Errorf("POST /b = %+v", got)
	}
	if got := f.do(t, "GET", "/c"); got.status != 404 {
		t.Errorf("GET /c = %+v", got)
	}
	if got := <-done; got.body != "old" {
		t.Errorf("request in progress = %+v", got)
	}

	// A stopped server keeps the configuration for its next start.
	if _, err := f.m.Stop(t.Context(), f.srv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.UpdateConfig(f.srv.ID, mockhttp.Config{Routes: []mockhttp.Route{route("c", "GET", "/c", 200, "c")}}); err != nil {
		t.Fatal(err)
	}
	s, err = f.m.Start(t.Context(), f.srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.url = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port()))
	if got := f.do(t, "GET", "/c"); got.body != "c" {
		t.Errorf("GET /c after restart = %+v", got)
	}
}

func TestInvalidRoutesAreSkipped(t *testing.T) {
	t.Parallel()
	badStatus := route("bad-status", "GET", "/status", 99, "x")
	disabled := route("off", "GET", "/off", 200, "off")
	disabled.Enabled = false
	f := start(t, mockhttp.Options{}, mockhttp.Config{Routes: []mockhttp.Route{
		route("bad-path", "GET", "/users/{id", 200, "x"),
		badStatus,
		disabled,
		route("ok", "GET", "/ok", 200, "ok"),
	}})
	for path, want := range map[string]int{"/users/{id": 404, "/status": 404, "/off": 404, "/ok": 200} {
		if got := f.do(t, "GET", path); got.status != want {
			t.Errorf("GET %s = %d, want %d", path, got.status, want)
		}
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	hdr := route("hdr", "GET", "/h", 200, "")
	hdr.Headers = []mockhttp.Header{
		{Enabled: true, Name: "", Value: ""},
		{Enabled: true, Name: "Bad Name", Value: "v"},
		{Enabled: true, Name: "X-Ok", Value: "line\nbreak"},
		{Enabled: false, Name: "Also Bad", Value: "ignored when disabled"},
	}
	delay := route("delay", "GET", "/d", 200, "")
	delay.Delay = mockhttp.Delay{Mode: mockhttp.DelayRandom, MinMs: 500, MaxMs: 100}
	long := route("long", "GET", "/l", 200, "")
	long.Delay = mockhttp.Delay{Mode: mockhttp.DelayFixed, Ms: int(mockhttp.MaxDelay/time.Millisecond) + 1}
	body := route("body", "GET", "/b", 600, "")
	body.Body = mockhttp.Body{Mode: mockhttp.BodyRaw, Type: "toml"}
	off := route("off", "GET", "/users/:id", 200, "")
	off.Enabled = false
	offBad := route("off-bad", "GET", "/x//y", 200, "")
	offBad.Enabled = false

	diags := mockhttp.Check(mockhttp.Config{Routes: []mockhttp.Route{
		route("u1", "GET", "/users/:id", 200, ""), // 0
		off,    // 1: a disabled duplicate is not a duplicate
		offBad, // 2
		route("u2", "GET", "/users/:name", 200, ""), // 3: duplicate of route 1 (index 0)
		hdr,                               // 4
		delay,                             // 5
		long,                              // 6
		body,                              // 7
		route("m", "GE T", "/m", 200, ""), // 8
	}})
	type key struct {
		idx         int
		code, field string
		header      int
	}
	var got []key
	for _, d := range diags {
		got = append(got, key{d.RouteIndex, d.Code, d.Field, d.HeaderIndex})
		if d.Message == "" {
			t.Errorf("no message: %+v", d)
		}
	}
	want := []key{
		{2, mockroute.CodeEmptySegment, mockroute.FieldPattern, -1},
		{3, mockroute.CodeDuplicateRoute, mockroute.FieldPattern, -1},
		{4, mockhttp.CodeInvalidHeaderName, mockhttp.FieldHeader, 1},
		{4, mockhttp.CodeInvalidHeaderValue, mockhttp.FieldHeader, 2},
		{5, mockhttp.CodeInvalidDelay, mockhttp.FieldDelay, -1},
		{6, mockhttp.CodeInvalidDelay, mockhttp.FieldDelay, -1},
		{7, mockhttp.CodeInvalidStatus, mockhttp.FieldStatus, -1},
		{7, mockhttp.CodeInvalidBody, mockhttp.FieldBody, -1},
		{8, mockroute.CodeInvalidMethod, mockroute.FieldMethod, -1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("diagnostics:\n got %v\nwant %v", got, want)
	}
	for _, d := range diags {
		if d.Code == mockroute.CodeDuplicateRoute && !strings.Contains(d.Message, "route 1,") {
			t.Errorf("duplicate message = %q, want it to name route 1", d.Message)
		}
		if d.Code == mockroute.CodeEmptySegment && (d.Start == 0 && d.End == 0) {
			t.Errorf("empty segment has no range: %+v", d)
		}
	}
}

func TestNormalizeConfig(t *testing.T) {
	t.Parallel()
	typ := mockhttp.New(mockhttp.Options{})
	def, err := typ.NormalizeConfig(nil)
	if err != nil || len(def.(mockhttp.Config).Routes) != 1 {
		t.Fatalf("default = %+v, %v", def, err)
	}
	in := mockhttp.Config{Routes: []mockhttp.Route{{Method: " GET ", Path: " /x ", Headers: []mockhttp.Header{{Name: "A"}}}}}
	out, err := typ.NormalizeConfig(&in)
	if err != nil {
		t.Fatal(err)
	}
	r := out.(mockhttp.Config).Routes[0]
	if r.ID == "" || r.Method != "GET" || r.Path != "/x" || r.Body.Mode != "none" || r.Delay.Mode != "none" {
		t.Errorf("normalized = %+v", r)
	}
	r.Headers[0].Name = "changed"
	if in.Routes[0].Headers[0].Name != "A" {
		t.Error("the normalized config shares headers with the input")
	}
	if _, err := typ.NormalizeConfig("nope"); err == nil {
		t.Error("a string config was accepted")
	}
	if _, err := typ.NormalizeConfig(mockhttp.Config{Routes: make([]mockhttp.Route, mockhttp.MaxRoutes+1)}); err == nil {
		t.Error("too many routes were accepted")
	}
	big := mockhttp.Config{Routes: []mockhttp.Route{{Body: mockhttp.Body{Mode: "raw", Text: strings.Repeat("x", mockhttp.MaxBodyBytes+1)}}}}
	if _, err := typ.NormalizeConfig(big); err == nil {
		t.Error("a too large body was accepted")
	}
}
