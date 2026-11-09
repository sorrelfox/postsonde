package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/incremental"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// partGap is the pause the test servers make before each part after the
// first: the parts' times must be at least this far apart.
const partGap = 20 * time.Millisecond

// waitItems waits until the responses received for operation id satisfy
// ok, and returns them.
func (r *recorder) waitItems(t *testing.T, id string, ok func([]ResponseDTO) bool) []ResponseDTO {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		r.mu.Lock()
		items := append([]ResponseDTO(nil), r.items[id]...)
		r.mu.Unlock()
		if ok(items) {
			return items
		}
		select {
		case <-r.itemAdded:
		case <-deadline:
			t.Fatalf("operation %s: responses so far %d", id, len(items))
		}
	}
}

// incrementalServer streams payloads one at a time: after writing one it
// waits until the test lets it go on (next), so the test sees each part
// before the next one exists. sse sends an event stream instead of
// multipart/mixed.
func incrementalServer(t *testing.T, payloads []string, sse bool, next <-chan struct{}, accept chan<- string) *httptest.Server {
	return httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		accept <- r.Header.Get("Accept")
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", `multipart/mixed; boundary="-"; deferSpec=20220824`)
		}
		w.WriteHeader(200)
		fl := w.(nethttp.Flusher)
		for i, p := range payloads {
			if i > 0 {
				select {
				case <-next:
				case <-r.Context().Done():
					return
				}
				time.Sleep(partGap)
			}
			if sse {
				fmt.Fprintf(w, "event: next\ndata: %s\n\n", p)
			} else {
				fmt.Fprintf(w, "\r\n---\r\nContent-Type: application/json; charset=utf-8\r\n\r\n%s", p)
				// The delimiter after the part ends it, as servers send it.
				fmt.Fprint(w, "\r\n---")
			}
			fl.Flush()
		}
		if sse {
			fmt.Fprint(w, "event: complete\ndata:\n\n")
		} else {
			fmt.Fprint(w, "--\r\n")
		}
	}))
}

func compactText(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return b.String()
}

