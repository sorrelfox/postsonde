package sioclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/realtime/engineio"
)

// maxErrorBody is how much of a refused handshake's body is kept.
const maxErrorBody = 4 << 10

// pollTransport is the Engine.IO long-polling transport: GET requests
// receive payloads (one at a time, each held open by the server until it
// has something to send), POST requests send them.
type pollTransport struct {
	c   *Client
	hc  *http.Client
	sid string
	// ctx is the transport's own context: it keeps the values of the
	// connect context (the user-operation mark for the dialer) but not its
	// deadline, and abort cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	postMu sync.Mutex

	mu       sync.Mutex
	down     func(error)
	pausing  bool
	stopped  bool
	loopDone chan struct{}
}

func (t *pollTransport) name() string { return "polling" }

// openPolling runs the long-polling handshake and returns the transport,
// the handshake and packets that came with it.
func (c *Client) openPolling(ctx context.Context) (*pollTransport, engineio.Handshake, []engineio.Packet, error) {
	tctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t := &pollTransport{
		c: c,
		hc: &http.Client{
			Transport: httpclient.NewTransport(c.o.Dialer, httpclient.TransportOptions{
				RootCAs:            c.o.RootCAs,
				InsecureSkipVerify: c.o.InsecureSkipVerify,
				ClientCertificate:  c.o.ClientCertificate,
			}),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		ctx:      tctx,
		cancel:   cancel,
		loopDone: make(chan struct{}),
	}
	pkts, err := t.do(ctx, http.MethodGet, nil)
	if err != nil {
		t.abort()
		return nil, engineio.Handshake{}, nil, err
	}
	if pkts[0].Type != engineio.Open {
		t.abort()
		return nil, engineio.Handshake{}, nil, errs.New(errs.Protocol, "The server did not answer the handshake with an Engine.IO open packet; the first packet was "+pkts[0].Type.String()+".").
			WithCode(CodeInvalidHandshake).
			WithHint(versionHint(c.v2))
	}
	hs, err := engineio.ParseHandshake(pkts[0].Data)
	if err != nil {
		t.abort()
		return nil, engineio.Handshake{}, nil, err
	}
	t.sid = hs.SID
	return t, hs, pkts[1:], nil
}

// do sends one request: a GET (body nil) that returns the payload's
// packets, or a POST of body.
func (t *pollTransport) do(ctx context.Context, method string, body []byte) ([]engineio.Packet, error) {
	c := t.c
	raw := c.t.url(false, t.sid, c.eio())
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, rd)
	if err != nil {
		return nil, invalid(CodeInvalidURL, "The Socket.IO URL cannot be requested: "+err.Error())
	}
	cookieRow := false
	for _, h := range c.headers {
		if strings.EqualFold(h.Name, "Host") {
			req.Host = h.Value
			continue
		}
		if strings.EqualFold(h.Name, "Cookie") {
			cookieRow = true
		}
		req.Header[h.Name] = append(req.Header[h.Name], h.Value)
	}
	if body != nil {
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	}
	var jarURL *url.URL
	if c.o.Jar != nil {
		jarURL = &url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host, Path: req.URL.Path}
		if !cookieRow {
			var parts []string
			for _, ck := range c.o.Jar.Cookies(jarURL) {
				if ck.Name == "" {
					parts = append(parts, ck.Value)
				} else {
					parts = append(parts, ck.Name+"="+ck.Value)
				}
			}
			if len(parts) > 0 {
				req.Header.Set("Cookie", strings.Join(parts, "; "))
			}
		}
	}
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if c.o.Jar != nil {
		if lines := resp.Header.Values("Set-Cookie"); len(lines) > 0 {
			c.o.Jar.SetCookieLines(jarURL, lines)
		}
	}
	limit := c.o.MaxMessageSize
	if limit == 0 {
		limit = engineio.DefaultMaxSize
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, statusError(resp, data, t.sid == "", c.v2)
	}
	var r io.Reader = resp.Body
	if limit > 0 {
		r = io.LimitReader(resp.Body, int64(limit)+1)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if method != http.MethodGet {
		return nil, nil
	}
	if c.v2 {
		return engineio.DecodePayloadV3(data, engineio.Options{MaxSize: limit})
	}
	pkts, err := engineio.DecodePayload(data, engineio.Options{MaxSize: limit})
	if err != nil && t.sid == "" && engineio.LooksLikeV3Payload(data) {
		return nil, errs.Wrap(err, errs.Protocol, "The server answered the handshake in Engine.IO 3: it is a Socket.IO v2 server.").
			WithCode(CodeVersionMismatch).
			WithHint("Choose Socket.IO v2 in the Options.").
			WithDetail("version", string(VersionV2))
	}
	return pkts, err
}

