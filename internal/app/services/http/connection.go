package http

import (
	"slices"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Phases of HopTimingDTO, in wire order (httpclient.Timing).
const (
	PhaseDNS          = "dns"
	PhaseConnect      = "connect"
	PhaseProxy        = "proxy"
	PhaseTLS          = "tls"
	PhaseRequestWrite = "request_write"
	PhaseWait         = "wait"
	PhaseDownload     = "download"
)

// Why a PhaseDTO was not observed (PhaseDTO.Skipped).
const (
	// SkipReused: DNS, connect and TLS on a reused connection.
	SkipReused = "reused"
	// SkipNoLookup: DNS for an IP address.
	SkipNoLookup = "no_lookup"
	// SkipNoTLS: TLS on an http:// connection.
	SkipNoTLS = "no_tls"
	// SkipNotFinal: download on a redirect or auth hop, whose body
	// net/http drains itself.
	SkipNotFinal = "not_final"
)

// HopTimingDTO is where the time of one hop went (DBG-03): its phases, for
// a waterfall.
type HopTimingDTO struct {
	// TotalMs is the hop's span: from its start to the start of the next
	// hop, or, for the final hop, to the last body byte read.
	TotalMs float64 `json:"totalMs"`
	// Phases are the six phases, in order: dns, connect, tls,
	// request_write (request sent), wait (time to first byte), download;
	// and, on a new connection through a proxy tunnel, proxy (the proxy's
	// TLS and handshake, SOCKS-12) between connect and tls.
	Phases []PhaseDTO `json:"phases"`
}

// PhaseDTO is one phase of a hop.
type PhaseDTO struct {
	// Phase is one of the Phase* names.
	Phase string `json:"phase"`
	// Observed is set when the phase happened; StartMs (from the start of
	// the hop) and DurationMs are then its span, else both are 0.
	Observed   bool    `json:"observed"`
	StartMs    float64 `json:"startMs"`
	DurationMs float64 `json:"durationMs"`
	// Skipped says why a phase did not happen: "reused", "no_lookup",
	// "no_tls", "not_final", or "" when it was observed or the reason is
	// not known.
	Skipped string `json:"skipped"`
}

// ConnInfoDTO is the connection a hop went over (DBG-04, DBG-05). It
// holds no secret: addresses and the server's certificates.
type ConnInfoDTO struct {
	// Reused is set for a kept-alive connection (HTTP/1.1) or a new stream
	// on an open one (h2); WasIdle when it was idle, for IdleMs.
	Reused  bool    `json:"reused"`
	WasIdle bool    `json:"wasIdle"`
	IdleMs  float64 `json:"idleMs"`
	// LocalAddr and RemoteAddr are "ip:port"; "" when not known.
	LocalAddr  string `json:"localAddr"`
	RemoteAddr string `json:"remoteAddr"`
	// Protocol is "h2" or "HTTP/1.1"; "" when not known.
	Protocol string `json:"protocol"`
	// Via is the proxy the connection goes through ("socks5h://proxy:1080",
	// "http://proxy:3128", never with credentials), new or reused; "" for a
	// direct one (NET-06).
	Via string `json:"via"`
	// Proxy is the hop through a proxy of a new connection (SOCKS-12),
	// null for a direct or reused one.
	Proxy *ProxyHopDTO `json:"proxy"`
	// TLS is the TLS session, null for http.
	TLS *TLSInfoDTO `json:"tls"`
}

// ProxyHopDTO is how a connection went through a proxy (netx.ProxyHop,
// SOCKS-12). It holds no credential.
type ProxyHopDTO struct {
	// Scheme is socks5, socks5h, http or https; Version the protocol
	// spoken with the proxy ("SOCKS5", "HTTP/1.1").
	Scheme  string `json:"scheme"`
	Version string `json:"version"`
	// Proxy is the proxy's host:port as configured, ProxyAddr the address
	// connected to ("" when the connection to it failed).
	Proxy     string `json:"proxy"`
	ProxyAddr string `json:"proxyAddr"`
	// Target is the host:port asked of the proxy.
	Target string `json:"target"`
	// Auth is "none", "username/password" or "basic"; "" when not chosen.
	Auth string `json:"auth"`
	// RemoteDNS is set when the proxy resolves the target's host name.
	RemoteDNS bool `json:"remoteDns"`
	// Reply is the proxy's answer to the connect request (the SOCKS
	// reply code or the CONNECT's HTTP status), null when none came;
	// ReplyText its meaning.
	Reply     *int   `json:"reply"`
	ReplyText string `json:"replyText"`
	// Forward is set for an HTTP proxy connection carrying absolute-form
	// requests (no CONNECT).
	Forward bool `json:"forward"`
	// LookupMs, ConnectMs, TLSMs and HandshakeMs are the local lookup of
	// the target, the connection to the proxy, the TLS handshake with an
	// https:// proxy and the proxy protocol; 0 for a step that did not
	// happen.
	LookupMs    float64 `json:"lookupMs"`
	ConnectMs   float64 `json:"connectMs"`
	TLSMs       float64 `json:"tlsMs"`
	HandshakeMs float64 `json:"handshakeMs"`
}

// proxyHopDTO converts a proxy hop; nil for nil.
func proxyHopDTO(h *netx.ProxyHop) *ProxyHopDTO {
	if h == nil {
		return nil
	}
	d := &ProxyHopDTO{
		Scheme: h.Scheme, Version: h.Version, Proxy: h.Proxy, ProxyAddr: h.ProxyAddr, Target: h.Target,
		Auth: h.Auth, RemoteDNS: h.RemoteDNS, ReplyText: h.ReplyText, Forward: h.Forward,
		LookupMs: ms(h.Lookup), ConnectMs: ms(h.Connect), TLSMs: ms(h.TLS), HandshakeMs: ms(h.Handshake),
	}
	if h.Replied {
		reply := h.Reply
		d.Reply = &reply
	}
	return d
}

// TLSInfoDTO is a TLS session (httpclient.TLSInfo).
type TLSInfoDTO struct {
	// Version is "TLS 1.2" or "TLS 1.3"; CipherSuite the IANA name.
	Version     string `json:"version"`
	CipherSuite string `json:"cipherSuite"`
	// ServerName is the SNI sent, "" for an IP address.
	ServerName string `json:"serverName"`
	// ALPN is the negotiated protocol: "h2", "http/1.1" or "".
	ALPN string `json:"alpn"`
	// Resumed is set for a resumed session.
	Resumed bool `json:"resumed"`
	// Certificates is the chain the server sent, leaf first.
	Certificates []CertificateDTO `json:"certificates"`
}

// CertificateDTO summarizes a server certificate.
type CertificateDTO struct {
	Subject string `json:"subject"`
	Issuer  string `json:"issuer"`
	// NotBefore and NotAfter are RFC 3339 times in UTC.
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
	// DNSNames and IPAddresses are the subject alternative names.
	DNSNames    []string `json:"dnsNames"`
	IPAddresses []string `json:"ipAddresses"`
	// Validity is "valid", "expired" or "not_yet_valid" at the time of the
	// request, whether or not TLS verification was on.
	Validity string `json:"validity"`
	// SelfSigned is set for a certificate issued by itself.
	SelfSigned bool `json:"selfSigned"`
}

// hopTimingDTO converts the timing of a hop whose span is total; final is
// set for the last hop, conn is its connection.
func hopTimingDTO(t httpclient.Timing, total time.Duration, conn httpclient.Conn, final bool) HopTimingDTO {
	o := t.Offsets
	phases := []struct {
		name     string
		p        httpclient.Phases
		off, dur time.Duration
	}{
		{PhaseDNS, httpclient.PhaseDNS, o.DNS, t.DNS},
		{PhaseConnect, httpclient.PhaseConnect, o.Connect, t.Connect},
		{PhaseProxy, httpclient.PhaseProxy, o.Proxy, t.Proxy},
		{PhaseTLS, httpclient.PhaseTLS, o.TLS, t.TLS},
		{PhaseRequestWrite, httpclient.PhaseRequestWrite, o.RequestWrite, t.RequestWrite},
		{PhaseWait, httpclient.PhaseWait, o.Wait, t.Wait},
		{PhaseDownload, httpclient.PhaseDownload, o.Download, t.Download},
	}
	out := HopTimingDTO{TotalMs: ms(max(total, 0)), Phases: make([]PhaseDTO, 0, len(phases))}
	for _, ph := range phases {
		if ph.name == PhaseProxy && !t.Observed.Has(ph.p) {
			continue // only through a proxy tunnel
		}
		d := PhaseDTO{Phase: ph.name}
		if t.Observed.Has(ph.p) {
			d.Observed, d.StartMs, d.DurationMs = true, ms(ph.off), ms(ph.dur)
		} else {
			d.Skipped = skipReason(ph.name, conn, final)
		}
		out.Phases = append(out.Phases, d)
	}
	return out
}

// skipReason says why phase was not observed on a hop over conn.
func skipReason(phase string, conn httpclient.Conn, final bool) string {
	switch phase {
	case PhaseDNS, PhaseConnect, PhaseTLS:
		if conn.Reused {
			return SkipReused
		}
		if phase == PhaseDNS && conn.RemoteAddr != "" {
			return SkipNoLookup
		}
		if phase == PhaseTLS && conn.Protocol != "" && conn.TLS == nil {
			return SkipNoTLS
		}
	case PhaseDownload:
		if !final {
			return SkipNotFinal
		}
	}
	return ""
}

// connInfoDTO converts a hop's connection.
func connInfoDTO(c httpclient.Conn) ConnInfoDTO {
	out := ConnInfoDTO{
		Reused:     c.Reused,
		WasIdle:    c.WasIdle,
		IdleMs:     ms(c.IdleTime),
		LocalAddr:  c.LocalAddr,
		RemoteAddr: c.RemoteAddr,
		Protocol:   c.Protocol,
		Via:        c.Via,
		Proxy:      proxyHopDTO(c.Proxy),
	}
	if t := c.TLS; t != nil {
		out.TLS = &TLSInfoDTO{
			Version:      t.Version,
			CipherSuite:  t.CipherSuite,
			ServerName:   t.ServerName,
			ALPN:         t.NegotiatedProtocol,
			Resumed:      t.DidResume,
			Certificates: make([]CertificateDTO, 0, len(t.PeerCertificates)),
		}
		for _, cert := range t.PeerCertificates {
			out.TLS.Certificates = append(out.TLS.Certificates, CertificateDTO{
				Subject:     cert.Subject,
				Issuer:      cert.Issuer,
				NotBefore:   cert.NotBefore.UTC().Format(time.RFC3339),
				NotAfter:    cert.NotAfter.UTC().Format(time.RFC3339),
				DNSNames:    stringsOrEmpty(cert.DNSNames),
				IPAddresses: stringsOrEmpty(cert.IPAddresses),
				Validity:    cert.Validity,
				SelfSigned:  cert.SelfSigned,
			})
		}
	}
	return out
}

// stringsOrEmpty returns a copy of s, empty rather than nil.
func stringsOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}
