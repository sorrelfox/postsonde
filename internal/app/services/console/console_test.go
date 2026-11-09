package console

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/core/logging"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

type recorder struct {
	mu     sync.Mutex
	events []EntriesEventDTO
}

func (r *recorder) emit(name string, data any) {
	if name != EventEntries {
		panic("unexpected event " + name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, data.(EntriesEventDTO))
}

func (r *recorder) entries() []EntryDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []EntryDTO
	for _, ev := range r.events {
		if len(ev.Entries) == 0 {
			panic("empty event")
		}
		out = append(out, ev.Entries...)
	}
	return out
}

func newLogger(t *testing.T) (*slog.Logger, *logging.Ring, *logging.Redactor) {
	t.Helper()
	red := logging.NewRedactor()
	ring := logging.NewRing(logging.RingOptions{Size: 100, Redactor: red})
	return logging.New(logging.Options{Output: &bytes.Buffer{}, Redactor: red, Ring: ring}), ring, red
}

func TestListAndClear(t *testing.T) {
	t.Parallel()
	logger, ring, _ := newLogger(t)
	s := New(ring, nil, nil)
	logger.Debug("d")
	logger.Info("i", "opId", "op_1", "domain", "http", "kind", "send")
	logger.Warn("w", "password", "hunter22")
	logger.Error("e")

	got := s.List(context.Background())
	if got.Capacity != 100 || len(got.Entries) != 4 {
		t.Fatalf("List = %+v", got)
	}
	levels := []string{LevelDebug, LevelInfo, LevelWarn, LevelError}
	for i, e := range got.Entries {
		if e.Level != levels[i] || e.Seq != uint64(i+1) {
			t.Errorf("entry %d = %+v", i, e)
		}
	}
	if e := got.Entries[1]; e.OpID != "op_1" || e.OpKind != "http.send" {
		t.Errorf("op = %q %q", e.OpID, e.OpKind)
	}
	if f := got.Entries[2].Fields; len(f) != 1 || f[0].Value != logging.Mask {
		t.Errorf("password not redacted: %+v", f)
	}

	s.Clear(context.Background())
	if n := len(s.List(context.Background()).Entries); n != 0 {
		t.Errorf("after Clear: %d entries", n)
	}
}

func TestStream(t *testing.T) {
	t.Parallel()
	logger, ring, red := newLogger(t)
	rec := &recorder{}
	s := New(ring, rec.emit, nil)
	logger.Info("before startup")
	if err := s.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 600 {
		logger.Info("x")
	}
	logger.Info("key is late-secret")
	red.AddSecret("late-secret")
	if err := s.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	logger.Info("after shutdown")

	got := rec.entries()
	if len(got) != 601 {
		t.Fatalf("streamed %d entries, want 601", len(got))
	}
	for i, e := range got {
		if e.Seq != uint64(i+2) {
			t.Fatalf("entry %d has seq %d", i, e.Seq)
		}
	}
	rec.mu.Lock()
	for _, ev := range rec.events {
		if len(ev.Entries) > MaxBatch {
			t.Errorf("batch of %d", len(ev.Entries))
		}
	}
	rec.mu.Unlock()
	if last := got[len(got)-1]; last.Message != "key is "+logging.Mask {
		t.Errorf("late secret not masked: %q", last.Message)
	}
	// Idempotent.
	if err := s.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestLevelName(t *testing.T) {
	t.Parallel()
	for l, want := range map[slog.Level]string{
		slog.LevelDebug - 4: LevelDebug,
		slog.LevelDebug:     LevelDebug,
		slog.LevelInfo + 1:  LevelInfo,
		slog.LevelWarn:      LevelWarn,
		slog.LevelError + 4: LevelError,
	} {
		if got := levelName(l); got != want {
			t.Errorf("levelName(%v) = %q, want %q", l, got, want)
		}
	}
}

// TestDTOJSON pins the JSON shape of the DTOs, the contract with the
// generated TypeScript models.
func TestDTOJSON(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 19, 10, 30, 0, 123e6, time.FixedZone("x", 3600))
	entry := toEntryDTO(logging.Entry{
		Seq: 7, Time: at, Level: slog.LevelWarn, Message: "dial refused: not started by a user operation",
		Source: "netx", OpID: "op_3f9a0c12d4e5b678", OpKind: "http.send",
		Fields: []logging.Field{{Key: "component", Value: "netx"}, {Key: "address", Value: "example.com:443"}},
	})
	tests := []struct {
		name string
		dto  any
	}{
		{"list", ListDTO{Entries: []EntryDTO{entry}, Capacity: 2000}},
		{"entries_event", EntriesEventDTO{Entries: []EntryDTO{entry}}},
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
