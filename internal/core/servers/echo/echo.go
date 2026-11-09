// Package echo is the Echo HTTP server type: it logs every connection and
// request and answers each request with 200 and a JSON description of it.
// It is the basic server type the local server manager was built with.
// Serving, connection events and stopping are httpbase, shared with the
// HTTP mock (mockhttp). See docs/servers.md.
package echo

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/httpbase"
)

// TypeName is the Spec.Type of the echo server.
const TypeName = "echo_http"

// Type is the Echo HTTP server type. The zero value is ready to use.
type Type struct{}

// New returns the echo server type.
func New() Type { return Type{} }

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "Echo HTTP" }

// ALPN offers HTTP/1.1 over TLS (SRV-05); HTTP/2 is not served.
func (Type) ALPN() []string { return httpbase.ALPN() }

// Start serves HTTP/1.1 on ln.
func (Type) Start(_ servers.Spec, ln net.Listener, sink servers.Sink) (servers.Instance, error) {
	inst := httpbase.New(sink)
	inst.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveHTTP(inst, w, r) }))
	return inst, nil
}

// Echo is the JSON body of every response.
type Echo struct {
	Method     string              `json:"method"`
	Target     string              `json:"target"`
	Path       string              `json:"path"`
	Query      map[string][]string `json:"query"`
	Proto      string              `json:"proto"`
	Host       string              `json:"host"`
	RemoteAddr string              `json:"remoteAddr"`
	Headers    map[string][]string `json:"headers"`
	// Body is the request body as text; BodyBase64 is set instead when it
	// is not UTF-8.
	Body          string `json:"body"`
	BodyBase64    string `json:"bodyBase64,omitempty"`
	BodySize      int64  `json:"bodySize"`
	BodyTruncated bool   `json:"bodyTruncated"`
}

func serveHTTP(i *httpbase.Instance, w http.ResponseWriter, r *http.Request) {
	limit := i.ContentLimit()
	body, size, readErr := httpbase.ReadCapped(r.Body, limit)
	peer := r.RemoteAddr
	connID := i.ConnID(r)
	reqInfo := &servers.HTTPInfo{
		Method:  r.Method,
		Target:  r.RequestURI,
		Proto:   r.Proto,
		Headers: httpbase.RequestHeaders(r),
	}
	i.Emit(servers.Event{
		Peer:      peer,
		ConnID:    connID,
		Direction: servers.In,
		Kind:      servers.KindRequest,
		Summary:   r.Method + " " + r.RequestURI,
		Content:   body,
		Size:      int(size),
		Truncated: size > int64(len(body)),
		HTTP:      reqInfo,
	})
	if readErr != nil {
		i.Emit(servers.Event{
			Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
			Summary: "Reading the request body failed: " + readErr.Error(),
		})
	}

	echo := Echo{
		Method:        r.Method,
		Target:        r.RequestURI,
		Path:          r.URL.Path,
		Query:         nonNil(r.URL.Query()),
		Proto:         r.Proto,
		Host:          r.Host,
		RemoteAddr:    r.RemoteAddr,
		Headers:       nonNil(r.Header),
		BodySize:      size,
		BodyTruncated: size > int64(len(body)),
	}
	if utf8.Valid(body) {
		echo.Body = string(body)
	} else {
		echo.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	}
	out, err := json.MarshalIndent(echo, "", "  ")
	if err != nil { // cannot happen: only strings, maps and numbers
		out = []byte("{}")
	}
	out = append(out, '\n')

	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(out)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(out)
	}

	respHeaders := httpbase.HeaderList(h)
	respInfo := &servers.HTTPInfo{
		Method:  r.Method,
		Target:  r.RequestURI,
		Proto:   r.Proto,
		Status:  http.StatusOK,
		Headers: respHeaders,
	}
	var content []byte
	if r.Method != http.MethodHead {
		content = out
	}
	i.Emit(servers.Event{
		Peer:      peer,
		ConnID:    connID,
		Direction: servers.Out,
		Kind:      servers.KindResponse,
		Summary:   "200 OK",
		Content:   content,
		HTTP:      respInfo,
	})
}

func nonNil(m map[string][]string) map[string][]string {
	if m == nil {
		return map[string][]string{}
	}
	return m
}
