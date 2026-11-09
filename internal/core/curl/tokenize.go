package curl

import (
	"strings"
	"unicode/utf8"
)

// Dialect is the shell syntax a command is written in.
type Dialect string

// Dialects.
const (
	// DialectAuto picks a dialect with DetectDialect.
	DialectAuto Dialect = ""
	// DialectPOSIX is POSIX sh with Bash's $'...' quoting: what curl
	// documentation, Postman, Insomnia and the "Copy as cURL (bash)" of
	// browser DevTools produce.
	DialectPOSIX Dialect = "posix"
	// DialectCmd is Windows cmd.exe: ^ escapes and continuations, then the
	// Microsoft C runtime's argument rules ("..." with \" and ""). Chrome's
	// "Copy as cURL (cmd)" produces it.
	DialectCmd Dialect = "cmd"
	// DialectPowerShell is PowerShell: ` escapes and continuations, '...'
	// with two single quotes for one, and "..." with ` escapes.
	DialectPowerShell Dialect = "powershell"
)

// Token is one shell word after quote removal.
type Token struct {
	// Value is the word as the program would receive it. Line endings in
	// quoted text are normalised to "\n".
	Value string `json:"value"`
	// Start and End are the byte range [Start, End) of the word in the
	// source, quotes included.
	Start int `json:"start"`
	End   int `json:"end"`
}

