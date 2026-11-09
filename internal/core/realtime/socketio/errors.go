package socketio

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes. Decoding errors have kind Protocol, encoding errors kind
// InvalidArgument.
const (
	// CodeEmptyPacket: a text packet with no bytes.
	CodeEmptyPacket = "socketio.empty_packet"
	// CodeUnknownType: the first character is not a packet type 0-6.
	CodeUnknownType = "socketio.unknown_type"
	// CodeInvalidAttachments: the attachment count of a binary packet is
	// missing, not a canonical decimal number or not followed by "-".
	CodeInvalidAttachments = "socketio.invalid_attachments"
	// CodeTooManyAttachments: more attachments than
	// Options.MaxAttachments.
	CodeTooManyAttachments = "socketio.too_many_attachments"
	// CodeInvalidNamespace: a namespace that does not start with "/" or
	// contains ",".
	CodeInvalidNamespace = "socketio.invalid_namespace"
	// CodeInvalidAckID: an ack ID with leading zeros or above 2^53-1, an ack
	// ID on a packet type that has none, or an ACK without one.
	CodeInvalidAckID = "socketio.invalid_ack_id"
	// CodeInvalidPayload: the payload is not JSON, or not the JSON shape the
	// packet type needs (spec "Packet encoding"), or it is missing.
	CodeInvalidPayload = "socketio.invalid_payload"
	// CodeReservedEvent: an EVENT named connect, connect_error, disconnect,
	// disconnecting, newListener or removeListener.
	CodeReservedEvent = "socketio.reserved_event"
	// CodeInvalidPlaceholder: a binary placeholder whose "num" is not an
	// integer index of an attachment.
	CodeInvalidPlaceholder = "socketio.invalid_placeholder"
	// CodeUnexpectedText: a text packet arrived while the attachments of a
	// binary packet were still expected.
	CodeUnexpectedText = "socketio.unexpected_text"
	// CodeUnexpectedBinary: a binary message arrived while no attachment
	// was expected.
	CodeUnexpectedBinary = "socketio.unexpected_binary"
	// CodeNotMessage: Decoder.Feed got an Engine.IO packet that is not a
	// message.
	CodeNotMessage = "socketio.not_message"
	// CodeTooBig: a text packet or attachment over the size limit.
	CodeTooBig = "socketio.too_big"
	// CodeInvalidArgument: a packet or value that cannot be encoded.
	CodeInvalidArgument = "socketio.invalid_argument"
)

// Details keys.
const (
	// DetailOffset is the byte offset of the problem in the text packet.
	DetailOffset = "offset"
	// DetailLimit is the limit that was exceeded.
	DetailLimit = "limit"
)

// decodeError is a Protocol error at a byte offset of the text packet.
func decodeError(code string, offset int, msg string) *errs.Error {
	return errs.New(errs.Protocol, msg).WithCode(code).WithDetail(DetailOffset, strconv.Itoa(offset))
}

func protocolError(code, msg string) *errs.Error {
	return errs.New(errs.Protocol, msg).WithCode(code)
}

func invalidArgument(msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidArgument)
}

func tooBig(what string, limit int) *errs.Error {
	return protocolError(CodeTooBig, "The Socket.IO "+what+" is larger than the size limit.").
		WithHint("Raise the size limit if the server really sends data this large.").
		WithDetail(DetailLimit, strconv.Itoa(limit))
}
