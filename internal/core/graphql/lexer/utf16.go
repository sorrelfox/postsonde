package lexer

import (
	"sort"
	"unicode/utf8"
)

// UTF16Index converts offsets in one source between bytes (Token.Start and
// Token.End) and UTF-16 code units (CodeMirror and JavaScript string
// offsets). An invalid UTF-8 byte counts as one code unit, as it decodes to
// U+FFFD. Building the index is O(n); each conversion is O(log k) for k
// non-ASCII characters, and O(1) for ASCII-only sources.
type UTF16Index struct {
	size, size16 int
	marks        []utf16Mark // non-ASCII characters in source order
}

type utf16Mark struct {
	off, off16   int // start of the character
	width, units int // bytes and UTF-16 code units
}

// NewUTF16Index indexes src.
func NewUTF16Index(src string) *UTF16Index {
	x := &UTF16Index{size: len(src)}
	shift := 0 // off16 - off so far
	for i := 0; i < len(src); {
		if src[i] < utf8.RuneSelf {
			i++
			continue
		}
		r, width := utf8.DecodeRuneInString(src[i:])
		units := 1
		if r >= 0x10000 {
			units = 2
		}
		x.marks = append(x.marks, utf16Mark{off: i, off16: i + shift, width: width, units: units})
		shift += units - width
		i += width
	}
	x.size16 = len(src) + shift
	return x
}

// Len16 returns the source length in UTF-16 code units.
func (x *UTF16Index) Len16() int { return x.size16 }

// ToUTF16 converts a byte offset to a UTF-16 offset. Offsets inside a
// multi-byte character map to the character's start; out-of-range offsets
// are clamped.
func (x *UTF16Index) ToUTF16(off int) int {
	off = min(max(off, 0), x.size)
	i := sort.Search(len(x.marks), func(i int) bool { return x.marks[i].off > off }) - 1
	if i < 0 {
		return off
	}
	m := x.marks[i]
	if off < m.off+m.width {
		return m.off16
	}
	return m.off16 + m.units + off - (m.off + m.width)
}

// ToByte converts a UTF-16 offset to a byte offset. Offsets inside a
// surrogate pair map to the character's start; out-of-range offsets are
// clamped.
func (x *UTF16Index) ToByte(off16 int) int {
	off16 = min(max(off16, 0), x.size16)
	i := sort.Search(len(x.marks), func(i int) bool { return x.marks[i].off16 > off16 }) - 1
	if i < 0 {
		return off16
	}
	m := x.marks[i]
	if off16 < m.off16+m.units {
		return m.off
	}
	return m.off + m.width + off16 - (m.off16 + m.units)
}
