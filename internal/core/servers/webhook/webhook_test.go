package webhook_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
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

func (e *events) ofKind(kind servers.EventKind) []servers.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []servers.Event
	for _, ev := range e.list {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

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

type fixture struct {
	m    *servers.Manager
	id   string
	base string
	rec  *events
}

func start(t *testing.T, opts servers.Options, typ webhook.Type, config any) *fixture {
	t.Helper()
	rec := &events{}
	opts.Types = []servers.Type{typ}
	opts.OnTraffic = rec.add
	opts.StopTimeout = 2 * time.Second
	opts.Logger = slog.New(slog.DiscardHandler)
	m := servers.NewManager(opts)
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: webhook.TypeName, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Scope != "loopback" {
		t.Errorf("scope = %q, want loopback by default", s.Scope)
	}
	return &fixture{m: m, id: s.ID, base: "http://127.0.0.1:" + strconv.Itoa(s.Port()), rec: rec}
}

func (f *fixture) do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestDefaults(t *testing.T) {
	typ := webhook.New(webhook.Options{})
	if typ.Name() != "webhook" || typ.Label() != "Webhook receiver" {
		t.Errorf("name %q label %q", typ.Name(), typ.Label())
	}
	c, err := typ.NormalizeConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.(webhook.Config)
	if cfg.Status != 200 || len(cfg.Headers) != 0 || cfg.Body != "" {
		t.Errorf("default config = %+v", cfg)
	}
}

// Any method on any path is logged, answered with the default response and
// kept byte for byte.
func TestCapturesEveryRequestWhole(t *testing.T) {
	f := start(t, servers.Options{}, webhook.New(webhook.Options{}), nil)
	body := []byte{0x00, 0xff, 'h', 'i', '\r', '\n', 0x80}
	req, err := http.NewRequest("PATCH", f.base+"/hooks/stripe?a=1&a=2&b=%20x", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("X-Signature", "t=1,v1=abc")
	req.Header.Add("X-Multi", "one")
	req.Header.Add("X-Multi", "two")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, got := f.do(t, req)
	if resp.StatusCode != 200 || len(got) != 0 {
		t.Errorf("response %d %q, want 200 and no body", resp.StatusCode, got)
	}

	reqs := f.rec.waitFor(t, servers.KindRequest, 1)
	ev := reqs[0]
	if !ev.Captured || ev.Capture != nil {
		t.Errorf("request event Captured=%v Capture=%v, want the capture kept by the manager only", ev.Captured, ev.Capture)
	}
	if ev.Summary != "PATCH /hooks/stripe?a=1&a=2&b=%20x" || !ev.Binary || !bytes.Equal(ev.Content, body) {
		t.Errorf("request event = %q binary=%v content=%q", ev.Summary, ev.Binary, ev.Content)
	}
	if ev.HTTP == nil || ev.HTTP.Headers[0].Name != "Host" {
		t.Errorf("request headers = %+v, want Host first", ev.HTTP)
	}

	c, err := f.m.Capture(f.id, ev.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != "PATCH" || c.Target != "/hooks/stripe?a=1&a=2&b=%20x" || c.Proto != "HTTP/1.1" || c.Host != strings.TrimPrefix(f.base, "http://") {
		t.Errorf("capture = %+v", c)
	}
	if !bytes.Equal(c.Body, body) || c.Size != int64(len(body)) || c.Truncated {
		t.Errorf("capture body %q size %d truncated %v", c.Body, c.Size, c.Truncated)
	}
	if c.Seq != ev.Seq || c.Peer != ev.Peer || !c.Time.Equal(ev.Time) {
		t.Errorf("capture seq/peer/time %d %q %v, want the event's", c.Seq, c.Peer, c.Time)
	}
	var multi []string
	for _, h := range c.Headers {
		if h.Name == "Host" {
			t.Error("the capture's headers include Host")
		}
		if h.Name == "X-Multi" {
			multi = append(multi, h.Value)
		}
	}
	if !slices.Equal(multi, []string{"one", "two"}) {
		t.Errorf("X-Multi = %q", multi)
	}
	if list, _ := f.m.Captures(f.id); len(list) != 1 || list[0].Seq != ev.Seq {
		t.Errorf("Captures = %+v", list)
	}
	traffic, _ := f.m.Traffic(f.id)
	for _, e := range traffic {
		if e.Capture != nil {
			t.Error("Traffic returned an event with its capture")
		}
	}

	resps := f.rec.waitFor(t, servers.KindResponse, 1)
	if resps[0].Summary != "200 OK" || resps[0].HTTP.Status != 200 {
		t.Errorf("response event = %+v", resps[0])
	}
}

func TestConfiguredResponseAndReconfigure(t *testing.T) {
	cfg := webhook.Config{
		Status:  202,
		Headers: []webhook.Header{{Enabled: true, Name: "Content-Type", Value: "application/json"}, {Enabled: false, Name: "X-Off", Value: "1"}, {Enabled: true, Name: "X-Hook", Value: "yes"}},
		Body:    `{"received":true}`,
	}
	f := start(t, servers.Options{}, webhook.New(webhook.Options{}), cfg)
	req, _ := http.NewRequest("POST", f.base+"/", strings.NewReader("{}"))
	resp, body := f.do(t, req)
	if resp.StatusCode != 202 || string(body) != `{"received":true}` || resp.Header.Get("X-Hook") != "yes" || resp.Header.Get("X-Off") != "" ||
		resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("response %d %v %q", resp.StatusCode, resp.Header, body)
	}

	if _, err := f.m.UpdateConfig(f.id, webhook.Config{Status: 500, Body: "boom"}); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("HEAD", f.base+"/x", nil)
	resp, body = f.do(t, req)
	if resp.StatusCode != 500 || len(body) != 0 {
		t.Errorf("HEAD after reconfigure: %d %q", resp.StatusCode, body)
	}
	req, _ = http.NewRequest("GET", f.base+"/x", nil)
	resp, body = f.do(t, req)
	if resp.StatusCode != 500 || string(body) != "boom" {
		t.Errorf("GET after reconfigure: %d %q", resp.StatusCode, body)
	}
	resps := f.rec.waitFor(t, servers.KindResponse, 3)
	if resps[2].Summary != "500 Internal Server Error" || string(resps[2].Content) != "boom" {
		t.Errorf("response event = %q %q", resps[2].Summary, resps[2].Content)
	}
}

// A body over MaxBody is answered and logged, its capture cut and marked.
// The log keeps the content limit only.
func TestLargeBodies(t *testing.T) {
	f := start(t, servers.Options{ContentLimit: 10}, webhook.New(webhook.Options{MaxBody: 100}), nil)
	body := strings.Repeat("abcdefghij", 15)
	req, _ := http.NewRequest("POST", f.base+"/big", strings.NewReader(body))
	resp, _ := f.do(t, req)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	ev := f.rec.waitFor(t, servers.KindRequest, 1)[0]
	if len(ev.Content) != 10 || ev.Size != 150 || !ev.Truncated {
		t.Errorf("logged %d bytes of %d, truncated %v", len(ev.Content), ev.Size, ev.Truncated)
	}
	c, err := f.m.Capture(f.id, ev.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.Body) != body[:100] || c.Size != 150 || !c.Truncated {
		t.Errorf("capture %d bytes of %d, truncated %v", len(c.Body), c.Size, c.Truncated)
	}
}

// The manager keeps the newest captures within its count and byte budget;
// ClearTraffic forgets them.
func TestCaptureBudget(t *testing.T) {
	f := start(t, servers.Options{Captures: 2, CaptureBytes: 4096}, webhook.New(webhook.Options{}), nil)
	send := func(path string, size int) {
		req, _ := http.NewRequest("POST", f.base+path, strings.NewReader(strings.Repeat("x", size)))
		f.do(t, req)
	}
	send("/1", 10)
	send("/2", 10)
	send("/3", 10)
	list, _ := f.m.Captures(f.id)
	if len(list) != 2 || list[0].Target != "/2" || list[1].Target != "/3" {
		t.Fatalf("captures = %v", targets(list))
	}
	first := f.rec.waitFor(t, servers.KindRequest, 3)[0]
	if _, err := f.m.Capture(f.id, first.Seq); !isCode(err, servers.CodeCaptureNotFound) {
		t.Errorf("dropped capture: err = %v", err)
	}

	send("/big", 3900)
	list, _ = f.m.Captures(f.id)
	if len(list) != 1 || list[0].Target != "/big" {
		t.Errorf("after a large capture: %v, want only /big within 4096 bytes", targets(list))
	}
	send("/huge", 5000)
	list, _ = f.m.Captures(f.id)
	if len(list) != 1 || list[0].Target != "/big" {
		t.Errorf("a capture over the whole budget replaced the others: %v", targets(list))
	}
	huge := f.rec.waitFor(t, servers.KindRequest, 5)[4]
	if huge.Captured {
		t.Error("a capture that was not kept is marked Captured")
	}

	if err := f.m.ClearTraffic(f.id); err != nil {
		t.Fatal(err)
	}
	if list, _ = f.m.Captures(f.id); len(list) != 0 {
		t.Errorf("after ClearTraffic: %v", targets(list))
	}
	if _, err := f.m.Capture("srv_nope", 1); !isCode(err, servers.CodeNotFound) {
		t.Errorf("unknown server: %v", err)
	}
}

// Captures outlive a stop and a restart of the server.
func TestCapturesSurviveRestart(t *testing.T) {
	f := start(t, servers.Options{}, webhook.New(webhook.Options{}), nil)
	req, _ := http.NewRequest("PUT", f.base+"/keep", strings.NewReader("body"))
	f.do(t, req)
	if _, err := f.m.Stop(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Start(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.m.Captures(f.id); len(list) != 1 || string(list[0].Body) != "body" {
		t.Errorf("captures after restart: %v", targets(list))
	}
}

func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   webhook.Config
		field string
	}{
		{"status low", webhook.Config{Status: 199}, "status"},
		{"status high", webhook.Config{Status: 600}, "status"},
		{"header name", webhook.Config{Status: 200, Headers: []webhook.Header{{Enabled: true, Name: "Bad Name", Value: "x"}}}, "headers"},
		{"header value", webhook.Config{Status: 200, Headers: []webhook.Header{{Enabled: true, Name: "X", Value: "a\nb"}}}, "headers"},
		{"framing header", webhook.Config{Status: 200, Headers: []webhook.Header{{Enabled: true, Name: "content-length", Value: "3"}}}, "headers"},
		{"too many headers", webhook.Config{Status: 200, Headers: make([]webhook.Header, webhook.MaxHeaders+1)}, "headers"},
		{"body", webhook.Config{Status: 200, Body: strings.Repeat("x", webhook.MaxBodyBytes+1)}, "body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := webhook.Normalize(tc.cfg)
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != webhook.CodeInvalidConfig || e.Kind != errs.InvalidArgument || e.Details["field"] != tc.field {
				t.Errorf("err = %#v, want %s on %s", err, webhook.CodeInvalidConfig, tc.field)
			}
		})
	}
	got, err := webhook.Normalize(webhook.Config{Status: 201, Headers: []webhook.Header{{}, {Enabled: true, Name: " X-A ", Value: "1"}}})
	if err != nil || len(got.Headers) != 1 || got.Headers[0].Name != "X-A" {
		t.Errorf("Normalize = %+v, %v", got, err)
	}
	if _, err := webhook.New(webhook.Options{}).NormalizeConfig(42); !isCode(err, webhook.CodeInvalidConfig) {
		t.Errorf("wrong config type: %v", err)
	}
}

func targets(list []servers.Capture) []string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Target)
	}
	return out
}

func isCode(err error, code string) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Code == code
}
