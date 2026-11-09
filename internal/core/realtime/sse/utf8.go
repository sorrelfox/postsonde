package sse

import (
	"strings"
	"unicode/utf8"
)

// decodeUTF8 decodes b with the WHATWG "UTF-8 decode without BOM"
// algorithm: every maximal invalid subpart becomes one U+FFFD. It differs
// from strings.ToValidUTF8 (one U+FFFD per run) and from utf8.DecodeRune
// (one U+FFFD per byte) for truncated sequences such as "\xE2\x82A".
//
// Decoding line by line gives the same text as decoding the whole stream:
// CR, LF and ':' are never continuation bytes, so a sequence cut by them
// ends in U+FFFD either way.
func decodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b) + 8)
	var (
		cp           rune
		needed, seen int
		lower, upper byte = 0x80, 0xBF
	)
	for i := 0; i < len(b); {
		c := b[i]
		if needed == 0 {
			i++
			switch {
			case c <= 0x7F:
				sb.WriteByte(c)
			case c >= 0xC2 && c <= 0xDF:
				needed, cp = 1, rune(c&0x1F)
			case c >= 0xE0 && c <= 0xEF:
				if c == 0xE0 {
					lower = 0xA0
				} else if c == 0xED {
					upper = 0x9F
				}
				needed, cp = 2, rune(c&0x0F)
			case c >= 0xF0 && c <= 0xF4:
				if c == 0xF0 {
					lower = 0x90
				} else if c == 0xF4 {
					upper = 0x8F
				}
				needed, cp = 3, rune(c&0x07)
			default:
				sb.WriteRune(utf8.RuneError)
			}
			continue
		}
		if c < lower || c > upper {
			// The sequence is cut short; this byte starts over.
			cp, needed, seen, lower, upper = 0, 0, 0, 0x80, 0xBF
			sb.WriteRune(utf8.RuneError)
			continue
		}
		i++
		lower, upper = 0x80, 0xBF
		cp = cp<<6 | rune(c&0x3F)
		seen++
		if seen == needed {
			sb.WriteRune(cp)
			cp, needed, seen = 0, 0, 0
		}
	}
	if needed != 0 {
		sb.WriteRune(utf8.RuneError)
	}
	return sb.String()
}
