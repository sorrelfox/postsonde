package system

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func TestInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build Build
		want  InfoDTO
	}{
		{
			name:  "full build",
			build: Build{AppName: "PostSonde", Version: "v1.2.3", WailsVersion: "v3.0.0"},
			want:  InfoDTO{Name: "PostSonde", Version: "v1.2.3", WailsVersion: "v3.0.0"},
		},
		{
			name:  "missing details",
			build: Build{},
			want:  InfoDTO{Name: unknown, Version: unknown, WailsVersion: unknown},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := New(tt.build, nil).Info(t.Context())
			if err != nil {
				t.Fatalf("Info: %v", err)
			}
			tt.want.OS = runtime.GOOS
			tt.want.Arch = runtime.GOARCH
			tt.want.GoVersion = runtime.Version()
			if got != tt.want {
				t.Errorf("Info = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestInfoCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := New(Build{}, nil).Info(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Info with a cancelled context: err = %v, want context.Canceled", err)
	}
	if got := errs.KindOf(err); got != errs.Cancelled {
		t.Errorf("Info with a cancelled context: kind = %q, want %q", got, errs.Cancelled)
	}
}

func TestWait(t *testing.T) {
	t.Parallel()

	// cancelled returns a context that is cancelled after d (immediately
	// for d == 0), the way the frontend cancels a running call.
	cancelled := func(d time.Duration) func(*testing.T) context.Context {
		return func(t *testing.T) context.Context {
			ctx, cancel := context.WithCancel(t.Context())
			if d == 0 {
				cancel()
			} else {
				time.AfterFunc(d, cancel)
			}
			t.Cleanup(cancel)
			return ctx
		}
	}
	timeout := func(d time.Duration) func(*testing.T) context.Context {
		return func(t *testing.T) context.Context {
			ctx, cancel := context.WithTimeout(t.Context(), d)
			t.Cleanup(cancel)
			return ctx
		}
	}
	background := func(t *testing.T) context.Context { return t.Context() }

	tests := []struct {
		name       string
		ctx        func(*testing.T) context.Context
		ms         int
		wantKind   errs.Kind // "" when Wait should succeed
		wantIs     error     // also matched with errors.Is when set
		maxElapsed time.Duration
	}{
		{name: "completes", ctx: background, ms: 20, maxElapsed: 5 * time.Second},
		{name: "zero", ctx: background, ms: 0, maxElapsed: 5 * time.Second},
		{name: "negative", ctx: background, ms: -1, wantKind: errs.InvalidArgument, maxElapsed: time.Second},
		{name: "too long", ctx: background, ms: MaxWaitMs + 1, wantKind: errs.InvalidArgument, maxElapsed: time.Second},
		{name: "cancelled while waiting", ctx: cancelled(20 * time.Millisecond), ms: MaxWaitMs, wantKind: errs.Cancelled, wantIs: context.Canceled, maxElapsed: 5 * time.Second},
		{name: "cancelled before the call", ctx: cancelled(0), ms: MaxWaitMs, wantKind: errs.Cancelled, wantIs: context.Canceled, maxElapsed: 5 * time.Second},
		{name: "deadline", ctx: timeout(20 * time.Millisecond), ms: MaxWaitMs, wantKind: errs.Timeout, wantIs: context.DeadlineExceeded, maxElapsed: 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := New(Build{}, nil)
			start := time.Now()
			got, err := s.Wait(tt.ctx(t), tt.ms)
			elapsed := time.Since(start)

			if elapsed > tt.maxElapsed {
				t.Errorf("Wait took %v, want at most %v", elapsed, tt.maxElapsed)
			}
			switch {
			case tt.wantKind != "":
				if err == nil {
					t.Fatalf("Wait(%d) succeeded, want a %s error", tt.ms, tt.wantKind)
				}
				if _, ok := errors.AsType[*errs.Error](err); !ok {
					t.Errorf("Wait returned a %T, want an *errs.Error", err)
				}
				if kind := errs.KindOf(err); kind != tt.wantKind {
					t.Errorf("Wait: kind = %q, want %q (err: %v)", kind, tt.wantKind, err)
				}
				if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
					t.Errorf("Wait: err = %v, want it to wrap %v", err, tt.wantIs)
				}
				if got != (WaitResultDTO{}) {
					t.Errorf("Wait returned %+v with an error, want the zero value", got)
				}
			default:
				if err != nil {
					t.Fatalf("Wait: %v", err)
				}
				if got.RequestedMs != tt.ms {
					t.Errorf("RequestedMs = %d, want %d", got.RequestedMs, tt.ms)
				}
				if got.ElapsedMs < int64(tt.ms) {
					t.Errorf("ElapsedMs = %d, want at least %d", got.ElapsedMs, tt.ms)
				}
				if _, err := time.Parse(time.RFC3339Nano, got.FinishedAt); err != nil {
					t.Errorf("FinishedAt %q is not RFC 3339: %v", got.FinishedAt, err)
				}
			}
		})
	}
}

// TestWaitOutOfRange checks the invalid_argument error the UI shows for a
// bad ms: user-facing message, code, actionable hint and the field name.
func TestWaitOutOfRange(t *testing.T) {
	t.Parallel()

	for _, ms := range []int{-1, MaxWaitMs + 1} {
		t.Run(strconv.Itoa(ms), func(t *testing.T) {
			t.Parallel()

			_, err := New(Build{}, nil).Wait(t.Context(), ms)
			e, ok := errors.AsType[*errs.Error](err)
			if !ok {
				t.Fatalf("Wait(%d) = %v, want an *errs.Error", ms, err)
			}
			if e.Kind != errs.InvalidArgument || e.Code != CodeWaitOutOfRange {
				t.Errorf("kind %q, code %q, want %q, %q", e.Kind, e.Code, errs.InvalidArgument, CodeWaitOutOfRange)
			}
			if !strings.Contains(e.Message, strconv.Itoa(ms)) {
				t.Errorf("Message %q does not mention the value %d", e.Message, ms)
			}
			if !strings.Contains(e.Hint, "0 to 60000 ms") {
				t.Errorf("Hint %q does not give the valid range", e.Hint)
			}
			want := map[string]string{"field": "ms", "min": "0", "max": "60000"}
			if !maps.Equal(e.Details, want) {
				t.Errorf("Details = %v, want %v", e.Details, want)
			}
			if e.Err != nil {
				t.Errorf("cause = %v, want none", e.Err)
			}
		})
	}
}

func TestFormatTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"zero", time.Time{}, ""},
		{"utc", time.Date(2026, 9, 15, 10, 30, 0, 0, time.UTC), "2026-09-15T10:30:00Z"},
		{"converted to utc", time.Date(2026, 9, 15, 12, 30, 0, 0, time.FixedZone("CEST", 2*60*60)), "2026-09-15T10:30:00Z"},
		{"sub-second", time.Date(2026, 9, 15, 10, 30, 0, 250_000_000, time.UTC), "2026-09-15T10:30:00.25Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatTime(tt.in); got != tt.want {
				t.Errorf("formatTime = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDTOJSON pins the JSON shape of the DTOs, which is the contract with the
// generated TypeScript models: camelCase names, no omitted fields.
func TestDTOJSON(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 15, 10, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		dto  any
	}{
		{"info", InfoDTO{Name: "PostSonde", Version: "v1.2.3", OS: "darwin", Arch: "arm64", GoVersion: "go1.27.0", WailsVersion: "v3.0.0"}},
		{"wait_result", toWaitResultDTO(3000, start, start.Add(3001*time.Millisecond))},
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
