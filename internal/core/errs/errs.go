package errs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io/fs"
	"maps"
	"net"
	"os"
	"strings"
	"syscall"
)

// Kind is the category of an error. The set is small and stable: the UI
// picks titles, icons and behaviour by kind, so adding one is an API change
// (update docs/errors.md and frontend/src/lib/errors.ts too). Use Code for
// anything finer.
type Kind string

const (
	// InvalidArgument: the input is wrong and the user can fix it (a
	// settings value out of range, a malformed URL or header typed in the
	// editor, a bad request to a service method).
	InvalidArgument Kind = "invalid_argument"
	// NotFound: something the user referred to does not exist (a request,
	// collection, environment or file).
	NotFound Kind = "not_found"
	// AlreadyExists: creating something whose name or id is taken.
	AlreadyExists Kind = "already_exists"
	// PermissionDenied: the OS refused access (a file or directory, a
	// keychain entry, binding a privileged port).
	PermissionDenied Kind = "permission_denied"
	// Cancelled: the user or the app cancelled the operation.
	Cancelled Kind = "cancelled"
	// Timeout: a deadline expired (request timeout, dial or TLS handshake
	// timeout).
	Timeout Kind = "timeout"
	// Network: connecting or talking to a remote host failed (DNS, refused
	// or reset connections, proxy errors).
	Network Kind = "network"
	// TLS: the TLS handshake or certificate verification failed.
	TLS Kind = "tls"
	// Protocol: the peer broke the protocol (a malformed HTTP response, a
	// GraphQL response without data or errors, a bad WebSocket frame).
	Protocol Kind = "protocol"
	// Parse: content could not be parsed (JSON, GraphQL documents, imported
	// collections, stored files).
	Parse Kind = "parse"
	// IO: reading or writing local files failed for another reason than
	// NotFound or PermissionDenied (disk full, rename failed).
	IO Kind = "io"
	// Internal: a bug or an unexpected failure. Errors that are not *Error
	// and cannot be classified become Internal.
	Internal Kind = "internal"
	// Unsupported: valid, but PostSonde does not support it (yet): an
	// unknown auth scheme, an HTTP version, a platform feature.
	Unsupported Kind = "unsupported"
)

var kinds = []Kind{
	InvalidArgument, NotFound, AlreadyExists, PermissionDenied,
	Cancelled, Timeout,
	Network, TLS, Protocol, Parse, IO,
	Internal, Unsupported,
}

// Kinds returns every kind, in the order docs/errors.md lists them.
func Kinds() []Kind { return append([]Kind(nil), kinds...) }

// Valid reports whether k is one of the defined kinds.
func (k Kind) Valid() bool {
	for _, v := range kinds {
		if k == v {
			return true
		}
	}
	return false
}

// Error is an error with a kind, a user-facing message and an optional
// hint, code, details and cause. Build it with New or Wrap and the With
// methods, which return a modified copy.
type Error struct {
	// Kind is the category; an empty or unknown Kind counts as Internal.
	Kind Kind
	// Code is an optional finer machine-readable code, namespaced by area:
	// "settings.invalid_value", "system.wait_out_of_range".
	Code string
	// Message says what went wrong, as a sentence for the user. It must not
	// contain secret values.
	Message string
	// Hint optionally says what the user can do about it.
	Hint string
	// Details holds optional machine-readable context such as the field
	// name. Values must not be secret.
	Details map[string]string
	// Err is the cause, if any.
	Err error
}

// New returns an error of kind with the user-facing message msg.
func New(kind Kind, msg string) *Error {
	return &Error{Kind: kind, Message: msg}
}

// Wrap returns an error of kind with the user-facing message msg, caused by
// err. Wrap(nil, kind, msg) is the same as New(kind, msg).
func Wrap(err error, kind Kind, msg string) *Error {
	return &Error{Kind: kind, Message: msg, Err: err}
}

// WithHint returns a copy of e with the hint set.
func (e *Error) WithHint(hint string) *Error {
	c := e.clone()
	c.Hint = hint
	return c
}

// WithCode returns a copy of e with the code set.
func (e *Error) WithCode(code string) *Error {
	c := e.clone()
	c.Code = code
	return c
}

// WithDetail returns a copy of e with Details[key] set to value.
func (e *Error) WithDetail(key, value string) *Error {
	c := e.clone()
	c.Details[key] = value
	return c
}

func (e *Error) clone() *Error {
	c := *e
	c.Details = make(map[string]string, len(e.Details)+1)
	maps.Copy(c.Details, e.Details)
	return &c
}

