package grpc

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protojson"
)

// MessageInputDTO names a method's request message in a proto setup, and
// optionally holds its JSON text.
type MessageInputDTO struct {
	// Files and ImportPaths are the effective proto setup, as for
	// Describe and Invoke.
	Files       []string `json:"files"`
	ImportPaths []string `json:"importPaths"`
	// Service is the fully-qualified service, Method the method's name.
	Service string `json:"service"`
	Method  string `json:"method"`
	// Message is the JSON text to check (ValidateMessage only).
	Message string `json:"message"`
	// Output uses the method's response type instead of its request
	// type: the gRPC mock server's canned responses (SSRV-07).
	Output bool `json:"output"`
}

// messageType is the method's request type, or its response type with
// output.
func messageType(m *desc.Method, output bool) *desc.Message {
	if output {
		return m.Output
	}
	return m.Input
}

// ExampleDTO is a template of a request message (GRPC-16).
type ExampleDTO struct {
	// Type is the full name of the method's input type.
	Type string `json:"type"`
	// JSON is the message with every field set to its default, indented by
	// two spaces.
	JSON string `json:"json"`
}

// MessageCheckDTO is the result of ValidateMessage.
type MessageCheckDTO struct {
	// Type is the full name of the method's input type.
	Type  string `json:"type"`
	Valid bool   `json:"valid"`
	// Error is the first problem, null when Valid.
	Error *MessageErrorDTO `json:"error"`
}

// MessageErrorDTO is where and why message JSON does not fit its type:
// a syntax error, an unknown field, a value of the wrong type or a missing
// required field.
type MessageErrorDTO struct {
	// Offset is the position of the offending character in UTF-16 code
	// units, as CodeMirror counts; the text's length when the problem is
	// at the end (the text ends too early, a required field is missing).
	Offset int `json:"offset"`
	// ByteOffset is the same position in UTF-8 bytes.
	ByteOffset int `json:"byteOffset"`
	// Line and Column are 1-based; Column counts UTF-16 code units.
	Line   int `json:"line"`
	Column int `json:"column"`
	// Message says what is wrong (`unknown field "nope"`).
	Message string `json:"message"`
}

// ExampleMessage returns a template of the method's request message:
// every field set to its default, one element in each list and map, the
// first member of each oneof, recursive types expanded once inside
// themselves (protojson.Example). Client-streaming methods have one too.
// It fails like Invoke for a method that is not in the setup
// (grpc.no_method, grpc.no_proto, grpc.method_not_found,
// grpc.unresolved_type).
func (s *GRPCService) ExampleMessage(ctx context.Context, in MessageInputDTO) (ExampleDTO, error) {
	pool, m, err := s.findMethod(ctx, in.Files, in.ImportPaths, in.Service, in.Method)
	if err != nil {
		return ExampleDTO{}, err
	}
	typ := messageType(m, in.Output)
	msg := protojson.Example(typ, protojson.ExampleOptions{})
	js, err := (protojson.MarshalOptions{Indent: "  ", EmitDefaultValues: true, AllowPartial: true, Resolver: pool}).Marshal(msg)
	if err != nil {
		return ExampleDTO{}, errs.Wrap(err, errs.Internal, "No example of "+typ.FullName+" could be written: "+jsonMessage(err)).
			WithDetail("type", typ.FullName)
	}
	return ExampleDTO{Type: typ.FullName, JSON: string(js)}, nil
}

// ValidateMessage checks in.Message against the method's input type with
// the protobuf JSON mapping, as Invoke parses it (blank text is an empty
// message). A message that does not fit is a result with the position of
// the first problem, not an error; the method must be in the setup, as
// for ExampleMessage.
func (s *GRPCService) ValidateMessage(ctx context.Context, in MessageInputDTO) (MessageCheckDTO, error) {
	pool, m, err := s.findMethod(ctx, in.Files, in.ImportPaths, in.Service, in.Method)
	if err != nil {
		return MessageCheckDTO{}, err
	}
	typ := messageType(m, in.Output)
	out := MessageCheckDTO{Type: typ.FullName, Valid: true}
	if _, err := parseMessage(pool, typ, in.Message); err != nil {
		out.Valid = false
		out.Error = toMessageError(in.Message, err)
	}
	return out, nil
}

// toMessageError locates a parse error of text in UTF-16 units.
func toMessageError(text string, err error) *MessageErrorDTO {
	je, ok := errors.AsType[*protojson.Error](err)
	if !ok || je.Offset < 0 {
		// Not a position in the text (blank text parses as "{}"): the end.
		msg := err.Error()
		if ok {
			msg = je.Message
		}
		return positionError(text, len(text), msg)
	}
	off := je.Offset
	if strings.TrimSpace(text) == "" {
		off = len(text)
	}
	return positionError(text, min(off, len(text)), je.Message)
}

// positionError is a MessageErrorDTO at byte offset off of text.
func positionError(text string, off int, msg string) *MessageErrorDTO {
	lineStart := strings.LastIndexByte(text[:off], '\n') + 1
	return &MessageErrorDTO{
		Offset:     utf16Len(text[:off]),
		ByteOffset: off,
		Line:       strings.Count(text[:off], "\n") + 1,
		Column:     utf16Len(text[lineStart:off]) + 1,
		Message:    msg,
	}
}

// utf16Len is the length of s in UTF-16 code units; invalid bytes count
// as one unit each (U+FFFD), as JavaScript decodes them.
func utf16Len(s string) int {
	n := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
		s = s[size:]
	}
	return n
}

