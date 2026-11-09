package transportsse

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

type recorder struct {
	mu   sync.Mutex
	list []Event
}

func (r *recorder) add(ev Event) {
	r.mu.Lock()
	r.list = append(r.list, ev)
	r.mu.Unlock()
}

// summary is "next keep-alive complete ...", with notes and errors.
func (r *recorder) summary() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var parts []string
	for _, ev := range r.list {
		s := ev.Type
		if ev.Note != "" {
			s += "(" + string(ev.Note) + ")"
		}
		if ev.Err != nil {
			s += "(err)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.list...)
}

func code(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

// chunks yields each string as one Read.
type chunks []string

func (c *chunks) Read(p []byte) (int, error) {
	if len(*c) == 0 {
		return 0, io.EOF
	}
	n := copy(p, (*c)[0])
	(*c)[0] = (*c)[0][n:]
	if (*c)[0] == "" {
		*c = (*c)[1:]
	}
	return n, nil
}

func TestStream(t *testing.T) {
	body := chunks{
		": ping\n\n",
		"event: next\ndata: {\"data\":{\"n\":1}}\n\n:",
		" keep\n\nevent: next\ndata: {\"data\":null,\n",
		"data: \"errors\":[{\"message\":\"m\"}]}\n\n",
		"event: other\ndata: x\n\ndata: {\"data\":{\"n\":2}}\n\n",
		"event: complete\ndata:\n\nevent: next\ndata: {}\n\n",
	}
	rec := &recorder{}
	at := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	out := Read(Head{Status: 200, ContentType: "text/event-stream; charset=utf-8"}, &body, Options{OnEvent: rec.add, Now: func() time.Time { return at }})
	if out.Kind != OutcomeComplete || out.Err != nil {
		t.Fatalf("outcome %+v", out)
	}
	want := "keep-alive next keep-alive next other(ignored) next complete"
	if got := rec.summary(); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
	evs := rec.all()
	if string(evs[1].Payload) != `{"data":{"n":1}}` || evs[0].Data != " ping" || evs[2].Data != " keep" {
		t.Errorf("events %+v", evs)
	}
	if string(evs[3].Payload) != "{\"data\":null,\n\"errors\":[{\"message\":\"m\"}]}" {
		t.Errorf("multi-line data %q", evs[3].Payload)
	}
	if string(evs[5].Payload) != `{"data":{"n":2}}` || evs[5].Size != len(`{"data":{"n":2}}`) {
		t.Errorf("unnamed event %+v", evs[5])
	}
	for _, ev := range evs {
		if !ev.At.Equal(at) {
			t.Errorf("event time %v", ev.At)
		}
	}
}

// A complete event without a data line is never dispatched; it counts
// when the server ends the stream after it.
func TestCompleteWithoutData(t *testing.T) {
	for body, want := range map[string]OutcomeKind{
		"event: next\ndata: {}\n\nevent: complete\n\n":                          OutcomeComplete,
		"event: next\ndata: {}\n\nevent: complete\r\n\r\n":                      OutcomeComplete,
		"event: complete\n\nevent: next\ndata: {}\n\n":                          OutcomeFailed,
		"event: next\ndata: {}\n\nevent: complete\n\nevent: next\ndata: {}\n\n": OutcomeFailed,
	} {
		rec := &recorder{}
		out := Read(Head{Status: 200, ContentType: "text/event-stream"}, strings.NewReader(body), Options{OnEvent: rec.add})
		if out.Kind != want {
			t.Errorf("%q: outcome %+v, events %s", body, out, rec.summary())
		}
		if want == OutcomeComplete && rec.summary() != "next complete" {
			t.Errorf("%q: events %s", body, rec.summary())
		}
	}
}

func TestStreamFailures(t *testing.T) {
	cases := []struct {
		name, body, code, events string
	}{
		{"ended", "event: next\ndata: {}\n\n", CodeStreamEnded, "next"},
		{"not an object", "event: next\ndata: [1]\n\ndata: {}\n\n", CodeInvalidEvent, "next(err)"},
		{"not JSON", "event: next\ndata: {x\n\n", CodeInvalidEvent, "next(err)"},
		{"empty message", "data:\n\n", CodeStreamEnded, "message(ignored)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			out := Read(Head{Status: 200, ContentType: "text/event-stream"}, strings.NewReader(tc.body), Options{OnEvent: rec.add})
			if out.Kind != OutcomeFailed || code(out.Err) != tc.code {
				t.Errorf("outcome %+v", out)
			}
			if got := rec.summary(); got != tc.events {
				t.Errorf("events %s, want %s", got, tc.events)
			}
		})
	}

	boom := errors.New("boom")
	out := Read(Head{Status: 200, ContentType: "text/event-stream"}, io.MultiReader(strings.NewReader("data: {}\n\n"), errReader{boom}), Options{})
	if out.Kind != OutcomeFailed || !errors.Is(out.Err, boom) {
		t.Errorf("read error: %+v", out)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestResponse(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		kind        OutcomeKind
		code        string
		payload     bool
	}{
		{"validation errors", 400, "application/json", `{"errors":[{"message":"Cannot query field"}]}`, OutcomeRejected, "", true},
		{"errors with 200", 200, "application/graphql-response+json", ` {"data":null,"errors":[{"message":"x"}]} `, OutcomeRejected, "", true},
		{"plain result", 200, "application/json", `{"data":{"a":1}}`, OutcomeComplete, "", true},
		{"partial result", 200, "application/json", `{"data":{"a":null},"errors":[{"message":"x"}]}`, OutcomeComplete, "", true},
		{"unauthorized", 401, "text/plain", "no", OutcomeFailed, CodeHTTPStatus, false},
		{"not found JSON", 404, "application/json", `{"message":"nope"}`, OutcomeFailed, CodeHTTPStatus, true},
		{"event stream with 500", 500, "text/event-stream", "data: {}\n\n", OutcomeFailed, CodeHTTPStatus, false},
		{"html", 200, "text/html", "<html>", OutcomeFailed, CodeUnexpectedResponse, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			out := Read(Head{Status: tc.status, ContentType: tc.contentType}, strings.NewReader(tc.body), Options{OnEvent: rec.add})
			if out.Kind != tc.kind || code(out.Err) != tc.code {
				t.Errorf("outcome %+v", out)
			}
			if tc.kind == OutcomeRejected && !strings.HasPrefix(string(out.Errors), `[{"message"`) {
				t.Errorf("errors %s", out.Errors)
			}
			evs := rec.all()
			if len(evs) != 1 || evs[0].Type != TypeResponse || evs[0].Note != NoteNotEventStream || evs[0].Size != len(tc.body) || (evs[0].Payload != nil) != tc.payload {
				t.Errorf("events %+v", evs)
			}
		})
	}
	if out := Read(Head{Status: 401, ContentType: "text/plain"}, strings.NewReader(""), Options{}); !errors.Is(out.Err, &errs.Error{Kind: errs.PermissionDenied}) {
		t.Errorf("401: %v", out.Err)
	}
}

// TestHTTP streams from a server over HTTP: events arrive as they are
// written, and cancelling the request stops reading.
func TestHTTP(t *testing.T) {
	proceed := make(chan struct{})
	finished := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		if r.Method != http.MethodPost || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request %s %s", r.Method, r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ":\n\nevent: next\ndata: {\"data\":{\"n\":1}}\n\n")
		w.(http.Flusher).Flush()
		<-proceed
		_, _ = io.WriteString(w, "event: next\ndata: {\"data\":{\"n\":2}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(`{"query":"subscription { n }"}`))
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rec := &recorder{}
	nexts := 0
	out := Read(Head{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type")}, resp.Body, Options{OnEvent: func(ev Event) {
		rec.add(ev)
		if ev.Type != TypeNext {
			return
		}
		nexts++
		switch nexts {
		case 1:
			close(proceed)
		case 2:
			cancel() // Stop
		}
	}})
	if out.Kind != OutcomeFailed || !errors.Is(out.Err, context.Canceled) {
		t.Errorf("outcome %+v", out)
	}
	if got := rec.summary(); got != "keep-alive next next" {
		t.Errorf("events %s", got)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Error("the server did not see the request end")
	}
}
