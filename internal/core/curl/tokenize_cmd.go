package curl

import (
	"strings"
	"unicode/utf8"
)

// unit is one character after cmd.exe's pass, with the source bytes it
// came from.
type unit struct {
	c          byte
	start, end int
}

// cmd scans a cmd.exe command in two passes, like Windows does. First
// cmd.exe: outside its own "..." state, ^ escapes the next character, ^ at
// the end of a line continues the command (and escapes the first character
// of the next line, which is how Chrome writes a newline inside a value:
// "^" + two line breaks), and & | < > end the command. Then the Microsoft C
// runtime splits the result into arguments: blanks separate them outside
// quotes, 2n backslashes before a quote are n backslashes and the quote
// toggles quoting, 2n+1 are n backslashes and a literal quote, and "" inside
// quotes is a literal quote.
//
// Chrome's "Copy as cURL (cmd)" doubles every backslash before quoting, so
// a run of 2n backslashes that is not followed by a quote would reach
// curl.exe as 2n backslashes. When the text contains Chrome's ^" quoting,
// such runs are halved instead, which recovers the request DevTools copied.
func (s *scanner) cmd() {
	src := s.src
	var units []unit
	inQuote, stopped := false, false
	i := s.pos
scan:
	for i < len(src) {
		c := src[i]
		if n := newlineLen(src, i); n > 0 {
			if !s.lineBreak(i, n) {
				stopped = true
				break scan
			}
			units = append(units, unit{' ', i, i + n})
			i = s.pos
			inQuote = false
			continue
		}
		switch {
		case c == '^' && !inQuote:
			if i+1 == len(src) {
				i++
				continue
			}
			if n := newlineLen(src, i+1); n > 0 {
				j := i + 1 + n
				if m := newlineLen(src, j); m > 0 {
					units = append(units, unit{'\n', i, j + m})
					i = j + m
				} else if j < len(src) {
					units = append(units, unit{src[j], i, j + 1})
					i = j + 1
				} else {
					i = j
				}
				continue
			}
			units = append(units, unit{src[i+1], i, i + 2})
			i += 2
		case c == '"':
			inQuote = !inQuote
			units = append(units, unit{c, i, i + 1})
			i++
		case !inQuote && strings.IndexByte("&|<>", c) >= 0:
			s.cmdArgs(units)
			s.stop(i)
			return
		default:
			if c == '%' {
				s.percentVar(i)
			}
			units = append(units, unit{c, i, i + 1})
			i++
		}
	}
	s.cmdArgs(units)
	if !stopped {
		s.end = len(src)
	}
	s.pos = len(src)
}

// percentVar reports a cmd.exe %NAME% expansion at i.
func (s *scanner) percentVar(i int) {
	j := i + 1
	for j < len(s.src) && (isNameChar(s.src[j]) || s.src[j] == '-') {
		j++
	}
	if j > i+1 && j < len(s.src) && s.src[j] == '%' {
		s.diag(CodeShellExpansion, SeverityWarning, i, j+1,
			"cmd.exe would expand "+quoteText(s.src[i:j+1])+"; it is kept as written.")
	}
}

