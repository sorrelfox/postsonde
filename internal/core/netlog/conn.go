package netlog

import (
	"net"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// countingConn counts the bytes read and written on a connection and
// records its Close in the log.
type countingConn struct {
	net.Conn
	log   *Log
	r     *record
	close sync.Once
}

// halfCloser is a countingConn over a connection that can shut down its
// sending side (TCP): it keeps CloseWrite and CloseRead visible, so raw TCP
// half-close (tcpclient.Conn.CloseWrite) still works through the log.
type halfCloser struct {
	*countingConn
}

type closeWriter interface {
	CloseWrite() error
	CloseRead() error
}

// datagramConn is a countingConn over a UDP connection (netx.DatagramConn):
// it keeps ReadFrom visible, so the UDP client still learns each
// datagram's sender through the log.
type datagramConn struct {
	*countingConn
}

// wrap returns conn wrapped so its bytes and Close are recorded in r.
func wrap(conn net.Conn, l *Log, r *record) net.Conn {
	c := &countingConn{Conn: conn, log: l, r: r}
	if _, ok := conn.(closeWriter); ok {
		return halfCloser{c}
	}
	if _, ok := conn.(netx.DatagramConn); ok {
		return datagramConn{c}
	}
	return c
}

func (d datagramConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := d.Conn.(netx.DatagramConn).ReadFrom(p)
	if n > 0 {
		d.r.bytesIn.Add(int64(n))
	}
	return n, addr, err
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.r.bytesIn.Add(int64(n))
	}
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.r.bytesOut.Add(int64(n))
	}
	return n, err
}

// Close closes the connection; the first call records it as closed.
func (c *countingConn) Close() error {
	err := c.Conn.Close()
	c.close.Do(func() { c.log.closed(c.r) })
	return err
}

// Unwrap returns the connection underneath, for code that needs it.
func (c *countingConn) Unwrap() net.Conn { return c.Conn }

func (h halfCloser) CloseWrite() error { return h.Conn.(closeWriter).CloseWrite() }
func (h halfCloser) CloseRead() error  { return h.Conn.(closeWriter).CloseRead() }
