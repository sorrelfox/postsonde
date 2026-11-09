package http

import (
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/testenv"
)

// historyStore is a history in a temporary directory, and the option that
// records sends into it.
func historyStore(t *testing.T) (*history.Store, Option) {
	t.Helper()
	st, err := history.Open(filepath.Join(t.TempDir(), history.DirName), history.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, WithHistory(func() func(history.Entry) {
		return func(e history.Entry) {
			if _, err := st.Append(e); err != nil {
				t.Errorf("Append = %v", err)
			}
		}
	})
}

// historyFiles returns everything written to the history files.
func historyFiles(t *testing.T, st *history.Store) string {
	t.Helper()
	var b strings.Builder
	ds, err := os.ReadDir(st.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		data, err := os.ReadFile(filepath.Join(st.Dir(), d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}

// waitEntries waits until the history holds n entries and returns them,
// newest first.
func waitEntries(t *testing.T, st *history.Store, n int) []history.Summary {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p := st.List(history.Query{})
		if p.Total >= n || time.Now().After(deadline) {
			if p.Total != n {
				t.Fatalf("history has %d entries, want %d", p.Total, n)
			}
			return p.Entries
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// NAV-02: every send is recorded with the request as sent and the response
// snapshot, and the secrets of the request (secret variables, the API key,
// Authorization) are masked in the history files, also where the server
// echoed them back.
func TestSendRecordsHistory(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Token", r.Header.Get("X-Token"))
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(nethttp.StatusCreated)
		_, _ = w.Write([]byte("got " + r.URL.RawQuery + " " + string(b) + " " + r.Header.Get("Authorization")))
	}))
	t.Cleanup(srv.Close)
	ws, store, dev, _ := envWorkspace(t, srv.URL)
	if err := store.SetActive(dev); err != nil {
		t.Fatal(err)
	}
	src := httpSource("{{base}}/items?key={{token}}&tag={{tag}}")
	src.Request.Name = "Create item"
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Token", Value: "{{token}}"}, {Name: "Authorization", Value: "Bearer plain-bearer"}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Text: `{"t":"{{token}}"}`}
	src.Auth = projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-Api-Key", Value: "k3y-value"}
	src.Workspace = ws
	st, opt := historyStore(t)
	f := newFixture(t, src, nil, opt)

	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	sums := waitEntries(t, st, 1)
	sum := sums[0]
	// The response names its entry, for a response example (HTTP-38).
	if got := f.rec.response(t, id).HistoryID; got != sum.ID {
		t.Errorf("response historyId = %q, want the entry's %q", got, sum.ID)
	}
	if sum.Kind != history.KindHTTP || sum.RequestID != "req_1" || sum.RequestName != "Create item" || sum.Method != "POST" ||
		sum.Outcome != history.OutcomeOK || sum.StatusCode != 201 || sum.Status != "201 Created" || !sum.Masked ||
		sum.RequestSize != int64(len(`{"t":"dev-secret"}`)) || sum.ResponseSize == 0 {
		t.Errorf("summary = %+v", sum)
	}
	if want := srv.URL + "/items?key=" + httpclient.Mask + "&tag=dev-tag"; sum.URL != want {
		t.Errorf("url = %q, want %q", sum.URL, want)
	}
	e, err := st.Get(sum.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Request.Body.Text != `{"t":"`+httpclient.Mask+`"}` || e.Request.Proto != "HTTP/1.1" {
		t.Errorf("request = %+v", e.Request)
	}
	headers := map[string]string{}
	for _, h := range e.Request.Headers {
		headers[h.Name] = h.Value
	}
	if headers["X-Token"] != httpclient.Mask || headers["X-Api-Key"] != httpclient.Mask || headers["Authorization"] != "Bearer "+httpclient.Mask {
		t.Errorf("request headers = %v", headers)
	}
	if e.Response == nil || e.Response.StatusCode != 201 || e.Response.ContentType != "text/plain" || !strings.HasPrefix(e.Response.Body.Text, "got key="+httpclient.Mask) {
		t.Fatalf("response = %+v", e.Response)
	}
	// The final hop's phases and addresses, for the HAR export: a new
	// plain connection to an IP address has no DNS and no TLS.
	if tm := e.Response.Timings; tm == nil || tm.DNS != -1 || tm.TLS != -1 || tm.Connect < 0 || tm.Wait < 0 || !testenv.Measured(tm.Total) ||
		!strings.HasPrefix(e.Response.RemoteAddr, "127.0.0.1:") || e.Response.LocalAddr == "" {
		t.Errorf("timings = %+v, addresses %q %q", e.Response.Timings, e.Response.RemoteAddr, e.Response.LocalAddr)
	}

	// NAV-03: the environment and the request as the editor had it, the
	// references as written, a secret typed in masked.
	if sum.Environment != "dev" {
		t.Errorf("environment = %q", sum.Environment)
	}
	tpl := e.Template
	if tpl == nil || tpl.URL != "{{base}}/items?key={{token}}&tag={{tag}}" || tpl.Method != "POST" || tpl.Body == nil || tpl.Body.Text != `{"t":"{{token}}"}` ||
		len(tpl.Headers) != 2 || tpl.Headers[0].Value != "{{token}}" || tpl.Headers[1].Value != "Bearer "+httpclient.Mask {
		t.Errorf("template = %+v", tpl)
	}
	restored := history.Restore(e, nil)
	if !restored.Templated || restored.URL != tpl.URL || restored.Headers[1].Value != "Bearer " {
		t.Errorf("restored = %+v", restored)
	}

	files := historyFiles(t, st)
	for _, secret := range []string{"dev-secret", "k3y-value", "plain-bearer"} {
		if strings.Contains(files, secret) {
			t.Errorf("the history files hold %q:\n%s", secret, files)
		}
	}
}

// Failed and cancelled sends are recorded too, with the error.
func TestSendRecordsFailures(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	st, opt := historyStore(t)
	src := httpSource("http://user:pa55@" + addr + "/x")
	src.Request.Headers = []projectfile.KeyValue{{Name: "Authorization", Value: "Bearer tok"}, {Name: "X-Off", Value: "no", Disabled: true}}
	f := newFixture(t, src, nil, opt)
	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusError {
		t.Fatalf("done = %+v", done)
	}
	sum := waitEntries(t, st, 1)[0]
	if sum.Outcome != history.OutcomeError || sum.ErrorCode == "" || sum.Error == "" || sum.StatusCode != 0 || sum.URL != "http://"+addr+"/x" {
		t.Errorf("failure = %+v", sum)
	}
	e, err := st.Get(sum.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Response != nil || len(e.Request.Headers) != 1 || e.Request.Headers[0].Value != "Bearer "+httpclient.Mask {
		t.Errorf("failed entry = %+v", e)
	}
	if files := historyFiles(t, st); strings.Contains(files, "pa55") || strings.Contains(files, "Bearer tok") {
		t.Errorf("credentials in the history files:\n%s", files)
	}

	slow := slowServer(t)
	f.src = httpSource(slow.URL)
	id = f.send(t)
	time.Sleep(50 * time.Millisecond)
	if !f.ops.Cancel(id) {
		t.Fatal("Cancel = false")
	}
	f.rec.wait(t, id)
	if sum := waitEntries(t, st, 2)[0]; sum.Outcome != history.OutcomeCancelled {
		t.Errorf("cancelled = %+v", sum)
	}
}

// A replayed webhook request is recorded as a replay.
func TestSendPreparedRecordsHistory(t *testing.T) {
	t.Parallel()
	srv := echoServer(t)
	st, opt := historyStore(t)
	f := newFixture(t, httpSource(srv.URL), nil, opt)
	started, err := SendPrepared(f.svc, httpclient.Request{Method: "PUT", URL: srv.URL + "/hook", Body: httpclient.RawBody{Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.wait(t, started.OpID)
	sum := waitEntries(t, st, 1)[0]
	if sum.Kind != history.KindReplay || sum.RequestID != "" || sum.Method != "PUT" || sum.StatusCode != 200 || sum.RequestSize != 1 {
		t.Errorf("replay = %+v", sum)
	}
}
