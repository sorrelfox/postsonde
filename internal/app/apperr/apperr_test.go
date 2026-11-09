package apperr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// TestErrorDTOJSON pins the JSON shape of representative errors: the
// contract with AppError in frontend/src/lib/errors.ts, whose tests read
// these golden files too.
func TestErrorDTOJSON(t *testing.T) {
	t.Parallel()

	pathErr := &fs.PathError{Op: "open", Path: "/cfg/settings.json", Err: syscall.EACCES}
	tests := []struct {
		name string
		err  error
	}{
		{"invalid_argument", errs.New(errs.InvalidArgument, "The wait time must be between 0 and 60000 ms, not -1 ms.").
			WithCode("system.wait_out_of_range").
			WithHint("Enter a value from 0 to 60000 ms.").
			WithDetail("field", "ms").WithDetail("min", "0").WithDetail("max", "60000")},
		{"io_with_causes", errs.Wrap(pathErr, errs.IO, "Could not save the settings.").
			WithHint("Check that PostSonde can write to its settings folder.")},
		{"cancelled", errs.Wrap(context.Canceled, errs.Cancelled, "The wait was cancelled after 1200 ms.")},
		{"unwrapped_timeout", fmt.Errorf("dial api.example.test: %w", context.DeadlineExceeded)},
		{"unknown", fmt.Errorf("render template: %w", errors.New("unexpected nil map"))},
		{"joined", errors.Join(
			errs.New(errs.InvalidArgument, "Name is required.").WithDetail("field", "name"),
			errs.New(errs.InvalidArgument, "URL is required.").WithDetail("field", "url"),
		)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(ToDTO(tt.err), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "error/"+tt.name, append(got, '\n'))
		})
	}
}

func TestToDTO(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want ErrorDTO
	}{
		{
			name: "nil",
			err:  nil,
			want: ErrorDTO{Kind: "internal", Message: DefaultMessage(errs.Internal)},
		},
		{
			name: "*Error wrapped by fmt",
			err:  fmt.Errorf("settings: %w", errs.New(errs.InvalidArgument, "Bad theme.").WithHint("Pick dark or light.")),
			want: ErrorDTO{Kind: "invalid_argument", Message: "Bad theme.", Hint: "Pick dark or light.", Causes: []string{"settings"}},
		},
		{
			name: "*Error without a message",
			err:  &errs.Error{Kind: errs.NotFound},
			want: ErrorDTO{Kind: "not_found", Message: DefaultMessage(errs.NotFound)},
		},
		{
			name: "plain context error",
			err:  context.Canceled,
			want: ErrorDTO{Kind: "cancelled", Message: DefaultMessage(errs.Cancelled), Causes: []string{"context canceled"}},
		},
		{
			name: "unknown error keeps raw text out of the message",
			err:  errors.New("pq: password authentication failed"),
			want: ErrorDTO{Kind: "internal", Message: DefaultMessage(errs.Internal), Causes: []string{"pq: password authentication failed"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ToDTO(tt.err)
			if got.Details == nil || got.Causes == nil {
				t.Fatalf("ToDTO returned nil Details or Causes: %+v", got)
			}
			if tt.want.Details == nil {
				tt.want.Details = map[string]string{}
			}
			if tt.want.Causes == nil {
				tt.want.Causes = []string{}
			}
			if got.Kind != tt.want.Kind || got.Code != tt.want.Code || got.Message != tt.want.Message ||
				got.Hint != tt.want.Hint || !slices.Equal(got.Causes, tt.want.Causes) || len(got.Details) != len(tt.want.Details) {
				t.Errorf("ToDTO =\n  %+v\nwant\n  %+v", got, tt.want)
			}
		})
	}
}

func TestToDTOCopiesDetails(t *testing.T) {
	t.Parallel()

	e := errs.New(errs.InvalidArgument, "x").WithDetail("field", "a")
	dto := ToDTO(e)
	dto.Details["field"] = "changed"
	if e.Details["field"] != "a" {
		t.Error("ToDTO shares the Details map with the error")
	}
}

func TestDefaultMessages(t *testing.T) {
	t.Parallel()

	for _, k := range errs.Kinds() {
		if _, ok := defaultMessages[k]; !ok {
			t.Errorf("no default message for kind %q", k)
		}
		if got := ToDTO(&errs.Error{Kind: k}); got.Kind != string(k) || got.Message != defaultMessages[k] {
			t.Errorf("ToDTO(kind %q) = %+v, want its default message", k, got)
		}
	}
	if DefaultMessage("bogus") != defaultMessages[errs.Internal] {
		t.Error("DefaultMessage of an unknown kind is not the internal message")
	}
}

func TestFallbackJSON(t *testing.T) {
	t.Parallel()

	var dto ErrorDTO
	if err := json.Unmarshal(fallbackJSON, &dto); err != nil {
		t.Fatalf("fallbackJSON is not valid: %v", err)
	}
	if want := ToDTO(nil); dto.Kind != want.Kind || dto.Message != want.Message {
		t.Errorf("fallbackJSON = %+v, want %+v", dto, want)
	}
}

func TestMarshaler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		wantLevel string
	}{
		{"internal logged as error", fmt.Errorf("render: %w", errors.New("nil map")), "ERROR"},
		{"expected error logged at debug", errs.New(errs.InvalidArgument, "Bad."), "DEBUG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			data := Marshaler(logger)(tt.err)

			var got ErrorDTO
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Marshaler output is not JSON: %v\n%s", err, data)
			}
			if want := ToDTO(tt.err); got.Kind != want.Kind || got.Message != want.Message || !slices.Equal(got.Causes, want.Causes) {
				t.Errorf("Marshaler = %+v, want %+v", got, want)
			}
			line := logs.String()
			if !strings.Contains(line, `"level":"`+tt.wantLevel+`"`) {
				t.Errorf("log level: want %s in %s", tt.wantLevel, line)
			}
			if !strings.Contains(line, tt.err.Error()) {
				t.Errorf("log does not contain the full error %q: %s", tt.err.Error(), line)
			}
		})
	}
}
