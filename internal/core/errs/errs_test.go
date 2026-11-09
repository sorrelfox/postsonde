package errs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"slices"
	"syscall"
	"testing"
)

func TestKinds(t *testing.T) {
	t.Parallel()

	got := Kinds()
	if len(got) != 13 {
		t.Fatalf("Kinds() has %d kinds, want 13 (update docs/errors.md and lib/errors.ts when adding one)", len(got))
	}
	seen := map[Kind]bool{}
	for _, k := range got {
		if !k.Valid() {
			t.Errorf("%q is not Valid", k)
		}
		if seen[k] {
			t.Errorf("%q is listed twice", k)
		}
		seen[k] = true
	}
	got[0] = "changed"
	if Kinds()[0] != InvalidArgument {
		t.Error("Kinds() returns the package's slice; callers can modify it")
	}
	for _, k := range []Kind{"", "bogus", "Internal"} {
		if k.Valid() {
			t.Errorf("%q is Valid, want not", k)
		}
	}
}

func TestErrorString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{"message", New(NotFound, "The request does not exist."), "The request does not exist."},
		{"with cause", Wrap(fs.ErrPermission, IO, "Could not save."), "Could not save.: permission denied"},
		{"no message", &Error{Kind: Timeout}, "timeout"},
		{"no message or kind", &Error{}, "internal"},
		{"wrap nil", Wrap(nil, Parse, "Bad JSON."), "Bad JSON."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnwrap(t *testing.T) {
	t.Parallel()

	err := Wrap(context.Canceled, Cancelled, "Stopped.")
	if !errors.Is(err, context.Canceled) {
		t.Error("errors.Is(Wrap(context.Canceled), context.Canceled) = false")
	}
	if New(Internal, "x").Unwrap() != nil {
		t.Error("New(...).Unwrap() != nil")
	}
}

func TestWithCopies(t *testing.T) {
	t.Parallel()

	base := New(InvalidArgument, "Bad value.").WithDetail("field", "a")
	got := base.WithHint("Use 1 to 10.").WithCode("settings.invalid_value").WithDetail("max", "10")

	if got.Hint != "Use 1 to 10." || got.Code != "settings.invalid_value" {
		t.Errorf("got hint %q, code %q", got.Hint, got.Code)
	}
	if got.Details["field"] != "a" || got.Details["max"] != "10" {
		t.Errorf("Details = %v, want field and max", got.Details)
	}
	if base.Hint != "" || base.Code != "" {
		t.Errorf("With* modified the original: %+v", base)
	}
	if _, ok := base.Details["max"]; ok {
		t.Error("WithDetail shares the Details map with the original")
	}
	if got.Kind != InvalidArgument || got.Message != "Bad value." {
		t.Errorf("With* lost fields: %+v", got)
	}
}

func TestIs(t *testing.T) {
	t.Parallel()

	sentinel := New(NotFound, "The request does not exist.").WithCode("workspace.no_request")
	err := fmt.Errorf("open tab: %w", New(NotFound, "Request 42 does not exist.").WithCode("workspace.no_request"))

	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"same code", err, sentinel, true},
		{"kind only", err, &Error{Kind: NotFound}, true},
		{"code only", err, &Error{Code: "workspace.no_request"}, true},
		{"other kind", err, &Error{Kind: IO}, false},
		{"other code", err, New(NotFound, "").WithCode("workspace.no_folder"), false},
		{"empty target", err, &Error{}, false},
		{"not an *Error", err, context.Canceled, false},
		{"empty kind is internal", &Error{Message: "x"}, &Error{Kind: Internal}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := errors.Is(tt.err, tt.target); got != tt.want {
				t.Errorf("errors.Is = %v, want %v", got, tt.want)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestKindOf(t *testing.T) {
	t.Parallel()

	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	tests := []struct {
		name string
		err  error
		want Kind
	}{
		{"nil", nil, ""},
		{"*Error", New(NotFound, "x"), NotFound},
		{"wrapped *Error", fmt.Errorf("load: %w", New(Parse, "x")), Parse},
		{"outermost *Error wins", Wrap(New(NotFound, "inner"), IO, "outer"), IO},
		{"*Error beats context", Wrap(context.Canceled, IO, "x"), IO},
		{"empty kind", &Error{Message: "x"}, Internal},
		{"unknown kind", &Error{Kind: "bogus"}, Internal},
		{"context.Canceled", fmt.Errorf("wait: %w", context.Canceled), Cancelled},
		{"context.DeadlineExceeded", context.DeadlineExceeded, Timeout},
		{"os.ErrDeadlineExceeded", &fs.PathError{Op: "read", Path: "/x", Err: os.ErrDeadlineExceeded}, Timeout},
		{"net timeout", &net.OpError{Op: "read", Net: "tcp", Err: timeoutError{}}, Timeout},
		{"connection refused", refused, Network},
		{"url.Error", &url.Error{Op: "Get", URL: "http://localhost:1", Err: refused}, Network},
		{"url.Error timeout", &url.Error{Op: "Get", URL: "http://x", Err: timeoutError{}}, Timeout},
		{"unknown authority", &url.Error{Op: "Get", URL: "https://x", Err: x509.UnknownAuthorityError{}}, TLS},
		{"hostname mismatch", x509.HostnameError{Host: "x"}, TLS},
		{"invalid certificate", x509.CertificateInvalidError{Reason: x509.Expired}, TLS},
		{"verification", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, TLS},
		{"record header", &net.OpError{Op: "remote error", Err: tls.RecordHeaderError{Msg: "not TLS"}}, TLS},
		{"alert", tls.AlertError(40), TLS},
		{"not exist", &fs.PathError{Op: "open", Path: "/x", Err: syscall.ENOENT}, NotFound},
		{"exist", fs.ErrExist, AlreadyExists},
		{"permission", &fs.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}, PermissionDenied},
		{"unsupported", fmt.Errorf("x: %w", errors.ErrUnsupported), Unsupported},
		{"bare errno", fmt.Errorf("write: %w", syscall.EIO), Internal},
		{"bare errno timeout", syscall.ETIMEDOUT, Timeout},
		{"permission in a net op", &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EACCES)}, Network},
		{"plain", errors.New("boom"), Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := KindOf(tt.err); got != tt.want {
				t.Errorf("KindOf(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestChain(t *testing.T) {
	t.Parallel()

	pathErr := &fs.PathError{Op: "open", Path: "/cfg/settings.json", Err: syscall.EACCES}
	refused := &net.OpError{Op: "dial", Net: "tcp", Addr: nil, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}

	tests := []struct {
		name string
		err  error
		want []string
	}{
		{"nil", nil, []string{}},
		{"plain", errors.New("boom"), []string{"boom"}},
		{"fmt chain", fmt.Errorf("load collection: %w", fmt.Errorf("read file: %w", errors.New("EOF"))),
			[]string{"load collection", "read file", "EOF"}},
		{"*Error over library errors", Wrap(pathErr, IO, "Could not save the settings."),
			[]string{"Could not save the settings.", "open /cfg/settings.json", "permission denied"}},
		{"*Error without message", &Error{Kind: IO, Err: errors.New("disk full")}, []string{"disk full"}},
		{"wrapped *Error", fmt.Errorf("settings: %w", New(InvalidArgument, "Bad theme.")),
			[]string{"settings", "Bad theme."}},
		{"url.Error", &url.Error{Op: "Get", URL: "http://localhost:1", Err: refused},
			[]string{`Get "http://localhost:1"`, "dial tcp", "connect", "connection refused"}},
		{"join", errors.Join(New(InvalidArgument, "Name is required."), nil, errors.New("URL is required.")),
			[]string{"Name is required.", "URL is required."}},
		{"join nested in wrap", fmt.Errorf("validate: %w", errors.Join(errors.New("a"), fmt.Errorf("b: %w", errors.New("c")))),
			[]string{"validate", "a", "b", "c"}},
		{"multiple %w", fmt.Errorf("both failed (%w, %w)", errors.New("x"), errors.New("y")),
			[]string{"both failed (x, y)", "x", "y"}},
		{"repeated", fmt.Errorf("%w", errors.New("same")), []string{"same"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Chain(tt.err)
			if got == nil {
				t.Fatal("Chain returned nil, want a non-nil slice")
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Chain =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestChainDepthIsBounded(t *testing.T) {
	t.Parallel()

	var err error = errors.New("root")
	for i := range 100 {
		err = fmt.Errorf("level %d: %w", i, err)
	}
	if got := len(Chain(err)); got > maxChainDepth+1 {
		t.Errorf("Chain has %d entries, want at most %d", got, maxChainDepth+1)
	}
}
