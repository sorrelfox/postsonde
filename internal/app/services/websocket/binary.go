package websocket

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/realtime/hexbytes"
)

// Message formats of SendInputDTO.Format (WS-03, WS-04). Text and JSON are
// sent as text messages with their {{variables}} rendered; hex and Base64
// spell the bytes of a binary message and are sent as written.
const (
	FormatText   = "text"
	FormatJSON   = "json"
	FormatHex    = "hex"
	FormatBase64 = "base64"
)

// Error codes of binary messages. Bad hex keeps its hexbytes.* codes.
const (
	// CodeInvalidFormat: a message format the service does not know
	// (invalid_argument, details.format).
	CodeInvalidFormat = "ws.invalid_format"
	// CodeInvalidBase64: Base64 text that does not spell bytes
	// (invalid_argument, details.offset: the position of the offending
	// character, counted in characters from 0).
	CodeInvalidBase64 = "ws.invalid_base64"
)

const base64Hint = "Write bytes as Base64 (A–Z, a–z, 0–9, + and /, or - and _), e.g. \"3q2+7w==\"."

// isBinaryFormat reports whether format spells the bytes of a binary
// message.
func isBinaryFormat(format string) bool {
	return format == FormatHex || format == FormatBase64
}

// binaryPayload returns the bytes text spells in format "hex" or "base64".
func binaryPayload(format, text string) ([]byte, error) {
	if format == FormatHex {
		return hexbytes.Parse(text)
	}
	return parseBase64(text)
}

// checkFormat refuses a format the service does not know; "" is text.
func checkFormat(format string) error {
	switch format {
	case "", FormatText, FormatJSON, FormatHex, FormatBase64:
		return nil
	}
	return errs.New(errs.InvalidArgument, "The message format is not one PostSonde knows.").
		WithCode(CodeInvalidFormat).
		WithHint(`Use "text", "json", "hex" or "base64".`).
		WithDetail("format", format)
}

// ParseBase64 is parseBase64, for the other composers that take Base64
// (the UDP request's, UDP-01), so every composer reads Base64 the same way
// and the frontend's parseBase64 validates them all.
func ParseBase64(s string) ([]byte, error) { return parseBase64(s) }

// parseBase64 decodes Base64 as people paste it: the standard or the URL
// alphabet (even mixed), whitespace anywhere, padding optional but only at
// the end. frontend/src/lib/base64Bytes.ts follows the same rules while
// typing; keep the two in step. Errors are ws.invalid_base64 with the
// offset of the offending character.
func parseBase64(s string) ([]byte, error) {
	clean := make([]byte, 0, len(s))
	pad, padAt, pos, last := 0, -1, 0, -1
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
		case c == '=':
			if pad == 0 {
				padAt = pos
			}
			pad++
			if pad > 2 {
				return nil, base64Error(pos, fmt.Sprintf("Too much padding at position %d: Base64 ends with at most two “=”.", pos+1))
			}
			last = pos
		case pad > 0:
			return nil, base64Error(padAt, fmt.Sprintf("Padding (“=”) at position %d is followed by more text: it may only end the Base64.", padAt+1))
		case 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '+' || c == '/':
			clean = append(clean, c)
			last = pos
		case c == '-':
			clean = append(clean, '+')
			last = pos
		case c == '_':
			clean = append(clean, '/')
			last = pos
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if size == 1 && r == utf8.RuneError {
				r = rune(c)
			}
			return nil, base64Error(pos, fmt.Sprintf("%s at position %d is not a Base64 character.", quoteRune(r), pos+1))
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		pos++
	}
	if len(clean)%4 == 1 {
		return nil, base64Error(last, fmt.Sprintf("The Base64 ends in the middle of a byte at position %d: a character is missing or extra.", last+1))
	}
	if pad > 0 && (len(clean)+pad)%4 != 0 {
		return nil, base64Error(padAt, fmt.Sprintf("The padding at position %d does not fit the length of the Base64.", padAt+1))
	}
	out, err := base64.RawStdEncoding.DecodeString(string(clean))
	if err != nil {
		// Cannot happen after the checks above; report it at the end.
		return nil, base64Error(last, "The Base64 does not spell bytes.")
	}
	return out, nil
}

func base64Error(at int, message string) *errs.Error {
	return errs.New(errs.InvalidArgument, message).
		WithCode(CodeInvalidBase64).
		WithHint(base64Hint).
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