func TestSendGraphQLIncremental(t *testing.T) {
	t.Parallel()
	older := []string{
		`{"data":{"user":{"id":"1","friends":[{"name":"Ada"}]}},"hasNext":true}`,
		`{"incremental":[{"data":{"email":"a@example.com"},"path":["user"],"label":"Details"}],"hasNext":true}`,
		`{"incremental":[{"items":[{"name":"Bo"}],"path":["user","friends",1]}],"hasNext":true}`,
		`{"incremental":[{"items":[{"name":"Cy"}],"path":["user","friends",2]}],"hasNext":false}`,
	}
	newer := []string{
		`{"data":{"user":{"id":"1","friends":[{"name":"Ada"}]}},"pending":[{"id":"0","path":["user"],"label":"Details"},{"id":"1","path":["user","friends"]}],"hasNext":true}`,
		`{"incremental":[{"id":"0","data":{"email":"a@example.com"}}],"completed":[{"id":"0"}],"hasNext":true}`,
		`{"incremental":[{"id":"1","items":[{"name":"Bo"}]}],"hasNext":true}`,
		`{"incremental":[{"id":"1","items":[{"name":"Cy"}]}],"completed":[{"id":"1"}],"hasNext":false}`,
	}
	merged := `{"data":{"user":{"id":"1","friends":[{"name":"Ada"},{"name":"Bo"},{"name":"Cy"}],"email":"a@example.com"}}}`
	for _, tc := range []struct {
		name     string
		payloads []string
		sse      bool
		format   string
	}{
		{"older multipart", older, false, "multipart"},
		{"newer multipart", newer, false, "multipart"},
		{"newer event stream", newer, true, "event-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			next := make(chan struct{})
			accept := make(chan string, 1)
			srv := incrementalServer(t, tc.payloads, tc.sse, next, accept)
			defer srv.Close()
			src := collection.HTTPSource{
				GraphQL: &projectfile.GraphQLRequest{
					ID:    "req_1",
					URL:   srv.URL,
					Query: `{ user { id ... @defer(label: "Details") { email } friends @stream(initialCount: 1) { name } } }`,
				},
				Auth: projectfile.Auth{Type: projectfile.AuthNone},
			}
			f := newFixture(t, src, nil)
			id := f.send(t)
			if a := <-accept; a != request.GraphQLIncrementalAccept {
				t.Errorf("Accept %q", a)
			}

			// Each part is shown before the server sends the next one.
			for n := 1; n <= len(tc.payloads); n++ {
				items := f.rec.waitItems(t, id, func(items []ResponseDTO) bool {
					if len(items) == 0 {
						return false
					}
					last := items[len(items)-1]
					return last.Incremental != nil && last.Incremental.PartCount >= n
				})
				last := items[len(items)-1]
				inc := last.Incremental
				if n < len(tc.payloads) {
					if !inc.Receiving || inc.PartCount != n || inc.Completed || last.StatusCode != 200 {
						t.Fatalf("partial response %d: %+v", n, inc)
					}
					if n == 1 && compactText(t, inc.Merged) != `{"data":{"user":{"id":"1","friends":[{"name":"Ada"}]}}}` {
						t.Errorf("merged after the first part: %s", inc.Merged)
					}
					next <- struct{}{}
				}
			}
			if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
				t.Fatalf("done = %+v", done)
			}
			f.rec.mu.Lock()
			items := f.rec.items[id]
			f.rec.mu.Unlock()
			final := items[len(items)-1]
			for _, it := range items[:len(items)-1] {
				if it.Incremental == nil || !it.Incremental.Receiving {
					t.Errorf("an earlier response is not partial: %+v", it.Incremental)
				}
			}
			inc := final.Incremental
			if inc == nil || inc.Receiving || !inc.Completed || inc.HasNext || inc.Format != tc.format || inc.PartCount != len(tc.payloads) || inc.Pending != 0 {
				t.Fatalf("final incremental %+v", inc)
			}
			if got := compactText(t, inc.Merged); got != merged {
				t.Errorf("merged\n got %s\nwant %s", got, merged)
			}
			for i, p := range inc.Parts {
				if compactText(t, p.Text) != tc.payloads[i] || p.Index != i || p.Size != len(tc.payloads[i]) {
					t.Errorf("part %d: %+v", i, p)
				}
				if i > 0 && p.AtMs-inc.Parts[i-1].AtMs < float64(partGap.Milliseconds()) {
					t.Errorf("part %d at %.1f ms, part %d at %.1f ms", i, p.AtMs, i-1, inc.Parts[i-1].AtMs)
				}
				if p.HasNext == nil || *p.HasNext != (i < len(tc.payloads)-1) {
					t.Errorf("part %d hasNext %v", i, p.HasNext)
				}
			}
			kinds := func(p IncrementalPartDTO) string {
				var k []string
				for _, e := range p.Entries {
					k = append(k, e.Kind+":"+e.Path+":"+e.Label)
				}
				return strings.Join(k, " ")
			}
			if got := kinds(inc.Parts[1]); !strings.HasPrefix(got, "defer:user:Details") {
				t.Errorf("second part's entries %q", got)
			}
			if got := kinds(inc.Parts[2]); !strings.HasPrefix(got, "stream:user.friends") {
				t.Errorf("third part's entries %q", got)
			}
			// The body as received stays for Raw.
			if !strings.Contains(final.Body, `"hasNext":false`) || final.BodyID == "" && f.svc.bodies != nil {
				t.Errorf("final body %q", final.Body)
			}
		})
	}
}

// A GraphQL response of another type, and an HTTP request answered with
// multipart/mixed, get no incremental view; a plain query keeps the
// usual Accept.
func TestSendNotIncremental(t *testing.T) {
	t.Parallel()
	accept := make(chan string, 1)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		accept <- r.Header.Get("Accept")
		w.Header().Set("Content-Type", "multipart/mixed")
		fmt.Fprint(w, "---\r\n\r\n{\"data\":{}}\r\n-----\r\n")
	}))
	defer srv.Close()
	f := newFixture(t, collection.HTTPSource{
		Request: projectfile.HTTPRequest{ID: "req_1", Method: "GET", URL: srv.URL},
		Auth:    projectfile.Auth{Type: projectfile.AuthNone},
	}, nil)
	id := f.send(t)
	<-accept
	f.rec.wait(t, id)
	if r := f.rec.response(t, id); r.Incremental != nil {
		t.Errorf("an HTTP request got %+v", r.Incremental)
	}

	g := newFixture(t, collection.HTTPSource{
		GraphQL: &projectfile.GraphQLRequest{ID: "req_1", URL: srv.URL, Query: "{ a }"},
		Auth:    projectfile.Auth{Type: projectfile.AuthNone},
	}, nil)
	id = g.send(t)
	if a := <-accept; a != request.GraphQLAccept {
		t.Errorf("Accept %q", a)
	}
	g.rec.wait(t, id)
	// The server answered multipart anyway: it is read.
	items := g.rec.waitItems(t, id, func(items []ResponseDTO) bool { return len(items) > 0 })
	if r := items[len(items)-1]; r.Incremental == nil || r.Incremental.Receiving || r.Incremental.PartCount != 1 || r.Incremental.Completed {
		t.Errorf("incremental %+v", r.Incremental)
	}
}

