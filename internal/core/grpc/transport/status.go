package transport

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

// Code is a gRPC status code. Values above Unauthenticated are kept as
// received.
type Code uint32

// The gRPC status codes (https://grpc.github.io/grpc/core/md_doc_statuscodes.html).
const (
	OK                 Code = 0
	Canceled           Code = 1
	Unknown            Code = 2
	InvalidArgument    Code = 3
	DeadlineExceeded   Code = 4
	NotFound           Code = 5
	AlreadyExists      Code = 6
	PermissionDenied   Code = 7
	ResourceExhausted  Code = 8
	FailedPrecondition Code = 9
	Aborted            Code = 10
	OutOfRange         Code = 11
	Unimplemented      Code = 12
	Internal           Code = 13
	Unavailable        Code = 14
	DataLoss           Code = 15
	Unauthenticated    Code = 16
)

var codeNames = [...]string{
	OK:                 "OK",
	Canceled:           "CANCELLED",
	Unknown:            "UNKNOWN",
	InvalidArgument:    "INVALID_ARGUMENT",
	DeadlineExceeded:   "DEADLINE_EXCEEDED",
	NotFound:           "NOT_FOUND",
	AlreadyExists:      "ALREADY_EXISTS",
	PermissionDenied:   "PERMISSION_DENIED",
	ResourceExhausted:  "RESOURCE_EXHAUSTED",
	FailedPrecondition: "FAILED_PRECONDITION",
	Aborted:            "ABORTED",
	OutOfRange:         "OUT_OF_RANGE",
	Unimplemented:      "UNIMPLEMENTED",
	Internal:           "INTERNAL",
	Unavailable:        "UNAVAILABLE",
	DataLoss:           "DATA_LOSS",
	Unauthenticated:    "UNAUTHENTICATED",
}

// String returns the canonical upper-case name ("NOT_FOUND"), or
// "CODE(42)" for a code outside the known range.
func (c Code) String() string {
	if int(c) < len(codeNames) {
		return codeNames[c]
	}
	return "CODE(" + strconv.FormatUint(uint64(c), 10) + ")"
}

// Status is the outcome of a call.
type Status struct {
	Code Code
	// Message is grpc-message, percent-decoded.
	Message string
	// Details is grpc-status-details-bin, base64-decoded but not parsed
	// (a serialized google.rpc.Status). Nil when absent or malformed.
	Details []byte
}

// OK reports whether the status is OK.
func (s Status) OK() bool { return s.Code == OK }

// StatusError is a non-OK Status as an error. The errors a Stream returns
// for a status wrap one; StatusFromError finds it.
type StatusError struct {
	Status Status
	// Cause is the local error behind a status the client made up (a
	// connection failure, a cancelled context); nil for a server status.
	Cause error
}

// Unwrap returns the cause.
func (e *StatusError) Unwrap() error { return e.Cause }

func (e *StatusError) Error() string {
	if e.Status.Message == "" {
		return "rpc error: " + e.Status.Code.String()
	}
	return "rpc error: " + e.Status.Code.String() + ": " + e.Status.Message
}

// Header and trailer names the transport reads or writes itself.
const (
	HeaderContentType    = "content-type"
	HeaderTE             = "te"
	HeaderUserAgent      = "user-agent"
	HeaderTimeout        = "grpc-timeout"
	HeaderEncoding       = "grpc-encoding"
	HeaderAcceptEncoding = "grpc-accept-encoding"
	HeaderStatus         = "grpc-status"
	HeaderMessage        = "grpc-message"
	HeaderStatusDetails  = "grpc-status-details-bin"
	ContentTypeGRPC      = "application/grpc"
	binarySuffix         = "-bin"
)