// Error returns the message followed by the cause's message, the way
// fmt.Errorf("%s: %w") would.
func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = string(e.kind())
	}
	if e.Err != nil {
		return msg + ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// Is makes errors.Is match by kind and code: err matches a target *Error
// whose Kind is empty or equal to err's and whose Code is empty or equal to
// err's. A target with neither set matches nothing. This lets packages
// declare sentinels:
//
//	var ErrNoRequest = errs.New(errs.NotFound, "The request does not exist.").WithCode("workspace.no_request")
//	errors.Is(err, ErrNoRequest)          // same code
//	errors.Is(err, &errs.Error{Kind: errs.NotFound}) // any not_found
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok || (t.Kind == "" && t.Code == "") {
		return false
	}
	return (t.Kind == "" || t.Kind == e.kind()) && (t.Code == "" || t.Code == e.Code)
}

func (e *Error) kind() Kind {
	if e.Kind.Valid() {
		return e.Kind
	}
	return Internal
}

// KindOf classifies err. The first *Error in err's tree decides; otherwise
// well-known standard library errors map to their kind:
//
//   - context.Canceled: Cancelled
//   - context.DeadlineExceeded, os.ErrDeadlineExceeded, net.Error timeouts: Timeout
//   - crypto/tls and crypto/x509 handshake and verification errors: TLS
//   - other net.Error values (including *url.Error): Network
//   - fs.ErrNotExist, fs.ErrExist, fs.ErrPermission: NotFound, AlreadyExists, PermissionDenied
//   - errors.ErrUnsupported: Unsupported
//
// Anything else is Internal. KindOf(nil) is "".
func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.kind()
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Cancelled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return Timeout
	case isTLS(err):
		return TLS
	}
	// syscall.Errno implements net.Error too; a bare errno is not a
	// network error unless it is a timeout.
	if ne, ok := errors.AsType[net.Error](err); ok {
		if ne.Timeout() {
			return Timeout
		}
		if _, bare := ne.(syscall.Errno); !bare {
			return Network
		}
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return NotFound
	case errors.Is(err, fs.ErrExist):
		return AlreadyExists
	case errors.Is(err, fs.ErrPermission):
		return PermissionDenied
	case errors.Is(err, errors.ErrUnsupported):
		return Unsupported
	}
	return Internal
}

func isTLS(err error) bool {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return true
	}
	if _, ok := errors.AsType[tls.AlertError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		return true
	}
	return false
}

// maxChainDepth bounds how deep Chain follows causes.
const maxChainDepth = 32

// Chain returns the messages of err and its causes, outermost first,
// following errors.Unwrap and walking errors.Join trees depth-first in
// order. Each entry is the part of a message its cause does not already
// say: fmt.Errorf("open settings: %w", err) contributes "open settings".
// For an *Error the entry is its Message. Empty and repeated consecutive
// entries are dropped. Chain(nil) is empty, never nil.
//
// Causes come from any library, so entries may contain paths, hosts and
// URLs; they are diagnostics, not messages written for the user.
func Chain(err error) []string {
	out := make([]string, 0, 4)
	appendChain(&out, err, 0)
	return out
}

func appendChain(out *[]string, err error, depth int) {
	if err == nil || depth > maxChainDepth {
		return
	}
	switch u := err.(type) {
	case *Error:
		add(out, u.Message)
		appendChain(out, u.Err, depth+1)
	case interface{ Unwrap() []error }:
		children := u.Unwrap()
		if !isJoin(err, children) {
			add(out, err.Error())
		}
		for _, c := range children {
			appendChain(out, c, depth+1)
		}
	case interface{ Unwrap() error }:
		child := u.Unwrap()
		add(out, ownMessage(err, child))
		appendChain(out, child, depth+1)
	default:
		add(out, err.Error())
	}
}

// ownMessage returns err's message without the trailing ": <cause>" that
// most wrappers (fmt.Errorf, *fs.PathError, *net.OpError, *url.Error) add.
func ownMessage(err, cause error) string {
	msg := err.Error()
	if cause == nil {
		return msg
	}
	cm := cause.Error()
	if msg == cm {
		return ""
	}
	return strings.TrimSuffix(msg, ": "+cm)
}

// isJoin reports whether err's message is just its children's messages on
// separate lines, as errors.Join formats them.
func isJoin(err error, children []error) bool {
	msgs := make([]string, 0, len(children))
	for _, c := range children {
		if c != nil {
			msgs = append(msgs, c.Error())
		}
	}
	return err.Error() == strings.Join(msgs, "\n")
}

func add(out *[]string, s string) {
	s = strings.TrimSpace(s)
	if s == "" || (len(*out) > 0 && (*out)[len(*out)-1] == s) {
		return
	}
	*out = append(*out, s)
}