// A query set to run over SSE (GQL-32, GQL-35) is sent asking for an event
// stream and its answer read like an incremental one: a normal response.
func TestSendGraphQLOverSSE(t *testing.T) {
	t.Parallel()
	accept := make(chan string, 1)
	srv := incrementalServer(t, []string{`{"data":{"a":1}}`}, true, nil, accept)
	defer srv.Close()
	f := newFixture(t, collection.HTTPSource{
		GraphQL: &projectfile.GraphQLRequest{
			ID: "req_1", URL: srv.URL, Query: "query Q { a }",
			Settings: projectfile.RequestSettings{GraphQLTransport: &projectfile.GraphQLTransport{Query: projectfile.GraphQLTransportSSE}},
		},
		Auth: projectfile.Auth{Type: projectfile.AuthNone},
	}, nil)
	id := f.send(t)
	if a := <-accept; a != request.EventStreamAccept {
		t.Errorf("Accept %q", a)
	}
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	items := f.rec.waitItems(t, id, func(items []ResponseDTO) bool { return len(items) > 0 })
	r := items[len(items)-1]
	if r.StatusCode != 200 || r.Incremental == nil || r.Incremental.Receiving || !r.Incremental.Completed || r.Incremental.PartCount != 1 || compactText(t, r.Incremental.Merged) != `{"data":{"a":1}}` {
		t.Errorf("response %d %+v", r.StatusCode, r.Incremental)
	}

	// An unknown transport fails the send.
	g := newFixture(t, collection.HTTPSource{
		GraphQL: &projectfile.GraphQLRequest{
			ID: "req_1", URL: srv.URL, Query: "query Q { a }",
			Settings: projectfile.RequestSettings{GraphQLTransport: &projectfile.GraphQLTransport{Query: "pigeon"}},
		},
		Auth: projectfile.Auth{Type: projectfile.AuthNone},
	}, nil)
	_, err := g.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != request.CodeGraphQLInvalidTransport {
		t.Errorf("Send = %v", err)
	}
}

// The JSON shape of IncrementalDTO, pinned.
func TestIncrementalDTOJSON(t *testing.T) {
	t.Parallel()
	c := incremental.NewCollector("multipart/mixed", time.Now(), nil)
	c.Write([]byte("---\r\n\r\n{\"data\":{\"a\":{}},\"pending\":[{\"id\":\"0\",\"path\":[\"a\"],\"label\":\"L\"}],\"hasNext\":true}\r\n---\r\n\r\n" +
		"{\"incremental\":[{\"id\":\"0\",\"data\":{\"b\":1}}],\"completed\":[{\"id\":\"0\"}],\"hasNext\":false}\r\n---\r\n\r\nnot json\r\n-----\r\n"))
	dto := toIncrementalDTO(c.Snapshot(), false)
	for i := range dto.Parts {
		dto.Parts[i].AtMs = float64(i) * 12.5
	}
	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "dto/incremental", append(data, '\n'))
}

// A poke inside the interval is delivered at its end, once; none after
// stop.
func TestThrottle(t *testing.T) {
	t.Parallel()
	calls := make(chan time.Time, 10)
	th := &throttle{interval: 30 * time.Millisecond, fn: func() { calls <- time.Now() }}
	th.poke()
	first := <-calls
	th.poke()
	th.poke()
	second := <-calls
	if second.Sub(first) < 30*time.Millisecond {
		t.Errorf("second call after %v", second.Sub(first))
	}
	th.stop()

	stopped := &throttle{interval: time.Hour, fn: func() { calls <- time.Now() }}
	stopped.poke()
	stopped.poke() // waits for the hour
	stopped.stop()
	stopped.poke()
	if n := len(calls); n != 1 {
		t.Errorf("%d calls, want the first only", n)
	}
}

// Cancelling while parts arrive ends the send as cancelled, and no
// partial response follows the done event.
func TestSendIncrementalCancelled(t *testing.T) {
	t.Parallel()
	next := make(chan struct{})
	accept := make(chan string, 1)
	srv := incrementalServer(t, []string{`{"data":{"a":1},"hasNext":true}`, `{"hasNext":false}`}, false, next, accept)
	defer srv.Close()
	f := newFixture(t, collection.HTTPSource{
		GraphQL: &projectfile.GraphQLRequest{ID: "req_1", URL: srv.URL, Query: "{ a ... @defer { b } }"},
		Auth:    projectfile.Auth{Type: projectfile.AuthNone},
	}, nil)
	id := f.send(t)
	<-accept
	f.rec.waitItems(t, id, func(items []ResponseDTO) bool { return len(items) == 1 })
	f.ops.Cancel(id)
	if done := f.rec.wait(t, id); done.Status != operation.StatusCancelled {
		t.Fatalf("done = %+v", done)
	}
	f.rec.mu.Lock()
	n := len(f.rec.items[id])
	f.rec.mu.Unlock()
	if n != 1 {
		t.Errorf("%d responses", n)
	}
}
