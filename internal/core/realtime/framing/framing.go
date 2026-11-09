// Package framing splits a byte stream into messages and frames messages
// for sending (TCP-03). A TCP connection delivers bytes in chunks of any
// size; a framing mode says where one message ends:
//
//   - none: no messages, every chunk is passed on as it was read;
//   - newline: a message ends at a line feed (LF); a carriage return
//     right before it is part of the line ending, so LF and CRLF lines are
//     both understood. Sending appends LF or CRLF (Options.Delimiter);
//   - length: every message starts with its length, an unsigned integer of
//     1, 2, 4 or 8 bytes in big- or little-endian order that counts the
//     bytes after the prefix (not the prefix itself).
//
// A Splitter keeps the bytes of an unfinished message between chunks, so
// messages split across reads, and several messages in one read, come out
// whole. Messages longer than Options.MaxMessageSize fail with
// CodeMessageTooLarge instead of growing the buffer without bound.
//
// Standard library only. See docs/tcp.md#framing.
package framing

import (
	"bytes"
	"encoding/binary"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Mode is a framing mode.
type Mode string

// The framing modes.
const (
	ModeNone    Mode = "none"
	ModeNewline Mode = "newline"
	ModeLength  Mode = "length"
)

// Delimiters of newline framing, for Options.Delimiter.
const (
	DelimiterLF   = "lf"
	DelimiterCRLF = "crlf"
)

// Byte orders of the length prefix, for Options.ByteOrder.
const (
	BigEndian    = "big"
	LittleEndian = "little"
)

// Limits of Options.MaxMessageSize.
const (
	// DefaultMaxMessageSize is the largest message received when
	// Options.MaxMessageSize is zero (1 MiB).
	DefaultMaxMessageSize = 1 << 20
	// LimitMaxMessageSize is the largest MaxMessageSize accepted (32 MiB,
	// the WebSocket client's limit).
	LimitMaxMessageSize = 32 << 20
	// DefaultPrefixSize is the length prefix's size when Options.PrefixSize
	// is zero.
	DefaultPrefixSize = 4
)

// Error codes. See docs/tcp.md#framing.
const (
	// CodeInvalidOptions: an unknown mode, delimiter or byte order, a
	// prefix size other than 1, 2, 4 or 8, or a maximum out of range
	// (invalid_argument). Details: field, value.
	CodeInvalidOptions = "framing.invalid_options"
	// CodeMessageTooLarge: a received message is longer than
	// MaxMessageSize, or a message to send does not fit the length prefix
	// (protocol). Details: size (when known), maxSize.
	CodeMessageTooLarge = "framing.message_too_large"
)

// Options configure a framing. The zero value is ModeNone.
type Options struct {
	// Mode is "none" (or ""), "newline" or "length".
	Mode Mode
	// Delimiter is what newline framing appends when sending: "lf" (or
	// "") or "crlf". Receiving splits at LF and drops a CR before it
	// either way.
	Delimiter string
	// PrefixSize is the length prefix's size in bytes: 1, 2, 4 or 8; zero
	// means DefaultPrefixSize.
	PrefixSize int
	// ByteOrder of the length prefix: "big" (or "", network order) or
	// "little".
	ByteOrder string
	// MaxMessageSize limits a received message in bytes, without its
	// delimiter or prefix: zero means DefaultMaxMessageSize; at most
	// LimitMaxMessageSize. Ignored by ModeNone.
	MaxMessageSize int
}

// Normalize checks o and returns it with the defaults filled in, or a
// CodeInvalidOptions error.
func (o Options) Normalize() (Options, error) {
	switch o.Mode {
	case "":
		o.Mode = ModeNone
	case ModeNone, ModeNewline, ModeLength:
	default:
		return o, invalid("mode", string(o.Mode), "The framing mode "+strconv.Quote(string(o.Mode))+" is not one PostSonde knows.",
			`Use "none", "newline" or "length".`)
	}
	switch o.Delimiter {
	case "":
		o.Delimiter = DelimiterLF
	case DelimiterLF, DelimiterCRLF:
	default:
		return o, invalid("delimiter", o.Delimiter, "The line delimiter "+strconv.Quote(o.Delimiter)+" is not one PostSonde knows.",
			`Use "lf" or "crlf".`)
	}
	switch o.PrefixSize {
	case 0:
		o.PrefixSize = DefaultPrefixSize
	case 1, 2, 4, 8:
	default:
		return o, invalid("prefixSize", strconv.Itoa(o.PrefixSize), "A length prefix of "+strconv.Itoa(o.PrefixSize)+" bytes is not supported.",
			"Use 1, 2, 4 or 8 bytes.")
	}
	switch o.ByteOrder {
	case "":
		o.ByteOrder = BigEndian
	case BigEndian, LittleEndian:
	default:
		return o, invalid("byteOrder", o.ByteOrder, "The byte order "+strconv.Quote(o.ByteOrder)+" is not one PostSonde knows.",
			`Use "big" or "little".`)
	}
	switch {
	case o.MaxMessageSize == 0:
		o.MaxMessageSize = DefaultMaxMessageSize
	case o.MaxMessageSize < 0 || o.MaxMessageSize > LimitMaxMessageSize:
		return o, invalid("maxMessageSize", strconv.Itoa(o.MaxMessageSize), "The maximum message size must be from 1 byte to 32 MiB.",
			"Enter a size from 1 to "+strconv.Itoa(LimitMaxMessageSize)+" bytes, or leave it empty for 1 MiB.")
	}
	return o, nil
}

// Framed reports whether o splits messages (a mode other than none).
func (o Options) Framed() bool {
	return o.Mode == ModeNewline || o.Mode == ModeLength
}

func invalid(field, value, msg, hint string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).
		WithCode(CodeInvalidOptions).
		WithHint(hint).
		WithDetail("field", field).
		WithDetail("value", value)
}

