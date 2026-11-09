// Package graphqlmock is the GraphQL mock server type (SSRV-08): it
// serves a schema loaded from SDL (a file or pasted text) or from a saved
// introspection result, and answers every operation with data shaped
// exactly like its selection set. Operations are parsed, validated and
// executed with the repository's GraphQL engine (lexer, parser, validator,
// schema model); values come from static overrides per field (or per
// scalar or enum type) and are generated from the schema otherwise:
// deterministic, of the right type, lists of a set length, nullable
// fields null at a set rate, abstract types resolved to one of their
// object types. Variables, aliases, fragments, @skip/@include, __typename
// and introspection (__schema, __type) work as on a real server, so a
// client can introspect it; errors are the spec's (message, locations,
// path). Requests are GraphQL over HTTP: POST with JSON and GET with
// query parameters, application/graphql-response+json or
// application/json. Subscriptions are not served. HTTP serving is
// httpbase. See docs/servers.md.
package graphqlmock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/httpbase"
)

// TypeName is the Spec.Type of the GraphQL mock server.
const TypeName = "graphql_mock"

// MaxRequestBody bounds the body of a request (the document and its
// variables).
const MaxRequestBody = 4 << 20

// Media types of GraphQL over HTTP.
const (
	MediaGraphQLResponse = "application/graphql-response+json"
	MediaJSON            = "application/json"
)

// Options configure the type.
type Options struct {
	// ReadFile reads a stored schema file path: relative paths are
	// relative to the open project's root, and the service refuses paths
	// outside the allowed directories (PRV-10). Nil reads absolute paths
	// from disk only.
	ReadFile func(stored string) ([]byte, error)
}

// Type is the GraphQL mock server type.
type Type struct {
	opts Options
}

// New returns the GraphQL mock server type.
func New(opts Options) Type { return Type{opts: opts} }

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "GraphQL mock" }

// ALPN is HTTP/1.1, as the other HTTP types.
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
	return nil, errs.New(errs.Internal, fmt.Sprintf("A GraphQL mock server cannot use a %T configuration.", config)).
		WithCode(CodeInvalidConfig)
}

// Describe loads the schema of config and checks its overrides, for the
// editor: Describe with the type's file reader.
func (t Type) Describe(config Config) (Description, error) {
	return Describe(config, t.opts.ReadFile)
}

// Start loads the schema and serves it on ln. A schema that does not
// load fails the start (graphqlmock.schema_failed); overrides that do
// not fit it and the schema's problems are logged as errors.
func (t Type) Start(spec servers.Spec, ln net.Listener, sink servers.Sink) (servers.Instance, error) {
	cfg, err := t.NormalizeConfig(spec.Config)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	c := cfg.(Config)
	l, err := LoadSchema(c, t.opts.ReadFile)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	inst := &Instance{base: httpbase.New(sink), opts: t.opts}
	st := compile(c, l)
	inst.state.Store(st)
	inst.report(st, nil)
	inst.base.Serve(ln, http.HandlerFunc(inst.serveHTTP))
	return inst, nil
}

var _ servers.Reconfigurable = (*Instance)(nil)

// Instance is one run of a GraphQL mock server. It implements
// servers.Instance and servers.Reconfigurable.
type Instance struct {
	base     *httpbase.Instance
	opts     Options
	state    atomic.Pointer[state]
	requests atomic.Uint64
}

func (i *Instance) Done() <-chan struct{}          { return i.base.Done() }
func (i *Instance) Err() error                     { return i.base.Err() }
func (i *Instance) Stop(ctx context.Context) error { return i.base.Stop(ctx) }

// Reconfigure loads the schema again and answers with the new overrides;
// requests in progress finish with the old ones. When the schema no
// longer loads, the error is logged and the server keeps the schema it
// had, with the new settings.
func (i *Instance) Reconfigure(config any) {
	c, ok := config.(Config)
	if !ok {
		return
	}
	old := i.state.Load()
	l, err := LoadSchema(c, i.opts.ReadFile)
	if err != nil {
		i.base.Emit(servers.Event{Direction: servers.System, Kind: servers.KindError,
			Summary: errText(err) + " The server keeps serving the schema it loaded before."})
		l = old.loaded
	}
	st := compile(c, l)
	i.state.Store(st)
	i.report(st, old)
}

