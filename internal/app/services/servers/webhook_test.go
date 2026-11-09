package servers

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
)

// opsRecorder collects the http:op:* events of replays.
type opsRecorder struct {
	mu        sync.Mutex
	responses map[string][]httpservice.ResponseDTO
	done      map[string]operation.DoneEventDTO
}

func (r *opsRecorder) emit(_ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch d := data.(type) {
	case operation.DataEventDTO[httpservice.ResponseDTO]:
		r.responses[d.OpID] = append(r.responses[d.OpID], d.Items...)
	case operation.DoneEventDTO:
		r.done[d.OpID] = d
	}
}

func (r *opsRecorder) wait(t *testing.T, id string) (operation.DoneEventDTO, []httpservice.ResponseDTO) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		d, ok := r.done[id]
		resps := slices.Clone(r.responses[id])
		r.mu.Unlock()
		if ok {
			return d, resps
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s did not finish", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// newReplayService is a ServersService that replays through a real
// HTTPService on the guarded dialer the app uses.
func newReplayService(t *testing.T) (*ServersService, *recorder, *opsRecorder) {
	t.Helper()
	opsRec := &opsRecorder{responses: map[string][]httpservice.ResponseDTO{}, done: map[string]operation.DoneEventDTO{}}
	logger := slog.New(slog.DiscardHandler)
	ops := operation.NewRegistry(opsRec.emit, logger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ops.Shutdown(ctx)
	})
	dialer := netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(logger)))
	httpSvc := httpservice.New(nil, nil, ops, dialer, nil, logger)
	t.Cleanup(func() { _ = httpSvc.ServiceShutdown() })
	rec := &recorder{}
	svc := newService(rec.emit, logger, core.Options{StopTimeout: time.Second}, WithReplayer(func(req httpclient.Request) (operation.StartedDTO, error) {
		return httpservice.SendPrepared(httpSvc, req)
	}))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	return svc, rec, opsRec
}

