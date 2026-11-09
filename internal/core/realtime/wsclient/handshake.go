package wsclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/realtime/wsframe"
)

// Handshake is what happened while connecting, for the inspector and the
// timeline. Client.Handshake returns it once Connect returned, whether the
// connection opened or not.
type Handshake struct {
	// URL is the URL connected to, without userinfo.
	URL string
	// RequestHeaders are the headers sent, in order, as spelled: the
	// generated ones (Host, Upgrade, Connection, Sec-WebSocket-*), the
	// rows, and the cookie jar's Cookie header.
	RequestHeaders []httpclient.Header
	// RawRequest is the request as written to the connection. Header
	// values may hold bytes that are not UTF-8.
	RawRequest string
	// RequestCookie is the Cookie header sent (rows and jar), or "".
	RequestCookie string
	// Response is the server's answer; nil when none arrived.
	Response *Response
	// Subprotocol is the subprotocol the server selected, or "".
	Subprotocol string
	// Timing covers the handshake: DNS, Connect, TLS, RequestWrite (the
	// upgrade request), Wait (to the first response byte) and Download (the
	// response headers, and the body of a rejected upgrade). Total runs
	// from the start of Connect to the end of the response.
	Timing httpclient.Timing
	// Conn describes the connection (addresses, TLS session); Protocol is
	// always "HTTP/1.1".
	Conn httpclient.Conn
	// Events is the raw trace, with the httpclient.Event* names plus
	// EventUpgraded.
	Events []httpclient.TimelineEvent
}

// Response is the answer to the upgrade request.
type Response struct {
	// Proto is "HTTP/1.1" (or "HTTP/1.0").
	Proto string
	// StatusCode and Status ("101 Switching Protocols") as received.
	StatusCode int
	Status     string
	// Headers are the response headers in the order received, names as
	// spelled. Enabled is always true.
	Headers []httpclient.Header
	// RawHeader is the status line and headers as received, up to and
	// including the empty line.
	RawHeader string
	// Body is the body of a response that is not 101, cut at
	// Options.MaxResponseBody (BodyTruncated is then set). Never nil.
	Body          []byte
	BodyTruncated bool
}

// Header returns the values of the header name (any case), in order.
func (r *Response) Header(name string) []string {
	var v []string
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, name) {
			v = append(v, h.Value)
		}
	}
	return v
}

// EventUpgraded is the timeline event of a response that was read; its
// detail is the status.
const EventUpgraded = "upgrade_response"

// recorder collects handshake events and phase times.
type recorder struct {
	now   func() time.Time
	start time.Time

	mu     sync.Mutex
	done   bool
	events []httpclient.TimelineEvent

	dnsStart, dnsDone, connStart, connDone time.Time
	tlsStart, tlsDone                      time.Time
	writeStart, wrote, firstByte, end      time.Time
}

func (r *recorder) event(name, detail string, set *time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	at := r.now()
	r.events = append(r.events, httpclient.TimelineEvent{At: at.Sub(r.start), Name: name, Detail: detail})
	if set != nil && set.IsZero() {
		*set = at
	}
}

func (r *recorder) contextWithTraces(ctx context.Context) context.Context {
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart: func(i httptrace.DNSStartInfo) { r.event(httpclient.EventDNSStart, i.Host, &r.dnsStart) },
		DNSDone: func(i httptrace.DNSDoneInfo) {
			detail := errDetail(i.Err)
			if i.Err == nil {
				addrs := make([]string, len(i.Addrs))
				for n, a := range i.Addrs {
					addrs[n] = a.String()
				}
				detail = strings.Join(addrs, ", ")
			}
			r.event(httpclient.EventDNSDone, detail, &r.dnsDone)
		},
		ConnectStart: func(network, addr string) {
			r.event(httpclient.EventConnectStart, network+" "+addr, &r.connStart)
		},
		ConnectDone: func(network, addr string, err error) {
			detail := network + " " + addr
			if err != nil {
				detail += ": " + err.Error()
			}
			r.mu.Lock()
			if err == nil {
				// The connect phase ends at the attempt that worked.
				r.connDone = time.Time{}
			}
			r.mu.Unlock()
			r.event(httpclient.EventConnectDone, detail, &r.connDone)
		},
	})
	return netx.WithTrace(ctx, &netx.Trace{
		DialStart: func(i netx.DialStartInfo) { r.event(httpclient.EventDialStart, i.Network+" "+i.Address, nil) },
		DialDone: func(i netx.DialDoneInfo) {
			detail := errDetail(i.Err)
			if i.Err == nil {
				detail = i.LocalAddr.String() + " → " + i.RemoteAddr.String()
			}
			r.event(httpclient.EventDialDone, detail, nil)
			if i.Proxy != nil {
				// The hop through a proxy (SOCKS-12).
				r.event(httpclient.EventProxyHop, i.Proxy.String(), nil)
			}
		},
	})
}

