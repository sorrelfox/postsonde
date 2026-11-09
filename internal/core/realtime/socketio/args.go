package socketio

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/hexbytes"
)

// Arguments typed as JSON text, as the Socket.IO editor, the Socket.IO
// server's composer and its auto-reply rules take them.

// Binary argument markers: an object with one of these as its only member
// is a binary argument (an attachment), in the args text and in the log.
const (
	MarkerBase64 = "$base64"
	MarkerHex    = "$hex"
)

// CodeInvalidArgs: arguments that are not JSON, or a binary marker that
// does not decode (kind invalid_argument). Details: offset.
const CodeInvalidArgs = "socketio.invalid_args"

// ParseArgs turns the args text into emit arguments: "" is none, an
// array one argument per element, another JSON value one argument.
// Arguments without binary markers are kept as their JSON text (so object
// members keep their order); marked objects become []byte.
func ParseArgs(text string) ([]any, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}
	lead := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
	var raw json.RawMessage
	dec := json.NewDecoder(strings.NewReader(trimmed))
	if err := dec.Decode(&raw); err != nil {
		off := len(trimmed)
		if se, ok := errors.AsType[*json.SyntaxError](err); ok && se.Offset > 0 {
			off = int(se.Offset) - 1
		}
		return nil, argsError(lead+off, "The arguments are not valid JSON.")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, argsError(lead+int(dec.InputOffset()), "The arguments have more text after the JSON value.").
			WithHint(`Put several arguments in an array: ["a", {"b": 1}].`)
	}
	var list []json.RawMessage
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, argsError(lead, "The arguments are not valid JSON.")
		}
	} else {
		list = []json.RawMessage{raw}
	}
	out := make([]any, len(list))
	for i, a := range list {
		if !bytes.Contains(a, []byte(`"$`)) {
			out[i] = compact(a)
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(a))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, argsError(lead, "The arguments are not valid JSON.")
		}
		v, err := binaryValues(v)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func compact(a json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, a); err != nil {
		return a
	}
	return buf.Bytes()
}

// binaryValues replaces the marker objects in v with their bytes.
func binaryValues(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 1 {
			if s, ok := x[MarkerBase64].(string); ok {
				b, err := decodeBase64(s)
				if err != nil {
					return nil, errs.New(errs.InvalidArgument, "A "+MarkerBase64+" argument is not valid Base64.").
						WithCode(CodeInvalidArgs)
				}
				return b, nil
			}
			if s, ok := x[MarkerHex].(string); ok {
				b, err := hexbytes.Parse(s)
				if err != nil {
					return nil, errs.New(errs.InvalidArgument, "A "+MarkerHex+" argument is not valid hex: "+err.Error()).
						WithCode(CodeInvalidArgs)
				}
				return b, nil
			}
		}
		for k, e := range x {
			r, err := binaryValues(e)
			if err != nil {
				return nil, err
			}
			x[k] = r
		}
	case []any:
		for i, e := range x {
			r, err := binaryValues(e)
			if err != nil {
				return nil, err
			}
			x[i] = r
		}
	}
	return v, nil
}

// decodeBase64 accepts the standard and the URL alphabet, padded or not,
// with whitespace anywhere.
func decodeBase64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		case '-':
			return '+'
		case '_':
			return '/'
		}
		return r
	}, s)
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

func argsError(offset int, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).
		WithCode(CodeInvalidArgs).
		WithHint(`Enter a JSON array with one element per argument, like ["hello", {"n": 1}]; {"$base64": "…"} or {"$hex": "…"} is a binary argument.`).
		WithDetail("offset", strconv.Itoa(offset))
}

// DisplayArgs is the JSON text of received or sent arguments for the log:
// the payload as on the wire, binary attachments as {"$base64": "…"}.
// skip leaves out the first element (an event's name).
func DisplayArgs(data json.RawMessage, atts [][]byte, skip int) string {
	var list []json.RawMessage
	if err := json.Unmarshal(data, &list); err != nil {
		return string(data)
	}
	if skip > len(list) {
		skip = len(list)
	}
	list = list[skip:]
	var b strings.Builder
	b.WriteByte('[')
	for i, a := range list {
		if i > 0 {
			b.WriteByte(',')
		}
		if len(atts) == 0 || !bytes.Contains(a, []byte(`"_placeholder"`)) {
			b.Write(a)
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(a))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			b.Write(a)
			continue
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(withMarkers(v, atts)); err != nil {
			b.Write(a)
			continue
		}
		b.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	}
	b.WriteByte(']')
	return b.String()
}

// withMarkers replaces the placeholders in v with {"$base64": "…"}.
func withMarkers(v any, atts [][]byte) any {
	switch x := v.(type) {
	case map[string]any:
		if p, ok := x["_placeholder"].(bool); ok && p {
			if n, ok := x["num"].(json.Number); ok {
				if i, err := strconv.Atoi(n.String()); err == nil && i >= 0 && i < len(atts) {
					return map[string]any{MarkerBase64: base64.StdEncoding.EncodeToString(atts[i])}
				}
			}
		}
		for k, e := range x {
			x[k] = withMarkers(e, atts)
		}
	case []any:
		for i, e := range x {
			x[i] = withMarkers(e, atts)
		}
	}
	return v
}
