package listen

import (
	"errors"
	"net"
	"sync"
)

// multiListener accepts on several listeners at once (127.0.0.1 and ::1 for
// Spec.DualStack). One goroutine per listener feeds an unbuffered channel;
// Close stops them all.
type multiListener struct {
	lns   []net.Listener
	conns chan accepted
	done  chan struct{}
	once  sync.Once
	err   error
	wg    sync.WaitGroup
}

type accepted struct {
	conn net.Conn
	err  error
}

func newMultiListener(lns []net.Listener) *multiListener {
	m := &multiListener{lns: lns, conns: make(chan accepted), done: make(chan struct{})}
	for _, ln := range lns {
		m.wg.Go(func() { m.acceptLoop(ln) })
	}
	return m
}

func (m *multiListener) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		select {
		case m.conns <- accepted{c, err}:
		case <-m.done:
			if c != nil {
				c.Close()
			}
			return
		}
		if err != nil {
			if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
				continue
			}
			return // a closed or broken listener accepts nothing more
		}
	}
}

// Accept returns the next connection from any of the listeners. After
// Close it returns an error matching net.ErrClosed.
func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case <-m.done:
		return nil, m.closedErr()
	case a := <-m.conns:
		select {
		case <-m.done:
			if a.conn != nil {
				a.conn.Close()
			}
			return nil, m.closedErr()
		default:
		}
		return a.conn, a.err
	}
}

func (m *multiListener) closedErr() error {
	return &net.OpError{Op: "accept", Net: m.Addr().Network(), Addr: m.Addr(), Err: net.ErrClosed}
}

// Close closes every listener and waits for the accept goroutines to end.
func (m *multiListener) Close() error {
	m.once.Do(func() {
		close(m.done)
		var es []error
		for _, ln := range m.lns {
			es = append(es, ln.Close())
		}
		m.err = errors.Join(es...)
		m.wg.Wait()
	})
	return m.err
}

// Addr returns the first listener's address.
func (m *multiListener) Addr() net.Addr { return m.lns[0].Addr() }

// Addrs returns every listener's address.
func (m *multiListener) Addrs() []net.Addr {
	out := make([]net.Addr, len(m.lns))
	for i, ln := range m.lns {
		out[i] = ln.Addr()
	}
	return out
}
