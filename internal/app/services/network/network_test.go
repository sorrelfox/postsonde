package network

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/netlog"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

type recorder struct {
	mu     sync.Mutex
	events []ActivityEventDTO
}

func (r *recorder) emit(name string, data any) {
	if name != EventActivity {
		panic("unexpected event " + name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, data.(ActivityEventDTO))
}

func (r *recorder) snapshot() []ActivityEventDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ActivityEventDTO(nil), r.events...)
}

// waitFor polls until ok returns true or a second has passed.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func listener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestActivityEvents(t *testing.T) {
	t.Parallel()
	log := netlog.New(netlog.Options{})
	rec := &recorder{}
	s := New(log, rec.emit, nil, WithInterval(5*time.Millisecond))
	if err := s.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	defer s.ServiceShutdown()

	d := netx.Chain(netx.NewDirect(netx.Options{}), log.Middleware(), netx.Guarded(netx.RequireUserOp(nil)))
	addr := listener(t)
	ctx := netx.WithUserOp(t.Context(), netx.UserOp{Kind: "http.send", ID: "op_1"})
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("abc"))

	// While it is open the counts refresh, with the bytes written.
	waitFor(t, "an event with the bytes", func() bool {
		for _, ev := range rec.snapshot() {
			if ev.Active == 1 && len(ev.Conns) == 1 && ev.Conns[0].BytesOut == 3 && ev.Conns[0].State == "open" {
				return true
			}
		}
		return false
	})
	conn.Close()
	waitFor(t, "the close", func() bool {
		evs := rec.snapshot()
		last := evs[len(evs)-1]
		return last.Active == 0 && len(last.Conns) == 1 && last.Conns[0].State == "closed"
	})
	c := rec.snapshot()[0].Conns[0]
	if c.OpKind != "http.send" || c.OpID != "op_1" || c.Address != addr {
		t.Errorf("conn = %+v", c)
	}

	// Idle: no more events.
	n := len(rec.snapshot())
	time.Sleep(30 * time.Millisecond)
	if got := len(rec.snapshot()); got != n {
		t.Errorf("%d events while idle", got-n)
	}

	// A refused dial is reported too.
	if _, err := d.DialContext(t.Context(), "tcp", addr); err == nil {
		t.Fatal("unmarked dial succeeded")
	}
	waitFor(t, "the refusal", func() bool {
		evs := rec.snapshot()
		last := evs[len(evs)-1]
		return len(last.Conns) == 1 && last.Conns[0].State == "refused" && last.Conns[0].ErrorCode == netx.CodeNotUserInitiated
	})

	list := s.List(context.Background())
	if len(list.Conns) != 2 || list.Active != 0 || list.Capacity != netlog.DefaultSize {
		t.Errorf("List = %+v", list)
	}
	s.Clear(context.Background())
	if n := len(s.List(context.Background()).Conns); n != 0 {
		t.Errorf("after Clear: %d conns", n)
	}

	// After shutdown nothing is sent.
	if err := s.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	n = len(rec.snapshot())
	conn, err = d.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	time.Sleep(30 * time.Millisecond)
	if got := len(rec.snapshot()); got != n {
		t.Errorf("%d events after shutdown", got-n)
	}
}

// TestDTOJSON pins the JSON shape of the DTOs, the contract with the
// generated TypeScript models.
func TestDTOJSON(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 19, 10, 30, 0, 0, time.FixedZone("x", 3600))
	open := toConnDTO(netlog.Conn{
		ID: 3, Op: netx.UserOp{Kind: "http.send", ID: "op_3f9a0c12d4e5b678"},
		Network: "tcp", Address: "example.com:443",
		LocalAddr: "192.168.1.20:53211", RemoteAddr: "10.0.0.1:1080", Via: "socks5h://proxy.lan:1080",
		State: netlog.Closed, Started: at, Connected: at.Add(42500 * time.Microsecond), Ended: at.Add(2 * time.Second),
		BytesIn: 5120, BytesOut: 812,
	})
	refused := toConnDTO(netlog.Conn{
		ID: 4, Network: "tcp", Address: "example.com:443", State: netlog.Refused,
		Started: at, Ended: at.Add(time.Millisecond),
		ErrCode: netx.CodeNotUserInitiated, Err: "PostSonde did not connect to example.com:443 because no action of yours started the connection.",
	})
	tests := []struct {
		name string
		dto  any
	}{
		{"list", ListDTO{Conns: []ConnDTO{open, refused}, Active: 0, Capacity: 500}},
		{"activity_event", ActivityEventDTO{Active: 1, Conns: []ConnDTO{open}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(tt.dto, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "dto/"+tt.name, append(got, '\n'))
		})
	}
}
