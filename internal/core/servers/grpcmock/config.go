package grpcmock

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
)

// CodeInvalidConfig: a configuration the gRPC mock server cannot use.
// Details: field ("files", "importPaths", "methods"), and for a method
// methodIndex (0-based) and methodField ("service", "method", "messages",
// "status", "message", "headers", "trailers", "delayMs", "intervalMs",
// "bidi"), plus row for a metadata row.
const CodeInvalidConfig = "grpcmock.invalid_config"

// Bidirectional streaming modes (Method.Bidi).
const (
	// BidiEach answers every message the client sends with the next
	// canned message, starting over after the last one, and ends with the
	// status when the client half-closes.
	BidiEach = "each"
	// BidiStream sends every canned message as a server stream does,
	// right away, then ends with the status once the client half-closes.
	BidiStream = "stream"
	// BidiEcho sends every message the client sends back as it is; the
	// method's input and output types must be the same.
	BidiEcho = "echo"
)

// Limits of a configuration.
const (
	MaxFiles       = 100
	MaxMethods     = 1000
	MaxMessages    = 1000
	MaxMessageText = 1 << 20 // bytes of JSON per canned message
	MaxMetadata    = 100     // rows of headers, and of trailers
	MaxDelayMs     = 300_000 // 5 minutes, as the HTTP mock
	MaxStatusCode  = int(transport.Unauthenticated)
	maxStatusText  = 4096
)

// Config is the configuration of a gRPC mock server (Spec.Config).
type Config struct {
	// Files are the .proto files that define the services served, and
	// ImportPaths their import roots, as a gRPC request stores them:
	// relative to the open project's root, or absolute
	// (Options.Resolve makes them absolute).
	Files       []string
	ImportPaths []string
	// Reflection serves the server reflection service (v1 and v1alpha),
	// so clients such as grpcurl can list the services without the
	// .proto files.
	Reflection bool
	// Methods are the canned answers, one per method at most. A method of
	// the .proto files without one answers UNIMPLEMENTED.
	Methods []Method
}

// Method is the canned answer of one method.
type Method struct {
	// Service is the fully-qualified service ("shop.v1.Orders"), Method
	// the method's name ("GetOrder").
	Service string
	Method  string
	// Messages are the response messages in the JSON mapping of the
	// method's output type. A unary or client-streaming method sends the
	// first (an empty message when there is none); a server-streaming
	// method sends them all; a bidirectional one as Bidi says.
	Messages []string
	// Status is the status code the call ends with (0, OK, to 16) and
	// StatusMessage its message. A unary or client-streaming call that
	// fails sends no message.
	Status        int
	StatusMessage string
	// Headers are sent as the response headers, Trailers with the status.
	// Values of "-bin" keys are Base64.
	Headers  []Metadata
	Trailers []Metadata
	// DelayMs holds back the answer: after the request (unary, server
	// streaming), after the client's half-close (client streaming), before
	// each reply (bidi "each" and "echo") or before the stream (bidi
	// "stream"). IntervalMs is the pause between streamed messages.
	DelayMs    int
	IntervalMs int
	// Bidi is how a bidirectional method answers: BidiEach (the default
	// for ""), BidiStream or BidiEcho.
	Bidi string
}

// Path is the method's gRPC path, "/pkg.Service/Method".
func (m Method) Path() string { return "/" + m.Service + "/" + m.Method }

// Metadata is one row of response metadata; a disabled row is not sent.
type Metadata struct {
	Enabled bool
	Name    string
	Value   string
}

// DefaultConfig is the configuration of a new gRPC mock server: no files
// yet, reflection on.
func DefaultConfig() Config {
	return Config{Reflection: true, Files: []string{}, ImportPaths: []string{}, Methods: []Method{}}
}

// Normalize checks c and returns it with paths trimmed, empty paths
// dropped, the bidi mode and metadata names normalized (lower case).
// Whether the methods and messages fit the .proto files is checked when
// the server links them (Start, Reconfigure), not here.
func Normalize(c Config) (Config, error) {
	var err error
	if c.Files, err = paths(c.Files, "files", "file"); err != nil {
		return Config{}, err
	}
	if c.ImportPaths, err = paths(c.ImportPaths, "importPaths", "import path"); err != nil {
		return Config{}, err
	}
	if len(c.Methods) > MaxMethods {
		return Config{}, invalidConfig("methods", fmt.Sprintf("There are %d methods; at most %d can have a canned response.", len(c.Methods), MaxMethods))
	}
	out := make([]Method, 0, len(c.Methods))
	seen := map[string]int{}
	for i, m := range c.Methods {
		m, err := normalizeMethod(i, m)
		if err != nil {
			return Config{}, err
		}
		if j, dup := seen[m.Path()]; dup {
			return Config{}, methodError(i, "method", fmt.Sprintf("Method %d: %s has a canned response already (method %d).", i+1, m.Path(), j+1))
		}
		seen[m.Path()] = i
		out = append(out, m)
	}
	c.Methods = out
	return c, nil
}

func paths(in []string, field, what string) ([]string, error) {
	if len(in) > MaxFiles {
		return nil, invalidConfig(field, fmt.Sprintf("There are %d entries; at most %d are allowed.", len(in), MaxFiles))
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !utf8.ValidString(p) || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, invalidConfig(field, "The "+what+" "+strconv.Quote(p)+" is not a valid path.")
		}
		out = append(out, p)
	}
	return out, nil
}

