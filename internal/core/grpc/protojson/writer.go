package protojson

import (
	"errors"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"unicode/utf8"
)

// writer writes JSON in the layout of protobuf-go's JSON encoder, without
// its random extra spaces: compact ("{"a":1,"b":[1,2]}"), or with an indent
// one element per line, "name": value with one space, and empty objects and
// arrays as {} and [].
type writer struct {
	indent  string
	last    kind
	indents []byte
	out     []byte
}

var errInvalidUTF8 = errors.New("invalid UTF-8")

func newWriter(indent string) (*writer, error) {
	if strings.Trim(indent, " \t") != "" {
		return nil, outputError("indent may only contain spaces and tabs")
	}
	return &writer{indent: indent}, nil
}

func (w *writer) null() {
	w.prepare(kindNull)
	w.out = append(w.out, "null"...)
}

func (w *writer) bool(b bool) {
	w.prepare(kindBool)
	w.out = strconv.AppendBool(w.out, b)
}

func (w *writer) string(s string) error {
	w.prepare(kindString)
	var err error
	w.out, err = appendString(w.out, s)
	return err
}

func (w *writer) name(s string) error {
	w.prepare(kindName)
	var err error
	w.out, err = appendString(w.out, s)
	w.out = append(w.out, ':')
	return err
}

func (w *writer) int(n int64) {
	w.prepare(kindNumber)
	w.out = strconv.AppendInt(w.out, n, 10)
}

func (w *writer) uint(n uint64) {
	w.prepare(kindNumber)
	w.out = strconv.AppendUint(w.out, n, 10)
}

func (w *writer) float(f float64, bitSize int) {
	w.prepare(kindNumber)
	w.out = appendFloat(w.out, f, bitSize)
}

func (w *writer) startObject() {
	w.prepare(kindObjectOpen)
	w.out = append(w.out, '{')
}

func (w *writer) endObject() {
	w.prepare(kindObjectClose)
	w.out = append(w.out, '}')
}

func (w *writer) startArray() {
	w.prepare(kindArrayOpen)
	w.out = append(w.out, '[')
}

func (w *writer) endArray() {
	w.prepare(kindArrayClose)
	w.out = append(w.out, ']')
}

// prepare writes the separator and indentation before a token of kind next.
func (w *writer) prepare(next kind) {
	const value = kindName | kindScalar | kindObjectOpen | kindArrayOpen
	const closing = kindObjectClose | kindArrayClose
	last := w.last
	w.last = next
	if next&kindScalar != 0 {
		w.last = kindScalar
	}
	if w.indent == "" {
		if last&(kindScalar|closing) != 0 && next&value != 0 {
			w.out = append(w.out, ',')
		}
		return
	}
	switch {
	case last&(kindObjectOpen|kindArrayOpen) != 0:
		if next&closing == 0 {
			w.indents = append(w.indents, w.indent...)
			w.out = append(w.out, '\n')
			w.out = append(w.out, w.indents...)
		}
	case last&(kindScalar|closing) != 0:
		switch {
		case next&value != 0:
			w.out = append(w.out, ',', '\n')
		case next&closing != 0:
			w.indents = w.indents[:len(w.indents)-len(w.indent)]
			w.out = append(w.out, '\n')
		}
		w.out = append(w.out, w.indents...)
	case last&kindName != 0:
		w.out = append(w.out, ' ')
	}
}

// appendString appends s as a JSON string. Only '"', '\\' and control
// characters are escaped; invalid UTF-8 is an error.
func appendString(out []byte, s string) ([]byte, error) {
	out = append(out, '"')
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && n == 1:
			return out, errInvalidUTF8
		case r < ' ' || r == '"' || r == '\\':
			out = append(out, '\\')
			switch r {
			case '"', '\\':
				out = append(out, byte(r))
			case '\b':
				out = append(out, 'b')
			case '\f':
				out = append(out, 'f')
			case '\n':
				out = append(out, 'n')
			case '\r':
				out = append(out, 'r')
			case '\t':
				out = append(out, 't')
			default:
				out = append(out, 'u')
				out = append(out, "000"[:3-(bits.Len32(uint32(r))-1)/4]...)
				out = strconv.AppendUint(out, uint64(r), 16)
			}
		default:
			out = append(out, s[:n]...)
		}
		s = s[n:]
	}
	return append(out, '"'), nil
}

// appendFloat formats f like protobuf-go (and encoding/json): shortest
// representation, exponent form below 1e-6 and from 1e21, and the strings
// "NaN", "Infinity" and "-Infinity".
func appendFloat(out []byte, f float64, bitSize int) []byte {
	switch {
	case math.IsNaN(f):
		return append(out, `"NaN"`...)
	case math.IsInf(f, 1):
		return append(out, `"Infinity"`...)
	case math.IsInf(f, -1):
		return append(out, `"-Infinity"`...)
	}
	format := byte('f')
	if abs := math.Abs(f); abs != 0 {
		if bitSize == 64 && (abs < 1e-6 || abs >= 1e21) ||
			bitSize == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	out = strconv.AppendFloat(out, f, format, -1, bitSize)
	if format == 'e' {
		// Clean up e-09 to e-9.
		n := len(out)
		if n >= 4 && out[n-4] == 'e' && out[n-3] == '-' && out[n-2] == '0' {
			out[n-2] = out[n-1]
			out = out[:n-1]
		}
	}
	return out
}
