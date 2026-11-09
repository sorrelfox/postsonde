// Package protowire reads and writes the Protocol Buffers binary wire
// format at the level of tags, varints, fixed-width values,
// length-delimited values and groups. It knows nothing about messages;
// package dynamic builds on it.
//
// Every Consume function checks bounds strictly and returns the number of
// bytes it read, or an error that is one of the Err* values below; it never
// panics on any input. Append functions write the canonical (shortest)
// encoding.
//
// See docs/protobuf-codec.md.
package protowire

import (
	"errors"
	"math/bits"
)

// Number is a field number.
type Number int32

// Field number limits.
const (
	MinValidNumber      Number = 1
	FirstReservedNumber Number = 19000
	LastReservedNumber  Number = 19999
	MaxValidNumber      Number = 1<<29 - 1
)

// Type is a wire type.
type Type int8

// Wire types.
const (
	VarintType     Type = 0
	Fixed64Type    Type = 1
	BytesType      Type = 2
	StartGroupType Type = 3
	EndGroupType   Type = 4
	Fixed32Type    Type = 5
)

func (t Type) String() string {
	switch t {
	case VarintType:
		return "varint"
	case Fixed64Type:
		return "fixed64"
	case BytesType:
		return "bytes"
	case StartGroupType:
		return "start group"
	case EndGroupType:
		return "end group"
	case Fixed32Type:
		return "fixed32"
	}
	return "invalid wire type"
}

// MaxGroupDepth is how deeply groups may nest inside a value skipped by
// ConsumeFieldValue or ConsumeGroup (protobuf-go uses the same limit).
const MaxGroupDepth = 10000

// Errors returned by the Consume functions.
var (
	ErrTruncated   = errors.New("unexpected end of input")
	ErrOverflow    = errors.New("varint overflows 64 bits")
	ErrFieldNumber = errors.New("invalid field number")
	ErrWireType    = errors.New("invalid wire type")
	ErrEndGroup    = errors.New("end group without a matching start group")
	ErrGroupDepth  = errors.New("groups nested too deeply")
)

// AppendVarint appends v as a base-128 varint.
func AppendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// SizeVarint returns the encoded size of v: 1 to 10 bytes.
func SizeVarint(v uint64) int {
	// 9/64 is close enough to 1/7 for 1 to 64 significant bits.
	return int(9*uint32(bits.Len64(v))+64) / 64
}

// ConsumeVarint reads a varint. Non-minimal encodings are accepted; a
// tenth byte greater than 1 overflows.
func ConsumeVarint(b []byte) (uint64, int, error) {
	var v uint64
	for i := 0; i < 10; i++ {
		if i >= len(b) {
			return 0, 0, ErrTruncated
		}
		c := b[i]
		if i == 9 && c > 1 {
			return 0, 0, ErrOverflow
		}
		v |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return v, i + 1, nil
		}
	}
	return 0, 0, ErrOverflow
}

// EncodeTag returns the tag of a field number and wire type.
func EncodeTag(num Number, typ Type) uint64 {
	return uint64(num)<<3 | uint64(typ&7)
}

// AppendTag appends a tag.
func AppendTag(b []byte, num Number, typ Type) []byte {
	return AppendVarint(b, EncodeTag(num, typ))
}

// SizeTag returns the encoded size of a tag with field number num.
func SizeTag(num Number) int { return SizeVarint(EncodeTag(num, VarintType)) }

// ConsumeTag reads a tag. The field number must be in [1, MaxValidNumber]
// (ErrFieldNumber) and the wire type one of the six defined ones
// (ErrWireType).
func ConsumeTag(b []byte) (Number, Type, int, error) {
	v, n, err := ConsumeVarint(b)
	if err != nil {
		return 0, 0, 0, err
	}
	num := v >> 3
	if num < uint64(MinValidNumber) || num > uint64(MaxValidNumber) {
		return 0, 0, 0, ErrFieldNumber
	}
	typ := Type(v & 7)
	if typ > Fixed32Type {
		return 0, 0, 0, ErrWireType
	}
	return Number(num), typ, n, nil
}

// AppendFixed32 appends v in little-endian order.
func AppendFixed32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// ConsumeFixed32 reads a little-endian uint32.
func ConsumeFixed32(b []byte) (uint32, int, error) {
	if len(b) < 4 {
		return 0, 0, ErrTruncated
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24, 4, nil
}

// AppendFixed64 appends v in little-endian order.
func AppendFixed64(b []byte, v uint64) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
}

