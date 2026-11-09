package engineio

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes. Decoding errors have kind Protocol, encoding errors kind
// InvalidArgument.
const (
	// CodeEmptyPacket: a packet (a WebSocket message or a record of a
	// long-polling payload) has no bytes, so no type.
	CodeEmptyPacket = "engineio.empty_packet"
	// CodeUnknownType: the first character is not a packet type 0-6 (or
	// "b" in a long-polling payload).
	CodeUnknownType = "engineio.unknown_type"
	// CodeInvalidUTF8: a text packet is not valid UTF-8.
	CodeInvalidUTF8 = "engineio.invalid_utf8"
	// CodeInvalidBase64: the Base64 of a "b" packet is not valid standard
	// Base64 with padding.
	CodeInvalidBase64 = "engineio.invalid_base64"
	// CodeUnexpectedData: a close, upgrade or noop packet carries data.
	CodeUnexpectedData = "engineio.unexpected_data"
	// CodeTooBig: a WebSocket message or long-polling body is larger than
	// Options.MaxSize.
	CodeTooBig = "engineio.too_big"
	// CodeInvalidHandshake: the open packet's data is not a valid handshake
	// object. Details: "field" when one field is wrong.
	CodeInvalidHandshake = "engineio.invalid_handshake"
	// CodeInvalidPayload: an Engine.IO 3 long-polling payload whose
	// length prefixes or markers are malformed. Details: index, offset.
	CodeInvalidPayload = "engineio.invalid_payload"
	// CodeInvalidArgument: a packet that cannot be encoded.
	CodeInvalidArgument = "engineio.invalid_argument"
)

// Details keys.
const (
	// DetailIndex is the 0-based index of the bad packet in a payload.
	DetailIndex = "index"
	// DetailOffset is the byte offset of the bad packet in a payload.
	DetailOffset = "offset"
	// DetailField is the bad handshake field.
	DetailField = "field"
	// DetailLimit is the size limit that was exceeded.
	DetailLimit = "limit"
)

func protocolError(code, msg string) *errs.Error {
	return errs.New(errs.Protocol, msg).WithCode(code)
}

func invalidArgument(msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidArgument)
}

func tooBig(limit int) *errs.Error {
	return protocolError(CodeTooBig, "The Engine.IO message is larger than the size limit.").
		WithHint("Raise the message size limit if the server really sends messages this large.").
		WithDetail(DetailLimit, strconv.Itoa(limit))
}

func handshakeError(field, msg string) *errs.Error {
	e := protocolError(CodeInvalidHandshake, msg)
	if field != "" {
		e = e.WithDetail(DetailField, field)
	}
	return e
}