// versionHint is the hint of a handshake that is not Engine.IO.
func versionHint(v2 bool) string {
	if v2 {
		return "Check that this is a Socket.IO v2 server (or v3/v4 with allowEIO3) and the path is right."
	}
	return "Check that this is a Socket.IO v3 or v4 server and the path is right."
}

// statusError is the error of a response that is not 200: the server's
// Engine.IO error message ({"code":…,"message":…}) when it sent one.
func statusError(resp *http.Response, body []byte, handshake, v2 bool) error {
	var msg struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &msg)
	text := "The server answered the long-polling request with " + resp.Status
	code := CodeTransportError
	if handshake {
		text = "The server refused the Engine.IO handshake with " + resp.Status
		code = CodeHandshakeRejected
	}
	if msg.Message != "" {
		text += ": " + msg.Message
	} else {
		text += "."
	}
	e := errs.New(errs.Protocol, text).WithCode(code).WithDetail("status", strconv.Itoa(resp.StatusCode))
	switch {
	case handshake && msg.Message == "Unsupported protocol version" && v2:
		e = e.WithHint("The server speaks Socket.IO v3 or v4 and does not allow v2 clients (allowEIO3): choose Socket.IO v3 / v4 in the Options.")
	case handshake && msg.Message == "Unsupported protocol version":
		e = e.WithHint("The server does not speak Engine.IO 4: if it is a Socket.IO v2 server, choose Socket.IO v2 in the Options.")
	case handshake:
		e = e.WithHint("Check the URL, the path (" + DefaultPath + " by default), the headers and that the server allows long-polling.")
	}
	if len(body) > 0 {
		e = e.WithDetail("body", strings.ToValidUTF8(string(body), "�"))
	}
	return e
}

// attach starts polling: pending (the packets of the handshake response)
// and then every received packet go to c.
func (t *pollTransport) attach(c *Client, pending []engineio.Packet) {
	t.mu.Lock()
	t.down = func(err error) { c.transportDown(t, err) }
	t.mu.Unlock()
	go t.loop(pending)
}

func (t *pollTransport) loop(pending []engineio.Packet) {
	defer close(t.loopDone)
	for _, p := range pending {
		t.c.receive(p)
	}
	for {
		t.mu.Lock()
		stop := t.pausing || t.stopped
		t.mu.Unlock()
		if stop {
			return
		}
		pkts, err := t.do(t.ctx, http.MethodGet, nil)
		if err != nil {
			t.fail(err)
			return
		}
		for _, p := range pkts {
			t.c.receive(p)
			if p.Type == engineio.Close {
				return
			}
		}
	}
}

// fail reports the transport's end, unless it was stopped.
func (t *pollTransport) fail(err error) {
	t.mu.Lock()
	d, stopped := t.down, t.stopped
	t.mu.Unlock()
	if stopped || t.ctx.Err() != nil || d == nil {
		return
	}
	d(err)
}

// pause stops polling after the request in flight, for an upgrade (the
// server ends that request with a noop packet).
func (t *pollTransport) pause(ctx context.Context) error {
	t.mu.Lock()
	t.pausing = true
	t.mu.Unlock()
	select {
	case <-t.loopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *pollTransport) send(pkts []engineio.Packet) error {
	encode := engineio.EncodePayload
	if t.c.v2 {
		encode = engineio.EncodePayloadV3
	}
	body, err := encode(pkts)
	if err != nil {
		return err
	}
	t.postMu.Lock()
	defer t.postMu.Unlock()
	if _, err := t.do(t.ctx, http.MethodPost, body); err != nil {
		t.mu.Lock()
		stopped := t.stopped
		t.mu.Unlock()
		if stopped {
			return invalid(CodeNotOpen, "The connection closed while sending.")
		}
		go t.fail(err)
		return transportError(err, t.c.t.authority)
	}
	return nil
}

// close ends polling; the close packet was already sent.
func (t *pollTransport) close(context.Context) { t.abort() }

func (t *pollTransport) abort() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
	t.cancel()
	t.hc.CloseIdleConnections()
}