// httpStatusCodes maps HTTP status codes of a response without a gRPC
// status to gRPC codes, per the "HTTP to gRPC Status Code Mapping" of the
// gRPC documentation. Anything else is Unknown.
var httpStatusCodes = map[int]Code{
	http.StatusBadRequest:         Internal,
	http.StatusUnauthorized:       Unauthenticated,
	http.StatusForbidden:          PermissionDenied,
	http.StatusNotFound:           Unimplemented,
	http.StatusTooManyRequests:    Unavailable,
	http.StatusBadGateway:         Unavailable,
	http.StatusServiceUnavailable: Unavailable,
	http.StatusGatewayTimeout:     Unavailable,
}

// CodeForHTTPStatus returns the gRPC code for an HTTP status code of a
// response that is not a gRPC response.
func CodeForHTTPStatus(status int) Code {
	if c, ok := httpStatusCodes[status]; ok {
		return c
	}
	return Unknown
}

// EncodeStatusMessage percent-encodes a grpc-message value: bytes outside
// printable ASCII (0x20-0x7E) and '%' become %XX with upper-case hex.
func EncodeStatusMessage(msg string) string {
	n := 0
	for i := 0; i < len(msg); i++ {
		if needsPercent(msg[i]) {
			n++
		}
	}
	if n == 0 {
		return msg
	}
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(msg) + n*2)
	for i := 0; i < len(msg); i++ {
		c := msg[i]
		if needsPercent(c) {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func needsPercent(c byte) bool { return c < 0x20 || c > 0x7E || c == '%' }

// DecodeStatusMessage undoes EncodeStatusMessage leniently, as the spec
// asks: a '%' not followed by two hex digits is kept as it is.
func DecodeStatusMessage(v string) string {
	if !strings.Contains(v, "%") {
		return v
	}
	b := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		if v[i] == '%' && i+2 < len(v) {
			hi, ok1 := unhex(v[i+1])
			lo, ok2 := unhex(v[i+2])
			if ok1 && ok2 {
				b = append(b, hi<<4|lo)
				i += 2
				continue
			}
		}
		b = append(b, v[i])
	}
	return string(b)
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// EncodeBinaryValue encodes the value of a "-bin" key: base64, standard
// alphabet, unpadded (what the spec says to emit).
func EncodeBinaryValue(v []byte) string {
	return base64.RawStdEncoding.EncodeToString(v)
}

// DecodeBinaryValue decodes the value of a "-bin" key, padded or not.
func DecodeBinaryValue(v string) ([]byte, error) {
	if len(v)%4 == 0 {
		return base64.StdEncoding.DecodeString(v)
	}
	return base64.RawStdEncoding.DecodeString(v)
}

// parseStatus reads grpc-status, grpc-message and grpc-status-details-bin
// from h. found is false when grpc-status is absent. A grpc-status that is
// not a number is an Internal status.
func parseStatus(h http.Header) (st Status, found bool) {
	raw, ok := headerValue(h, HeaderStatus)
	if !ok {
		return Status{}, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil {
		return Status{Code: Internal, Message: "malformed grpc-status: " + strconv.Quote(raw)}, true
	}
	st.Code = Code(n)
	if m, ok := headerValue(h, HeaderMessage); ok {
		st.Message = DecodeStatusMessage(m)
	}
	if d, ok := headerValue(h, HeaderStatusDetails); ok {
		if b, err := DecodeBinaryValue(d); err == nil {
			st.Details = b
		}
	}
	return st, true
}

// headerValue returns the first value of name in h, looked up by its
// canonical form (how net/http stores received fields) and as spelled.
func headerValue(h http.Header, name string) (string, bool) {
	if vv := h[http.CanonicalHeaderKey(name)]; len(vv) > 0 {
		return vv[0], true
	}
	if vv := h[name]; len(vv) > 0 {
		return vv[0], true
	}
	return "", false
}

// validContentType reports whether ct is application/grpc, optionally
// followed by "+subtype" or ";params".
func validContentType(ct string) bool {
	rest, ok := cutPrefixFold(ct, ContentTypeGRPC)
	return ok && (rest == "" || rest[0] == '+' || rest[0] == ';')
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}
