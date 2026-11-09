package echo_test

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"github.com/sorrelfox/postsonde/internal/core/servers/echo"
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

// waitFor polls until an event of kind has been recorded n times.
func (e *events) waitFor(t *testing.T, kind servers.EventKind, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		count := 0
		for _, ev := range e.all() {
			if ev.Kind == kind {
				count++
			}
		}
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("saw %d %s events, want %d: %s", count, kind, n, kinds(e.all()))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func kinds(evs []servers.Event) string {
	parts := make([]string, len(evs))
	for i, ev := range evs {
		parts[i] = string(ev.Kind)
	}
	return strings.Join(parts, ",")
}

func startEcho(t *testing.T, limit int) (*servers.Manager, servers.Server, *events) {
	t.Helper()
	rec := &events{}
	m := servers.NewManager(servers.Options{
		Types:        []servers.Type{echo.New()},
		OnTraffic:    rec.add,
		ContentLimit: limit,
		StopTimeout:  time.Second,
		Logger:       slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: echo.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if s.Spec.Name != "Echo HTTP" {
		t.Errorf("default name = %q", s.Spec.Name)
	}
	s, err = m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m, s, rec
}

func baseURL(s servers.Server) string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port()))
}

func TestEchoLogsRequestAndConnection(t *testing.T) {
	t.Parallel()
	m, s, rec := startEcho(t, 16)

	body := strings.Repeat("0123456789", 4) // 40 bytes, over the 16-byte limit
	req, err := http.NewRequest(http.MethodPost, baseURL(s)+"/users/7?q=a&q=b", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test", "yes")
	req.Header.Set("Content-Type", "text/plain")
	client := &http.Client{Transport: &http.Transport{}} // keep-alive: the connection stays open
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("response %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var got echo.Echo
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("response body %q: %v", raw, err)
	}
	if got.Method != "POST" || got.Path != "/users/7" || got.Target != "/users/7?q=a&q=b" ||
		fmt.Sprint(got.Query["q"]) != "[a b]" || got.Headers["X-Test"][0] != "yes" ||
		got.Body != body[:16] || got.BodySize != 40 || !got.BodyTruncated || got.RemoteAddr == "" {
		t.Errorf("echo = %+v", got)
	}

	rec.waitFor(t, servers.KindResponse, 1)
	if _, err := m.Stop(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}

	evs := rec.all()
	if k := kinds(evs); k != "started,connection_open,request,response,connection_close,stopped" {
		t.Fatalf("events = %s", k)
	}
	for i, ev := range evs {
		if ev.ServerID != s.ID || ev.Seq != uint64(i+1) || ev.Time.IsZero() {
			t.Errorf("event %d = %+v", i, ev)
		}
	}
	open, request, response, closed := evs[1], evs[2], evs[3], evs[4]
	if open.Direction != servers.System || open.ConnID != 1 || open.Peer == "" {
		t.Errorf("open = %+v", open)
	}
	if request.Direction != servers.In || request.Summary != "POST /users/7?q=a&q=b" || request.Peer != open.Peer ||
		request.ConnID != 1 || string(request.Content) != body[:16] || request.Size != 40 || !request.Truncated || request.Binary {
		t.Errorf("request = %+v", request)
	}
	h := request.HTTP
	if h == nil || h.Method != "POST" || h.Target != "/users/7?q=a&q=b" || h.Proto != "HTTP/1.1" || h.Status != 0 {
		t.Fatalf("request HTTP = %+v", h)
	}
	if h.Headers[0] != (servers.Header{Name: "Host", Value: strings.TrimPrefix(baseURL(s), "http://")}) ||
		!slices.Contains(h.Headers, servers.Header{Name: "X-Test", Value: "yes"}) {
		t.Errorf("request headers = %+v", h.Headers)
	}
	if response.Direction != servers.Out || response.Summary != "200 OK" || response.HTTP.Status != 200 ||
		!response.Truncated || response.Size != len(raw) || !bytes.Equal(response.Content, raw[:16]) {
		t.Errorf("response = %+v", response)
	}
	if !slices.Contains(response.HTTP.Headers, servers.Header{Name: "Content-Length", Value: strconv.Itoa(len(raw))}) {
		t.Errorf("response headers = %+v", response.HTTP.Headers)
	}
	if closed.ConnID != 1 || closed.Summary != "Connection closed (server stopped)" {
		t.Errorf("close = %+v", closed)
	}
}

func TestEchoBinaryBodyAndClosedConnection(t *testing.T) {
	t.Parallel()
	m, s, rec := startEcho(t, 0)

	payload := []byte{0xff, 0xfe, 0x00, 0x01}
	req, _ := http.NewRequest(http.MethodPut, baseURL(s)+"/bin", bytes.NewReader(payload))
	req.Close = true // the connection closes after the response
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	var got echo.Echo
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.Body != "" || got.BodyBase64 != base64.StdEncoding.EncodeToString(payload) || got.BodySize != 4 || got.BodyTruncated {
		t.Errorf("echo = %+v", got)
	}

	rec.waitFor(t, servers.KindConnectionClose, 1)
	var request servers.Event
	for _, ev := range rec.all() {
		if ev.Kind == servers.KindRequest {
			request = ev
		}
	}
	if !request.Binary || !bytes.Equal(request.Content, payload) || request.Truncated {
		t.Errorf("request = %+v", request)
	}
	if _, err := m.Stop(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	for _, ev := range rec.all() {
		if ev.Summary == "Connection closed (server stopped)" {
			t.Errorf("a closed connection was closed again at stop: %+v", ev)
		}
	}
}

func TestEchoHead(t *testing.T) {
	t.Parallel()
	_, s, rec := startEcho(t, 0)
	resp, err := http.Head(baseURL(s) + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	rec.waitFor(t, servers.KindResponse, 1)
	for _, ev := range rec.all() {
		if ev.Kind == servers.KindResponse && (len(ev.Content) != 0 || ev.HTTP.Method != "HEAD") {
			t.Errorf("HEAD response event = %+v", ev)
		}
	}
}

// A stop that times out closes busy connections and still returns.
func TestEchoStopClosesSlowRequest(t *testing.T) {
	t.Parallel()
	rec := &events{}
	m := servers.NewManager(servers.Options{
		Types:       []servers.Type{echo.New()},
		OnTraffic:   rec.add,
		StopTimeout: 100 * time.Millisecond,
		Logger:      slog.New(slog.DiscardHandler),
	})
	s, _ := m.Add(servers.Spec{Type: echo.TypeName})
	s, err := m.Start(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A request whose body never ends keeps its handler busy.
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port())))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\npartial")
	rec.waitFor(t, servers.KindConnectionOpen, 1)
	time.Sleep(50 * time.Millisecond) // let the handler start reading

	done := make(chan struct{})
	go func() {
		m.StopAll(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StopAll did not return")
	}
	evs := rec.all()
	if last := evs[len(evs)-1]; last.Kind != servers.KindStopped {
		t.Errorf("events = %s, want stopped last", kinds(evs))
	}
	if got, _ := m.Get(s.ID); got.State != servers.StateStopped {
		t.Errorf("state = %s", got.State)
	}
}
