// Package charset decodes text in the few character encodings PostSonde
// supports without tables from outside the standard library: UTF-8,
// UTF-16 (LE and BE), ISO-8859-1, US-ASCII and Windows-1252. It also finds
// the encoding a body declares (a byte order mark, an XML declaration, an
// HTML <meta>) and dumps bytes that cannot be decoded as hex. The response
// viewer uses it (HTTP-31); see docs/http-client.md.
package charset

import (
	"bytes"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Encoding names, as Canonical returns them.
const (
	UTF8        = "UTF-8"
	UTF16LE     = "UTF-16LE"
	UTF16BE     = "UTF-16BE"
	ISO88591    = "ISO-8859-1"
	ASCII       = "US-ASCII"
	Windows1252 = "windows-1252"
)

// Canonical maps an encoding label (a Content-Type charset, an XML
// encoding declaration) to one of the supported encodings, or "" when it
// is not supported. Labels are matched case-insensitively. "UTF-16"
// without a byte order mark is read as little-endian, as browsers do.
func Canonical(label string) string {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(label), `"'`)) {
	case "utf-8", "utf8", "unicode-1-1-utf-8", "unicode11utf8", "unicode20utf8", "x-unicode20utf8":
		return UTF8
	case "utf-16", "utf-16le", "unicode", "ucs-2", "csunicode", "iso-10646-ucs-2", "unicodefeff":
		return UTF16LE
	case "utf-16be", "unicodefffe":
		return UTF16BE
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "iso_8859-1:1987", "iso88591", "latin1", "latin-1", "l1", "cp819", "ibm819", "iso-ir-100", "csisolatin1":
		return ISO88591
	case "us-ascii", "ascii", "iso646-us", "ansi_x3.4-1968", "us", "csascii":
		return ASCII
	case "windows-1252", "cp1252", "x-cp1252":
		return Windows1252
	}
	return ""
}

// BOM returns the encoding of a byte order mark at the start of data and
// its length, or "" and 0 when there is none.
func BOM(data []byte) (string, int) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return UTF8, 3
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return UTF16LE, 2
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return UTF16BE, 2
	}
	return "", 0
}

// Decode converts data in encoding enc (a Canonical name) to UTF-8 text.
// Bytes that are not valid in enc become U+FFFD, and invalid reports
// whether there were any: malformed UTF-8 or UTF-16, bytes above 0x7F in
// US-ASCII. ISO-8859-1 and Windows-1252 decode every byte. An unsupported
// enc is decoded as UTF-8.
func Decode(data []byte, enc string) (text string, invalid bool) {
	switch enc {
	case UTF16LE, UTF16BE:
		return decodeUTF16(data, enc == UTF16BE)
	case ISO88591:
		return decodeSingleByte(data, nil), false
	case Windows1252:
		return decodeSingleByte(data, &windows1252), false
	case ASCII:
		var b strings.Builder
		b.Grow(len(data))
		for _, c := range data {
			if c >= 0x80 {
				b.WriteRune(utf8.RuneError)
				invalid = true
				continue
			}
			b.WriteByte(c)
		}
		return b.String(), invalid
	}
	if utf8.Valid(data) {
		return string(data), false
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), true
}

func decodeUTF16(data []byte, bigEndian bool) (string, bool) {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if bigEndian {
			units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
		} else {
			units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
		}
	}
	invalid := len(data)%2 != 0
	for i := 0; i < len(units); i++ {
		u := rune(units[i])
		switch {
		case u >= 0xD800 && u < 0xDC00 && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] < 0xE000:
			i++
		case utf16.IsSurrogate(u):
			invalid = true
		}
	}
	text := string(utf16.Decode(units))
	if len(data)%2 != 0 {
		text += "\uFFFD"
	}
	return text, invalid
}