// Tokens is the result of Tokenize.
type Tokens struct {
	// Dialect is the dialect used: the one given, or the detected one.
	Dialect Dialect `json:"dialect"`
	// Words of the first command, in order. The first is normally "curl".
	Words []Token `json:"words"`
	// End is the byte offset where the command ends: len(src), or where
	// ignored trailing input (a pipe, a second command) starts.
	End         int          `json:"end"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// DetectDialect guesses the shell syntax of src: cmd when a line ends with
// "^" or the text contains `^"`, PowerShell when a line ends with a
// backtick, POSIX otherwise.
func DetectDialect(src string) Dialect {
	if strings.Contains(src, `^"`) {
		return DialectCmd
	}
	for line := range strings.SplitSeq(src, "\n") {
		line = strings.TrimRight(line, " \t\r")
		switch {
		case strings.HasSuffix(line, "^"):
			return DialectCmd
		case strings.HasSuffix(line, "`"):
			return DialectPowerShell
		}
	}
	return DialectPOSIX
}

// Tokenize splits the first command in src into words. It never executes or
// expands anything: variables, command substitutions and globs are kept as
// written and reported. A leading shell prompt ("$ ", "% ", "> ",
// "PS C:\>", "C:\>") is skipped. The command ends at an unescaped line
// break, unless the next line starts with "-" (a forgotten continuation,
// reported), or at a pipe, ";", "&&" or "||"; the rest is reported as
// trailing input.
func Tokenize(src string, d Dialect) Tokens {
	if d == DialectAuto {
		d = DetectDialect(src)
	}
	s := &scanner{src: src, start: -1, end: len(src)}
	s.pos = skipPrompt(src)
	switch d {
	case DialectCmd:
		s.cmd()
	case DialectPowerShell:
		s.powershell()
	default:
		d = DialectPOSIX
		s.posix()
	}
	sortDiagnostics(s.diags)
	return Tokens{Dialect: d, Words: s.words, End: s.end, Diagnostics: s.diags}
}

// scanner holds the state shared by the dialects: the word being built and
// the diagnostics.
type scanner struct {
	src   string
	pos   int
	end   int
	words []Token
	diags []Diagnostic

	buf     []byte
	start   int // start of the current word, -1 between words
	last    int // end of the current word so far
	globbed bool

	// discard: the next word is the target of a redirection, from redirAt.
	discard bool
	redirAt int

	inBacktick bool // after the opening backtick of `...`
}

func (s *scanner) diag(code string, sev Severity, start, end int, msg string) {
	s.diags = append(s.diags, Diagnostic{Code: code, Severity: sev, Start: start, End: end, Message: msg})
}

// begin starts a word at offset at if none is in progress.
func (s *scanner) begin(at int) {
	if s.start < 0 {
		s.start = at
	}
}

// add appends b to the current word, which ends at end so far.
func (s *scanner) add(end int, b ...byte) {
	s.buf = append(s.buf, b...)
	s.last = end
}

// addText appends text with CRLF normalised to LF.
func (s *scanner) addText(end int, text string) {
	s.buf = append(s.buf, strings.ReplaceAll(text, "\r\n", "\n")...)
	s.last = end
}

// finish ends the current word, if any.
func (s *scanner) finish() {
	if s.start < 0 {
		return
	}
	if s.discard {
		s.discard = false
		s.diag(CodeRedirectIgnored, SeverityWarning, s.redirAt, s.last, "The shell redirection is ignored.")
	} else {
		s.words = append(s.words, Token{Value: string(s.buf), Start: s.start, End: s.last})
	}
	s.buf, s.start, s.globbed = s.buf[:0], -1, false
}

// done ends the scan at the end of the text.
func (s *scanner) done() {
	s.finish()
	if s.discard {
		s.diag(CodeRedirectIgnored, SeverityWarning, s.redirAt, len(s.src), "The shell redirection is ignored.")
		s.discard = false
	}
	s.end = len(s.src)
	s.pos = len(s.src)
}

// stop ends the command at offset at and reports the rest as ignored.
func (s *scanner) stop(at int) {
	s.finish()
	s.discard = false
	s.end = at
	s.pos = len(s.src)
	s.diag(CodeTrailingInput, SeverityWarning, at, len(s.src),
		"Only the first command is imported; the text from here on is ignored.")
}

// newlineLen returns the length of the line break at i: 2 for CRLF, 1 for
// LF, 0 for anything else.
func newlineLen(src string, i int) int {
	if i < len(src) {
		switch src[i] {
		case '\n':
			return 1
		case '\r':
			if i+1 < len(src) && src[i+1] == '\n' {
				return 2
			}
		}
	}
	return 0
}

// lineBreak handles an unescaped line break of n bytes at i outside quotes.
// It reports whether the scan continues; s.pos is updated either way.
func (s *scanner) lineBreak(i, n int) bool {
	s.finish()
	j := i + n
	for j < len(s.src) && isBlank(s.src[j]) {
		j++
	}
	switch {
	case j == len(s.src):
		s.done()
		return false
	case s.src[j] == '-':
		s.diag(CodeMissingContinuation, SeverityWarning, i, i+n,
			"The line ends without a line continuation; the next line is read as part of the command.")
		s.pos = j
		return true
	default:
		s.stop(j)
		return false
	}
}

func isBlank(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// skipPrompt returns the offset after leading blank space and a shell
// prompt, if any.
func skipPrompt(src string) int {
	i := 0
	if strings.HasPrefix(src, "\uFEFF") {
		i = 3
	}
	for i < len(src) && isBlank(src[i]) {
		i++
	}
	rest := src[i:]
	lineEnd := strings.IndexByte(rest, '\n')
	if lineEnd < 0 {
		lineEnd = len(rest)
	}
	line := rest[:lineEnd]
	skip := 0
	switch {
	case strings.HasPrefix(line, "$ "), strings.HasPrefix(line, "% "), strings.HasPrefix(line, "> "):
		skip = 2
	case strings.HasPrefix(line, "PS ") || isDrivePrompt(line):
		if k := strings.IndexByte(line, '>'); k >= 0 {
			skip = k + 1
		}
	}
	if skip == 0 {
		return i
	}
	i += skip
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return i
}

// isDrivePrompt reports whether line starts like a cmd prompt: "C:\...>".
func isDrivePrompt(line string) bool {
	if len(line) < 4 || line[1] != ':' || line[2] != '\\' {
		return false
	}
	c := line[0] | 0x20
	k := strings.IndexByte(line, '>')
	return c >= 'a' && c <= 'z' && k > 0 && !strings.ContainsAny(line[:k], `"'`)
}

func isNameStart(c byte) bool {
	return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isNameChar(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

// dollar reports a shell expansion starting with "$" at i, if there is one.
// POSIX and PowerShell share the forms: $name, ${...}, $(...), $1, $?.
func (s *scanner) dollar(i int) {
	if i+1 >= len(s.src) {
		return
	}
	c := s.src[i+1]
	end := i + 2
	switch {
	case isNameStart(c):
		for end < len(s.src) && isNameChar(s.src[end]) {
			end++
		}
	case c == '{', c == '(':
		// The closing brace is looked for nearby only, which keeps the scan
		// linear on text with many unclosed "${".
		closing := byte('}')
		if c == '(' {
			closing = ')'
		}
		window := s.src[i:min(len(s.src), i+256)]
		if k := strings.IndexByte(window, closing); k > 0 {
			end = i + k + 1
		}
	case c >= '0' && c <= '9', strings.IndexByte("@*#?!$-", c) >= 0:
	default:
		return
	}
	s.diag(CodeShellExpansion, SeverityWarning, i, end,
		"The shell would expand "+quoteText(s.src[i:end])+"; it is kept as written.")
}

// quoteText quotes a short piece of source text for a message.
func quoteText(t string) string {
	const limit = 40
	if len(t) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(t[cut]) {
			cut--
		}
		t = t[:cut] + "…"
	}
	return "“" + strings.ToValidUTF8(t, "\uFFFD") + "”"
}

// posix scans POSIX sh words with Bash's $'...' and $"..." quoting.
func (s *scanner) posix() {
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
		case '\\':
			if n := newlineLen(src, i+1); n > 0 {
				s.pos = i + 1 + n // continuation
				continue
			}
			s.begin(i)
			if i+1 == len(src) {
				s.add(i+1, '\\')
				s.pos++
				continue
			}
			s.add(i+2, src[i+1])
			s.pos += 2
		case '\'':
			s.singleQuoted()
		case '"':
			s.begin(i)
			s.pos++
			s.doubleQuoted(i)
		case '$':
			switch {
			case i+1 < len(src) && src[i+1] == '\'':
				s.ansiC()
			case i+1 < len(src) && src[i+1] == '"':
				s.begin(i)
				s.pos += 2
				s.doubleQuoted(i)
			default:
				s.dollar(i)
				s.begin(i)
				s.add(i+1, '$')
				s.pos++
			}
		case '`':
			s.backtick(i)
		case '#':
			if s.start >= 0 {
				s.begin(i)
				s.add(i+1, c)
				s.pos++
				continue
			}
			for s.pos < len(src) && newlineLen(src, s.pos) == 0 {
				s.pos++
			}
		case '|', ';':
			s.stop(i)
			return
		case '&':
			switch {
			case i+1 < len(src) && src[i+1] == '>':
				s.redirect(i)
			case i+1 == len(src) || isBlank(src[i+1]) || src[i+1] == '&' || s.start < 0:
				s.stop(i)
				return
			default:
				s.diag(CodeUnquotedAmpersand, SeverityWarning, i, i+1,
					"An unquoted “&” would end the command in a shell; it is kept as part of the word.")
				s.add(i+1, c)
				s.pos++
			}
		case '<':
			if i+1 < len(src) && src[i+1] == '<' {
				s.stop(i) // here-document: its body is not part of the command
				return
			}
			s.redirect(i)
		case '>':
			s.redirect(i)
		case '*', '?', '[':
			s.glob(i)
		default:
			s.begin(i)
			s.add(i+1, c)
			s.pos++
		}
	}
	s.done()
}

func (s *scanner) glob(i int) {
	if !s.globbed {
		s.globbed = true
		start := i
		if s.start >= 0 {
			start = s.start
		}
		s.diag(CodeShellGlob, SeverityInfo, start, i+1,
			"A shell may expand an unquoted “*”, “?” or “[”; the text is kept as written.")
	}
	s.begin(i)
	s.add(i+1, s.src[i])
	s.pos++
}

// backtick reports a POSIX `...` command substitution and keeps it.
func (s *scanner) backtick(i int) {
	if s.inBacktick {
		s.inBacktick = false // the closing backtick, reported with the opening one
	} else {
		end := len(s.src)
		if k := strings.IndexByte(s.src[i+1:], '`'); k >= 0 {
			end = i + 1 + k + 1
			s.inBacktick = true
		}
		s.diag(CodeShellExpansion, SeverityWarning, i, end,
			"The shell would run the command "+quoteText(s.src[i:end])+"; it is kept as written.")
	}
	s.begin(i)
	s.add(i+1, '`')
	s.pos++
}

// redirect handles a redirection operator at i: an fd number just before it
// is dropped and the next word is discarded.
func (s *scanner) redirect(i int) {
	at := i
	if s.start >= 0 && s.last == i && len(s.buf) <= 2 && string(s.buf) == s.src[s.start:i] && allDigits(s.buf) {
		at = s.start
		s.buf, s.start = s.buf[:0], -1
	}
	s.finish()
	j := i + 1
	if s.src[i] == '&' {
		j++ // &>
	}
	if j < len(s.src) && (s.src[j] == '>' || s.src[j] == '&' || s.src[j] == '|') {
		j++
	}
	s.discard, s.redirAt = true, at
	s.pos = j
}

func allDigits(b []byte) bool {
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(b) > 0
}

func (s *scanner) unterminated(open int) {
	s.diag(CodeUnterminatedQuote, SeverityError, open, len(s.src), "The quote that starts here is never closed.")
}

// singleQuoted scans '...' at s.pos.
func (s *scanner) singleQuoted() {
	open := s.pos
	s.begin(open)
	k := strings.IndexByte(s.src[open+1:], '\'')
	if k < 0 {
		s.addText(len(s.src), s.src[open+1:])
		s.unterminated(open)
		s.pos = len(s.src)
		return
	}
	s.addText(open+1+k+1, s.src[open+1:open+1+k])
	s.pos = open + 1 + k + 1
}

// doubleQuoted scans the body of "..." from s.pos; open is the offset of
// the opening quote (or of the "$" of $"...").
func (s *scanner) doubleQuoted(open int) {
	src := s.src
	for s.pos < len(src) {
		i := s.pos
		switch c := src[i]; c {
		case '"':
			s.last = i + 1
			s.pos++
			return
		case '\\':
			if n := newlineLen(src, i+1); n > 0 {
				s.pos = i + 1 + n
				s.last = s.pos
				continue
			}
			if i+1 < len(src) && strings.IndexByte("$`\"\\", src[i+1]) >= 0 {
				s.add(i+2, src[i+1])
				s.pos += 2
				continue
			}
			s.add(i+1, c)
			s.pos++
		case '$':
			s.dollar(i)
			s.add(i+1, c)
			s.pos++
		case '`':
			s.backtick(i)
		case '\r':
			if newlineLen(src, i) == 2 {
				s.add(i+2, '\n')
				s.pos += 2
				continue
			}
			s.add(i+1, c)
			s.pos++
		default:
			s.add(i+1, c)
			s.pos++
		}
	}
	s.last = len(src)
	s.unterminated(open)
}

// ansiC scans Bash's $'...' at s.pos.
func (s *scanner) ansiC() {
	src := s.src
	open := s.pos
	s.begin(open)
	s.pos += 2
	var pendingHigh rune // a UTF-16 high surrogate waiting for its pair
	flushHigh := func() {
		if pendingHigh != 0 {
			s.buf = utf8.AppendRune(s.buf, utf8.RuneError)
			pendingHigh = 0
		}
	}
	for s.pos < len(src) {
		i := s.pos
		c := src[i]
		if c == '\'' {
			flushHigh()
			s.last = i + 1
			s.pos++
			return
		}
		if c != '\\' || i+1 == len(src) {
			flushHigh()
			if n := newlineLen(src, i); n == 2 {
				s.add(i+2, '\n')
				s.pos += 2
				continue
			}
			s.add(i+1, c)
			s.pos++
			continue
		}
		e := src[i+1]
		s.pos = i + 2
		if e == 'u' || e == 'U' {
			r, ok := s.ansiUnicode(e)
			if !ok {
				flushHigh()
				s.add(s.pos, '\\', e)
				continue
			}
			switch {
			case r >= 0xD800 && r < 0xDC00:
				flushHigh()
				pendingHigh = r
			case r >= 0xDC00 && r < 0xE000:
				if pendingHigh != 0 {
					r = 0x10000 + (pendingHigh-0xD800)<<10 + (r - 0xDC00)
					pendingHigh = 0
				} else {
					r = utf8.RuneError
				}
				s.buf = utf8.AppendRune(s.buf, r)
			default:
				flushHigh()
				if !utf8.ValidRune(r) {
					r = utf8.RuneError
				}
				s.buf = utf8.AppendRune(s.buf, r)
			}
			s.last = s.pos
			continue
		}
		flushHigh()
		switch e {
		case 'a':
			s.add(s.pos, '\a')
		case 'b':
			s.add(s.pos, '\b')
		case 'e', 'E':
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
		case '\\', '\'', '"', '?':
			s.add(s.pos, e)
		case 'x':
			v, n := parseHex(src[s.pos:], 2)
			if n == 0 {
				s.add(s.pos, '\\', 'x')
				continue
			}
			s.pos += n
			s.add(s.pos, byte(v))
		case 'c':
			if s.pos < len(src) {
				s.pos++
				s.add(s.pos, src[s.pos-1]&0x1f)
			} else {
				s.add(s.pos, '\\', 'c')
			}
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v, n := int(e-'0'), 1
			for n < 3 && s.pos < len(src) && src[s.pos] >= '0' && src[s.pos] <= '7' {
				v = v*8 + int(src[s.pos]-'0')
				s.pos++
				n++
			}
			s.add(s.pos, byte(v))
		default:
			if n := newlineLen(src, i+1); n == 2 {
				s.pos = i + 3
				s.add(s.pos, '\\', '\n')
				continue
			}
			s.add(s.pos, '\\', e)
		}
	}
	flushHigh()
	s.last = len(src)
	s.unterminated(open)
}

// ansiUnicode reads the digits of \u (up to 4), \U (up to 8) or \u{...}
// after s.pos.
func (s *scanner) ansiUnicode(e byte) (rune, bool) {
	rest := s.src[s.pos:]
	if strings.HasPrefix(rest, "{") {
		k := strings.IndexByte(rest[:min(len(rest), 10)], '}')
		if k < 2 || k > 9 {
			return 0, false
		}
		v, n := parseHex(rest[1:k], 8)
		if n != k-1 {
			return 0, false
		}
		s.pos += k + 1
		return clampRune(v), true
	}
	limit := 4
	if e == 'U' {
		limit = 8
	}
	v, n := parseHex(rest, limit)
	if n == 0 {
		return 0, false
	}
	s.pos += n
	return clampRune(v), true
}

func clampRune(v uint64) rune {
	if v > utf8.MaxRune {
		return utf8.RuneError
	}
	return rune(v)
}

// parseHex reads up to limit hex digits from the start of t.
func parseHex(t string, limit int) (uint64, int) {
	var v uint64
	n := 0
	for n < limit && n < len(t) {
		d, ok := hexDigit(t[n])
		if !ok {
			break
		}
		v = v<<4 | uint64(d)
		n++
	}
	return v, n
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c|0x20 >= 'a' && c|0x20 <= 'f':
		return (c | 0x20) - 'a' + 10, true
	}
	return 0, false
}