// finish stops recording and returns the timing and events.
func (r *recorder) finish() (httpclient.Timing, []httpclient.TimelineEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	if r.end.IsZero() {
		r.end = r.now()
	}
	var t httpclient.Timing
	phase := func(p httpclient.Phases, d *time.Duration, from, to time.Time) {
		if !from.IsZero() && !to.IsZero() {
			*d = max(to.Sub(from), 0)
			t.Observed |= p
		}
	}
	phase(httpclient.PhaseDNS, &t.DNS, r.dnsStart, r.dnsDone)
	phase(httpclient.PhaseConnect, &t.Connect, r.connStart, r.connDone)
	phase(httpclient.PhaseTLS, &t.TLS, r.tlsStart, r.tlsDone)
	phase(httpclient.PhaseRequestWrite, &t.RequestWrite, r.writeStart, r.wrote)
	phase(httpclient.PhaseWait, &t.Wait, r.wrote, r.firstByte)
	if !r.firstByte.IsZero() {
		phase(httpclient.PhaseDownload, &t.Download, r.firstByte, r.end)
	}
	t.Total = r.end.Sub(r.start)
	return t, r.events
}

func errDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// handshake dials, runs TLS for wss and the upgrade exchange. On success
// it returns the connection and the reader holding any bytes the server
// sent after the 101 response. hs is filled in either way.
func (c *Client) handshake(ctx context.Context, hs *Handshake) (conn net.Conn, br *bufio.Reader, err error) {
	rec := &recorder{now: c.now, start: c.now()}
	host := c.t.dialAddr
	defer func() {
		hs.Timing, hs.Events = rec.finish()
		if err != nil && conn != nil {
			conn.Close()
			conn, br = nil, nil
		}
	}()

	if timeout := c.o.HandshakeTimeout; timeout >= 0 {
		if timeout == 0 {
			timeout = DefaultHandshakeTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, timeout, errHandshakeTimeout)
		defer cancel()
	}

	key, err := newKey(c.o.Rand)
	if err != nil {
		return nil, nil, err
	}
	req, sentKey := c.buildRequest(key, hs, rec)

	if err := ctx.Err(); err != nil {
		return nil, nil, mapConnectError(ctx, err, host)
	}
	conn, err = c.o.Dialer.DialContext(rec.contextWithTraces(ctx), "tcp", host)
	if err != nil {
		return nil, nil, mapConnectError(ctx, err, host)
	}
	hs.Conn = httpclient.Conn{
		LocalAddr:  conn.LocalAddr().String(),
		RemoteAddr: conn.RemoteAddr().String(),
		Protocol:   "HTTP/1.1",
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	raw := conn
	stop := context.AfterFunc(ctx, func() { _ = raw.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	// Set by GetClientCertificate (the handshake runs on this goroutine).
	// Once the server asked for a client certificate, a failure before the
	// response is its refusal: with TLS 1.3 its alert races with the
	// request write and the first read (netx.ClassifyClientCert).
	var certRequested, certSent bool
	connectErr := func(err error) error {
		if certRequested && ctx.Err() == nil {
			if e := netx.FailureError(netx.ClassifyClientCert(err, certSent), err, host); e != nil {
				return e.WithDetail("host", host)
			}
		}
		return mapConnectError(ctx, err, host)
	}

	if c.t.secure {
		cfg := &tls.Config{
			ServerName:         c.t.serverName,
			MinVersion:         tls.VersionTLS12,
			RootCAs:            c.o.RootCAs,
			InsecureSkipVerify: c.o.InsecureSkipVerify,
			// No h2: WebSocket over HTTP/2 (RFC 8441) is not supported.
			NextProtos: []string{"http/1.1"},
		}
		// A client certificate is always sent when the server asks, as
		// httpclient does; the request is noted either way, for the
		// timeline and to explain a TLS 1.2 "handshake failure".
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			certRequested = true
			if cc := c.o.ClientCertificate; cc != nil && cc.Certificate != nil {
				certSent = true
				rec.event(httpclient.EventClientCert, cc.Description, nil)
				return cc.Certificate, nil
			}
			rec.event(httpclient.EventClientCert, "requested by the server; none is set for this host", nil)
			return &tls.Certificate{}, nil
		}
		tc := tls.Client(conn, cfg)
		conn = tc
		rec.event(httpclient.EventTLSStart, "", &rec.tlsStart)
		err := tc.HandshakeContext(ctx)
		if err != nil {
			rec.event(httpclient.EventTLSDone, err.Error(), &rec.tlsDone)
			return conn, nil, connectErr(err)
		}
		cs := tc.ConnectionState()
		detail := tls.VersionName(cs.Version)
		if cs.NegotiatedProtocol != "" {
			detail += ", " + cs.NegotiatedProtocol
		}
		rec.event(httpclient.EventTLSDone, detail, &rec.tlsDone)
		hs.Conn.TLS = httpclient.NewTLSInfo(&cs)
	}

	rec.event(httpclient.EventGotConn, "new "+hs.Conn.LocalAddr+" → "+hs.Conn.RemoteAddr, &rec.writeStart)
	if _, err := conn.Write(req); err != nil {
		return conn, nil, connectErr(err)
	}
	rec.event(httpclient.EventWroteRequest, "", &rec.wrote)

	br = bufio.NewReaderSize(conn, 4096)
	if _, err := br.Peek(1); err != nil {
		return conn, nil, connectErr(err)
	}
	rec.event(httpclient.EventFirstByte, "", &rec.firstByte)
	rawHeader, err := readHeaderSection(br)
	if err != nil {
		return conn, nil, connectErr(err)
	}
	resp, err := parseResponse(rawHeader, host)
	if err != nil {
		return conn, nil, err
	}
	hs.Response = resp
	rec.event(EventUpgraded, resp.Status, nil)

	if c.o.Jar != nil {
		if lines := resp.Header("Set-Cookie"); len(lines) > 0 {
			stored, rejected := c.o.Jar.SetCookieLines(c.t.jarURL, lines)
			rec.event(httpclient.EventCookiesStored, cookiesStoredDetail(stored, rejected), nil)
		}
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		resp.Body, resp.BodyTruncated = c.readBody(rawHeader, br)
		rec.mu.Lock()
		rec.end = rec.now()
		rec.mu.Unlock()
		msg := "The server at " + host + " answered the WebSocket upgrade with " + resp.Status + "."
		hint := HintUpgradeRejected
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			if loc := resp.Header("Location"); len(loc) > 0 {
				msg = "The server at " + host + " redirected the WebSocket upgrade (" + resp.Status + ") to " + loc[0] + "."
				hint = "WebSocket connections do not follow redirects. Connect to the new URL instead."
			}
		}
		return conn, nil, handshakeFailure(CodeUpgradeRejected, host, msg, hint).
			WithDetail("status", strconv.Itoa(resp.StatusCode))
	}
	rec.mu.Lock()
	rec.end = rec.now()
	rec.mu.Unlock()

	if err := c.checkUpgrade(resp, sentKey, hs); err != nil {
		return conn, nil, err
	}
	if !stop() {
		// The context ended while the response was read: the deadline is
		// already in the past.
		return conn, nil, mapConnectError(ctx, context.Cause(ctx), host)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return conn, nil, mapConnectError(ctx, err, host)
	}
	return conn, br, nil
}

func newKey(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", errs.Wrap(err, errs.Internal, "Could not generate a WebSocket key.")
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// buildRequest writes the upgrade request. It returns the request bytes
// and the Sec-WebSocket-Key actually sent (a row may replace it).
func (c *Client) buildRequest(key string, hs *Handshake, rec *recorder) ([]byte, string) {
	rows := c.headers
	has := func(name string) bool {
		for _, h := range rows {
			if strings.EqualFold(h.Name, name) {
				return true
			}
		}
		return false
	}
	var list []httpclient.Header
	add := func(name, value string) {
		if !has(name) {
			list = append(list, httpclient.Header{Name: name, Value: value, Enabled: true})
		}
	}
	hostDone := false
	for _, h := range rows {
		if strings.EqualFold(h.Name, "Host") {
			list = append(list, h)
			hostDone = true
		}
	}
	if !hostDone {
		list = append(list, httpclient.Header{Name: "Host", Value: c.t.hostHeader, Enabled: true})
	}
	add("Upgrade", "websocket")
	add("Connection", "Upgrade")
	add("Sec-WebSocket-Key", key)
	add("Sec-WebSocket-Version", "13")
	if len(c.o.Subprotocols) > 0 {
		add("Sec-WebSocket-Protocol", strings.Join(c.o.Subprotocols, ", "))
	}
	if c.t.auth != "" {
		add("Authorization", c.t.auth)
	}
	for _, h := range rows {
		if !strings.EqualFold(h.Name, "Host") {
			list = append(list, h)
		}
	}
	sentKey := key
	for _, h := range rows {
		if strings.EqualFold(h.Name, "Sec-WebSocket-Key") {
			sentKey = h.Value
			break
		}
	}
	list = c.addJarCookies(list, rec)

	var b bytes.Buffer
	b.WriteString("GET " + c.t.requestURI + " HTTP/1.1\r\n")
	var cookie []string
	for _, h := range list {
		b.WriteString(h.Name + ": " + h.Value + "\r\n")
		if strings.EqualFold(h.Name, "Cookie") {
			cookie = append(cookie, h.Value)
		}
	}
	b.WriteString("\r\n")
	hs.URL = c.t.display
	hs.RequestHeaders = list
	hs.RawRequest = b.String()
	hs.RequestCookie = strings.Join(cookie, "; ")
	return b.Bytes(), sentKey
}

// addJarCookies adds the jar's cookies for the URL: after the first Cookie
// row's value, or as a new Cookie header at the end. A jar cookie whose
// name a Cookie row sends is left out.
func (c *Client) addJarCookies(list []httpclient.Header, rec *recorder) []httpclient.Header {
	if c.o.Jar == nil {
		return list
	}
	jarCookies := c.o.Jar.Cookies(c.t.jarURL)
	if len(jarCookies) == 0 {
		return list
	}
	first := -1
	rowNames := map[string]bool{}
	for i, h := range list {
		if !strings.EqualFold(h.Name, "Cookie") {
			continue
		}
		if first < 0 {
			first = i
		}
		for pair := range strings.SplitSeq(h.Value, ";") {
			if n, _, _ := strings.Cut(pair, "="); strings.TrimSpace(n) != "" {
				rowNames[strings.TrimSpace(n)] = true
			}
		}
	}
	var parts, names []string
	for _, ck := range jarCookies {
		if rowNames[ck.Name] {
			continue
		}
		if ck.Name == "" {
			parts = append(parts, ck.Value)
			names = append(names, `""`)
		} else {
			parts = append(parts, ck.Name+"="+ck.Value)
			names = append(names, ck.Name)
		}
	}
	if len(parts) == 0 {
		return list
	}
	rec.event(httpclient.EventCookiesSent, strings.Join(names, ", "), nil)
	value := strings.Join(parts, "; ")
	if first < 0 {
		return append(list, httpclient.Header{Name: "Cookie", Value: value, Enabled: true})
	}
	if v := strings.TrimRight(list[first].Value, "; \t"); v != "" {
		value = v + "; " + value
	}
	list[first].Value = value
	return list
}

func cookiesStoredDetail(stored, rejected []string) string {
	var parts []string
	if len(stored) > 0 {
		names := make([]string, len(stored))
		for i, n := range stored {
			if n == "" {
				n = `""`
			}
			names[i] = n
		}
		parts = append(parts, "stored "+strings.Join(names, ", "))
	}
	if len(rejected) > 0 {
		parts = append(parts, "ignored "+strings.Join(rejected, ", "))
	}
	return strings.Join(parts, "; ")
}

// readHeaderSection reads the status line and headers up to and including
// the empty line, leaving everything after it in br.
func readHeaderSection(br *bufio.Reader) (string, error) {
	var b []byte
	for {
		line, err := br.ReadSlice('\n')
		b = append(b, line...)
		if len(b) > maxResponseHeader {
			return "", errs.New(errs.Protocol, "The upgrade response headers are larger than 64 KiB.").
				WithCode(CodeMalformedResponse)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(b) == 0 {
				return "", io.EOF
			}
			return "", io.ErrUnexpectedEOF
		case err != nil:
			return "", err
		}
		if string(line) == "\r\n" || string(line) == "\n" {
			return string(b), nil
		}
	}
}

// parseResponse parses the status line and headers, keeping order and
// spelling.
func parseResponse(raw, host string) (*Response, error) {
	malformed := func(msg string) error {
		return handshakeFailure(CodeMalformedResponse, host, "The server at "+host+" sent an upgrade response that is not valid HTTP: "+msg, HintNotHTTP)
	}
	lines := strings.Split(strings.TrimRight(raw, "\r\n"), "\n")
	status := strings.TrimSuffix(lines[0], "\r")
	proto, rest, ok := strings.Cut(status, " ")
	if !ok || !strings.HasPrefix(proto, "HTTP/1.") || len(proto) != 8 {
		return nil, malformed("the status line is " + strconv.Quote(truncate(status, 80)) + ".")
	}
	codeStr, _, _ := strings.Cut(rest, " ")
	code, err := strconv.Atoi(codeStr)
	if err != nil || len(codeStr) != 3 || code < 100 {
		return nil, malformed("the status code is " + strconv.Quote(truncate(codeStr, 20)) + ".")
	}
	resp := &Response{Proto: proto, StatusCode: code, Status: rest, RawHeader: raw, Body: []byte{}}
	for _, l := range lines[1:] {
		l = strings.TrimSuffix(l, "\r")
		if l == "" {
			continue
		}
		if l[0] == ' ' || l[0] == '\t' {
			return nil, malformed("a header is folded over several lines.")
		}
		name, value, ok := strings.Cut(l, ":")
		if !ok || !isToken(name) {
			return nil, malformed("the header line " + strconv.Quote(truncate(l, 80)) + " is not valid.")
		}
		resp.Headers = append(resp.Headers, httpclient.Header{Name: name, Value: strings.Trim(value, " \t"), Enabled: true})
	}
	return resp, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// readBody reads the body of a rejected upgrade, capped, framed as HTTP
// (Content-Length, chunked, or up to the end of the connection). Read
// errors keep what arrived.
func (c *Client) readBody(rawHeader string, br *bufio.Reader) ([]byte, bool) {
	limit := c.o.MaxResponseBody
	switch {
	case limit == 0:
		limit = DefaultMaxResponseBody
	case limit < 0:
		return []byte{}, false
	}
	resp, err := http.ReadResponse(bufio.NewReader(io.MultiReader(strings.NewReader(rawHeader), br)), &http.Request{Method: http.MethodGet})
	if err != nil {
		return []byte{}, false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if int64(len(body)) > limit {
		return body[:limit], true
	}
	if body == nil {
		body = []byte{}
	}
	return body, false
}

// hasToken reports whether a comma-separated header list holds token (any
// case).
func hasToken(values []string, token string) bool {
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// checkUpgrade validates a 101 response (RFC 6455 §4.1, client
// requirements 1–6).
func (c *Client) checkUpgrade(resp *Response, key string, hs *Handshake) error {
	host := c.t.dialAddr
	if !hasToken(resp.Header("Upgrade"), "websocket") {
		return handshakeFailure(CodeInvalidUpgrade, host,
			"The server at "+host+" answered 101 without \"Upgrade: websocket\".", HintNotWebSocket)
	}
	if !hasToken(resp.Header("Connection"), "upgrade") {
		return handshakeFailure(CodeInvalidUpgrade, host,
			"The server at "+host+" answered 101 without \"Connection: Upgrade\".", HintNotWebSocket)
	}
	accept := resp.Header("Sec-WebSocket-Accept")
	if want := wsframe.ComputeAccept(key); len(accept) != 1 || accept[0] != want {
		got := "no Sec-WebSocket-Accept header"
		if len(accept) > 0 {
			got = "Sec-WebSocket-Accept " + strconv.Quote(truncate(strings.Join(accept, ", "), 80))
		}
		return handshakeFailure(CodeInvalidAccept, host,
			"The server at "+host+" sent "+got+", but the key requires "+strconv.Quote(want)+".",
			"The server's WebSocket handshake is broken, or a proxy changed the request.")
	}
	if ext := resp.Header("Sec-WebSocket-Extensions"); len(ext) > 0 && strings.TrimSpace(strings.Join(ext, "")) != "" {
		return handshakeFailure(CodeUnrequestedExtension, host,
			"The server at "+host+" selected the extension "+strconv.Quote(truncate(strings.Join(ext, ", "), 80))+", which was not requested.",
			"PostSonde does not offer WebSocket extensions (such as permessage-deflate) yet.")
	}
	proto := resp.Header("Sec-WebSocket-Protocol")
	if len(proto) > 0 {
		selected := strings.Join(proto, ", ")
		ok := len(proto) == 1
		if ok {
			ok = false
			for _, p := range c.o.Subprotocols {
				if p == selected {
					ok = true
				}
			}
		}
		if !ok {
			return handshakeFailure(CodeUnrequestedSubprotocol, host,
				"The server at "+host+" selected the subprotocol "+strconv.Quote(truncate(selected, 80))+", which was not requested.",
				"Add the subprotocol to the list, or check the server.").
				WithDetail("subprotocol", selected)
		}
		hs.Subprotocol = selected
	}
	return nil
}