// ConsumeFixed64 reads a little-endian uint64.
func ConsumeFixed64(b []byte) (uint64, int, error) {
	if len(b) < 8 {
		return 0, 0, ErrTruncated
	}
	lo, _, _ := ConsumeFixed32(b)
	hi, _, _ := ConsumeFixed32(b[4:])
	return uint64(lo) | uint64(hi)<<32, 8, nil
}

// AppendBytes appends a length-delimited value.
func AppendBytes(b, v []byte) []byte {
	return append(AppendVarint(b, uint64(len(v))), v...)
}

// AppendString appends a length-delimited string.
func AppendString(b []byte, v string) []byte {
	return append(AppendVarint(b, uint64(len(v))), v...)
}

// SizeBytes returns the encoded size of a length-delimited value of n
// bytes.
func SizeBytes(n int) int { return SizeVarint(uint64(n)) + n }

// ConsumeBytes reads a length-delimited value. The result aliases b.
func ConsumeBytes(b []byte) ([]byte, int, error) {
	l, n, err := ConsumeVarint(b)
	if err != nil {
		return nil, 0, err
	}
	if l > uint64(len(b)-n) {
		return nil, 0, ErrTruncated
	}
	return b[n : n+int(l)], n + int(l), nil
}

// ConsumeGroup reads the body of group num, starting after its start
// group tag, up to and including the matching end group tag. It returns
// the fields between the tags (aliasing b) and the bytes read.
func ConsumeGroup(num Number, b []byte) ([]byte, int, error) {
	n, end, err := consumeValue(num, StartGroupType, b)
	if err != nil {
		return nil, 0, err
	}
	return b[:end], n, nil
}

// ConsumeFieldValue reads the value of a field whose tag (num, typ) has
// already been read, and returns its size. Groups are skipped up to the
// matching end group tag. An end group tag on its own is ErrEndGroup.
func ConsumeFieldValue(num Number, typ Type, b []byte) (int, error) {
	n, _, err := consumeValue(num, typ, b)
	return n, err
}

// ConsumeField reads a whole field, tag included.
func ConsumeField(b []byte) (Number, Type, int, error) {
	num, typ, n, err := ConsumeTag(b)
	if err != nil {
		return 0, 0, 0, err
	}
	m, _, err := consumeValue(num, typ, b[n:])
	if err != nil {
		return 0, 0, 0, err
	}
	return num, typ, n + m, nil
}

// consumeValue skips one value iteratively, so deep groups do not grow
// the Go stack. For a group, tagStart is where the end group tag begins
// (b contains the bytes after the start tag).
func consumeValue(num Number, typ Type, b []byte) (n, tagStart int, err error) {
	var open []Number // group numbers waiting for their end tag
	pos := 0
	for {
		switch typ {
		case VarintType:
			_, m, err := ConsumeVarint(b[pos:])
			if err != nil {
				return 0, 0, err
			}
			pos += m
		case Fixed32Type:
			if len(b)-pos < 4 {
				return 0, 0, ErrTruncated
			}
			pos += 4
		case Fixed64Type:
			if len(b)-pos < 8 {
				return 0, 0, ErrTruncated
			}
			pos += 8
		case BytesType:
			_, m, err := ConsumeBytes(b[pos:])
			if err != nil {
				return 0, 0, err
			}
			pos += m
		case StartGroupType:
			if len(open) >= MaxGroupDepth {
				return 0, 0, ErrGroupDepth
			}
			open = append(open, num)
		case EndGroupType:
			if len(open) == 0 || open[len(open)-1] != num {
				return 0, 0, ErrEndGroup
			}
			open = open[:len(open)-1]
		default:
			return 0, 0, ErrWireType
		}
		if len(open) == 0 {
			return pos, tagStart, nil
		}
		tagStart = pos
		var m int
		num, typ, m, err = ConsumeTag(b[pos:])
		if err != nil {
			return 0, 0, err
		}
		pos += m
	}
}

// EncodeZigZag maps signed to unsigned integers so that small magnitudes
// stay small: 0, -1, 1, -2 become 0, 1, 2, 3.
func EncodeZigZag(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>63) }

// DecodeZigZag reverses EncodeZigZag.
func DecodeZigZag(v uint64) int64 { return int64(v>>1) ^ -int64(v&1) }

// EncodeBool returns 1 for true and 0 for false.
func EncodeBool(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}

// DecodeBool reports whether v is non-zero.
func DecodeBool(v uint64) bool { return v != 0 }
