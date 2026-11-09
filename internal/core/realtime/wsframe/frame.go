package wsframe

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// Opcode is the 4-bit frame opcode (RFC 6455 §5.2).
type Opcode uint8

// Opcodes defined by RFC 6455. 3–7 and 11–15 are reserved.
const (
	OpContinuation Opcode = 0x0
	OpText         Opcode = 0x1
	OpBinary       Opcode = 0x2
	OpClose        Opcode = 0x8
	OpPing         Opcode = 0x9
	OpPong         Opcode = 0xA
)

// IsControl reports whether op is a control opcode (0x8–0xF, including the
// reserved ones).
func (op Opcode) IsControl() bool { return op&0x8 != 0 }

// IsReserved reports whether op is not defined by RFC 6455.
func (op Opcode) IsReserved() bool {
	switch op {
	case OpContinuation, OpText, OpBinary, OpClose, OpPing, OpPong:
		return false
	}
	return true
}

// String returns the opcode's name, or "reserved(0xN)".
func (op Opcode) String() string {
	switch op {
	case OpContinuation:
		return "continuation"
	case OpText:
		return "text"
	case OpBinary:
		return "binary"
	case OpClose:
		return "close"
	case OpPing:
		return "ping"
	case OpPong:
		return "pong"
	}
	return "reserved(0x" + strconv.FormatUint(uint64(op), 16) + ")"
}

// RSV bits as they appear in the first header byte.
const (
	RSV1 byte = 0x40
	RSV2 byte = 0x20
	RSV3 byte = 0x10
)

// Size limits of the wire format.
const (
	// MaxHeaderSize is the largest frame header: 2 bytes, an 8-byte
	// extended length and a 4-byte mask key.
	MaxHeaderSize = 14
	// MaxControlPayload is the largest payload of a control frame.
	MaxControlPayload = 125
)

// Header is a decoded frame header with every wire detail kept.
type Header struct {
	Fin  bool
	Rsv1 bool
	Rsv2 bool
	Rsv3 bool
	// Opcode is the 4-bit opcode, reserved values included.
	Opcode Opcode
	// Masked is the MASK bit; MaskKey is the key when it is set.
	Masked  bool
	MaskKey [4]byte
	// Length is the payload length. On encoding it is taken from the
	// payload and this field is ignored.
	Length int64
	// LengthSize is the number of extended length bytes on the wire: 0
	// (7-bit length), 2 (16-bit) or 8 (64-bit). On encoding, 0 picks the
	// minimal encoding; 2 or 8 force a longer one (a non-minimal header, for
	// testing peers).
	LengthSize uint8
}

// RSV returns the RSV bits as a mask of RSV1, RSV2 and RSV3.
func (h Header) RSV() byte {
	var b byte
	if h.Rsv1 {
		b |= RSV1
	}
	if h.Rsv2 {
		b |= RSV2
	}
	if h.Rsv3 {
		b |= RSV3
	}
	return b
}

// Size returns the encoded header size in bytes.
func (h Header) Size() int {
	n := 2 + int(h.LengthSize)
	if h.Masked {
		n += 4
	}
	return n
}

// Frame is a header and its payload. Payload is always unmasked; MaskKey in
// the header says how it was (or will be) masked on the wire.
type Frame struct {
	Header
	Payload []byte
}

// Limits configure what ReadFrame accepts.
type Limits struct {
	// MaxPayload is the largest frame payload in bytes. 0 means
	// DefaultMaxPayload; a negative value means no limit.
	MaxPayload int64
	// AllowedRSV is the mask of RSV bits a negotiated extension defines.
	// Any other RSV bit set is a protocol error. No extension is
	// implemented yet, so this is 0 in practice (the permessage-deflate hook).
	AllowedRSV byte
	// AllowNonMinimalLength accepts lengths not encoded in the minimal
	// number of bytes. RFC 6455 §5.2 says the minimal encoding MUST be used;
	// by default a non-minimal length is a protocol error.
	AllowNonMinimalLength bool
}

// DefaultMaxPayload is the default frame payload limit (16 MiB).
const DefaultMaxPayload = 16 << 20

func (l Limits) maxPayload() int64 {
	switch {
	case l.MaxPayload == 0:
		return DefaultMaxPayload
	case l.MaxPayload < 0:
		return math.MaxInt
	}
	return min(l.MaxPayload, math.MaxInt)
}

