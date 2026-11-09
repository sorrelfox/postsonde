// Package webhook is the webhook receiver server type (MOCK-08): a
// catch-all HTTP endpoint that accepts any method on any path, logs every
// request in full and answers each with the configured response. Every
// request is also kept whole, byte for byte (servers.Capture), so the app
// can show it and replay it to another URL unchanged. Serving, connection
// events and stopping are httpbase. See docs/servers.md.
package webhook

import (
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"

	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/httpbase"
)

// TypeName is the Spec.Type of the webhook receiver.
const TypeName = "webhook"

// DefaultMaxBody is how much of a request body is kept for replay when
// Options.MaxBody is 0. A longer body is logged and answered, but its
// capture is cut there and cannot be replayed.
const DefaultMaxBody = 16 << 20

// Options configure the type.
type Options struct {
	// MaxBody is the most body bytes kept per request; 0 means
	// DefaultMaxBody.
	MaxBody int
}

// Type is the webhook receiver type.
type Type struct {
	opts Options
}

// New returns the webhook receiver type.
func New(opts Options) Type {
	if opts.MaxBody <= 0 {
		opts.MaxBody = DefaultMaxBody
	}
	return Type{opts: opts}
}

func (Type) Name() string  { return TypeName }
func (Type) Label() string { return "Webhook receiver" }

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
	return nil, invalidConfig("", "A webhook receiver cannot use this configuration.")
}

// Start serves HTTP/1.1 on ln.
func (t Type) Start(spec servers.Spec, ln net.Listener, sink servers.Sink) (servers.Instance, error) {
	cfg, err := t.NormalizeConfig(spec.Config)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	inst := &Instance{Instance: httpbase.New(sink), maxBody: t.opts.MaxBody}
	inst.Reconfigure(cfg)
	inst.Serve(ln, http.HandlerFunc(inst.serveHTTP))
	return inst, nil
}

// Instance is one run of a webhook receiver. It implements
// servers.Instance and servers.Reconfigurable.
type Instance struct {
	*httpbase.Instance
	maxBody int
	cfg     atomic.Pointer[Config]
}

// Reconfigure applies config (a Config) to the next requests.
func (i *Instance) Reconfigure(config any) {
	c, ok := config.(Config)
	if !ok {
		return
	}
	i.cfg.Store(&c)
}

func (i *Instance) serveHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := i.cfg.Load()
	body, size, readErr := httpbase.ReadCapped(r.Body, i.maxBody)
	peer := r.RemoteAddr
	connID := i.ConnID(r)
	// The log shows the first bytes; the capture keeps the rest, so the
	// log's copy must not hold on to the whole body.
	shown := slices.Clone(body[:min(len(body), i.ContentLimit())])
	i.Emit(servers.Event{
		Peer:      peer,
		ConnID:    connID,
		Direction: servers.In,
		Kind:      servers.KindRequest,
		Summary:   r.Method + " " + r.RequestURI,
		Content:   shown,
		Size:      int(size),
		Truncated: size > int64(len(shown)),
		HTTP: &servers.HTTPInfo{
			Method:  r.Method,
			Target:  r.RequestURI,
			Proto:   r.Proto,
			Headers: httpbase.RequestHeaders(r),
		},
		Capture: &servers.Capture{
			Method:    r.Method,
			Target:    r.RequestURI,
			Proto:     r.Proto,
			Host:      r.Host,
			Headers:   httpbase.HeaderList(r.Header),
			Body:      body,
			Size:      size,
			Truncated: size > int64(len(body)),
		},
	})
	if readErr != nil {
		i.Emit(servers.Event{
			Peer: peer, ConnID: connID, Direction: servers.System, Kind: servers.KindError,
			Summary: "Reading the request body failed: " + readErr.Error(),
		})
	}

	h := w.Header()
	for _, row := range cfg.Headers {
		if row.Enabled && row.Name != "" {
			h.Add(row.Name, row.Value)
		}
	}
	out := []byte(cfg.Body)
	if r.Method == http.MethodHead {
		out = nil
	} else {
		h.Set("Content-Length", strconv.Itoa(len(out)))
	}
	w.WriteHeader(cfg.Status)
	if len(out) > 0 {
		_, _ = w.Write(out)
	}
	i.Emit(servers.Event{
		Peer:      peer,
		ConnID:    connID,
		Direction: servers.Out,
		Kind:      servers.KindResponse,
		Summary:   strconv.Itoa(cfg.Status) + " " + http.StatusText(cfg.Status),
		Content:   out,
		HTTP: &servers.HTTPInfo{
			Method:  r.Method,
			Target:  r.RequestURI,
			Proto:   r.Proto,
			Status:  cfg.Status,
			Headers: httpbase.HeaderList(h),
		},
	})
}
