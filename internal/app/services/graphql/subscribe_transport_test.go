package graphql

import (
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/transportsse"
	"github.com/sorrelfox/postsonde/internal/core/graphql/transportws"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// The legacy subscriptions-transport-ws protocol (GQL-31), GraphQL over
// SSE (GQL-32) and the per-operation transport (GQL-35), end to end
// through the HTTP service as in subscribe_test.go.

// withTransport is eventsFile (or f) with the transport of op picked.
func withTransport(f projectfile.GraphQLRequest, query, mutation, subscription string) projectfile.GraphQLRequest {
	f.Settings.GraphQLTransport = &projectfile.GraphQLTransport{Query: query, Mutation: mutation, Subscription: subscription}
	return f
}

func legacyFile() projectfile.GraphQLRequest {
	f := eventsFile()
	f.Extensions = `{"trace": "{{token}}"}`
	return withTransport(f, "", "", projectfile.GraphQLTransportLegacyWS)
}

func findEvent(evs []SubscriptionEventDTO, kind, typ string) *SubscriptionEventDTO {
	for i := range evs {
		if evs[i].Kind == kind && (typ == "" || evs[i].Type == typ) {
			return &evs[i]
		}
	}
	return nil
}

func TestSubscribeLegacy(t *testing.T) {
	var gotInit, gotStart, gotTerm gqlMsg
	var protocol string
	var closeFrame *wsframe.CloseError
	srvDone := make(chan struct{})
	srv := startGraphQLServerWith(t, transportws.LegacySubprotocol, func(p *gqlPeer) {
		defer close(srvDone)
		protocol = p.headers.Get("Sec-WebSocket-Protocol")
		gotInit = p.expect("connection_init")
		p.mustSend(`{"type":"connection_ack"}`)
		gotStart = p.expect("start")
		p.mustSend(`{"type":"ka"}`)
		id := gotStart.ID
		p.mustSend(`{"type":"data","id":"` + id + `","payload":{"data":{"events":{"n":1}}}}`)
		p.mustSend(`{"type":"ka"}`)
		p.mustSend(`{"type":"data","id":"` + id + `","payload":{"data":null,"errors":[{"message":"a"}]}}`)
		p.mustSend(`{"type":"complete","id":"` + id + `"}`)
		gotTerm = p.expect("connection_terminate")
		closeFrame = p.drain()
	})
	f := newSubFixture(t, srv.URL, legacyFile())
	id := f.subscribe(t)
	done := f.rec.waitDone(t, id)
	<-srvDone
	if done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if protocol != transportws.LegacySubprotocol {
		t.Errorf("offered %q", protocol)
	}
	if string(gotInit.Payload) != `{"authToken":"tok"}` {
		t.Errorf("init payload %s", gotInit.Payload)
	}
	want := `{"query":"` + eventsQuery + `","operationName":"Events","variables":{"room":"tok-room"},"extensions":{"trace":"tok"}}`
	if string(gotStart.Payload) != want {
		t.Errorf("start payload %s\nwant %s", gotStart.Payload, want)
	}
	if gotTerm.raw != `{"type":"connection_terminate"}` || closeFrame == nil || closeFrame.Code != wsframe.CloseNormalClosure {
		t.Errorf("terminate %q, close %+v", gotTerm.raw, closeFrame)
	}
	evs := f.rec.events(id)
	wantEvs := "connecting open sent:connection_init received:connection_ack sent:start#1 received:ka received:data#1 received:ka " +
		"received:data#1 received:complete#1 sent:connection_terminate closed:1000"
	if got := summary(evs); got != wantEvs {
		t.Fatalf("events:\n got %s\nwant %s", got, wantEvs)
	}
	if evs[0].Transport != transportws.LegacySubprotocol || evs[0].OperationType != "subscription" || evs[1].Subprotocol != transportws.LegacySubprotocol {
		t.Errorf("connecting %+v, open %+v", evs[0], evs[1])
	}
	if evs[8].ErrorCount != 1 || evs[6].ErrorCount != 0 {
		t.Errorf("error counts %d %d", evs[6].ErrorCount, evs[8].ErrorCount)
	}
	if findEvent(evs, EventKindResult, "") != nil {
		t.Error("a subscription got a result item")
	}
}

// Stop sends stop, then connection_terminate, and closes with 1000.
func TestSubscribeLegacyStop(t *testing.T) {
	seen := make(chan []string, 1)
	srv := startGraphQLServerWith(t, transportws.LegacySubprotocol, func(p *gqlPeer) {
		p.expect("connection_init")
		p.mustSend(`{"type":"connection_ack"}`)
		id := p.expect("start").ID
		p.mustSend(`{"type":"data","id":"` + id + `","payload":{"data":{"n":1}}}`)
		var types []string
		for {
			m, err := p.tryRead()
			if err != nil {
				break
			}
			types = append(types, m.raw)
		}
		if cf := p.conn.PeerClose(); cf != nil {
			types = append(types, "close:"+cf.Code.String())
		}
		seen <- types
	})
	f := newSubFixture(t, srv.URL, legacyFile())
	id := f.subscribe(t)
	f.rec.waitFor(t, id, func(evs []SubscriptionEventDTO) bool { return findEvent(evs, EventKindMessage, "data") != nil })
	if ok, err := f.svc.StopSubscription(t.Context(), StopSubscriptionInputDTO{OpID: id}); !ok || err != nil {
		t.Fatalf("StopSubscription = %v, %v", ok, err)
	}
	if done := f.rec.waitDone(t, id); done.Status != operation.StatusCancelled {
		t.Errorf("done = %+v", done)
	}
	select {
	case got := <-seen:
		want := []string{`{"type":"stop","id":"1"}`, `{"type":"connection_terminate"}`, "close:1000 (normal closure)"}
		if strings.Join(got, " | ") != strings.Join(want, " | ") {
			t.Errorf("server saw %q\nwant %q", got, want)
		}
	case <-time.After(subWait):
		t.Fatal("the server did not see the end")
	}
	if s := summary(f.rec.events(id)); !strings.HasSuffix(s, "sent:stop#1 sent:connection_terminate closed:1000") {
		t.Errorf("events %s", s)
	}
}

// An error message (an object in this protocol) fails the operation; a
// connection_error fails the connection.
func TestSubscribeLegacyErrors(t *testing.T) {
	srv := startGraphQLServerWith(t, transportws.LegacySubprotocol, func(p *gqlPeer) {
		p.expect("connection_init")
		p.mustSend(`{"type":"connection_ack"}`)
		id := p.expect("start").ID
		p.mustSend(`{"type":"error","id":"` + id + `","payload":{"message":"Cannot query field \"x\""}}`)
		p.drain()
	})
	f := newSubFixture(t, srv.URL, legacyFile())
	id := f.subscribe(t)
	done := f.rec.waitDone(t, id)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != CodeSubscriptionError || done.Error.Details["count"] != "1" {
		t.Fatalf("done = %+v", done.Error)
	}
	if ev := findEvent(f.rec.events(id), EventKindMessage, "error"); ev == nil || ev.ErrorCount != 1 {
		t.Errorf("error event %+v", ev)
	}

	srv = startGraphQLServerWith(t, transportws.LegacySubprotocol, func(p *gqlPeer) {
		p.expect("connection_init")
		p.mustSend(`{"type":"connection_error","payload":{"message":"Bad token"}}`)
		p.drain()
	})
	f = newSubFixture(t, srv.URL, legacyFile())
	id = f.subscribe(t)
	done = f.rec.waitDone(t, id)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != transportws.CodeConnectionError || done.Error.Kind != string(errs.PermissionDenied) {
		t.Fatalf("done = %+v", done.Error)
	}
	evs := f.rec.events(id)
	if ev := findEvent(evs, EventKindMessage, "connection_error"); ev == nil || ev.Error == nil || ev.Payload != `{"message":"Bad token"}` {
		t.Errorf("connection_error event %+v; events %s", ev, summary(evs))
	}
}

// sseServer serves GraphQL over SSE with handler, recording the request.
type sseRequest struct {
	method, accept, contentType, auth string
	body                              string
}

func startSSEServer(t *testing.T, handler func(w nethttp.ResponseWriter, r *nethttp.Request)) (*httptest.Server, chan sseRequest) {
	t.Helper()
	reqs := make(chan sseRequest, 4)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		body, _ := io.ReadAll(r.Body)
		reqs <- sseRequest{r.Method, r.Header.Get("Accept"), r.Header.Get("Content-Type"), r.Header.Get("Authorization"), string(body)}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, reqs
}

func writeEvents(w nethttp.ResponseWriter, s string) {
	_, _ = io.WriteString(w, s)
	w.(nethttp.Flusher).Flush()
}

func sseFile() projectfile.GraphQLRequest {
	f := eventsFile()
	f.Extensions = `{"trace": "{{token}}"}`
	return withTransport(f, "", "", projectfile.GraphQLTransportSSE)
}

func TestSubscribeSSE(t *testing.T) {
	srv, reqs := startSSEServer(t, func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeEvents(w, ":\n\n")
		writeEvents(w, "event: next\ndata: {\"data\":{\"events\":{\"n\":1}}}\n\n")
		writeEvents(w, "event: next\ndata: {\"data\":null,\"errors\":[{\"message\":\"a\"},{\"message\":\"b\"}]}\n\n: keep\n\n")
		writeEvents(w, "event: complete\ndata:\n\n")
	})
	f := newSubFixture(t, srv.URL, sseFile())
	id := f.subscribe(t)
	done := f.rec.waitDone(t, id)
	if done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	req := <-reqs
	wantBody := `{"query":"` + eventsQuery + `","variables":{"room":"tok-room"},"operationName":"Events","extensions":{"trace":"tok"}}`
	if req.method != "POST" || req.accept != request.EventStreamAccept || req.contentType != "application/json" || req.auth != "Bearer tok" || req.body != wantBody {
		t.Errorf("request %+v\nwant body %s", req, wantBody)
	}
	evs := f.rec.events(id)
	want := "connecting sent:request open received:keep-alive received:next received:next received:keep-alive received:complete closed"
	if got := summary(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if evs[0].Transport != TransportNameSSE || evs[0].Method != "POST" || !strings.HasPrefix(evs[0].URL, "http://127.0.0.1:") {
		t.Errorf("connecting %+v", evs[0])
	}
	if evs[1].Payload != wantBody || evs[1].Size != len(wantBody) {
		t.Errorf("request event %+v", evs[1])
	}
	if evs[2].Status != 200 || evs[2].ContentType != "text/event-stream" {
		t.Errorf("open %+v", evs[2])
	}
	if evs[4].Payload != `{"data":{"events":{"n":1}}}` || evs[5].ErrorCount != 2 || evs[6].Payload != " keep" {
		t.Errorf("events %+v", evs[4:7])
	}
	if last := evs[len(evs)-1]; last.Error != nil || last.Close != nil {
		t.Errorf("closed %+v", last)
	}
}

// Stop cancels the request: the server sees it end.
func TestSubscribeSSEStop(t *testing.T) {
	ended := make(chan struct{})
	srv, _ := startSSEServer(t, func(w nethttp.ResponseWriter, r *nethttp.Request) {
		defer close(ended)
		w.Header().Set("Content-Type", "text/event-stream")
		writeEvents(w, "event: next\ndata: {\"data\":{\"n\":1}}\n\n")
		<-r.Context().Done()
	})
	f := newSubFixture(t, srv.URL, sseFile())
	id := f.subscribe(t)
	f.rec.waitFor(t, id, func(evs []SubscriptionEventDTO) bool { return findEvent(evs, EventKindMessage, "next") != nil })
	if ok, err := f.svc.StopSubscription(t.Context(), StopSubscriptionInputDTO{OpID: id}); !ok || err != nil {
		t.Fatalf("StopSubscription = %v, %v", ok, err)
	}
	if done := f.rec.waitDone(t, id); done.Status != operation.StatusCancelled {
		t.Errorf("done = %+v", done)
	}
	select {
	case <-ended:
	case <-time.After(subWait):
		t.Fatal("the server did not see the request end")
	}
	evs := f.rec.events(id)
	if s := summary(evs); s != "connecting sent:request open received:next closed" {
		t.Errorf("events %s", s)
	}
	if last := evs[len(evs)-1]; last.Error != nil {
		t.Errorf("closed with error %+v", last.Error)
	}
}

// Errors: a request error answered with JSON, a stream that ends without
// complete, an invalid next event, an HTTP error.
func TestSubscribeSSEErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler func(w nethttp.ResponseWriter)
		code    string
		events  string
	}{
		{"validation", func(w nethttp.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"errors":[{"message":"Cannot query field \"x\""}]}`)
		}, CodeSubscriptionError, "connecting sent:request open received:response closed"},
		{"ended", func(w nethttp.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			writeEvents(w, "event: next\ndata: {\"data\":{}}\n\n")
		}, transportsse.CodeStreamEnded, "connecting sent:request open received:next closed"},
		{"invalid", func(w nethttp.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			writeEvents(w, "event: next\ndata: nope\n\n")
		}, transportsse.CodeInvalidEvent, "connecting sent:request open received:next closed"},
		{"forbidden", func(w nethttp.ResponseWriter) {
			w.WriteHeader(403)
		}, transportsse.CodeHTTPStatus, "connecting sent:request open received:response closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := startSSEServer(t, func(w nethttp.ResponseWriter, _ *nethttp.Request) { tc.handler(w) })
			f := newSubFixture(t, srv.URL, sseFile())
			id := f.subscribe(t)
			done := f.rec.waitDone(t, id)
			if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != tc.code {
				t.Fatalf("done = %+v", done.Error)
			}
			evs := f.rec.events(id)
			if s := summary(evs); s != tc.events {
				t.Errorf("events %s, want %s", s, tc.events)
			}
			if last := evs[len(evs)-1]; last.Error == nil || last.Error.Code != tc.code {
				t.Errorf("closed %+v", last.Error)
			}
		})
	}
}

// A query set to run over WebSocket or SSE ends with its result (GQL-35);
// one that runs over HTTP is refused.
func TestSubscribeQueryTransports(t *testing.T) {
	const query = "query Q { a b }"
	t.Run("ws", func(t *testing.T) {
		srv := startGraphQLServer(t, func(p *gqlPeer) {
			p.expect("connection_init")
			p.mustSend(`{"type":"connection_ack"}`)
			sub := p.expect("subscribe")
			if !strings.Contains(string(sub.Payload), `"query":"query Q { a b }"`) {
				t.Errorf("subscribe %s", sub.raw)
			}
			p.mustSend(`{"type":"next","id":"1","payload":{"data":{"a":1,"b":2}}}`)
			p.mustSend(`{"type":"complete","id":"1"}`)
			p.drain()
		})
		file := withTransport(projectfile.GraphQLRequest{URL: "{{base}}/graphql", Query: query}, projectfile.GraphQLTransportWS, "", "")
		f := newSubFixture(t, srv.URL, file)
		id := f.subscribe(t)
		if done := f.rec.waitDone(t, id); done.Status != operation.StatusOK {
			t.Fatalf("done = %+v", done)
		}
		evs := f.rec.events(id)
		if s := summary(evs); !strings.HasSuffix(s, "received:next#1 received:complete#1 result closed:1000") {
			t.Errorf("events %s", s)
		}
		res := findEvent(evs, EventKindResult, "")
		var got any
		if res == nil || json.Unmarshal([]byte(res.Payload), &got) != nil || evs[0].OperationType != "query" {
			t.Fatalf("result %+v", res)
		}
		if b, _ := json.Marshal(got); string(b) != `{"data":{"a":1,"b":2}}` {
			t.Errorf("result %s", b)
		}
	})
	t.Run("sse", func(t *testing.T) {
		srv, _ := startSSEServer(t, func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			// @defer over SSE: the parts are merged into the result.
			writeEvents(w, "event: next\ndata: {\"data\":{\"a\":1},\"hasNext\":true}\n\n")
			writeEvents(w, "event: next\ndata: {\"incremental\":[{\"path\":[],\"data\":{\"b\":2}}],\"hasNext\":false}\n\n")
			writeEvents(w, "event: complete\n\n")
		})
		file := withTransport(projectfile.GraphQLRequest{URL: "{{base}}/graphql", Query: "mutation M { a }"}, "", projectfile.GraphQLTransportSSE, "")
		f := newSubFixture(t, srv.URL, file)
		id := f.subscribe(t)
		if done := f.rec.waitDone(t, id); done.Status != operation.StatusOK {
			t.Fatalf("done = %+v", done)
		}
		res := findEvent(f.rec.events(id), EventKindResult, "")
		var got any
		if res == nil || json.Unmarshal([]byte(res.Payload), &got) != nil {
			t.Fatalf("result %+v", res)
		}
		if b, _ := json.Marshal(got); string(b) != `{"data":{"a":1,"b":2}}` {
			t.Errorf("result %s", b)
		}
	})
	t.Run("http", func(t *testing.T) {
		f := newSubFixture(t, "http://127.0.0.1:1", projectfile.GraphQLRequest{URL: "{{base}}/graphql", Query: query})
		_, err := f.svc.Subscribe(t.Context(), SubscribeInputDTO{RequestID: "gql"})
		if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != CodeRunsOverHTTP {
			t.Errorf("Subscribe = %v", err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		f := newSubFixture(t, "http://127.0.0.1:1", withTransport(eventsFile(), "", "", "carrier-pigeon"))
		_, err := f.svc.Subscribe(t.Context(), SubscribeInputDTO{RequestID: "gql"})
		if e, ok := errors.AsType[*errs.Error](err); !ok || e.Code != request.CodeGraphQLInvalidTransport || e.Details["field"] != "settings.graphqlTransport.subscription" {
			t.Errorf("Subscribe = %v", err)
		}
	})
}