// report logs the problems of st that old did not have.
func (i *Instance) report(st, old *state) {
	if st.loaded.Schema == nil {
		if old == nil || old.loaded.Schema != nil {
			i.base.Emit(servers.Event{Direction: servers.System, Kind: servers.KindError,
				Summary: "No schema yet: every request answers with an error until SDL or a schema file is set."})
		}
		return
	}
	for _, p := range st.problems {
		if old != nil && slices.Contains(old.problems, p) {
			continue
		}
		i.base.Emit(servers.Event{Direction: servers.System, Kind: servers.KindError, Summary: p.Message})
	}
}

func errText(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Message != "" {
		return e.Message
	}
	return err.Error()
}

// exchange is one HTTP request being answered, for its events.
type exchange struct {
	inst   *Instance
	w      http.ResponseWriter
	r      *http.Request
	peer   string
	connID uint64
	body   []byte
	size   int64
	info   *servers.GraphQLInfo
	logged bool
}

func (i *Instance) serveHTTP(w http.ResponseWriter, r *http.Request) {
	st := i.state.Load()
	x := &exchange{inst: i, w: w, r: r, peer: r.RemoteAddr, connID: i.base.ConnID(r)}
	var readErr error
	x.body, x.size, readErr = httpbase.ReadCapped(r.Body, MaxRequestBody+1)
	if st.cfg.AllowOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", st.cfg.AllowOrigin)
		if st.cfg.AllowOrigin != "*" {
			w.Header().Add("Vary", "Origin")
		}
	}
	switch {
	case r.URL.Path != st.cfg.Path:
		x.plain(http.StatusNotFound, "the GraphQL endpoint is "+st.cfg.Path, nil)
		return
	case r.Method == http.MethodOptions:
		x.preflight(st)
		return
	case r.Method != http.MethodGet && r.Method != http.MethodPost:
		x.plain(http.StatusMethodNotAllowed, "GraphQL requests are GET or POST requests", map[string]string{"Allow": "GET, POST"})
		return
	case readErr != nil:
		x.requestError(MediaJSON, http.StatusBadRequest, "Reading the request body failed: "+readErr.Error())
		return
	case x.size > MaxRequestBody:
		x.requestError(MediaJSON, http.StatusRequestEntityTooLarge, fmt.Sprintf("The request body is larger than %d MB.", MaxRequestBody>>20))
		return
	}
	media, ok := negotiate(r.Header.Values("Accept"))
	if !ok {
		x.requestError(MediaJSON, http.StatusNotAcceptable, "The Accept header allows neither "+MediaGraphQLResponse+" nor "+MediaJSON+".")
		return
	}
	req, status, msg := x.params()
	if msg != "" {
		x.requestError(media, status, msg)
		return
	}
	x.info = &servers.GraphQLInfo{Request: i.requests.Add(1)}
	if len(req.Variables) > 0 {
		x.info.Variables = canonical(req.Variables)
	}
	if st.loaded.Schema == nil {
		x.logRequest()
		x.answer(media, http.StatusInternalServerError, result{Errors: []gqlError{{
			Message: "The PostSonde GraphQL mock has no schema yet: paste SDL or choose a schema file in its settings.",
			Code:    "MOCK_NO_SCHEMA",
		}}})
		return
	}
	status = http.StatusOK
	allow := func(op schema.Operation) *gqlError {
		switch {
		case op == schema.OpSubscription:
			return &gqlError{Message: "The PostSonde GraphQL mock does not serve subscriptions; send queries and mutations.", Code: ErrCodeOperation}
		case op == schema.OpMutation && r.Method == http.MethodGet:
			status = http.StatusMethodNotAllowed
			w.Header().Set("Allow", "POST")
			return &gqlError{Message: "Can only perform a mutation operation from a POST request.", Code: ErrCodeBadRequest}
		}
		return nil
	}
	res := execute(st, req, allow)
	x.info.Operation, x.info.OperationType, x.info.Introspection = res.Operation, res.OpType, res.Introspection
	x.logRequest()
	if !res.HasData && status == http.StatusOK && media == MediaGraphQLResponse {
		// A request error: the GraphQL-over-HTTP spec's 4xx for the
		// graphql-response media type; application/json stays 200.
		status = http.StatusBadRequest
	}
	x.answer(media, status, res)
}