func (o Options) order() binary.ByteOrder {
	if o.ByteOrder == LittleEndian {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

// maxPrefixValue is the largest length a prefix of o.PrefixSize bytes
// holds.
func (o Options) maxPrefixValue() uint64 {
	if o.PrefixSize >= 8 {
		return ^uint64(0)
	}
	return 1<<(8*o.PrefixSize) - 1
}

// Encode frames payload for sending: unchanged for none, with the
// delimiter appended for newline, after its length for length. o must be
// normalized. A payload longer than the prefix can hold fails with
// CodeMessageTooLarge. The payload is sent as it is otherwise: a line
// break inside it is not escaped.
func Encode(o Options, payload []byte) ([]byte, error) {
	switch o.Mode {
	case ModeNewline:
		delim := "\n"
		if o.Delimiter == DelimiterCRLF {
			delim = "\r\n"
		}
		out := make([]byte, 0, len(payload)+len(delim))
		return append(append(out, payload...), delim...), nil
	case ModeLength:
		if uint64(len(payload)) > o.maxPrefixValue() {
			return nil, errs.New(errs.Protocol, "The message is too long for a "+strconv.Itoa(o.PrefixSize)+"-byte length prefix.").
				WithCode(CodeMessageTooLarge).
				WithHint("A "+strconv.Itoa(o.PrefixSize)+"-byte prefix holds at most "+strconv.FormatUint(o.maxPrefixValue(), 10)+" bytes. Send a shorter message or use a longer prefix.").
				WithDetail("size", strconv.Itoa(len(payload))).
				WithDetail("maxSize", strconv.FormatUint(o.maxPrefixValue(), 10))
		}
		out := make([]byte, o.PrefixSize, o.PrefixSize+len(payload))
		putPrefix(o, out, uint64(len(payload)))
		return append(out, payload...), nil
	default:
		return append([]byte(nil), payload...), nil
	}
}

func putPrefix(o Options, b []byte, n uint64) {
	switch o.PrefixSize {
	case 1:
		b[0] = byte(n)
	case 2:
		o.order().PutUint16(b, uint16(n))
	case 4:
		o.order().PutUint32(b, uint32(n))
	default:
		o.order().PutUint64(b, n)
	}
}

func readPrefix(o Options, b []byte) uint64 {
	switch o.PrefixSize {
	case 1:
		return uint64(b[0])
	case 2:
		return uint64(o.order().Uint16(b))
	case 4:
		return uint64(o.order().Uint32(b))
	default:
		return o.order().Uint64(b)
	}
}

// Splitter turns chunks of a stream into messages. It is not safe for
// concurrent use.
type Splitter struct {
	o   Options
	buf []byte // the unfinished message (and, for length, its prefix)
	// scanned is how much of buf is known to hold no LF (newline).
	scanned int
	failed  error
}

// NewSplitter returns a Splitter for o, which must be normalized.
func NewSplitter(o Options) *Splitter {
	return &Splitter{o: o}
}

// Feed adds a chunk and returns the messages it completed, in order. The
// messages are new slices, owned by the caller; chunk is not kept. For
// ModeNone the chunk itself (copied) is the one message.
//
// A message longer than MaxMessageSize fails with CodeMessageTooLarge: the
// messages completed before it are returned with the error, and the
// stream cannot be followed any further (every later Feed returns the same
// error).
func (s *Splitter) Feed(chunk []byte) ([][]byte, error) {
	if s.failed != nil {
		return nil, s.failed
	}
	if len(chunk) == 0 {
		return nil, nil
	}
	switch s.o.Mode {
	case ModeNewline:
		return s.feedNewline(chunk)
	case ModeLength:
		return s.feedLength(chunk)
	default:
		return [][]byte{append([]byte(nil), chunk...)}, nil
	}
}

// Rest returns the bytes of an unfinished message (with its length prefix
// for length framing), or nil. It is what is left when the stream ends in
// the middle of a message. After a failure it is nil.
func (s *Splitter) Rest() []byte {
	if s.failed != nil || len(s.buf) == 0 {
		return nil
	}
	return append([]byte(nil), s.buf...)
}

func (s *Splitter) fail(err error) error {
	s.failed = err
	s.buf, s.scanned = nil, 0
	return err
}

func (s *Splitter) tooLarge(size string, what string) error {
	limit := strconv.Itoa(s.o.MaxMessageSize)
	e := errs.New(errs.Protocol, what+" is longer than the maximum message size of "+limit+" bytes.").
		WithCode(CodeMessageTooLarge).
		WithHint("Raise the maximum message size in the framing settings, or check that the framing matches what the server sends.").
		WithDetail("maxSize", limit)
	if size != "" {
		e = e.WithDetail("size", size)
	}
	return s.fail(e)
}

func (s *Splitter) feedNewline(chunk []byte) ([][]byte, error) {
	s.buf = append(s.buf, chunk...)
	var msgs [][]byte
	start := 0
	for {
		i := bytes.IndexByte(s.buf[start+s.scanned:], '\n')
		if i < 0 {
			break
		}
		end := start + s.scanned + i // the LF
		line := s.buf[start:end]
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		if len(line) > s.o.MaxMessageSize {
			return msgs, s.tooLarge(strconv.Itoa(len(line)), "A line")
		}
		msgs = append(msgs, append([]byte(nil), line...))
		start = end + 1
		s.scanned = 0
	}
	rest := s.buf[start:]
	s.scanned = len(rest)
	// The unfinished line may end in the CR of a CRLF, which is not part
	// of the message.
	pending := len(rest)
	if pending > 0 && rest[pending-1] == '\r' {
		pending--
	}
	if pending > s.o.MaxMessageSize {
		return msgs, s.tooLarge("", "A line")
	}
	s.compact(start)
	return msgs, nil
}

func (s *Splitter) feedLength(chunk []byte) ([][]byte, error) {
	s.buf = append(s.buf, chunk...)
	var msgs [][]byte
	start := 0
	p := s.o.PrefixSize
	for len(s.buf)-start >= p {
		n := readPrefix(s.o, s.buf[start:start+p])
		if n > uint64(s.o.MaxMessageSize) {
			return msgs, s.tooLarge(strconv.FormatUint(n, 10), "A message of "+strconv.FormatUint(n, 10)+" bytes")
		}
		end := start + p + int(n)
		if end > len(s.buf) {
			break
		}
		msgs = append(msgs, append([]byte(nil), s.buf[start+p:end]...))
		start = end
	}
	s.compact(start)
	return msgs, nil
}

// compact drops the first n bytes of buf, releasing a large buffer once it
// is empty.
func (s *Splitter) compact(n int) {
	if n == 0 {
		return
	}
	rest := len(s.buf) - n
	if rest == 0 {
		if cap(s.buf) > 64<<10 {
			s.buf = nil
		} else {
			s.buf = s.buf[:0]
		}
		return
	}
	copy(s.buf, s.buf[n:])
	s.buf = s.buf[:rest]
}
