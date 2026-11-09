// Package mockhttp is the HTTP mock server type (MOCK-01, MOCK-02,
// MOCK-04 to MOCK-07): it answers requests with hand-made routes, a
// method and a path pattern each, with a status, headers, a body and an
// optional delay, and other responses picked by rules on the request or by
// the X-PostSonde-Response header. A templated response's body and header
// values read the request ({{request.params.id}}) and dynamic values. Routes are matched with mockroute (the
// most specific pattern wins); a path no route matches gets 404, a path
// matched only for other methods 405 with Allow. The configuration can
// change while the server runs. See docs/servers.md and
// docs/mock-routes.md.
package mockhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/httpbase"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockroute"
)

// TypeName is the Spec.Type of the mock server.
const TypeName = "mock_http"

// Options change how the type waits, for tests.
type Options struct {
	// Sleep waits for d or until ctx is done, and returns ctx's error then.
	// nil uses a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// RandN returns a number in [0, n); nil is math/rand/v2.Int64N.
	RandN func(n int64) int64
}

// Type is the HTTP mock server type.
type Type struct {
	opts Options
}

// New returns the mock server type.
func New(opts Options) Type {
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	if opts.RandN == nil {
		opts.RandN = rand.Int64N
	}
	return Type{opts: opts}
}

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "Mock HTTP" }

// ALPN offers HTTP/1.1 over TLS (SRV-05); HTTP/2 is not served.
func (Type) ALPN() []string { return httpbase.ALPN() }

// NormalizeConfig takes a Config, a *Config or nil (DefaultConfig) and
// returns a normalized Config (Normalize).
func (Type) NormalizeConfig(config any) (any, error) {
	switch c := config.(type) {
	case nil:
		return DefaultConfig(), nil
	case Config:
		return Normalize(c)
	case *Config:
		if c == nil {
			return DefaultConfig(), nil
		}
		return Normalize(*c)
	}
	return nil, errs.New(errs.Internal, fmt.Sprintf("A mock server cannot use a %T configuration.", config)).
		WithCode(CodeInvalidConfig)
}

// Start serves the routes of spec.Config on ln.
func (t Type) Start(spec servers.Spec, ln net.Listener, sink servers.Sink) (servers.Instance, error) {
	cfg, err := t.NormalizeConfig(spec.Config)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	stopCtx, cancel := context.WithCancel(context.Background())
	inst := &instance{Instance: httpbase.New(sink), opts: t.opts, stopCtx: stopCtx, cancel: cancel}
	inst.Reconfigure(cfg)
	inst.Serve(ln, http.HandlerFunc(inst.serveHTTP))
	return inst, nil
}

type instance struct {
	*httpbase.Instance
	opts Options
	cfg  atomic.Pointer[compiled]

	// stopCtx is cancelled when Stop begins, so delayed responses end at
	// once instead of holding the graceful stop.
	stopCtx context.Context
	cancel  context.CancelFunc
	// reconfMu keeps the last Reconfigure call's configuration.
	reconfMu sync.Mutex
}

// Reconfigure swaps in the routes of config (a Config). Requests being
// answered keep the routes they matched.
func (i *instance) Reconfigure(config any) {
	c, ok := config.(Config)
	if !ok {
		return
	}
	i.reconfMu.Lock()
	defer i.reconfMu.Unlock()
	compiled, _ := compile(c)
	i.cfg.Store(compiled)
}

// Stop ends delayed responses, then stops like every HTTP server.
func (i *instance) Stop(ctx context.Context) error {
	i.cancel()
	return i.Instance.Stop(ctx)
}

// compiled is a configuration ready to serve.
type compiled struct {
	matcher *mockroute.Matcher
	// routes are the served routes, in the order given to the matcher
	// (Result.RouteIndex).
	routes []*servedRoute
}

// rawMediaType is the media type of a raw body language, "" for an unknown
// one.
func rawMediaType(typ string) string { return request.RawMediaType(typ) }

// contentType is the implicit Content-Type of a raw body: the request
// body's rule (request.RawMediaType), with charset=utf-8 on text/* types,
// which browsers would otherwise read in another charset.
func contentType(typ string) string {
	if typ == BodyTypeBase64 {
		return "application/octet-stream"
	}
	mt := rawMediaType(typ)
	if strings.HasPrefix(mt, "text/") {
		mt += "; charset=utf-8"
	}
	return mt
}

// bodyAllowed reports whether a response with status can have a body.
func bodyAllowed(status int) bool {
	return status != http.StatusNoContent && status != http.StatusNotModified && status >= 200
}