// params reads the GraphQL request of a GET (query parameters) or a POST
// (JSON body, or application/graphql). msg is set, with the HTTP status,
// when the request is malformed.
func (x *exchange) params() (req gqlRequest, status int, msg string) {
	r := x.r
	var query, opName, vars any
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		if !q.Has("query") {
			return req, http.StatusBadRequest, "The request has no query parameter."
		}
		query = q.Get("query")
		if q.Has("operationName") {
			opName = q.Get("operationName")
		}
		if v := q.Get("variables"); v != "" {
			dv, err := decodeJSON(v)
			if err != nil {
				return req, http.StatusBadRequest, "The variables parameter is not valid JSON: " + err.Error()
			}
			vars = dv
		}
	} else {
		ct := r.Header.Get("Content-Type")
		mt, p, err := mime.ParseMediaType(ct)
		if err != nil {
			return req, http.StatusUnsupportedMediaType, "The request has no Content-Type; send the GraphQL request as application/json."
		}
		if cs := p["charset"]; cs != "" && !strings.EqualFold(cs, "utf-8") {
			return req, http.StatusUnsupportedMediaType, "The request body must be UTF-8, not " + cs + "."
		}
		switch mt {
		case MediaJSON, "application/graphql+json":
			v, err := decodeJSON(string(x.body))
			if err != nil {
				return req, http.StatusBadRequest, "The request body is not valid JSON: " + err.Error()
			}
			m, ok := v.(map[string]any)
			if !ok {
				return req, http.StatusBadRequest, "The request body must be a JSON object with a query."
			}
			query, opName, vars = m["query"], m["operationName"], m["variables"]
			if _, has := m["query"]; !has {
				return req, http.StatusBadRequest, "The request body has no query."
			}
		case "application/graphql":
			query = string(x.body)
		default:
			return req, http.StatusUnsupportedMediaType, "The Content-Type " + strconv.Quote(mt) + " is not supported; send the GraphQL request as application/json."
		}
	}
	s, ok := query.(string)
	if !ok {
		return req, http.StatusBadRequest, "The query must be a string."
	}
	req.Query = s
	switch v := opName.(type) {
	case nil:
	case string:
		req.OperationName = v
	default:
		return req, http.StatusBadRequest, "The operationName must be a string."
	}
	switch v := vars.(type) {
	case nil:
	case map[string]any:
		req.Variables = v
	default:
		return req, http.StatusBadRequest, "The variables must be a JSON object."
	}
	return req, 0, ""
}

// negotiate picks the response's media type from the Accept header: the
// highest quality of application/graphql-response+json and
// application/json (*/* and application/* count for the first), the
// graphql-response type on a tie. No header is application/json, as for
// legacy clients.
func negotiate(accept []string) (string, bool) {
	header := strings.Join(accept, ",")
	if strings.TrimSpace(header) == "" {
		return MediaJSON, true
	}
	var qGQL, qJSON float64 = -1, -1
	for part := range strings.SplitSeq(header, ",") {
		mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		switch mt {
		case MediaGraphQLResponse:
			qGQL = max(qGQL, q)
		case MediaJSON:
			qJSON = max(qJSON, q)
		case "*/*", "application/*":
			qGQL = max(qGQL, q)
		}
	}
	switch {
	case qGQL <= 0 && qJSON <= 0:
		return "", false
	case qGQL >= qJSON:
		return MediaGraphQLResponse, true
	}
	return MediaJSON, true
}

// logRequest emits the request event.
func (x *exchange) logRequest() {
	if x.logged {
		return
	}
	x.logged = true
	summary := x.r.Method + " " + x.r.RequestURI
	if g := x.info; g != nil && g.OperationType != "" {
		summary = g.OperationType
		if g.Operation != "" {
			summary += " " + g.Operation
		}
		summary += " (" + x.r.Method + " " + x.r.URL.Path + ")"
	}
	x.inst.base.Emit(servers.Event{
		Peer: x.peer, ConnID: x.connID, Direction: servers.In, Kind: servers.KindRequest,
		Summary: summary,
		Content: x.shown(x.body), Size: int(x.size), Truncated: x.size > int64(len(x.shown(x.body))),
		HTTP:    &servers.HTTPInfo{Method: x.r.Method, Target: x.r.RequestURI, Proto: x.r.Proto, Headers: httpbase.RequestHeaders(x.r)},
		GraphQL: x.copyInfo(),
	})
}

