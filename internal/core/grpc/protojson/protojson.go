// Package protojson converts dynamic Protocol Buffers messages to and from
// JSON with the canonical proto3 JSON mapping, so that gRPC messages can be
// edited and shown as JSON (GRPC-04, GRPC-15).
//
// The behaviour follows protobuf-go's encoding/protojson, the de-facto
// reference implementation, rule for rule:
//
//   - Field names: output uses the lowerCamelCase JSON name (json_name) or,
//     with UseProtoNames, the proto name; input accepts both. Extensions
//     are "[full.name]".
//   - int64, uint64, sint64, fixed64 and sfixed64 are written as strings;
//     input accepts numbers or strings, with exponents that denote integers
//     ("1e3"). Floats accept "NaN", "Infinity" and "-Infinity"; float input
//     must fit a float32.
//   - bytes are written as standard Base64 with padding; input also accepts
//     URL-safe and unpadded Base64.
//   - Enums are written by name, or as numbers for values the enum does not
//     declare (and with UseEnumNumbers); input accepts names and numbers.
//   - null means "not set", except for google.protobuf.Value (null_value)
//     and fields of type google.protobuf.NullValue.
//   - Duplicate fields, two members of one oneof, duplicate map keys and
//     null list elements are errors. Unknown fields are errors unless
//     DiscardUnknown is set. Unknown binary fields are not written.
//   - The well-known types have their special forms: Any ("@type" plus the
//     embedded fields, or "value" for well-known types), Timestamp (RFC
//     3339 in UTC with 0, 3, 6 or 9 fractional digits), Duration ("1.5s"),
//     Struct, Value, ListValue, NullValue, the wrappers, FieldMask
//     ("a.fooBar,b") and Empty.
//
// Output is compact, or indented with MarshalOptions.Indent. Unlike
// protobuf-go, which adds random spaces to discourage byte comparisons,
// the output is stable. See docs/protobuf-json.md.
package protojson

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
)

// DefaultRecursionLimit is how deeply messages may nest in JSON input, as
// in protobuf-go.
const DefaultRecursionLimit = 10000

// Resolver finds the message types named by google.protobuf.Any type URLs
// and extensions by name or number. *desc.Pool implements it.
type Resolver interface {
	FindMessage(fullName string) *desc.Message
	FindExtension(fullName string) *desc.Field
	FindExtensionByNumber(m *desc.Message, n int32) *desc.Field
}

// Error is a conversion error. For JSON input, Offset is the byte offset in
// the input where the problem was found, and Line and Column (1-based,
// columns counted in characters) locate the same position. For output,
// Offset is -1 and Line and Column are 0.
type Error struct {
	Offset       int
	Line, Column int
	Message      string
	// Err is the underlying error, if any (for example a binary decoding
	// error inside a google.protobuf.Any).
	Err error
}

func (e *Error) Error() string {
	if e.Offset < 0 {
		return "protojson: " + e.Message
	}
	return fmt.Sprintf("protojson: line %d, column %d (offset %d): %s", e.Line, e.Column, e.Offset, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// newError returns an input error at byte offset pos of in.
func newError(in []byte, pos int, format string, args ...any) *Error {
	pos = min(max(pos, 0), len(in))
	b := in[:pos]
	line := bytes.Count(b, []byte("\n")) + 1
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	return &Error{Offset: pos, Line: line, Column: utf8.RuneCount(b) + 1, Message: fmt.Sprintf(format, args...)}
}

// outputError returns a marshalling error.
func outputError(format string, args ...any) *Error {
	return &Error{Offset: -1, Message: fmt.Sprintf(format, args...)}
}