// MaxMessageCompletionItems bounds the items CompleteMessage returns; Count
// says how many matched. The editor asks again on every keystroke.
const MaxMessageCompletionItems = 300

// MessageCompleteInputDTO is the method's request message JSON and the
// cursor in it.
type MessageCompleteInputDTO struct {
	// Files and ImportPaths are the effective proto setup, as for
	// Describe and Invoke.
	Files       []string `json:"files"`
	ImportPaths []string `json:"importPaths"`
	// Service is the fully-qualified service, Method the method's name.
	Service string `json:"service"`
	Method  string `json:"method"`
	// Message is the JSON text, Offset the cursor in it in UTF-16 code
	// units (a CodeMirror position).
	Message string `json:"message"`
	Offset  int    `json:"offset"`
	// Output completes the method's response type instead of its request
	// type (the gRPC mock server's canned responses).
	Output bool `json:"output"`
}

// MessageCompletionDTO is the result of CompleteMessage.
type MessageCompletionDTO struct {
	// Type is the full name of the method's input type.
	Type string `json:"type"`
	// Context is "key" (a field name), "value" (an enum name, true, …)
	// or "none".
	Context string `json:"context"`
	// From and To are the range the chosen item replaces, in UTF-16 code
	// units: the whole key or word at the cursor, quotes included.
	From int `json:"from"`
	To   int `json:"to"`
	// Prefix is what is typed before the cursor in the range, without an
	// opening quote.
	Prefix string `json:"prefix"`
	// Owner is the message whose fields, or the field whose values, are
	// suggested.
	Owner string                     `json:"owner"`
	Items []MessageCompletionItemDTO `json:"items"`
	// Count is how many items matched; at most MaxMessageCompletionItems
	// are in Items.
	Count int `json:"count"`
}

// MessageCompletionItemDTO is one suggestion.
type MessageCompletionItemDTO struct {
	Label string `json:"label"`
	// Kind is "field", "extension", "enum-value", "keyword" (true, false,
	// null), "type" (an Any type URL) or "map-key".
	Kind string `json:"kind"`
	// Detail is the short type of a field ("repeated Line",
	// "map<string, int32>") or the number of an enum value.
	Detail string `json:"detail"`
	// Type is the full name of the message or enum type, if any.
	Type string `json:"type"`
	// Documentation is the comment in the .proto.
	Documentation string `json:"documentation"`
	Deprecated    bool   `json:"deprecated"`
	// Number is the field or enum value number, 0 for others.
	Number int32 `json:"number"`
	// Oneof is the oneof a field belongs to.
	Oneof string `json:"oneof"`
	// InsertText replaces From–To, Suffix follows it (`: ""` after a
	// key), with the cursor SuffixCursor UTF-16 units into Suffix.
	InsertText   string `json:"insertText"`
	Suffix       string `json:"suffix"`
	SuffixCursor int    `json:"suffixCursor"`
	// Retrigger is true when there are suggestions at the cursor after
	// the insertion (the value of an enum field), so the editor asks again.
	Retrigger bool `json:"retrigger"`
}

// CompleteMessage returns the suggestions at in.Offset of the request
// message JSON in.Message (GRPC-17): the fields not set yet of the
// message at the cursor, at any depth (lists, maps, oneofs, Any by its
// "@type"), with their types and comments, and the enum names, true,
// false and null that fit at a value (protojson.Complete). The linked
// descriptors come from the service's cache, so a call re-links nothing
// unless a .proto file changed. It fails like ExampleMessage for a method
// that is not in the setup.
func (s *GRPCService) CompleteMessage(ctx context.Context, in MessageCompleteInputDTO) (MessageCompletionDTO, error) {
	pool, m, err := s.findMethod(ctx, in.Files, in.ImportPaths, in.Service, in.Method)
	if err != nil {
		return MessageCompletionDTO{}, err
	}
	text := in.Message
	typ := messageType(m, in.Output)
	res := protojson.Complete(pool, typ, text, byteOffset(text, in.Offset))
	out := MessageCompletionDTO{
		Type:    typ.FullName,
		Context: string(res.Context),
		From:    utf16Len(text[:res.From]),
		To:      utf16Len(text[:res.To]),
		Prefix:  res.Prefix,
		Owner:   res.Owner,
		Count:   len(res.Items),
		Items:   make([]MessageCompletionItemDTO, 0, min(len(res.Items), MaxMessageCompletionItems)),
	}
	for i, it := range res.Items {
		if i == MaxMessageCompletionItems {
			break
		}
		out.Items = append(out.Items, MessageCompletionItemDTO{
			Label:         it.Label,
			Kind:          string(it.Kind),
			Detail:        it.Detail,
			Type:          it.Type,
			Documentation: it.Documentation,
			Deprecated:    it.Deprecated,
			Number:        it.Number,
			Oneof:         it.Oneof,
			InsertText:    it.InsertText,
			Suffix:        it.Suffix,
			// Suffixes are ASCII: bytes are UTF-16 code units.
			SuffixCursor: it.SuffixCursor,
			Retrigger:    it.Retrigger,
		})
	}
	return out, nil
}

// byteOffset is the byte offset in s of off16 UTF-16 code units, clamped
// to s; a position inside a surrogate pair goes to the character's end.
func byteOffset(s string, off16 int) int {
	n := 0
	for i, r := range s {
		if n >= off16 {
			return i
		}
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return len(s)
}
