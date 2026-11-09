package jsonschema

import (
	"regexp"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// JSON Schema patterns are ECMA-262 regular expressions; Go has RE2.
// translatePattern rewrites the ECMA-262 syntax RE2 spells differently:
//
//   - \uXXXX and \u{X…} become \x{…};
//   - \cX (a control character) becomes \x{…};
//   - \s and \S match the ECMA-262 white space and line terminators
//     (RE2's are ASCII only); \S inside a class stays ASCII;
//   - the empty class [] (matches nothing) and [^] (matches any
//     character), which RE2 reads as the start of a class holding "]".
//
// Lookahead and lookbehind ((?=, (?!, (?<=, (?<!) and backreferences
// (\1, \k<name>) have no RE2 equivalent: unsupportedPattern names them.
// Everything else is left for regexp.Compile, which rejects what it does
// not know. Other known differences, not translated: "." also matches
// \r, U+2028 and U+2029 in RE2; \d and \w are ASCII in both; case
// folding with (?i) follows Unicode simple folding in RE2.
func translatePattern(p string) string {
	if !strings.Contains(p, `\`) && !strings.Contains(p, "[]") && !strings.Contains(p, "[^]") {
		return p
	}
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '[' && !inClass && strings.HasPrefix(p[i:], "[]"):
			b.WriteString(`[^\x00-\x{10FFFF}]`)
			i++
			continue
		case c == '[' && !inClass && strings.HasPrefix(p[i:], "[^]"):
			b.WriteString(`[\x00-\x{10FFFF}]`)
			i += 2
			continue
		case c == '[' && !inClass:
			inClass = true
			b.WriteByte(c)
			continue
		case c == ']' && inClass:
			inClass = false
			b.WriteByte(c)
			continue
		case c != '\\' || i+1 >= len(p):
			b.WriteByte(c)
			continue
		}
		n := p[i+1]
		switch n {
		case 'u':
			if i+2 < len(p) && p[i+2] == '{' {
				if end := strings.IndexByte(p[i+3:], '}'); end > 0 && isHex(p[i+3:i+3+end]) {
					b.WriteString(`\x{` + p[i+3:i+3+end] + `}`)
					i += 3 + end
					continue
				}
			}
			if i+6 <= len(p) && isHex(p[i+2:i+6]) {
				b.WriteString(`\x{` + p[i+2:i+6] + `}`)
				i += 5
				continue
			}
		case 'c':
			if i+2 < len(p) && isASCIILetter(p[i+2]) {
				b.WriteString(`\x{` + hex2(p[i+2]%32) + `}`)
				i += 2
				continue
			}
		case 's':
			if inClass {
				b.WriteString(ecmaSpace)
			} else {
				b.WriteString("[" + ecmaSpace + "]")
			}
			i++
			continue
		case 'S':
			if !inClass {
				b.WriteString("[^" + ecmaSpace + "]")
				i++
				continue
			}
		}
		b.WriteByte(c)
		b.WriteByte(n)
		i++
	}
	return b.String()
}

// ecmaSpace is the ECMA-262 WhiteSpace and LineTerminator set, as class
// items.
const ecmaSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func hex2(c byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[c>>4], digits[c&15]})
}

// unsupportedPattern names an ECMA-262 feature of p that RE2 lacks, or "".
func unsupportedPattern(p string) string {
	for i := 0; i < len(p); i++ {
		switch {
		case p[i] == '\\' && i+1 < len(p):
			n := p[i+1]
			if n >= '1' && n <= '9' || n == 'k' && i+2 < len(p) && p[i+2] == '<' {
				return "backreferences"
			}
			i++
		case strings.HasPrefix(p[i:], "(?="), strings.HasPrefix(p[i:], "(?!"):
			return "lookahead"
		case strings.HasPrefix(p[i:], "(?<="), strings.HasPrefix(p[i:], "(?<!"):
			return "lookbehind"
		}
	}
	return ""
}

// isECMARegex reports whether p is an ECMA-262 regular expression, for
// the regex format. It is checked by RE2 after translatePattern, with
// these differences to the pattern keyword: lookaround and
// backreferences are valid (they are ECMA-262, though patterns cannot
// use them), and RE2 syntax that ECMA-262 lacks is not: Python named
// groups (?P<n>…) and (?P=n), comments (?#…), flag groups other than
// the ES2025 modifiers (?ims-ims:…), and the escapes \a \A \z \Z \Q \E
// \C, \p and \P without braces.
func isECMARegex(p string) bool {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			n := p[i+1]
			switch {
			case strings.IndexByte("aAzZQEC", n) >= 0:
				return false
			case (n == 'p' || n == 'P') && (i+2 >= len(p) || p[i+2] != '{'):
				return false
			case !inClass && n >= '1' && n <= '9':
				for i+2 < len(p) && p[i+2] >= '0' && p[i+2] <= '9' {
					i++
				}
				b.WriteString("(?:)")
				i++
				continue
			case !inClass && n == 'k' && i+2 < len(p) && p[i+2] == '<':
				end := strings.IndexByte(p[i:], '>')
				if end < 0 {
					return false
				}
				b.WriteString("(?:)")
				i += end
				continue
			}
			b.WriteByte(c)
			b.WriteByte(n)
			i++
			continue
		case inClass:
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
			if strings.HasPrefix(p[i:], "[]") || strings.HasPrefix(p[i:], "[^]") {
				inClass = false
			} else if strings.HasPrefix(p[i:], "[^") {
				b.WriteString("[^")
				i++
				continue
			}
		case c == '(' && strings.HasPrefix(p[i:], "(?"):
			rest := p[i+2:]
			switch {
			case strings.HasPrefix(rest, "="), strings.HasPrefix(rest, "!"):
				b.WriteString("(?:")
				i += 2
				continue
			case strings.HasPrefix(rest, "<="), strings.HasPrefix(rest, "<!"):
				b.WriteString("(?:")
				i += 3
				continue
			case strings.HasPrefix(rest, ":"), strings.HasPrefix(rest, "<"):
				// A non-capturing or named group.
			default:
				if !ecmaModifiers.MatchString(rest) {
					return false
				}
			}
		}
		b.WriteByte(c)
	}
	_, err := regexp.Compile(translatePattern(b.String()))
	return err == nil
}

// ecmaModifiers is the rest of an ES2025 modifiers group after "(?".
var ecmaModifiers = regexp.MustCompile(`^(?:[ims]+(?:-[ims]*)?|-[ims]+):`)

// compilePattern compiles an ECMA-262 pattern with RE2.
func compilePattern(p, loc string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(translatePattern(p))
	if err == nil {
		return re, nil
	}
	if feat := unsupportedPattern(p); feat != "" {
		return nil, errs.New(errs.Unsupported, "The pattern "+quote(p)+" at "+fragment(loc)+" uses "+feat+", which Go regular expressions (RE2) do not support.").
			WithCode(CodeInvalidPattern).
			WithHint("Rewrite the pattern without "+feat+".").
			WithDetail("location", fragment(loc)).
			WithDetail("pattern", p)
	}
	msg := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
	return nil, errs.New(errs.InvalidArgument, "The pattern "+quote(p)+" at "+fragment(loc)+" is not a valid regular expression: "+msg+".").
		WithCode(CodeInvalidPattern).
		WithDetail("location", fragment(loc)).
		WithDetail("pattern", p)
}