// decodeSingleByte converts ISO-8859-1 (table nil) or Windows-1252 text
// to UTF-8.
func decodeSingleByte(data []byte, table *[32]rune) string {
	var b strings.Builder
	b.Grow(len(data) + len(data)/8)
	for _, c := range data {
		switch {
		case c < 0x80:
			b.WriteByte(c)
		case table != nil && c < 0xA0:
			b.WriteRune(table[c-0x80])
		default:
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

// windows1252 maps bytes 0x80-0x9F; undefined bytes map to the C1 control
// of the same value, as browsers do.
var windows1252 = [32]rune{
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
}

// scanLimit bounds how far XMLDeclared and HTMLMeta look.
const scanLimit = 1024

// XMLDeclared returns the encoding pseudo-attribute of an XML declaration
// at the start of data (after an optional UTF-8 byte order mark), or "".
func XMLDeclared(data []byte) string {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if len(data) < 6 || !bytes.HasPrefix(data, []byte("<?xml")) || !isSpace(data[5]) {
		return ""
	}
	decl := data[5:min(len(data), scanLimit)]
	end := bytes.Index(decl, []byte("?>"))
	if end < 0 {
		return ""
	}
	return attrValue(decl[:end], "encoding")
}

// HTMLMeta returns the charset an HTML document declares in its first
// 1024 bytes, in <meta charset="…"> or <meta http-equiv="Content-Type"
// content="text/html; charset=…">, or "". A light scan, not the full
// HTML prescan algorithm: it ignores comments and scripts.
func HTMLMeta(data []byte) string {
	head := bytes.ToLower(data[:min(len(data), scanLimit)])
	for {
		i := bytes.Index(head, []byte("<meta"))
		if i < 0 {
			return ""
		}
		head = head[i+len("<meta"):]
		end := bytes.IndexByte(head, '>')
		if end < 0 {
			return ""
		}
		tag := head[:end]
		head = head[end:]
		if v := attrValue(tag, "charset"); v != "" {
			return v
		}
		if content := attrValue(tag, "content"); content != "" {
			if j := strings.Index(content, "charset="); j >= 0 {
				v := content[j+len("charset="):]
				if k := strings.IndexAny(v, "; \t"); k >= 0 {
					v = v[:k]
				}
				if v = strings.Trim(v, `"'`); v != "" {
					return v
				}
			}
		}
	}
}

// attrValue returns the value of name="…", name='…' or name=value in s,
// or "".
func attrValue(s []byte, name string) string {
	for from := 0; ; {
		i := bytes.Index(s[from:], []byte(name))
		if i < 0 {
			return ""
		}
		i += from
		from = i + len(name)
		if i > 0 && !isSpace(s[i-1]) && s[i-1] != '"' && s[i-1] != '\'' && s[i-1] != ';' {
			continue
		}
		rest := bytes.TrimLeft(s[i+len(name):], " \t\r\n")
		if len(rest) == 0 || rest[0] != '=' {
			continue
		}
		rest = bytes.TrimLeft(rest[1:], " \t\r\n")
		if len(rest) == 0 {
			return ""
		}
		if q := rest[0]; q == '"' || q == '\'' {
			end := bytes.IndexByte(rest[1:], q)
			if end < 0 {
				return ""
			}
			return string(rest[1 : 1+end])
		}
		end := bytes.IndexAny(rest, " \t\r\n/>;\"'")
		if end < 0 {
			end = len(rest)
		}
		return string(rest[:end])
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' }

// HexDump formats data as lines of 16 bytes: the offset, the bytes in hex
// in two groups of eight, and the printable ASCII characters (others as
// "."), as `hexdump -C` does:
//
//	00000000  48 65 6c 6c 6f 0a                                 |Hello.|
func HexDump(data []byte) string { return HexDumpAt(data, 0) }

// HexDumpAt is HexDump of data found at byte base of a larger body: the
// offsets count from there. They have eight digits, more past 4 GiB.
func HexDumpAt(data []byte, base int64) string {
	const hex = "0123456789abcdef"
	top := 28
	for last := base + int64(len(data)); top < 60 && last>>(top+4) > 0; top += 4 {
	}
	var b strings.Builder
	b.Grow((len(data)/16 + 1) * 78)
	for off := 0; off < len(data); off += 16 {
		line := data[off:min(off+16, len(data))]
		at := base + int64(off)
		for shift := top; shift >= 0; shift -= 4 {
			b.WriteByte(hex[(at>>shift)&0xF])
		}
		b.WriteString("  ")
		for i := range 16 {
			if i == 8 {
				b.WriteByte(' ')
			}
			if i < len(line) {
				b.WriteByte(hex[line[i]>>4])
				b.WriteByte(hex[line[i]&0xF])
				b.WriteByte(' ')
			} else {
				b.WriteString("   ")
			}
		}
		b.WriteString(" |")
		for _, c := range line {
			if c >= 0x20 && c < 0x7F {
				b.WriteByte(c)
			} else {
				b.WriteByte('.')
			}
		}
		b.WriteByte('|')
		if off+16 < len(data) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
