package xmldom

import (
	"bytes"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Encoding names reported in Document.Encoding.
const (
	EncodingUTF8        = "UTF-8"
	EncodingUTF16LE     = "UTF-16LE"
	EncodingUTF16BE     = "UTF-16BE"
	EncodingISO88591    = "ISO-8859-1"
	EncodingWindows1252 = "Windows-1252"
	EncodingASCII       = "US-ASCII"
)

// maxDeclScan bounds how far the encoding declaration is looked for.
const maxDeclScan = 1024

// decodeInput converts data to UTF-8 without a byte order mark. fallback
// is the encoding to use when the document declares none (e.g. from a
// Content-Type charset); "" means UTF-8.
func decodeInput(data []byte, fallback string) (src []byte, enc string, err error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return data[3:], EncodingUTF8, nil
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE, 0, 0}), bytes.HasPrefix(data, []byte{0, 0, 0xFE, 0xFF}),
		bytes.HasPrefix(data, []byte{0, 0, 0, '<'}), bytes.HasPrefix(data, []byte{'<', 0, 0, 0}):
		return nil, "", &EncodingError{Encoding: "UTF-32", Unsupported: true, Message: "UTF-32 is not supported"}
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return decodeUTF16(data[2:], false, 2)
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return decodeUTF16(data[2:], true, 2)
	case bytes.HasPrefix(data, []byte{'<', 0, '?', 0}):
		return decodeUTF16(data, false, 0)
	case bytes.HasPrefix(data, []byte{0, '<', 0, '?'}):
		return decodeUTF16(data, true, 0)
	case bytes.HasPrefix(data, []byte{0x4C, 0x6F, 0xA7, 0x94}):
		return nil, "", &EncodingError{Encoding: "EBCDIC", Unsupported: true, Message: "EBCDIC is not supported"}
	}

	label := declaredEncoding(data)
	if label == "" {
		label = fallback
	}
	switch canonicalEncoding(label) {
	case "", EncodingUTF8, EncodingUTF16LE:
		// UTF-16 declared on ASCII-compatible bytes: the text was
		// re-encoded without updating the declaration; read it as UTF-8.
		return data, EncodingUTF8, nil
	case EncodingISO88591:
		return decodeSingleByte(data, nil), EncodingISO88591, nil
	case EncodingWindows1252:
		return decodeSingleByte(data, &windows1252), EncodingWindows1252, nil
	case EncodingASCII:
		for i, c := range data {
			if c >= 0x80 {
				return nil, "", &EncodingError{Encoding: EncodingASCII, Offset: i, Message: "a byte above 0x7F"}
			}
		}
		return data, EncodingASCII, nil
	}
	return nil, "", &EncodingError{Encoding: label, Unsupported: true, Message: "encoding " + label + " is not supported"}
}

// canonicalEncoding maps an encoding label to a supported encoding name,
// or "?" when it is not supported.
func canonicalEncoding(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "":
		return ""
	case "utf-8", "utf8", "unicode-1-1-utf-8":
		return EncodingUTF8
	case "utf-16", "utf-16le", "utf-16be", "ucs-2", "unicode":
		return EncodingUTF16LE
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "latin-1", "l1", "cp819", "ibm819", "iso-ir-100":
		return EncodingISO88591
	case "windows-1252", "cp1252", "x-cp1252":
		return EncodingWindows1252
	case "us-ascii", "ascii", "iso646-us", "ansi_x3.4-1968":
		return EncodingASCII
	}
	return "?"
}

// declaredEncoding returns the encoding pseudo-attribute of an XML
// declaration at the start of ASCII-compatible data, or "".
func declaredEncoding(data []byte) string {
	if len(data) < 6 || !bytes.HasPrefix(data, []byte("<?xml")) || !isSpace(data[5]) {
		return ""
	}
	decl := data[5:min(len(data), maxDeclScan)]
	if end := bytes.Index(decl, []byte("?>")); end >= 0 {
		decl = decl[:end]
	} else {
		return ""
	}
	i := bytes.Index(decl, []byte("encoding"))
	if i < 0 {
		return ""
	}
	rest := bytes.TrimLeft(decl[i+len("encoding"):], " \t\r\n")
	if len(rest) == 0 || rest[0] != '=' {
		return ""
	}
	rest = bytes.TrimLeft(rest[1:], " \t\r\n")
	if len(rest) == 0 || (rest[0] != '"' && rest[0] != '\'') {
		return ""
	}
	end := bytes.IndexByte(rest[1:], rest[0])
	if end < 0 {
		return ""
	}
	return string(rest[1 : 1+end])
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func decodeUTF16(data []byte, bigEndian bool, bom int) ([]byte, string, error) {
	enc := EncodingUTF16LE
	if bigEndian {
		enc = EncodingUTF16BE
	}
	if len(data)%2 != 0 {
		return nil, "", &EncodingError{Encoding: enc, Offset: bom + len(data) - 1, Message: "an odd number of bytes"}
	}
	out := make([]byte, 0, len(data)/2+len(data)/8)
	for i := 0; i < len(data); i += 2 {
		u := rune(data[i]) | rune(data[i+1])<<8
		if bigEndian {
			u = rune(data[i])<<8 | rune(data[i+1])
		}
		switch {
		case utf16.IsSurrogate(u) && u < 0xDC00 && i+3 < len(data):
			u2 := rune(data[i+2]) | rune(data[i+3])<<8
			if bigEndian {
				u2 = rune(data[i+2])<<8 | rune(data[i+3])
			}
			r := utf16.DecodeRune(u, u2)
			if r == utf8.RuneError {
				return nil, "", &EncodingError{Encoding: enc, Offset: bom + i, Message: "an unpaired surrogate"}
			}
			out = utf8.AppendRune(out, r)
			i += 2
		case utf16.IsSurrogate(u):
			return nil, "", &EncodingError{Encoding: enc, Offset: bom + i, Message: "an unpaired surrogate"}
		default:
			out = utf8.AppendRune(out, u)
		}
	}
	return out, enc, nil
}

// decodeSingleByte converts ISO-8859-1 (table nil) or Windows-1252 text to
// UTF-8. ASCII-only input is returned as is.
func decodeSingleByte(data []byte, table *[32]rune) []byte {
	ascii := true
	for _, c := range data {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return data
	}
	out := make([]byte, 0, len(data)+len(data)/4)
	for _, c := range data {
		switch {
		case c < 0x80:
			out = append(out, c)
		case table != nil && c < 0xA0:
			out = utf8.AppendRune(out, table[c-0x80])
		default:
			out = utf8.AppendRune(out, rune(c))
		}
	}
	return out
}

// windows1252 maps bytes 0x80-0x9F; undefined bytes map to the C1 control
// of the same value, as browsers do.
var windows1252 = [32]rune{
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
}
