package netxtest

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx/listen"
)

// HTTPProxyServer is an in-process HTTP proxy for tests (NET-02): CONNECT
// tunnels and absolute-form requests, with optional Basic authentication
// and optional TLS (an https:// proxy). It records every request it gets,
// Proxy-Authorization included, so a test can check what went to the
// proxy.
//
// Targets are relayed like SOCKS5Server's: an IP address as it is, a host
// name through Hosts only, never DNS. A CONNECT to an unknown name gets
// 502, a refused one 502 too; an absolute-form request to one gets 502.
type HTTPProxyServer struct {
	// Username and Password, when Username is set, are the only
	// credentials the proxy accepts (Basic); without them it answers 407.
	Username, Password string
	// Challenge is the Proxy-Authenticate value of a 407; "" means
	// `Basic realm="test proxy"`.
	Challenge string
	// Hosts maps the host names the proxy can "resolve" to IP addresses.
	Hosts map[string]string
	// TLS, when set, makes it an https:// proxy: clients speak TLS to the
	// proxy itself.
	TLS *tls.Config
	// ConnectStatus, when not 0, is the status every CONNECT is answered
	// with instead of connecting (403, 502...).
	ConnectStatus int
	// AfterConnect, when set, is written to the client right after a
	// successful CONNECT answer, in the same write (a server speaking
	// first).
	AfterConnect []byte

	addr     string
	accepted atomic.Int32

	mu       sync.Mutex
	requests []HTTPProxyRequest
}

// HTTPProxyRequest is one request the proxy received.
type HTTPProxyRequest struct {
	// Method is "CONNECT", or the method of an absolute-form request.
	Method string
	// Target is the request-target as received: host:port for CONNECT,
	// the absolute URL otherwise.
	Target string
	// Host is the Host header field.
	Host string
	// Authorization is the Proxy-Authorization field, "" when none came.
	Authorization string
	// Header is the request's other header fields as received, without
	// Proxy-Authorization.
	Header http.Header
}

// Start listens on 127.0.0.1 and serves until the test ends; it returns
// the proxy's host:port.
func (s *HTTPProxyServer) Start(t testing.TB) string {
	t.Helper()
	ln, _, err := listen.Listen(context.Background(), listen.Spec{Host: "127.0.0.1"})
	if err != nil {
		t.Fatalf("http proxy test server: %v", err)
	}
	s.addr = ln.Addr().String()
	if s.TLS != nil {
		ln = tls.NewListener(ln, s.TLS)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	t.Cleanup(func() {
		close(done)
		ln.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				stop := make(chan struct{})
				defer close(stop)
				go func() {
					select {
					case <-done:
						c.Close()
					case <-stop:
					}
				}()
				s.serve(c, done)
			}()
		}
	}()
	return s.addr
}

// Addr is the proxy's host:port, once started.
func (s *HTTPProxyServer) Addr() string { return s.addr }

// Accepted is the number of connections the proxy has accepted.
func (s *HTTPProxyServer) Accepted() int { return int(s.accepted.Load()) }

// Requests returns the requests received so far, oldest first.
func (s *HTTPProxyServer) Requests() []HTTPProxyRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]HTTPProxyRequest(nil), s.requests...)
}

func (s *HTTPProxyServer) serve(c net.Conn, stop <-chan struct{}) {
	br := bufio.NewReader(c)
	for {
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		r := HTTPProxyRequest{Method: req.Method, Target: req.RequestURI, Host: req.Host, Authorization: req.Header.Get("Proxy-Authorization")}
		req.Header.Del("Proxy-Authorization")
		r.Header = req.Header.Clone()
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.mu.Unlock()

		if !s.authorized(r.Authorization) {
			challenge := s.Challenge
			if challenge == "" {
				challenge = `Basic realm="test proxy"`
			}
			_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: "+challenge+"\r\nContent-Length: 0\r\n\r\n")
			if req.Method == http.MethodConnect {
				return
			}
			_, _ = io.Copy(io.Discard, req.Body)
			continue
		}
		if req.Method == http.MethodConnect {
			s.tunnel(c, br, req.RequestURI, stop)
			return
		}
		if !s.forward(c, req) {
			return
		}
	}
}

func (s *HTTPProxyServer) authorized(got string) bool {
	if s.Username == "" {
		return true
	}
	return got == "Basic "+base64.StdEncoding.EncodeToString([]byte(s.Username+":"+s.Password))
}

// dialTarget connects to host:port, a name looked up in Hosts only.
func (s *HTTPProxyServer) dialTarget(hostport string) net.Conn {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil
	}
	ip := host
	if net.ParseIP(host) == nil {
		ip = s.Hosts[host]
	}
	if ip == "" {
		return nil
	}
	var d net.Dialer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
	if err != nil {
		return nil
	}
	return conn
}

func (s *HTTPProxyServer) tunnel(c net.Conn, br *bufio.Reader, target string, stop <-chan struct{}) {
	if s.ConnectStatus != 0 {
		_, _ = io.WriteString(c, "HTTP/1.1 "+strconv.Itoa(s.ConnectStatus)+" "+http.StatusText(s.ConnectStatus)+"\r\nContent-Length: 0\r\n\r\n")
		return
	}
	upstream := s.dialTarget(target)
	if upstream == nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer upstream.Close()
	if _, err := c.Write(append([]byte("HTTP/1.1 200 Connection established\r\n\r\n"), s.AfterConnect...)); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	finished := make(chan struct{}, 2)
	relay := func(dst io.Writer, src io.Reader) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		finished <- struct{}{}
	}
	go relay(upstream, br)
	go relay(c, upstream)
	for n := 0; n < 2; {
		select {
		case <-finished:
			n++
		case <-stop:
			c.Close()
			upstream.Close()
			stop = nil
		}
	}
}

// forward sends an absolute-form request to its server in origin form
// and copies the answer back; false ends the client connection.
func (s *HTTPProxyServer) forward(c net.Conn, req *http.Request) bool {
	if !req.URL.IsAbs() || req.URL.Scheme != "http" {
		_, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return false
	}
	hostport := req.URL.Host
	if req.URL.Port() == "" {
		hostport = net.JoinHostPort(req.URL.Hostname(), "80")
	}
	upstream := s.dialTarget(hostport)
	if upstream == nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return false
	}
	defer upstream.Close()
	req.Header.Del("Proxy-Connection")
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header["User-Agent"] = []string{""} // no Go default
	}
	if err := req.Write(upstream); err != nil {
		return false
	}
	resp, err := http.ReadResponse(bufio.NewReader(upstream), req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.Write(c) == nil && !resp.Close
}