// ReadFrame reads one frame from r and unmasks its payload.
//
// It rejects, with an *errs.Error of kind protocol whose close code
// CloseCodeOf returns: reserved opcodes, RSV bits not in lim.AllowedRSV,
// control frames that are fragmented or longer than 125 bytes, 64-bit
// lengths with the top bit set, non-minimal lengths (unless allowed) and
// payloads over lim.MaxPayload (1009). It does not check masking, which
// depends on the role (see Conn).
//
// At a frame boundary a clean end of r returns io.EOF. Whenever the header
// was decoded the returned Frame carries it, even with an error.
func ReadFrame(r io.Reader, lim Limits) (Frame, error) {
	var hb [MaxHeaderSize]byte
	h, err := readHeader(r, hb[:], lim)
	if err != nil {
		return Frame{Header: h}, err
	}
	p, err := readPayload(r, h.Length, nil)
	if err != nil {
		return Frame{Header: h}, err
	}
	if h.Masked {
		Mask(h.MaskKey, 0, p)
	}
	return Frame{Header: h, Payload: p}, nil
}

// readHeader reads and validates a header using hb (at least MaxHeaderSize
// bytes) as scratch space. Read errors are returned unwrapped except for a
// truncated header, which is io.ErrUnexpectedEOF.
func readHeader(r io.Reader, hb []byte, lim Limits) (Header, error) {
	var h Header
	if _, err := io.ReadFull(r, hb[:2]); err != nil {
		return h, err
	}
	b0, b1 := hb[0], hb[1]
	h.Fin = b0&0x80 != 0
	h.Rsv1 = b0&RSV1 != 0
	h.Rsv2 = b0&RSV2 != 0
	h.Rsv3 = b0&RSV3 != 0
	h.Opcode = Opcode(b0 & 0x0f)
	h.Masked = b1&0x80 != 0
	n := int64(b1 & 0x7f)
	switch n {
	case 126:
		h.LengthSize = 2
	case 127:
		h.LengthSize = 8
	}
	rest := int(h.LengthSize)
	if h.Masked {
		rest += 4
	}
	if rest > 0 {
		if _, err := io.ReadFull(r, hb[2:2+rest]); err != nil {
			return h, unexpected(err)
		}
	}
	ext := hb[2 : 2+h.LengthSize]
	switch h.LengthSize {
	case 2:
		n = int64(binary.BigEndian.Uint16(ext))
	case 8:
		u := binary.BigEndian.Uint64(ext)
		if u>>63 != 0 {
			return h, protocolError("The frame length has its most significant bit set.")
		}
		n = int64(u)
	}
	h.Length = n
	if h.Masked {
		copy(h.MaskKey[:], hb[2+h.LengthSize:])
	}

	switch {
	case h.Opcode.IsReserved():
		return h, protocolError("The frame uses the reserved opcode " + h.Opcode.String() + ".")
	case h.RSV()&^lim.AllowedRSV != 0:
		return h, protocolError("The frame sets RSV bits but no extension was negotiated.")
	case h.Opcode.IsControl() && !h.Fin:
		return h, protocolError("The " + h.Opcode.String() + " frame is fragmented.")
	case h.Opcode.IsControl() && n > MaxControlPayload:
		return h, protocolError("The " + h.Opcode.String() + " frame payload is longer than 125 bytes.")
	case !lim.AllowNonMinimalLength && h.LengthSize != minimalLengthSize(n):
		return h, protocolError("The frame length is not encoded in the minimal number of bytes.")
	case n > lim.maxPayload():
		return h, failure(CodeTooBig, CloseMessageTooBig,
			"The frame payload of "+strconv.FormatInt(n, 10)+" bytes is larger than the limit of "+
				strconv.FormatInt(lim.maxPayload(), 10)+" bytes.")
	}
	return h, nil
}

// unexpected turns io.EOF in the middle of a frame into io.ErrUnexpectedEOF.
func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// payloadChunk bounds how much readPayload allocates ahead of the data it
// has actually received, so a header that lies about its length cannot make
// it allocate the whole limit up front.
const payloadChunk = 1 << 20

// readPayload appends n bytes from r to dst and returns the extended slice.
func readPayload(r io.Reader, n int64, dst []byte) ([]byte, error) {
	start := len(dst)
	for int64(len(dst)-start) < n {
		want := int(min(n-int64(len(dst)-start), payloadChunk))
		if cap(dst)-len(dst) < want {
			grown := make([]byte, len(dst), len(dst)+max(want, len(dst)-start))
			copy(grown, dst)
			dst = grown
		}
		m, err := io.ReadFull(r, dst[len(dst):len(dst)+want])
		dst = dst[:len(dst)+m]
		if err != nil {
			return dst, unexpected(err)
		}
	}
	return dst, nil
}