func (x *exchange) copyInfo() *servers.GraphQLInfo {
	if x.info == nil {
		return nil
	}
	g := *x.info
	return &g
}

// shown is the part of b an event keeps (a copy, so it does not hold the
// whole body).
func (x *exchange) shown(b []byte) []byte {
	if limit := x.inst.base.ContentLimit(); limit >= 0 && len(b) > limit {
		return bytes.Clone(b[:limit])
	}
	return b
}

// answer writes res as the response and logs it.
func (x *exchange) answer(media string, status int, res result) {
	body := res.body()
	h := x.w.Header()
	h.Set("Content-Type", media+"; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	x.w.WriteHeader(status)
	_, _ = x.w.Write(body)
	summary := strconv.Itoa(status) + " " + http.StatusText(status) + ": "
	switch {
	case res.HasData && len(res.Errors) == 0:
		summary += "data"
	case res.HasData:
		summary += "data, " + plural(len(res.Errors), "error")
	case len(res.Errors) > 0:
		summary += plural(len(res.Errors), "error") + " (" + res.Errors[0].Message + ")"
	}
	g := x.copyInfo()
	if g != nil {
		g.Errors, g.HasData = len(res.Errors), res.HasData
	}
	x.inst.base.Emit(servers.Event{
		Peer: x.peer, ConnID: x.connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: summary,
		Content: x.shown(body), Size: len(body),
		HTTP:    &servers.HTTPInfo{Method: x.r.Method, Target: x.r.RequestURI, Proto: x.r.Proto, Status: status, Headers: httpbase.HeaderList(h)},
		GraphQL: g,
	})
}

// requestError answers a malformed request: a GraphQL error response
// with the HTTP status.
func (x *exchange) requestError(media string, status int, msg string) {
	x.logRequest()
	x.answer(media, status, result{Errors: []gqlError{{Message: msg, Code: ErrCodeBadRequest}}})
}

// plain answers a request that is not for the endpoint with text.
func (x *exchange) plain(status int, why string, headers map[string]string) {
	x.logRequest()
	for k, v := range headers {
		x.w.Header().Set(k, v)
	}
	body := "This is a PostSonde GraphQL mock server: " + why + ".\n"
	http.Error(x.w, strings.TrimSuffix(body, "\n"), status)
	x.inst.base.Emit(servers.Event{
		Peer: x.peer, ConnID: x.connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: strconv.Itoa(status) + " " + http.StatusText(status) + " (" + why + ")",
		Content: []byte(body),
		HTTP:    &servers.HTTPInfo{Method: x.r.Method, Target: x.r.RequestURI, Proto: x.r.Proto, Status: status, Headers: httpbase.HeaderList(x.w.Header())},
	})
}

// preflight answers OPTIONS: the CORS preflight when an origin is
// allowed, else the methods.
func (x *exchange) preflight(st *state) {
	x.logRequest()
	h := x.w.Header()
	h.Set("Allow", "GET, POST, OPTIONS")
	if st.cfg.AllowOrigin != "" {
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if req := x.r.Header.Get("Access-Control-Request-Headers"); req != "" {
			h.Set("Access-Control-Allow-Headers", req)
		} else {
			h.Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")
		}
		h.Set("Access-Control-Max-Age", "600")
	}
	x.w.WriteHeader(http.StatusNoContent)
	x.inst.base.Emit(servers.Event{
		Peer: x.peer, ConnID: x.connID, Direction: servers.Out, Kind: servers.KindResponse,
		Summary: "204 No Content (preflight)",
		HTTP:    &servers.HTTPInfo{Method: x.r.Method, Target: x.r.RequestURI, Proto: x.r.Proto, Status: http.StatusNoContent, Headers: httpbase.HeaderList(h)},
	})
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
