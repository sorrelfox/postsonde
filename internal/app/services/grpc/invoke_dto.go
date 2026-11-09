package grpc

import (
	"encoding/base64"

	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/grpc/rpcstatus"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
)

// Types of CallEventDTO. A call sends "request" first and "end" last;
// "sent" and "half-close" (streams from the client) interleave with
// "headers" and "message" in the order they happened.
const (
	// CallRequest is the request as sent: method, metadata and message.
	CallRequest = "request"
	// CallHeaders are the response headers; not sent for a call that
	// failed before any, or for a trailers-only response.
	CallHeaders = "headers"
	// CallSent is one message sent on the request stream of a
	// client-streaming or bidi call (SendMessage).
	CallSent = "sent"
	// CallHalfClose is the end of the request stream of a client-streaming
	// or bidi call (CloseSend).
	CallHalfClose = "half-close"
	// CallMessage is one response message.
	CallMessage = "message"
	// CallEnd is the final status and the trailers; sent last, once the
	// call has started.
	CallEnd = "end"
)

// InvokeInputDTO is a call as the gRPC editor holds it.
type InvokeInputDTO struct {
	// RequestID is the grpc request the call belongs to; only for logs.
	RequestID string `json:"requestId"`
	// Files and ImportPaths are the effective proto setup (the request's
	// own or its folder's) as stored paths, as for Describe.
	Files       []string `json:"files"`
	ImportPaths []string `json:"importPaths"`
	// Address is host:port.
	Address string `json:"address"`
	// TLS connects with TLS (ALPN h2); otherwise plaintext HTTP/2 (h2c).
	TLS bool `json:"tls"`
	// Transport is "grpc" (also ""), "grpc-web" (binary framing) or
	// "grpc-web-text" (base64 framing). gRPC-Web goes over HTTP/1.1, or
	// HTTP/2 when TLS negotiates it, and cannot stream from the client
	// (GRPC-25).
	Transport string `json:"transport"`
	// Compression is "" or "identity" (none) or "gzip": request messages
	// are compressed and gzip is advertised in grpc-accept-encoding.
	// Compressed responses are accepted either way (GRPC-24).
	Compression string `json:"compression"`
	// VerifyTLS false accepts any server certificate; null verifies.
	VerifyTLS *bool `json:"verifyTls"`
	// ConnectTimeoutMs bounds the dial and the TLS handshake: null for the
	// defaults, 0 for no limit of its own.
	ConnectTimeoutMs *int64 `json:"connectTimeoutMs"`
	// DeadlineMs is the call's deadline, sent as grpc-timeout: null or 0
	// for none.
	DeadlineMs *int64 `json:"deadlineMs"`
	// Service is the fully-qualified service, Method the method's name.
	Service string `json:"service"`
	Method  string `json:"method"`
	// Message is the request message as protobuf JSON; empty means {}.
	Message string `json:"message"`
	// Metadata are the request's metadata rows; disabled rows and rows
	// without a name are skipped. Values of "-bin" keys are Base64.
	Metadata []MetadataRowDTO `json:"metadata"`
}

// SendMessageInputDTO is one message to send on a running call's request
// stream.
type SendMessageInputDTO struct {
	// OpID is the call's operation ID, from Invoke.
	OpID string `json:"opId"`
	// Message is protobuf JSON of the method's input type; blank is {}.
	Message string `json:"message"`
}

// SentMessageDTO is a message SendMessage sent.
type SentMessageDTO struct {
	// Index counts the call's sent messages from 0.
	Index int `json:"index"`
	// Size is the encoded size in bytes.
	Size int `json:"size"`
}

// CallRefDTO names a running call.
type CallRefDTO struct {
	// OpID is the call's operation ID, from Invoke.
	OpID string `json:"opId"`
}

// MetadataRowDTO is a metadata row of the editor.
type MetadataRowDTO struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// CallEventDTO is one item of a call's operation (grpc:op:data). Type
// says which fields are set.
type CallEventDTO struct {
	// Type is "request", "sent", "half-close", "headers", "message" or
	// "end".
	Type string `json:"type"`
	// At is when it happened, RFC 3339 in UTC with nanoseconds.
	At string `json:"at"`
	// Method is the full method path, "/pkg.Service/Method" (request).
	Method string `json:"method"`
	// Metadata is the metadata sent (request), the response headers
	// (headers) or the trailers (end), in order.
	Metadata []MetadataDTO `json:"metadata"`
	// JSON is the message in the protobuf JSON mapping, indented
	// (request, sent, message). "" when a response message could not be
	// decoded, and in the request of a stream from the client.
	JSON string `json:"json"`
	// Size is the message's size in bytes, uncompressed (request, sent,
	// message).
	Size int `json:"size"`
	// Index counts response messages from 0 (message), and sent messages
	// from 0 (sent); for half-close it is the number of messages sent.
	Index int `json:"index"`
	// DecodeError says why a response message could not be shown as JSON;
	// Raw then holds its bytes in Base64 (message).
	DecodeError string `json:"decodeError"`
	Raw         string `json:"raw"`
	// DeadlineMs is the deadline sent as grpc-timeout, 0 for none
	// (request).
	DeadlineMs int64 `json:"deadlineMs"`
	// Transport and Compression are the call's, as in InvokeInputDTO with
	// the defaults filled in: "grpc", "grpc-web" or "grpc-web-text", and
	// "identity" or "gzip" (request).
	Transport   string `json:"transport"`
	Compression string `json:"compression"`
	// ClientStreaming is set when the call streams from the client
	// (client streaming, bidi): the request opened the stream without a
	// message, and SendMessage sends them (request).
	ClientStreaming bool `json:"clientStreaming"`
	// Status is the call's final status (end).
	Status *StatusDTO `json:"status"`
	// Messages is the number of response messages received (end).
	Messages int `json:"messages"`
	// Sent is the number of request messages sent (end).
	Sent int `json:"sent"`
	// ElapsedMs is the time from the start of the call (end).
	ElapsedMs int64 `json:"elapsedMs"`
	// Tests are the results of the request's assertions (TEST-08),
	// checked against the response messages and the status (end); empty
	// without assertions, null on the other items.
	Tests []streamcheck.TestResultDTO `json:"tests"`
}