// delay picks how long to hold the response of r back.
func (i *instance) delay(d Delay) time.Duration {
	switch d.Mode {
	case DelayFixed:
		return time.Duration(d.Ms) * time.Millisecond
	case DelayRandom:
		span := int64(d.MaxMs - d.MinMs)
		return time.Duration(int64(d.MinMs)+i.opts.RandN(span+1)) * time.Millisecond
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// errorBody is the JSON body of the 404 and 405 responses, of the 400
// for an unknown X-PostSonde-Response and of the 500 for a template that
// cannot be rendered.
type errorBody struct {
	Error   string   `json:"error"`
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Allowed []string `json:"allowed,omitempty"`
	// Responses are the names X-PostSonde-Response takes on the route.
	Responses []string `json:"responses,omitempty"`
}

func (i *instance) serveHTTP(w http.ResponseWriter, r *http.Request) {
	c := i.cfg.Load()
	res := c.matcher.Match(r.Method, r.URL.EscapedPath())

	var match *servers.RouteMatch
	var route *servedRoute
	var params []Param
	if res.Matched {
		route = c.routes[res.RouteIndex]
		match = &servers.RouteMatch{
			ID:       route.route.ID,
			Method:   route.route.Method,
			Pattern:  route.route.Path,
			Wildcard: res.Wildcard,
		}
		for _, p := range res.Params {
			match.Params = append(match.Params, servers.Header{Name: p.Name, Value: p.Value})
			params = append(params, Param{Name: p.Name, Value: p.Value})
		}
	}
	readLimit := 0
	if route != nil && (len(route.alts) > 0 || route.templated) {
		readLimit = MaxMatchBodyBytes
	}
	peer, connID, reqBody := i.ReadRequestLimit(r, match, readLimit)

	var status int
	var body []byte
	var note string
	var delay time.Duration
	h := w.Header()
	var chosen *reply
	var selErr *selectError
	var header http.Header
	var renderErr error
	if route != nil {
		sel := strings.TrimSpace(r.Header.Get(SelectHeader))
		in := newRequestData(r, reqBody, params, res.Wildcard)
		chosen, selErr = route.choose(in, sel)
		if chosen != nil && (len(route.alts) > 0 || sel != "") {
			// The response event says which response answered.
			m := *match
			m.Response = chosen.name
			match = &m
		}
		if chosen != nil {
			body, header, renderErr = chosen.render(in)
		}
	}
	switch {
	case selErr != nil:
		status = http.StatusBadRequest
		note = " (unknown " + SelectHeader + ")"
		body = errorJSON(h, errorBody{
			Error:     fmt.Sprintf("%s: route %s %s has no response %q.", SelectHeader, route.route.Method, route.route.Path, selErr.value),
			Method:    r.Method,
			Path:      r.URL.Path,
			Responses: route.names(),
		})
	case renderErr != nil:
		// A templated response that cannot be rendered (MOCK-06): 500,
		// saying why, in the body and the traffic log. No delay.
		status = http.StatusInternalServerError
		note = " (template error)"
		msg := fmt.Sprintf("Route %s %s, response %q: %s", route.route.Method, route.route.Path, chosen.name, renderErr.Error())
		body = errorJSON(h, errorBody{Error: msg, Method: r.Method, Path: r.URL.Path})
		i.Emit(servers.Event{
			Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
			Summary: msg,
		})
	case chosen != nil:
		delay = i.delay(route.route.Delay)
		if delay > 0 && !i.wait(r, delay, peer, connID) {
			// The client left or the server is stopping: close the
			// connection without a response.
			panic(http.ErrAbortHandler)
		}
		status = chosen.status
		for k, v := range header {
			h[k] = append([]string(nil), v...)
		}
		if bodyAllowed(status) {
			h.Set("Content-Length", strconv.Itoa(len(body)))
		} else {
			body = nil
			h.Del("Content-Type")
		}
	case res.MethodNotAllowed:
		status = http.StatusMethodNotAllowed
		allow := strings.Join(res.Allowed, ", ")
		h.Set("Allow", allow)
		note = " (allowed: " + allow + ")"
		body = errorJSON(h, errorBody{Error: "No route for " + r.Method + " on this path.", Method: r.Method, Path: r.URL.Path, Allowed: res.Allowed})
	default:
		status = http.StatusNotFound
		note = " (no route matches)"
		body = errorJSON(h, errorBody{Error: "No route matches " + r.Method + " " + r.URL.Path + ".", Method: r.Method, Path: r.URL.Path})
	}

	w.WriteHeader(status)
	head := r.Method == http.MethodHead
	if !head && len(body) > 0 {
		_, _ = w.Write(body)
	}
	if head {
		body = nil
	}
	i.Emit(servers.Event{
		Peer:      peer,
		ConnID:    connID,
		Direction: servers.Out,
		Kind:      servers.KindResponse,
		Summary:   strings.TrimSpace(strconv.Itoa(status)+" "+http.StatusText(status)) + note,
		Content:   body,
		HTTP: &servers.HTTPInfo{
			Method:  r.Method,
			Target:  r.RequestURI,
			Proto:   r.Proto,
			Status:  status,
			Headers: httpbase.HeaderList(h),
			Route:   match,
			Delay:   delay,
		},
	})
}

// wait holds the response back for d. It returns false, after logging why,
// when the client disconnects or the server stops first.
func (i *instance) wait(r *http.Request, d time.Duration, peer string, connID uint64) bool {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(i.stopCtx, cancel)
	defer stop()
	if i.opts.Sleep(ctx, d) == nil {
		return true
	}
	why := "the client closed the connection"
	if i.stopCtx.Err() != nil {
		why = "the server is stopping"
	}
	i.Emit(servers.Event{
		Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
		Summary: "No response sent: " + why + " during the " + d.String() + " delay.",
	})
	return false
}

func errorJSON(h http.Header, v errorBody) []byte {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil { // cannot happen: strings only
		out = []byte("{}")
	}
	out = append(out, '\n')
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(out)))
	return out
}
