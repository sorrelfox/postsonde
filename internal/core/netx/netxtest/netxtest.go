// Package netxtest holds test support for code that dials through netx:
// resolvers that fail or record name lookups in process, so tests of DNS
// never depend on the network or on the machine's resolver configuration,
// and in-process SOCKS5, SOCKS4/4a and HTTP proxies that record what they
// were asked to connect to (SOCKS5Server, SOCKS4Server, HTTPProxyServer).
//
// It is test support only; nothing outside tests imports it.
package netxtest

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
)

// NXDomainResolver returns a pure-Go resolver whose DNS server answers
// every query with NXDOMAIN, over an in-memory connection. Lookups fail
// with a *net.DNSError whose IsNotFound is set. Use it with reserved names
// (RFC 2606 .invalid) in netx.Options.Resolver.
func NXDomainResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go serveNXDomain(server)
			return client, nil
		},
	}
}

// serveNXDomain answers DNS-over-stream queries (2-byte length prefix,
// which the Go resolver uses for connections that are not PacketConns).
func serveNXDomain(c net.Conn) {
	defer c.Close()
	for {
		var n [2]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return
		}
		q := make([]byte, binary.BigEndian.Uint16(n[:]))
		if _, err := io.ReadFull(c, q); err != nil || len(q) < 12 {
			return
		}
		end := 12 // skip the question name, then type and class
		for end < len(q) && q[end] != 0 {
			end += int(q[end]) + 1
		}
		end += 5
		if end > len(q) {
			return
		}
		resp := append([]byte(nil), q[:end]...)
		resp[2] = 0x80 | q[2]&0x01 // QR, keep RD
		resp[3] = 0x80 | 3         // RA, RCODE NXDOMAIN
		clear(resp[6:12])          // keep the question; no answer, authority or additional records
		binary.BigEndian.PutUint16(n[:], uint16(len(resp)))
		if _, err := c.Write(append(n[:], resp...)); err != nil {
			return
		}
	}
}

// FailingResolver returns a pure-Go resolver whose DNS server cannot be
// reached. Lookups fail with a *net.DNSError that is neither a timeout nor
// not found.
func FailingResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("no DNS server in this test")
		},
	}
}

// Lookups records the names a resolver from RecordingResolver was asked
// for. It is safe for concurrent use.
type Lookups struct {
	mu    sync.Mutex
	names []string
}

// Names returns the names queried so far (without the trailing dot, one
// entry per DNS question: a lookup usually asks for A and AAAA).
func (l *Lookups) Names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.names...)
}

func (l *Lookups) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.names = append(l.names, name)
}

// RecordingResolver returns a pure-Go resolver whose DNS server, over an
// in-memory connection, answers A queries for the names in hosts (name to
// IPv4 address) and NXDOMAIN for any other name, and records every name it
// is asked for. Tests use it to prove that a name was, or was not, looked
// up locally. The machine's hosts file is still consulted first by the Go
// resolver, so use names that are not in it (RFC 2606 .test, .invalid).
func RecordingResolver(hosts map[string]string) (*net.Resolver, *Lookups) {
	l := &Lookups{}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go serveRecording(server, hosts, l)
			return client, nil
		},
	}, l
}

func serveRecording(c net.Conn, hosts map[string]string, l *Lookups) {
	defer c.Close()
	for {
		var n [2]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return
		}
		q := make([]byte, binary.BigEndian.Uint16(n[:]))
		if _, err := io.ReadFull(c, q); err != nil || len(q) < 12 {
			return
		}
		var labels []string
		end := 12
		for end < len(q) && q[end] != 0 {
			size := int(q[end])
			if end+1+size > len(q) {
				return
			}
			labels = append(labels, string(q[end+1:end+1+size]))
			end += size + 1
		}
		end += 5
		if end > len(q) {
			return
		}
		name := strings.Join(labels, ".")
		l.add(name)
		qtype := binary.BigEndian.Uint16(q[end-4 : end-2])

		resp := append([]byte(nil), q[:end]...)
		resp[2] = 0x80 | q[2]&0x01 // QR, keep RD
		resp[3] = 0x80             // RA, NOERROR
		clear(resp[6:12])
		ip, known := hosts[strings.ToLower(name)]
		addr, err := netip.ParseAddr(ip)
		switch {
		case !known || err != nil:
			resp[3] = 0x80 | 3 // NXDOMAIN
		case qtype == 1 && addr.Is4(): // A
			binary.BigEndian.PutUint16(resp[6:8], 1)
			a := addr.As4()
			resp = append(resp, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
			resp = append(resp, a[:]...)
		}
		binary.BigEndian.PutUint16(n[:], uint16(len(resp)))
		if _, err := c.Write(append(n[:], resp...)); err != nil {
			return
		}
	}
}
