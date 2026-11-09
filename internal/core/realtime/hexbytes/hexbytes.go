// Package hexbytes parses bytes typed as hex, the way people write them:
// "DE AD be ef", "0xDE,0xAD", "deadbeef", "\xde\xad". It is shared by the
// clients that send binary data typed in a composer (raw TCP, later
// WebSocket binary frames and UDP). frontend/src/lib/hexBytes.ts follows
// the same rules for validation while typing. See docs/tcp.md.
package hexbytes

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes. Every error is an *errs.Error of kind invalid_argument with
// the detail "offset": the position of the offending character, counted in
// characters (runes) from 0.
const (
	// CodeInvalidChar: a character that is neither a hex digit, a
	// separator nor a 0x or \x prefix.
	CodeInvalidChar = "hexbytes.invalid_char"
	// CodeOddDigits: a group ends after an odd number of digits, so its
	// last digit is half a byte. The offset is that digit's.
	CodeOddDigits = "hexbytes.odd_digits"
	// CodeMissingDigits: a 0x or \x prefix without digits after it. The
	// offset is the prefix's.
	CodeMissingDigits = "hexbytes.missing_digits"
)

// hint is the hint of every error.
const hint = `Write bytes as pairs of hex digits, e.g. "DE AD BE EF", "0xDE, 0xAD" or "deadbeef".`

// Parse returns the bytes written in s.
//
//   - Digits come in pairs, upper or lower case; each pair is one byte.
//   - Groups of pairs are separated by whitespace, commas, colons or
//     semicolons, any number of them: "de ad", "de,ad", "de:ad", "de, ad".
//   - A group may start with 0x or 0X ("0xDEAD" is two bytes), and \x may
//     come before any pair ("\xde\xad").
//   - A group with an odd number of digits is an error: "abc" does not
//     guess whether "a" is 0x0a or the start of another byte.
//
// An empty or all-separator s is no bytes and no error.
func Parse(s string) ([]byte, error) {
	out := make([]byte, 0, len(s)/2)
	var (
		pos      = 0 // rune offset of s[i]
		inGroup  = false
		half     = false // one digit of a pair read
		hi       byte
		halfAt   int // offset of the pending digit
		prefixAt = -1
	)
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case isSeparator(c):
			if half {
				return nil, oddDigits(halfAt)
			}
			if prefixAt >= 0 {
				return nil, missingDigits(prefixAt)
			}
			inGroup = false
			i++
			pos++
		case !half && !inGroup && c == '0' && i+1 < len(s) && (s[i+1] == 'x' || s[i+1] == 'X'):
			inGroup, prefixAt = true, pos
			i += 2
			pos += 2
		case !half && c == '\\' && i+1 < len(s) && s[i+1] == 'x':
			if prefixAt >= 0 {
				return nil, missingDigits(prefixAt)
			}
			inGroup, prefixAt = true, pos
			i += 2
			pos += 2
		case isHex(c):
			if half {
				out = append(out, hi<<4|unhex(c))
				half = false
			} else {
				hi, half, halfAt = unhex(c), true, pos
			}
			inGroup, prefixAt = true, -1
			i++
			pos++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if size == 1 && r == utf8.RuneError {
				r = rune(c)
			}
			return nil, errs.New(errs.InvalidArgument,
				fmt.Sprintf("%s at position %d is not a hex digit.", quoteRune(r), pos+1)).
				WithCode(CodeInvalidChar).
				WithHint(hint).
				WithDetail("offset", strconv.Itoa(pos))
		}
	}
	if half {
		return nil, oddDigits(halfAt)
	}
	if prefixAt >= 0 {
		return nil, missingDigits(prefixAt)
	}
	return out, nil
}

func isSeparator(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f', '\v', ',', ':', ';':
		return true
	}
	return false
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c >= 'a':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func oddDigits(at int) *errs.Error {
	return errs.New(errs.InvalidArgument,
		fmt.Sprintf("The hex digit at position %d has no partner: bytes are pairs of digits.", at+1)).
		WithCode(CodeOddDigits).
		WithHint(hint).
		WithDetail("offset", strconv.Itoa(at))
}

func missingDigits(at int) *errs.Error {
	return errs.New(errs.InvalidArgument,
		fmt.Sprintf("The prefix at position %d has no hex digits after it.", at+1)).
		WithCode(CodeMissingDigits).
		WithHint(hint).
		WithDetail("offset", strconv.Itoa(at))
}

// quoteRune names a character for a message: printable ones quoted, others
// as U+XXXX.
func quoteRune(r rune) string {
	if strconv.IsPrint(r) {
		return "“" + string(r) + "”"
	}
	return fmt.Sprintf("U+%04X", r)
}