// cmdArgs splits units into words with the C runtime's rules.
func (s *scanner) cmdArgs(units []unit) {
	inQuote, quoteAt := false, 0
	chrome := strings.Contains(s.src, `^"`)
	for k := 0; k < len(units); {
		u := units[k]
		switch {
		case !inQuote && (u.c == ' ' || u.c == '\t'):
			s.finish()
			k++
		case u.c == '\\':
			n := 0
			for k+n < len(units) && units[k+n].c == '\\' {
				n++
			}
			s.begin(u.start)
			if k+n < len(units) && units[k+n].c == '"' {
				s.add(units[k+n-1].end, []byte(strings.Repeat(`\`, n/2))...)
				if n%2 == 1 {
					s.add(units[k+n].end, '"')
					k += n + 1
				} else {
					k += n
				}
				continue
			}
			keep := n
			if chrome {
				keep = (n + 1) / 2 // undo Chrome's doubling of every backslash
			}
			s.add(units[k+n-1].end, []byte(strings.Repeat(`\`, keep))...)
			k += n
		case u.c == '"':
			s.begin(u.start)
			if inQuote && k+1 < len(units) && units[k+1].c == '"' {
				s.add(units[k+1].end, '"')
				k += 2
				continue
			}
			inQuote = !inQuote
			quoteAt = u.start
			s.last = u.end
			k++
		default:
			s.begin(u.start)
			s.add(u.end, u.c)
			k++
		}
	}
	if inQuote {
		s.unterminated(quoteAt)
	}
	s.finish()
}

// powershell scans a PowerShell command: ` escapes and continues lines,
// '...' is literal with two single quotes for one, "..." has ` escapes and ""
// for a quote, and $ expansions are reported and kept.
func (s *scanner) powershell() {
	src := s.src
	for s.pos < len(src) {
		i := s.pos
		c := src[i]
		if n := newlineLen(src, i); n > 0 {
			if !s.lineBreak(i, n) {
				return
			}
			continue
		}
		switch c {
		case ' ', '\t', '\r':
			s.finish()
			s.pos++
		case '`':
			if n := newlineLen(src, i+1); n > 0 {
				s.pos = i + 1 + n
				continue
			}
			s.begin(i)
			s.psEscape(i)
		case '\'':
			s.psSingle()
		case '"':
			s.psDouble()
		case '$':
			s.dollar(i)
			s.begin(i)
			s.add(i+1, c)
			s.pos++
		case '#':
			if s.start >= 0 {
				s.add(i+1, c)
				s.pos++
				continue
			}
			for s.pos < len(src) && newlineLen(src, s.pos) == 0 {
				s.pos++
			}
		case '|', ';', '>', '<':
			s.stop(i)
			return
		case '&':
			if i+1 < len(src) && src[i+1] == '&' {
				s.stop(i)
				return
			}
			s.begin(i)
			s.add(i+1, c)
			s.pos++
		default:
			s.begin(i)
			s.add(i+1, c)
			s.pos++
		}
	}
	s.done()
}

// psEscape adds the character escaped by the backtick at i and advances.
func (s *scanner) psEscape(i int) {
	src := s.src
	if i+1 == len(src) {
		s.add(i+1, '`')
		s.pos = i + 1
		return
	}
	e := src[i+1]
	s.pos = i + 2
	switch e {
	case '0':
		s.add(s.pos, 0)
	case 'a':
		s.add(s.pos, '\a')
	case 'b':
		s.add(s.pos, '\b')
	case 'e':
		s.add(s.pos, 0x1b)
	case 'f':
		s.add(s.pos, '\f')
	case 'n':
		s.add(s.pos, '\n')
	case 'r':
		s.add(s.pos, '\r')
	case 't':
		s.add(s.pos, '\t')
	case 'v':
		s.add(s.pos, '\v')
	case 'u':
		rest := src[s.pos:]
		if k := strings.IndexByte(rest[:min(len(rest), 8)], '}'); strings.HasPrefix(rest, "{") && k >= 2 && k <= 7 {
			if v, n := parseHex(rest[1:k], 6); n == k-1 {
				r := clampRune(v)
				if !utf8.ValidRune(r) {
					r = utf8.RuneError
				}
				s.buf = utf8.AppendRune(s.buf, r)
				s.pos += k + 1
				s.last = s.pos
				return
			}
		}
		s.add(s.pos, 'u')
	default:
		s.add(s.pos, e)
	}
}

func (s *scanner) psSingle() {
	src := s.src
	open := s.pos
	s.begin(open)
	s.pos++
	for s.pos < len(src) {
		i := s.pos
		if src[i] == '\'' {
			if i+1 < len(src) && src[i+1] == '\'' {
				s.add(i+2, '\'')
				s.pos += 2
				continue
			}
			s.last = i + 1
			s.pos++
			return
		}
		if newlineLen(src, i) == 2 {
			s.add(i+2, '\n')
			s.pos += 2
			continue
		}
		s.add(i+1, src[i])
		s.pos++
	}
	s.last = len(src)
	s.unterminated(open)
}

func (s *scanner) psDouble() {
	src := s.src
	open := s.pos
	s.begin(open)
	s.pos++
	for s.pos < len(src) {
		i := s.pos
		switch c := src[i]; {
		case c == '"':
			if i+1 < len(src) && src[i+1] == '"' {
				s.add(i+2, '"')
				s.pos += 2
				continue
			}
			s.last = i + 1
			s.pos++
			return
		case c == '`':
			if n := newlineLen(src, i+1); n > 0 {
				s.pos = i + 1 + n
				s.last = s.pos
				continue
			}
			s.psEscape(i)
		case c == '$':
			s.dollar(i)
			s.add(i+1, c)
			s.pos++
		case newlineLen(src, i) == 2:
			s.add(i+2, '\n')
			s.pos += 2
		default:
			s.add(i+1, c)
			s.pos++
		}
	}
	s.last = len(src)
	s.unterminated(open)
}
