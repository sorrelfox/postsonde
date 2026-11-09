package errs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	t.Parallel()
	mask := func(s string) string { return strings.ReplaceAll(s, "s3cret", "***") }

	// A transport error that repeats a URL holding a secret, as net/http
	// returns it, wrapped the way httpclient does.
	cause := &url.Error{Op: "Get", URL: "https://api.test/x?key=s3cret", Err: io.EOF}
	err := Wrap(cause, Network, "The server at api.test closed the connection.").
		WithCode("httpclient.connection_closed").
		WithHint("Check the server.").
		WithDetail("stage", "waiting").
		WithDetail("target", "/x?key=s3cret")

	got := Redact(err, mask)
	if strings.Contains(got.Error(), "s3cret") {
		t.Errorf("Error() = %q", got.Error())
	}
	for _, c := range Chain(got) {
		if strings.Contains(c, "s3cret") {
			t.Errorf("Chain = %q", Chain(got))
		}
	}
	if want := []string{"The server at api.test closed the connection.", `Get "https://api.test/x?key=***"`, "EOF"}; !slices.Equal(Chain(got), want) {
		t.Errorf("Chain = %q, want %q", Chain(got), want)
	}
	var e *Error
	if !errors.As(got, &e) || e.Kind != Network || e.Code != "httpclient.connection_closed" || e.Hint != "Check the server." ||
		e.Details["stage"] != "waiting" || e.Details["target"] != "/x?key=***" {
		t.Errorf("redacted = %#v", e)
	}
	if !errors.Is(got, io.EOF) || KindOf(got) != Network {
		t.Errorf("errors.Is(EOF) = %v, KindOf = %s", errors.Is(got, io.EOF), KindOf(got))
	}
	// The original is left as it was.
	if !strings.Contains(err.Error(), "s3cret") || err.Details["target"] != "/x?key=s3cret" {
		t.Errorf("the original changed: %v", err)
	}
}

func TestRedactUnchanged(t *testing.T) {
	t.Parallel()
	mask := func(s string) string { return strings.ReplaceAll(s, "s3cret", "***") }
	for _, err := range []error{
		nil,
		New(InvalidArgument, "Bad."),
		Wrap(fmt.Errorf("open x: %w", io.EOF), IO, "Could not read."),
		errors.Join(io.EOF, context.Canceled),
	} {
		if got := Redact(err, mask); got != err {
			t.Errorf("Redact(%v) = %v, want the same error", err, got)
		}
	}
	err := New(Internal, "s3cret")
	if got := Redact(err, nil); got != err {
		t.Errorf("Redact with no function = %v", got)
	}
}

func TestRedactOtherShapes(t *testing.T) {
	t.Parallel()
	mask := func(s string) string { return strings.ReplaceAll(s, "s3cret", "***") }
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"plain", errors.New("token s3cret refused"), "token *** refused"},
		{"wrapped", fmt.Errorf("sending s3cret: %w", context.Canceled), "sending ***: context canceled"},
		{"join", errors.Join(errors.New("a s3cret"), io.EOF), "a ***\nEOF"},
		{"cause only", Wrap(errors.New("s3cret"), Internal, "Failed."), "Failed.: ***"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Redact(tt.err, mask)
			if got.Error() != tt.want {
				t.Errorf("Error() = %q, want %q", got.Error(), tt.want)
			}
		})
	}
	// errors.Is keeps working through a redacted wrapper.
	if got := Redact(fmt.Errorf("sending s3cret: %w", context.Canceled), mask); !errors.Is(got, context.Canceled) {
		t.Errorf("errors.Is(Canceled) = false for %v", got)
	}
}
