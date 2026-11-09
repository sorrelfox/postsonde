package snippet

// curlwin.go: the curl command for the Windows shells (GEN-02):
// cmd.exe and PowerShell, the same options as Curl with each
// shell's quoting. docs/snippets.md#curl-for-windows.

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CurlCmd returns r as a curl command for the Windows command prompt
// (cmd.exe), typed or pasted at the prompt (not in a batch file, where %
// must be doubled). The options are Curl's; lines are continued with ^.
// It fails with CodeNulByte for a NUL byte and CodeUnsupportedChar for a
// carriage return, another control character or text that is not UTF-8,
// which cmd.exe cannot pass.
func CurlCmd(r Request) (string, error) {
	return curlCommand(r, curlShell{command: "curl", quote: quoteCmd, cont: " ^\n  "})
}

// CurlPowerShell returns r as a curl.exe command for PowerShell 7.3 or
// later ("curl" is Invoke-WebRequest in Windows PowerShell). The options
// are Curl's; lines are continued with a backtick. Windows PowerShell 5.1
// passes an argument holding a double quote, or an empty one, to a
// native program wrongly (docs/snippets.md). It fails with CodeNulByte
// for a NUL byte and CodeUnsupportedChar for text that is not UTF-8.
func CurlPowerShell(r Request) (string, error) {
	return curlCommand(r, curlShell{command: "curl.exe", quote: quotePowerShell, cont: " `\n  "})
}

// quoteCmd returns s as one argument of a cmd.exe command line that
// curl.exe reads back as s. Two parsers read it: cmd.exe, which only
// knows "..." and ^, and then the C runtime, which splits the arguments.
//
//   - s itself when it only has letters, digits and - _ . / \ : = @ + ,
//     (and does not start with -);
//   - "..." otherwise: inside it cmd.exe leaves & | < > ^ ( ) ! alone. A
//     quote is "" (the C runtime reads "" inside quotes as one quote, and
//     cmd.exe stays inside its quotes), with the backslashes before it
//     doubled; backslashes before the closing quote are doubled too.
//   - % leaves the quotes, "%", so cmd.exe never sees a %NAME% (the name
//     would start with a quote);
//   - a line feed leaves the quotes too: ^ and two line breaks, cmd.exe's
//     way to escape a line break (as Chrome writes it).
//
// A carriage return or another control character (tab aside) cannot be
// typed at the prompt, and cmd.exe has no escape for it: it fails with
// CodeUnsupportedChar, as does text that is not UTF-8, and a NUL byte with
// CodeNulByte.
func quoteCmd(s, field string) (string, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return "", nulError(field)
	}
	if !utf8.ValidString(s) {
		return "", unsupportedChar(field, "cmd")
	}
	plain := s != "" && s[0] != '-'
	for _, c := range s {
		switch {
		case c == '\n' || c == '\t':
			plain = false
		case c < ' ' || c == 0x7f:
			return "", unsupportedChar(field, "cmd")
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.ContainsRune(`-_./\:=@+,`, c):
		default:
			plain = false
		}
	}
	if plain {
		return s, nil
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, c := range s {
		switch c {
		case '\\':
			slashes++
			b.WriteByte('\\')
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes) + `""`)
		case '%':
			b.WriteString(strings.Repeat(`\`, slashes) + `"%"`)
		case '\n':
			b.WriteString(strings.Repeat(`\`, slashes) + "\"^\n\n\"")
		default:
			b.WriteRune(c)
		}
		slashes = 0
	}
	b.WriteString(strings.Repeat(`\`, slashes) + `"`)
	return b.String(), nil
}

// quotePowerShell returns s as one PowerShell argument:
//
//   - s itself when it only has letters, digits and - _ . / : = +;
//   - '...' otherwise, literal, with each ' doubled (line breaks and tabs
//     kept as they are);
//   - "..." with backtick escapes when s has another control character, a
//     character that is not printable, or a typographic single quote
//     (‘ ’ ‚ ‛, which PowerShell takes for '): ` $ " “ ” „ are escaped with
//     a backtick, `r `n `t and `u{…} (PowerShell 6 and later) written for
//     control characters.
//
// It fails with CodeNulByte for a NUL byte (no argument carries one) and
// CodeUnsupportedChar for text that is not UTF-8.
func quotePowerShell(s, field string) (string, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return "", nulError(field)
	}
	if !utf8.ValidString(s) {
		return "", unsupportedChar(field, "PowerShell")
	}
	plain, single := s != "", true
	for _, c := range s {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.ContainsRune("-_./:=+", c):
		case c == '\n' || c == '\t':
			plain = false
		case c < ' ' || c == 0x7f || c >= 0x2018 && c <= 0x201b || c > 0x7f && !unicode.IsPrint(c):
			plain, single = false, false
		default:
			plain = false
		}
	}
	switch {
	case plain:
		return s, nil
	case single:
		return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '`', '$', '"', '“', '”', '„':
			b.WriteRune('`')
			b.WriteRune(c)
		case '\r':
			b.WriteString("`r")
		case '\n':
			b.WriteString("`n")
		case '\t':
			b.WriteString("`t")
		default:
			if c < ' ' || c == 0x7f || c > 0x7f && !unicode.IsPrint(c) {
				fmt.Fprintf(&b, "`u{%X}", c)
				continue
			}
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}