func normalizeMethod(i int, m Method) (Method, error) {
	m.Service = strings.TrimPrefix(strings.TrimSpace(m.Service), ".")
	m.Method = strings.TrimSpace(m.Method)
	if !dottedName(m.Service) {
		return Method{}, methodError(i, "service", fmt.Sprintf("Method %d: %q is not a service name.", i+1, m.Service))
	}
	if !identifier(m.Method) {
		return Method{}, methodError(i, "method", fmt.Sprintf("Method %d: %q is not a method name.", i+1, m.Method))
	}
	if len(m.Messages) > MaxMessages {
		return Method{}, methodError(i, "messages", fmt.Sprintf("Method %d has %d messages; at most %d are allowed.", i+1, len(m.Messages), MaxMessages))
	}
	for j, text := range m.Messages {
		if len(text) > MaxMessageText {
			return Method{}, methodError(i, "messages", fmt.Sprintf("Method %d: message %d is larger than 1 MiB.", i+1, j+1))
		}
		if !utf8.ValidString(text) {
			return Method{}, methodError(i, "messages", fmt.Sprintf("Method %d: message %d is not valid UTF-8.", i+1, j+1))
		}
	}
	if m.Messages == nil {
		m.Messages = []string{}
	}
	if m.Status < 0 || m.Status > MaxStatusCode {
		return Method{}, methodError(i, "status", fmt.Sprintf("Method %d: %d is not a gRPC status code (0 to %d).", i+1, m.Status, MaxStatusCode))
	}
	if len(m.StatusMessage) > maxStatusText || !utf8.ValidString(m.StatusMessage) {
		return Method{}, methodError(i, "message", fmt.Sprintf("Method %d: the status message must be UTF-8 text of at most %d bytes.", i+1, maxStatusText))
	}
	var err error
	if m.Headers, err = metadataRows(i, "headers", m.Headers); err != nil {
		return Method{}, err
	}
	if m.Trailers, err = metadataRows(i, "trailers", m.Trailers); err != nil {
		return Method{}, err
	}
	if m.DelayMs < 0 || m.DelayMs > MaxDelayMs {
		return Method{}, methodError(i, "delayMs", fmt.Sprintf("Method %d: the delay must be 0 to %d ms.", i+1, MaxDelayMs))
	}
	if m.IntervalMs < 0 || m.IntervalMs > MaxDelayMs {
		return Method{}, methodError(i, "intervalMs", fmt.Sprintf("Method %d: the interval must be 0 to %d ms.", i+1, MaxDelayMs))
	}
	switch m.Bidi {
	case "":
		m.Bidi = BidiEach
	case BidiEach, BidiStream, BidiEcho:
	default:
		return Method{}, methodError(i, "bidi", fmt.Sprintf("Method %d: %q is not a bidirectional mode; use each, stream or echo.", i+1, m.Bidi))
	}
	return m, nil
}

// metadataRows checks rows as the transport checks metadata it sends: a
// key of lower-case letters, digits, '-', '_' and '.', not reserved
// (grpc-*, content-type, …), and a printable ASCII value, or Base64 for a
// "-bin" key. Names are trimmed and lower-cased; a row with neither name
// nor value is dropped.
func metadataRows(i int, field string, rows []Metadata) ([]Metadata, error) {
	if len(rows) > MaxMetadata {
		return nil, methodError(i, field, fmt.Sprintf("Method %d has %d %s; at most %d are allowed.", i+1, len(rows), field, MaxMetadata))
	}
	out := make([]Metadata, 0, len(rows))
	for _, r := range rows {
		r.Name = strings.ToLower(strings.TrimSpace(r.Name))
		if r.Name == "" && r.Value == "" {
			continue
		}
		row := strconv.Itoa(len(out))
		entry := transport.MDEntry{Key: r.Name, Value: r.Value}
		if transport.IsBinaryKey(r.Name) {
			b, ok := DecodeBase64(r.Value)
			if !ok {
				return nil, methodError(i, field, fmt.Sprintf("Method %d: the value of %s is not Base64; values of -bin keys are binary and entered as Base64.", i+1, r.Name)).
					WithDetail("row", row)
			}
			entry.Value = string(b)
		}
		if err := transport.ValidateMetadata(transport.Metadata{entry}); err != nil {
			msg := err.Error()
			if e, ok := err.(*errs.Error); ok {
				msg = e.Message
			}
			return nil, methodError(i, field, fmt.Sprintf("Method %d: %s", i+1, msg)).WithDetail("row", row)
		}
		out = append(out, r)
	}
	return out, nil
}

// metadata returns the enabled rows as transport metadata, "-bin" values
// decoded (the rows were checked by Normalize).
func metadata(rows []Metadata) transport.Metadata {
	var md transport.Metadata
	for _, r := range rows {
		if !r.Enabled || r.Name == "" {
			continue
		}
		v := r.Value
		if transport.IsBinaryKey(r.Name) {
			b, _ := DecodeBase64(v)
			v = string(b)
		}
		md = append(md, transport.MDEntry{Key: r.Name, Value: v})
	}
	return md
}

// DecodeBase64 accepts standard and URL-safe Base64, padded or not, with
// whitespace anywhere, as the gRPC request editor does.
func DecodeBase64(s string) ([]byte, bool) {
	s = strings.Join(strings.Fields(s), "")
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

func identifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || i > 0 && '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

func dottedName(s string) bool {
	for part := range strings.SplitSeq(s, ".") {
		if !identifier(part) {
			return false
		}
	}
	return true
}

func invalidConfig(field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidConfig).WithDetail("field", field)
}

func methodError(i int, field, msg string) *errs.Error {
	return invalidConfig("methods", msg).WithDetail("methodIndex", strconv.Itoa(i)).WithDetail("methodField", field)
}