// MetadataDTO is one metadata entry.
type MetadataDTO struct {
	Key string `json:"key"`
	// Value is the text, or for a binary ("-bin") key the bytes in
	// standard Base64 with padding.
	Value  string `json:"value"`
	Binary bool   `json:"binary"`
}

// StatusDTO is a gRPC status.
type StatusDTO struct {
	// Code is the numeric code, Name its name ("NOT_FOUND").
	Code    int    `json:"code"`
	Name    string `json:"name"`
	Message string `json:"message"`
	// Details is grpc-status-details-bin (a serialized google.rpc.Status)
	// in standard Base64, "" when absent.
	Details string `json:"details"`
	// Rich is Details decoded; null when there are none.
	Rich *RichStatusDTO `json:"rich"`
}

// RichStatusDTO is grpc-status-details-bin decoded as a google.rpc.Status
// (GRPC-20).
type RichStatusDTO struct {
	// Code, Name and Message are the Status's own; servers normally repeat
	// grpc-status and grpc-message.
	Code    int    `json:"code"`
	Name    string `json:"name"`
	Message string `json:"message"`
	// Details are the Status's details in order.
	Details []StatusDetailDTO `json:"details"`
	// Error says why the bytes are not a google.rpc.Status; Details is then
	// empty and StatusDTO.Details holds the bytes.
	Error string `json:"error"`
}

// StatusDetailDTO is one detail of a google.rpc.Status, a
// google.protobuf.Any.
type StatusDetailDTO struct {
	// TypeURL is as sent; Type is its full name, after the last "/".
	TypeURL string `json:"typeUrl"`
	Type    string `json:"type"`
	// Known is set when the type is in the request's .proto files or is
	// one of the built-in error details (google/rpc/error_details.proto).
	Known bool `json:"known"`
	// JSON is the detail in the protobuf JSON mapping, indented; "" when
	// it could not be decoded.
	JSON string `json:"json"`
	// Error says why a detail of a known type did not decode.
	Error string `json:"error"`
	// Raw is the payload in standard Base64 when JSON is "".
	Raw string `json:"raw"`
}

func toMetadataDTOs(md transport.Metadata) []MetadataDTO {
	out := make([]MetadataDTO, 0, len(md))
	for _, e := range md {
		if transport.IsBinaryKey(e.Key) {
			out = append(out, MetadataDTO{Key: e.Key, Value: base64.StdEncoding.EncodeToString([]byte(e.Value)), Binary: true})
			continue
		}
		out = append(out, MetadataDTO{Key: e.Key, Value: e.Value})
	}
	return out
}

// toStatusDTO converts a call's status, decoding its details with the
// built-in error details and types (may be nil).
func toStatusDTO(st transport.Status, types rpcstatus.Resolver) *StatusDTO {
	out := &StatusDTO{Code: int(st.Code), Name: st.Code.String(), Message: st.Message}
	if len(st.Details) > 0 {
		out.Details = base64.StdEncoding.EncodeToString(st.Details)
		out.Rich = toRichStatusDTO(st.Details, types)
	}
	return out
}

func toRichStatusDTO(b []byte, types rpcstatus.Resolver) *RichStatusDTO {
	rs, err := rpcstatus.Decode(b, types)
	if err != nil {
		return &RichStatusDTO{Details: []StatusDetailDTO{}, Error: "The details could not be decoded: " + err.Error()}
	}
	out := &RichStatusDTO{
		Code:    int(rs.Code),
		Name:    transport.Code(uint32(rs.Code)).String(),
		Message: rs.Message,
		Details: make([]StatusDetailDTO, 0, len(rs.Details)),
	}
	for _, d := range rs.Details {
		dto := StatusDetailDTO{TypeURL: d.TypeURL, Type: d.Type, Known: d.Known()}
		js, err := rs.JSON(d)
		switch {
		case d.Err != nil:
			dto.Error = "The detail is not a valid " + d.Type + ": " + d.Err.Error()
		case err != nil:
			dto.Error = "The detail could not be shown as JSON: " + err.Error()
		default:
			dto.JSON = js
		}
		if dto.JSON == "" {
			dto.Raw = base64.StdEncoding.EncodeToString(d.Value)
		}
		out.Details = append(out.Details, dto)
	}
	return out
}
