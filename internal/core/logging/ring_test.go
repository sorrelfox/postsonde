package logging

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

type opCtxKey struct{}

func TestRingKeepsRedactedRecordsNextToTheOutput(t *testing.T) {
	var out bytes.Buffer
	red := NewRedactor()
	red.AddSecret("s3cr3t-value")
	ring := NewRing(RingOptions{Redactor: red})
	logger := New(Options{Output: &out, Redactor: red, Ring: ring})

	logger.Debug("debug only in the ring")
	logger.With("component", "netx").Info("sent s3cr3t-value", "authorization", "Bearer abc", "url", "https://x/?k=s3cr3t-value")

	if strings.Contains(out.String(), "debug only") {
		t.Errorf("the file output took a debug record: %s", out.String())
	}
	if !strings.Contains(out.String(), `"msg":"sent [REDACTED]"`) {
		t.Errorf("the file output changed: %s", out.String())
	}
	got := ring.Entries()
	if len(got) != 2 {
		t.Fatalf("ring has %d entries, want 2", len(got))
	}
	if got[0].Level != slog.LevelDebug || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("entries = %+v", got)
	}
	e := got[1]
	if e.Message != "sent [REDACTED]" || e.Source != "netx" {
		t.Errorf("entry = %+v", e)
	}
	want := []Field{{"component", "netx"}, {"authorization", Mask}, {"url", "https://x/?k=" + Mask}}
	if fmt.Sprint(e.Fields) != fmt.Sprint(want) {
		t.Errorf("fields = %v, want %v", e.Fields, want)
	}
}

func TestRingRedactsSecretsRegisteredLater(t *testing.T) {
	red := NewRedactor()
	ring := NewRing(RingOptions{Redactor: red})
	logger := New(Options{Output: &bytes.Buffer{}, Redactor: red, Ring: ring})
	logger.Info("token is later-secret", "v", "later-secret")
	red.AddSecret("later-secret")
	e := ring.Entries()[0]
	if e.Message != "token is "+Mask || e.Fields[0].Value != Mask {
		t.Errorf("entry = %+v", e)
	}
	if red := ring.Redact(Entry{Message: "later-secret"}); red.Message != Mask {
		t.Errorf("Redact = %+v", red)
	}
}

func TestRingIsBounded(t *testing.T) {
	ring := NewRing(RingOptions{Size: 3})
	logger := slog.New(ring.Handler())
	for i := range 5 {
		logger.Info(fmt.Sprint("m", i))
	}
	got := ring.Entries()
	if len(got) != 3 || got[0].Message != "m2" || got[2].Message != "m4" || got[2].Seq != 5 {
		t.Errorf("entries = %+v", got)
	}
	ring.Clear()
	if n := len(ring.Entries()); n != 0 {
		t.Errorf("after Clear: %d entries", n)
	}
	logger.Info("after")
	if got := ring.Entries(); len(got) != 1 || got[0].Seq != 6 || ring.LastSeq() != 6 {
		t.Errorf("after Clear: %+v", got)
	}
}

func TestRingLevelAndClip(t *testing.T) {
	ring := NewRing(RingOptions{Level: slog.LevelWarn})
	logger := slog.New(ring.Handler())
	logger.Info("skipped")
	logger.Warn(strings.Repeat("é", MaxRingText))
	got := ring.Entries()
	if len(got) != 1 {
		t.Fatalf("entries = %d", len(got))
	}
	if m := got[0].Message; len(m) > MaxRingText+len("…") || !strings.HasSuffix(m, "…") || !strings.HasPrefix(m, "éé") {
		t.Errorf("message not clipped at a rune boundary: %d bytes", len(m))
	}
}

func TestRingOperation(t *testing.T) {
	ring := NewRing(RingOptions{OpFromContext: func(ctx context.Context) (string, string, bool) {
		id, ok := ctx.Value(opCtxKey{}).(string)
		return id, "http.send", ok
	}})
	logger := slog.New(ring.Handler())

	logger.With("opId", "op_1", "domain", "http", "kind", "send").Info("operation finished")
	logger.Debug("dial allowed", "op", "tcp.connect", "opId", "conn_1")
	logger.InfoContext(context.WithValue(context.Background(), opCtxKey{}, "op_2"), "from ctx")
	logger.WithGroup("g").Info("grouped", "opId", "not-top", slog.Group("h", "a", 1))
	logger.Info("domain only", "domain", "grpc", "kind", "")

	got := ring.Entries()
	checks := []struct{ id, kind string }{
		{"op_1", "http.send"},
		{"conn_1", "tcp.connect"},
		{"op_2", "http.send"},
		{"", ""},
		{"", "grpc"},
	}
	for i, c := range checks {
		if got[i].OpID != c.id || got[i].OpKind != c.kind {
			t.Errorf("entry %d (%s): op = %q %q, want %q %q", i, got[i].Message, got[i].OpID, got[i].OpKind, c.id, c.kind)
		}
	}
	if fmt.Sprint(got[3].Fields) != "[{g.opId not-top} {g.h.a 1}]" {
		t.Errorf("group fields = %v", got[3].Fields)
	}
}

func TestRingSubscribe(t *testing.T) {
	ring := NewRing(RingOptions{})
	logger := slog.New(ring.Handler())
	var mu sync.Mutex
	var seqs []uint64
	stop := ring.Subscribe(func(e Entry) {
		mu.Lock()
		seqs = append(seqs, e.Seq)
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				logger.Info("x")
			}
		})
	}
	wg.Wait()
	stop()
	logger.Info("after")
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) != 400 {
		t.Fatalf("got %d records", len(seqs))
	}
	for i, s := range seqs {
		if s != uint64(i+1) {
			t.Fatalf("seq %d at %d: not in order", s, i)
		}
	}
}
