package parser

import "strings"

// gapComments splits the comments in gap, the text between two tokens, into
// the previous declaration's trailing comment, detached comment blocks and
// the next declaration's leading comment, following protoc's
// Tokenizer::NextWithComments. gap holds only whitespace and comments.
//
//   - atStart: gap is the start of the file (no previous token).
//   - nextIsCloser: the token after gap is "}", "]" or ")", which takes no
//     leading comment.
//   - atEOF: gap runs to the end of the input.
func gapComments(gap string, atStart, nextIsCloser, atEOF bool) (trailing string, detached []string, leading string) {
	c := collector{canAttachToPrev: true}
	s := gapScanner{s: gap}
	prevLine, trailingEndLine := 0, -1

	finish := func() (string, []string, string) {
		if c.hasComment {
			leading = c.buf.String()
		}
		return c.trailing, c.detached, leading
	}

	if atStart {
		c.canAttachToPrev = false
	} else {
		s.skipSpace()
		switch s.commentStart() {
		case lineComment:
			trailingEndLine = s.line
			s.lineComment(c.bufForLine())
			c.flush()
		case blockComment:
			s.blockComment(c.bufForBlock())
			trailingEndLine = s.line
			s.skipSpace()
			if !s.consume('\n') {
				// The next token is on the same line: the comment belongs to
				// neither side, and protoc drops every comment up to it.
				return "", nil, ""
			}
			c.flush()
		default:
			if !s.consume('\n') {
				return "", nil, "" // the next token is on the same line
			}
		}
	}

	for {
		s.skipSpace()
		switch s.commentStart() {
		case lineComment:
			s.lineComment(c.bufForLine())
		case blockComment:
			s.blockComment(c.bufForBlock())
			s.skipSpace()
			s.consume('\n')
		default:
			if s.consume('\n') { // a blank line
				c.flush()
				c.canAttachToPrev = false
				continue
			}
			if s.i < len(s.s) {
				// Not whitespace or a comment; cannot happen for a real gap.
				s.i = len(s.s)
			}
			if nextIsCloser || atEOF {
				c.flush() // a scope's end takes no leading comment
			}
			if !atEOF && (prevLine == s.line || trailingEndLine == s.line) {
				c.maybeDetach()
			}
			return finish()
		}
	}
}

// collector is protoc's CommentCollector.
type collector struct {
	trailing    string
	hasTrailing bool
	detached    []string

	buf        strings.Builder
	hasComment bool
	isLine     bool

	canAttachToPrev bool
	numComments     int
}

func (c *collector) bufForLine() *strings.Builder {
	if c.hasComment && !c.isLine { // join line comments, not block comments
		c.flush()
	}
	c.hasComment, c.isLine = true, true
	return &c.buf
}

func (c *collector) bufForBlock() *strings.Builder {
	if c.hasComment {
		c.flush()
	}
	c.hasComment, c.isLine = true, false
	return &c.buf
}

// flush ends the current comment block: it becomes the trailing comment if
// it can still attach to the previous token, otherwise a detached one.
func (c *collector) flush() {
	if !c.hasComment {
		return
	}
	if c.canAttachToPrev {
		c.trailing += c.buf.String()
		c.hasTrailing = true
		c.canAttachToPrev = false
	} else {
		c.detached = append(c.detached, c.buf.String())
	}
	c.buf.Reset()
	c.hasComment = false
	c.numComments++
}

// maybeDetach detaches a single comment when the previous and next tokens
// share its line, since it is unclear which one it belongs to.
func (c *collector) maybeDetach() {
	count := c.numComments
	if c.hasComment {
		count++
	}
	if count != 1 {
		return
	}
	if c.hasTrailing && c.trailing != "" {
		c.detached = append([]string{c.trailing}, c.detached...)
		c.trailing = ""
	}
	c.canAttachToPrev = false
	c.flush()
}

type commentKind uint8

const (
	noComment commentKind = iota
	lineComment
	blockComment
)

type gapScanner struct {
	s    string
	i    int
	line int // "\n" seen so far
}

// skipSpace skips whitespace other than "\n"; "\r" counts as whitespace, as
// in protoc.
func (s *gapScanner) skipSpace() {
	for s.i < len(s.s) {
		switch s.s[s.i] {
		case ' ', '\t', '\v', '\f', '\r':
			s.i++
		default:
			return
		}
	}
}

func (s *gapScanner) consume(c byte) bool {
	if s.i < len(s.s) && s.s[s.i] == c {
		s.i++
		if c == '\n' {
			s.line++
		}
		return true
	}
	return false
}

func (s *gapScanner) commentStart() commentKind {
	if s.i+1 < len(s.s) && s.s[s.i] == '/' {
		switch s.s[s.i+1] {
		case '/':
			s.i += 2
			return lineComment
		case '*':
			s.i += 2
			return blockComment
		}
	}
	return noComment
}

// lineComment records the rest of the line, with its "\n" if present.
func (s *gapScanner) lineComment(b *strings.Builder) {
	start := s.i
	for s.i < len(s.s) && s.s[s.i] != '\n' {
		s.i++
	}
	b.WriteString(strings.TrimSuffix(s.s[start:s.i], "\r"))
	if s.consume('\n') {
		b.WriteByte('\n')
	}
}

// blockComment records a block comment after its "/*": continuation lines
// lose leading whitespace and one "*", and the closing "*/" is dropped.
func (s *gapScanner) blockComment(b *strings.Builder) {
	rec := s.i
	recording := true
	stop := func(end int) {
		if recording {
			b.WriteString(strings.ReplaceAll(s.s[rec:end], "\r\n", "\n"))
			recording = false
		}
	}
	for {
		for s.i < len(s.s) && s.s[s.i] != '*' && s.s[s.i] != '/' && s.s[s.i] != '\n' {
			s.i++
		}
		switch {
		case s.i >= len(s.s):
			stop(s.i)
			return
		case s.consume('\n'):
			stop(s.i)
			s.skipSpace()
			if s.i < len(s.s) && s.s[s.i] == '*' {
				s.i++
				if s.i < len(s.s) && s.s[s.i] == '/' {
					s.i++
					return
				}
			}
			rec, recording = s.i, true
		case s.s[s.i] == '*':
			s.i++
			if s.i < len(s.s) && s.s[s.i] == '/' {
				s.i++
				stop(s.i - 2)
				return
			}
		default: // '/'
			s.i++
		}
	}
}
