package wsframe

import (
	"encoding/binary"
	"strconv"
	"unicode/utf8"
)

// StatusCode is a close status code (RFC 6455 §7.4 and the IANA
// "WebSocket Close Code Number" registry).
type StatusCode uint16

// Close status codes. CloseNoStatus, CloseAbnormalClosure and
// CloseTLSHandshake are never sent on the wire.
const (
	CloseNormalClosure      StatusCode = 1000
	CloseGoingAway          StatusCode = 1001
	CloseProtocolError      StatusCode = 1002
	CloseUnsupportedData    StatusCode = 1003
	CloseNoStatus           StatusCode = 1005
	CloseAbnormalClosure    StatusCode = 1006
	CloseInvalidPayload     StatusCode = 1007
	ClosePolicyViolation    StatusCode = 1008
	CloseMessageTooBig      StatusCode = 1009
	CloseMandatoryExtension StatusCode = 1010
	CloseInternalError      StatusCode = 1011
	CloseServiceRestart     StatusCode = 1012
	CloseTryAgainLater      StatusCode = 1013
	CloseBadGateway         StatusCode = 1014
	CloseTLSHandshake       StatusCode = 1015
)

// MaxCloseReason is the longest close reason in bytes: a control payload
// of 125 bytes minus the 2-byte status code.
const MaxCloseReason = MaxControlPayload - 2

// ValidOnWire reports whether c may appear in a close frame: 1000–1003 and
// 1007–1014 (defined), and 3000–4999 (registered and private use). 1004 is
// reserved, 1005, 1006 and 1015 are never sent, and everything below 1000,
// 1016–2999 and from 5000 on is not assigned.
func (c StatusCode) ValidOnWire() bool {
	switch {
	case c >= 1000 && c <= 1003, c >= 1007 && c <= 1014:
		return true
	case c >= 3000 && c <= 4999:
		return true
	}
	return false
}

// String returns the code and its name, e.g. "1000 (normal closure)".
func (c StatusCode) String() string {
	name := ""
	switch c {
	case CloseNormalClosure:
		name = "normal closure"
	case CloseGoingAway:
		name = "going away"
	case CloseProtocolError:
		name = "protocol error"
	case CloseUnsupportedData:
		name = "unsupported data"
	case CloseNoStatus:
		name = "no status received"
	case CloseAbnormalClosure:
		name = "abnormal closure"
	case CloseInvalidPayload:
		name = "invalid payload data"
	case ClosePolicyViolation:
		name = "policy violation"
	case CloseMessageTooBig:
		name = "message too big"
	case CloseMandatoryExtension:
		name = "mandatory extension"
	case CloseInternalError:
		name = "internal error"
	case CloseServiceRestart:
		name = "service restart"
	case CloseTryAgainLater:
		name = "try again later"
	case CloseBadGateway:
		name = "bad gateway"
	case CloseTLSHandshake:
		name = "TLS handshake"
	}
	if name == "" {
		return strconv.Itoa(int(c))
	}
	return strconv.Itoa(int(c)) + " (" + name + ")"
}

// CloseError is a close frame received from the peer. Code is
// CloseNoStatus when the frame had no payload.
type CloseError struct {
	Code   StatusCode
	Reason string
}

func (e *CloseError) Error() string {
	s := "websocket closed: " + e.Code.String()
	if e.Reason != "" {
		s += ": " + strconv.Quote(e.Reason)
	}
	return s
}

// AppendClosePayload appends the payload of a close frame with code and
// reason. CloseNoStatus with an empty reason is the empty payload. Other
// codes must be ValidOnWire; the reason must be UTF-8 and at most
// MaxCloseReason bytes.
func AppendClosePayload(dst []byte, code StatusCode, reason string) ([]byte, error) {
	if code == CloseNoStatus {
		if reason != "" {
			return dst, invalidArgument("A close frame without a status code cannot have a reason.")
		}
		return dst, nil
	}
	if !code.ValidOnWire() {
		return dst, invalidArgument("The close code " + strconv.Itoa(int(code)) + " must not be sent.")
	}
	if len(reason) > MaxCloseReason {
		return dst, invalidArgument("The close reason is longer than 123 bytes.")
	}
	if !utf8.ValidString(reason) {
		return dst, invalidArgument("The close reason is not valid UTF-8.")
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(code))
	return append(dst, reason...), nil
}

// ParseClosePayload decodes the payload of a received close frame. An empty
// payload is CloseNoStatus. A 1-byte payload or a code that is not
// ValidOnWire is a protocol error (1002); a reason that is not UTF-8 is an
// invalid payload (1007).
func ParseClosePayload(p []byte) (StatusCode, string, error) {
	switch {
	case len(p) == 0:
		return CloseNoStatus, "", nil
	case len(p) == 1:
		return 0, "", failure(CodeInvalidClosePayload, CloseProtocolError, "The close frame payload is 1 byte long.")
	}
	code := StatusCode(binary.BigEndian.Uint16(p))
	if !code.ValidOnWire() {
		return code, "", failure(CodeInvalidClosePayload, CloseProtocolError,
			"The close frame carries the status code "+strconv.Itoa(int(code))+", which must not be sent.")
	}
	if !utf8.Valid(p[2:]) {
		return code, "", failure(CodeInvalidUTF8, CloseInvalidPayload, "The close reason is not valid UTF-8.")
	}
	return code, string(p[2:]), nil
}
