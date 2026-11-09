package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Framing constants: every gRPC message is a 1-byte compressed flag, a
// 4-byte big-endian length and the payload.
const (
	// FrameHeaderLen is the size of the prefix before each message.
	FrameHeaderLen = 5
	// MaxFramePayload is the largest length the prefix can express.
	MaxFramePayload = math.MaxUint32
	// DefaultMaxRecvMessageSize is the receive limit when none is set: 4 MiB,
	// as in grpc-go.
	DefaultMaxRecvMessageSize = 4 << 20
)

// Frame flags. gRPC-Web adds the trailer bit: a frame with it set holds
// the trailers as HTTP/1-style header lines instead of a message.
const (
	flagUncompressed byte = 0
	flagCompressed   byte = 1
	flagTrailer      byte = 0x80
)

// ErrFrameTruncated is returned when the input ends inside a frame.
var ErrFrameTruncated = errors.New("grpc frame truncated")

// FrameTooLargeError is returned when a frame declares a payload longer
// than the reader's limit. The payload has not been read.
type FrameTooLargeError struct {
	Size uint32
	Max  int
}

func (e *FrameTooLargeError) Error() string {
	return fmt.Sprintf("grpc message of %d bytes exceeds the limit of %d bytes", e.Size, e.Max)
}

// FrameFlagError is returned for a flag byte other than 0 or 1 (or, from
// a gRPC-Web reader, 0x80 or 0x81).
type FrameFlagError struct {
	Flag byte
}

func (e *FrameFlagError) Error() string {
	return fmt.Sprintf("grpc frame has invalid compressed flag %#x", e.Flag)
}

// AppendFrame appends one frame holding payload to dst. It panics if the
// payload is longer than MaxFramePayload; callers check first.
func AppendFrame(dst []byte, compressed bool, payload []byte) []byte {
	if uint64(len(payload)) > MaxFramePayload {
		panic("transport: frame payload too long")
	}
	flag := flagUncompressed
	if compressed {
		flag = flagCompressed
	}
	dst = append(dst, flag)
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)))
	return append(dst, payload...)
}

// Frame is one message as read from the wire.
type Frame struct {
	Compressed bool
	// Trailer marks a gRPC-Web trailer frame (flag bit 0x80): Payload is
	// the trailer block, not a message. Only a web reader returns one.
	Trailer bool
	Payload []byte
}

// FrameReader reads frames from a byte stream, however the bytes are split
// across reads.
type FrameReader struct {
	r io.Reader
	// max is the payload limit; negative means no limit.
	max int
	// web accepts gRPC-Web trailer frames.
	web bool
	hdr [FrameHeaderLen]byte
}

// NewFrameReader returns a reader of frames from r. max limits a payload's
// length: zero means DefaultMaxRecvMessageSize, a negative value no limit
// besides MaxFramePayload.
func NewFrameReader(r io.Reader, max int) *FrameReader {
	if max == 0 {
		max = DefaultMaxRecvMessageSize
	}
	return &FrameReader{r: r, max: max}
}

// NewWebFrameReader is NewFrameReader for a gRPC-Web body: it also
// accepts trailer frames (flag 0x80, or 0x81 compressed), returned with
// Frame.Trailer set.
func NewWebFrameReader(r io.Reader, max int) *FrameReader {
	fr := NewFrameReader(r, max)
	fr.web = true
	return fr
}

// Max returns the payload limit in effect (negative: none).
func (fr *FrameReader) Max() int { return fr.max }

// Next reads one frame. It returns io.EOF when the input ends cleanly
// between frames, ErrFrameTruncated (wrapping io.ErrUnexpectedEOF) when it
// ends inside one, *FrameTooLargeError or *FrameFlagError for a bad
// prefix, and other errors of the underlying reader as they are.
func (fr *FrameReader) Next() (Frame, error) {
	n, err := io.ReadFull(fr.r, fr.hdr[:])
	if err != nil {
		if err == io.EOF && n == 0 {
			return Frame{}, io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return Frame{}, fmt.Errorf("%w: %w", ErrFrameTruncated, err)
		}
		return Frame{}, err
	}
	flag := fr.hdr[0]
	trailer := false
	if fr.web && flag&flagTrailer != 0 {
		trailer, flag = true, flag&^flagTrailer
	}
	if flag != flagUncompressed && flag != flagCompressed {
		return Frame{}, &FrameFlagError{Flag: fr.hdr[0]}
	}
	size := binary.BigEndian.Uint32(fr.hdr[1:])
	if fr.max >= 0 && uint64(size) > uint64(fr.max) {
		return Frame{}, &FrameTooLargeError{Size: size, Max: fr.max}
	}
	payload, err := readPayload(fr.r, int64(size))
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return Frame{}, fmt.Errorf("%w: %w", ErrFrameTruncated, io.ErrUnexpectedEOF)
		}
		return Frame{}, err
	}
	return Frame{Compressed: flag == flagCompressed, Trailer: trailer, Payload: payload}, nil
}

// directReadLimit is the largest payload allocated up front. Longer ones
// grow as the bytes arrive, so a peer cannot make the reader allocate
// gigabytes by declaring a length it never sends.
const directReadLimit = 1 << 20

func readPayload(r io.Reader, size int64) ([]byte, error) {
	if size <= directReadLimit {
		b := make([]byte, size)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		return b, nil
	}
	var buf bytes.Buffer
	buf.Grow(directReadLimit)
	n, err := io.CopyN(&buf, r, size)
	if n == size {
		return buf.Bytes(), nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}
