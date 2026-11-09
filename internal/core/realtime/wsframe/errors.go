package wsframe

import (
	"errors"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes. Errors that fail the connection carry the close code they
// map to (CloseCodeOf).
const (
	// CodeProtocolError: the peer broke RFC 6455 (bad header, masking,
	// fragmentation or control frame). Close code 1002.
	CodeProtocolError = "wsframe.protocol_error"
	// CodeInvalidClosePayload: a close frame with a 1-byte payload or a
	// status code that must not be sent. Close code 1002.
	CodeInvalidClosePayload = "wsframe.invalid_close_payload"
	// CodeInvalidUTF8: a text message or close reason that is not UTF-8.
	// Close code 1007.
	CodeInvalidUTF8 = "wsframe.invalid_utf8"
	// CodeTooBig: a frame or message is larger than the configured limit.
	// Close code 1009.
	CodeTooBig = "wsframe.too_big"
	// CodePeerClosed: the peer sent a close frame. The error wraps a
	// *CloseError with the peer's status code and reason.
	CodePeerClosed = "wsframe.peer_closed"
	// CodeAbnormalClosure: the connection ended without a close frame
	// (close code 1006, never sent).
	CodeAbnormalClosure = "wsframe.abnormal_closure"
	// CodeUnexpectedEOF: the connection ended in the middle of a frame.
	CodeUnexpectedEOF = "wsframe.unexpected_eof"
	// CodeReadFailed and CodeWriteFailed: the underlying transport failed
	// (the kind comes from the cause, for example timeout). A reset by the
	// peer has the shared netx.CodeConnectionReset instead.
	CodeReadFailed  = "wsframe.read_failed"
	CodeWriteFailed = "wsframe.write_failed"
	// CodeCloseSent: a write after this side sent its close frame.
	CodeCloseSent = "wsframe.close_sent"
	// CodeCloseTimeout: the peer did not answer the close handshake before
	// the context ended.
	CodeCloseTimeout = "wsframe.close_timeout"
	// CodeInvalidArgument: a bad argument to an encoding or write function.
	CodeInvalidArgument = "wsframe.invalid_argument"
)

// detailCloseCode is the Details key holding the close code an error maps to.
const detailCloseCode = "closeCode"

func failure(errCode string, status StatusCode, msg string) *errs.Error {
	return errs.New(errs.Protocol, msg).WithCode(errCode).WithDetail(detailCloseCode, strconv.Itoa(int(status)))
}

func protocolError(msg string) *errs.Error {
	return failure(CodeProtocolError, CloseProtocolError, msg)
}

func invalidArgument(msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidArgument)
}

// CloseCodeOf returns the close status an error maps to: the peer's code for
// a close received from the peer, 1002, 1007 or 1009 for a protocol
// violation, 1006 for a connection that ended without a close frame, and 0
// for anything else.
func CloseCodeOf(err error) StatusCode {
	if ce, ok := errors.AsType[*CloseError](err); ok {
		return ce.Code
	}
	if e, ok := errors.AsType[*errs.Error](err); ok {
		if v, ok := e.Details[detailCloseCode]; ok {
			n, convErr := strconv.Atoi(v)
			if convErr == nil {
				return StatusCode(n)
			}
		}
	}
	return 0
}

// failsConnection reports whether err is a protocol violation this side
// answers with a close frame, and with which code.
func failsConnection(err error) (StatusCode, bool) {
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		return 0, false
	}
	switch e.Code {
	case CodeProtocolError, CodeInvalidClosePayload, CodeInvalidUTF8, CodeTooBig:
		return CloseCodeOf(e), true
	}
	return 0, false
}