func (r *recorder) waitCaptures(t *testing.T, id string, n int) CapturesEventDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		var last *CapturesEventDTO
		for i := range r.captures {
			if r.captures[i].ServerID == id {
				last = &r.captures[i]
			}
		}
		r.mu.Unlock()
		if last != nil && len(last.Captures) == n {
			return *last
		}
		if time.Now().After(deadline) {
			t.Fatalf("no servers:captures event with %d requests", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A request caught by the webhook receiver is listed, shown in full and
// replayed unchanged to another server through the HTTP send pipeline.
func TestWebhookCaptureAndReplay(t *testing.T) {
	t.Parallel()
	svc, rec, opsRec := newReplayService(t)

	type seen struct {
		method, uri, host string
		header            http.Header
		body              []byte
	}
	got := make(chan seen, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.RequestURI, r.Host, r.Header.Clone(), body}
		w.WriteHeader(204)
	}))
	t.Cleanup(target.Close)

	srv, err := svc.Create(t.Context(), CreateInputDTO{Type: webhook.TypeName, Config: ConfigDTO{Webhook: &WebhookConfigDTO{
		Status: 202, Body: "thanks", Headers: []MockHeaderDTO{{Enabled: true, Name: "X-Receiver", Value: "postsonde"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Name != "Webhook receiver" || srv.Config.Webhook == nil || srv.Config.Webhook.Status != 202 {
		t.Fatalf("created %+v", srv)
	}
	srv, err = svc.Start(t.Context(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("{\"event\":\"paid\",\"amount\":42}\n\x00\xff")
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(srv.BoundPort)+"/hooks/pay?id=7&note=a%20b", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	req.Header.Set("User-Agent", "Stripe/1.0")
	req.Header.Add("X-Multi", "1")
	req.Header.Add("X-Multi", "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 202 || string(body) != "thanks" || resp.Header.Get("X-Receiver") != "postsonde" {
		t.Errorf("receiver answered %d %q %v", resp.StatusCode, body, resp.Header)
	}

	ev := rec.waitCaptures(t, srv.ID, 1)
	summary := ev.Captures[0]
	if summary.Method != "POST" || summary.Target != "/hooks/pay?id=7&note=a%20b" || summary.Size != int64(len(payload)) ||
		summary.ContentType != "application/json" || summary.Truncated {
		t.Errorf("summary = %+v", summary)
	}
	list, err := svc.Captures(t.Context(), srv.ID)
	if err != nil || len(list) != 1 || list[0] != summary {
		t.Errorf("Captures = %+v, %v", list, err)
	}
	rec.mu.Lock()
	captured := false
	for _, e := range rec.traffic {
		if e.Kind == "request" && e.Seq == summary.Seq {
			captured = e.Captured
		}
	}
	rec.mu.Unlock()
	if !captured {
		t.Error("the request's traffic event is not marked captured")
	}

	full, err := svc.Capture(t.Context(), CaptureInputDTO{ServerID: srv.ID, Seq: summary.Seq})
	if err != nil {
		t.Fatal(err)
	}
	if full.Path != "/hooks/pay" || len(full.Query) != 2 || full.Query[1] != (HeaderDTO{Name: "note", Value: "a b"}) ||
		full.Headers[0].Name != "Host" || full.Body.Size != int64(len(payload)) || full.Body.ContentType != "application/json" {
		t.Errorf("capture = %+v", full)
	}

	started, err := svc.Replay(t.Context(), ReplayInputDTO{ServerID: srv.ID, Seq: summary.Seq, URL: target.URL + "/elsewhere?x=1"})
	if err != nil {
		t.Fatal(err)
	}
	done, resps := opsRec.wait(t, started.OpID)
	if done.Status != operation.StatusOK || len(resps) != 1 || resps[0].StatusCode != 204 {
		t.Fatalf("replay done %+v, responses %+v", done, resps)
	}
	s := <-got
	if s.method != "POST" || s.uri != "/elsewhere?x=1" || s.host != target.Listener.Addr().String() || !bytes.Equal(s.body, payload) {
		t.Errorf("target saw %s %s host %s body %q", s.method, s.uri, s.host, s.body)
	}
	for _, name := range []string{"Content-Type", "Stripe-Signature", "User-Agent", "Accept-Encoding"} {
		if s.header.Get(name) != req.Header.Get(name) && !(name == "Accept-Encoding" && s.header.Get(name) == "gzip") {
			t.Errorf("%s = %q, want %q", name, s.header.Get(name), req.Header.Get(name))
		}
	}
	if !slices.Equal(s.header.Values("X-Multi"), []string{"1", "2"}) {
		t.Errorf("X-Multi = %q", s.header.Values("X-Multi"))
	}

	// Clearing the traffic forgets the requests and says so.
	if err := svc.ClearTraffic(t.Context(), srv.ID); err != nil {
		t.Fatal(err)
	}
	rec.waitCaptures(t, srv.ID, 0)
	if _, err := svc.Replay(t.Context(), ReplayInputDTO{ServerID: srv.ID, Seq: summary.Seq, URL: target.URL}); codeOf(err) != core.CodeCaptureNotFound {
		t.Errorf("replay after clear: %v", err)
	}
}

func TestWebhookRefusals(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	echoSrv, err := svc.Create(t.Context(), CreateInputDTO{Type: "echo_http"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Captures(t.Context(), echoSrv.ID); codeOf(err) != CodeNotWebhook {
		t.Errorf("Captures of an echo server: %v", err)
	}
	hook, err := svc.Create(t.Context(), CreateInputDTO{Type: webhook.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if hook.Config.Webhook == nil || hook.Config.Webhook.Status != 200 {
		t.Errorf("default config = %+v", hook.Config)
	}
	if _, err := svc.UpdateConfig(t.Context(), hook.ID, ConfigDTO{}); codeOf(err) != webhook.CodeInvalidConfig {
		t.Errorf("UpdateConfig without the response: %v", err)
	}
	if _, err := svc.UpdateConfig(t.Context(), hook.ID, ConfigDTO{Webhook: &WebhookConfigDTO{Status: 99}}); codeOf(err) != webhook.CodeInvalidConfig {
		t.Errorf("UpdateConfig with status 99: %v", err)
	}
	updated, err := svc.UpdateConfig(t.Context(), hook.ID, ConfigDTO{Webhook: &WebhookConfigDTO{Status: 404, Body: "no"}})
	if err != nil || updated.Config.Webhook.Status != 404 || updated.Config.Webhook.Body != "no" {
		t.Errorf("UpdateConfig = %+v, %v", updated.Config.Webhook, err)
	}
	if _, err := svc.Capture(t.Context(), CaptureInputDTO{ServerID: hook.ID, Seq: 1}); codeOf(err) != core.CodeCaptureNotFound {
		t.Errorf("Capture of nothing: %v", err)
	}
	list, err := svc.Captures(t.Context(), hook.ID)
	if err != nil || list == nil || len(list) != 0 {
		t.Errorf("Captures = %#v, %v", list, err)
	}
}

func TestReplayRequest(t *testing.T) {
	t.Parallel()
	c := core.Capture{
		Method: "PUT", Target: "/a", Host: "orig:1",
		Headers: []core.Header{
			{Name: "Connection", Value: "keep-alive"}, {Name: "Content-Length", Value: "0"},
			{Name: "Content-Type", Value: "text/plain"}, {Name: "Keep-Alive", Value: "timeout=5"},
		},
		Size: 0,
	}
	req, err := replayRequest(c, " http://127.0.0.1:9/b ")
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "PUT" || req.URL != "http://127.0.0.1:9/b" || len(req.Headers) != 1 || req.Headers[0].Name != "Content-Type" || !req.Headers[0].Enabled {
		t.Errorf("request = %+v", req)
	}
	if b, ok := req.Body.(httpclient.RawBody); !ok || len(b.Data) != 0 {
		t.Errorf("an empty body with Content-Length: 0 should be sent as an empty body, got %#v", req.Body)
	}
	c.Headers = nil
	if req, _ := replayRequest(c, "https://example.test/"); req.Body != nil {
		t.Errorf("no body and no Content-Length: body = %#v", req.Body)
	}
	for _, bad := range []string{"", "example.test/x", "ftp://example.test/", "http://", "::"} {
		if _, err := replayRequest(c, bad); codeOf(err) != CodeInvalidReplayURL {
			t.Errorf("URL %q: %v", bad, err)
		}
	}
	c.Truncated = true
	if _, err := replayRequest(c, "http://127.0.0.1/"); codeOf(err) != CodeCaptureTruncated {
		t.Errorf("truncated capture: %v", err)
	}
}

func TestReplayUnavailable(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	hook, err := svc.Create(t.Context(), CreateInputDTO{Type: webhook.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	hook, err = svc.Start(t.Context(), hook.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(hook.BoundPort) + "/x")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	list, _ := svc.Captures(t.Context(), hook.ID)
	if len(list) != 1 {
		t.Fatalf("captures = %+v", list)
	}
	if _, err := svc.Replay(t.Context(), ReplayInputDTO{ServerID: hook.ID, Seq: list[0].Seq, URL: "http://127.0.0.1:1/"}); codeOf(err) != CodeReplayUnavailable {
		t.Errorf("replay without a replayer: %v", err)
	}
}
