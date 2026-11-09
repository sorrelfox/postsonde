package httpclient

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// maxCertPools bounds the connection pools of per-host client
// certificates of one transport; past it the idle ones are closed and
// the pools rebuilt on demand (a certificate file edited over and over).
const maxCertPools = 32

// certRouter sends each https hop through the connection pool of its
// client certificate (TransportOptions.ClientCerts), and every other hop
// through base.
type certRouter struct {
	pick         ClientCertFunc
	base         *http.Transport
	newTransport func(*ClientCert) *http.Transport

	mu    sync.Mutex
	pools map[*tls.Certificate]*http.Transport
}

func (r *certRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return r.base.RoundTrip(req)
	}
	c, err := r.pick(hostPort(req.URL.Hostname(), req.URL.Port(), "443"))
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	if c == nil || c.Certificate == nil {
		return r.base.RoundTrip(req)
	}
	return r.pool(c).RoundTrip(req)
}

func (r *certRouter) pool(c *ClientCert) *http.Transport {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tr, ok := r.pools[c.Certificate]; ok {
		return tr
	}
	if r.pools == nil || len(r.pools) >= maxCertPools {
		for _, tr := range r.pools {
			tr.CloseIdleConnections()
		}
		r.pools = map[*tls.Certificate]*http.Transport{}
	}
	tr := r.newTransport(c)
	r.pools[c.Certificate] = tr
	return tr
}

func (r *certRouter) closeIdle() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tr := range r.pools {
		tr.CloseIdleConnections()
	}
}

// hostPort joins host and port, with def when port is empty.
func hostPort(host, port, def string) string {
	if port == "" {
		port = def
	}
	return net.JoinHostPort(host, port)
}

// ClientCertForURL returns the client certificate f gives for rawURL's
// host and port (443 by default), for the TLS schemes https and wss; nil
// for other schemes, a URL that does not parse, or a nil f.
func ClientCertForURL(f ClientCertFunc, rawURL string) (*ClientCert, error) {
	if f == nil {
		return nil, nil
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return nil, nil
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		return f(hostPort(u.Hostname(), u.Port(), "443"))
	}
	return nil, nil
}