func minimalLengthSize(n int64) uint8 {
	switch {
	case n <= 125:
		return 0
	case n <= math.MaxUint16:
		return 2
	}
	return 8
}

// AppendFrame appends the encoding of f to dst. The length comes from
// f.Payload; if f.Masked the payload is masked with f.MaskKey into dst
// (f.Payload is not modified). Nothing else is checked: opcode, RSV bits,
// FIN and control frame rules are encoded as given, so invalid frames can be
// crafted on purpose. It fails only when f.LengthSize cannot hold the length
// or the opcode does not fit in 4 bits.
func AppendFrame(dst []byte, f Frame) ([]byte, error) {
	n := int64(len(f.Payload))
	size := f.LengthSize
	switch {
	case f.Opcode > 0xf:
		return dst, invalidArgument("The opcode does not fit in 4 bits.")
	case size == 0:
		size = minimalLengthSize(n)
	case size != 2 && size != 8:
		return dst, invalidArgument("The length size must be 0, 2 or 8 bytes.")
	case size < minimalLengthSize(n):
		return dst, invalidArgument("The payload is too long for the requested length size.")
	}
	b0 := byte(f.Opcode) | f.RSV()
	if f.Fin {
		b0 |= 0x80
	}
	var b1 byte
	if f.Masked {
		b1 = 0x80
	}
	switch size {
	case 0:
		dst = append(dst, b0, b1|byte(n))
	case 2:
		dst = append(dst, b0, b1|126)
		dst = binary.BigEndian.AppendUint16(dst, uint16(n))
	case 8:
		dst = append(dst, b0, b1|127)
		dst = binary.BigEndian.AppendUint64(dst, uint64(n))
	}
	if f.Masked {
		dst = append(dst, f.MaskKey[:]...)
	}
	start := len(dst)
	dst = append(dst, f.Payload...)
	if f.Masked {
		Mask(f.MaskKey, 0, dst[start:])
	}
	return dst, nil
}

// WriteFrame encodes f with AppendFrame and writes it to w in one Write.
func WriteFrame(w io.Writer, f Frame) error {
	buf, err := AppendFrame(make([]byte, 0, MaxHeaderSize+len(f.Payload)), f)
	if err != nil {
		return err
	}
	_, err = w.Write(buf)
	return err
}

// Mask XORs b in place with key, starting at key offset pos (0–3), and
// returns the offset for the next byte. Masking and unmasking are the same
// operation (RFC 6455 §5.3).
func Mask(key [4]byte, pos int, b []byte) int {
	pos &= 3
	i := 0
	if len(b) >= 8 {
		k := [4]byte{key[pos], key[(pos+1)&3], key[(pos+2)&3], key[(pos+3)&3]}
		k32 := uint64(binary.LittleEndian.Uint32(k[:]))
		k64 := k32<<32 | k32
		for ; i+8 <= len(b); i += 8 {
			binary.LittleEndian.PutUint64(b[i:], binary.LittleEndian.Uint64(b[i:])^k64)
		}
	}
	for ; i < len(b); i++ {
		b[i] ^= key[(pos+i)&3]
	}
	return (pos + len(b)) & 3
}

// acceptGUID is the GUID of RFC 6455 §1.3.
const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ComputeAccept returns the Sec-WebSocket-Accept value for a
// Sec-WebSocket-Key (RFC 6455 §4.2.2).
func ComputeAccept(key string) string {
	sum := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// wrapIO maps a transport error to an *errs.Error, keeping it as the cause.
// A reset by the peer gets the shared netx.connection_reset code and hint.
func wrapIO(err error, code, msg string) error {
	if _, ok := errors.AsType[*errs.Error](err); ok {
		return err
	}
	if netx.IsReset(err) {
		return errs.Wrap(err, errs.Network, "The WebSocket connection was reset by the other side.").
			WithCode(netx.CodeConnectionReset).
			WithHint(netx.HintConnectionReset)
	}
	kind := errs.KindOf(err)
	if kind == errs.Internal {
		kind = errs.Network
	}
	return errs.Wrap(err, kind, msg).WithCode(code)
}
