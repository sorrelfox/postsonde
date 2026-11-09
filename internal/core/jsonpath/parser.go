package jsonpath

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxNesting is the deepest nesting of parentheses, filters and function
// calls Parse accepts.
const MaxNesting = 64

// maxExactInt is the largest magnitude of an index or slice bound: the
// I-JSON exact integer range [-(2^53)+1, (2^53)-1].
const maxExactInt = 1<<53 - 1

// Parse parses a JSONPath query (RFC 9535). The error is a *SyntaxError.
func Parse(src string) (*Path, error) {
	p := &parser{src: src}
	q, err := p.parse()
	if err != nil {
		return nil, err
	}
	return &Path{src: src, q: q}, nil
}

// MustParse is Parse that panics on an invalid query; for tests and
// package-level constants.
func MustParse(src string) *Path {
	p, err := Parse(src)
	if err != nil {
		panic(err)
	}
	return p
}

type parser struct {
	src   string
	pos   int
	depth int
	err   *SyntaxError
}

// bail is panicked with to unwind the recursive descent on the first error.
type bail struct{}

func (p *parser) parse() (q *query, err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bail); !ok {
				panic(r)
			}
			q, err = nil, p.err
		}
	}()
	if !p.peek('$') {
		p.fail(p.pos, "a query must start with '$'")
	}
	p.pos++
	q = &query{segments: p.segments()}
	if p.pos < len(p.src) {
		p.unexpected("expected '.', '..' or '['")
	}
	return q, nil
}

// fail stops parsing with an error at byte offset off.
func (p *parser) fail(off int, msg string) {
	off = min(max(off, 0), len(p.src))
	p.err = &SyntaxError{
		Offset:   off,
		Offset16: utf16Len(p.src[:off]),
		Message:  msg,
	}
	panic(bail{})
}

// unexpected fails at the current position naming what is there.
func (p *parser) unexpected(expected string) {
	p.fail(p.pos, "unexpected "+p.describe()+": "+expected)
}

func (p *parser) describe() string {
	if p.pos >= len(p.src) {
		return "end of query"
	}
	r, size := utf8.DecodeRuneInString(p.src[p.pos:])
	switch {
	case r == utf8.RuneError && size == 1:
		return "invalid UTF-8"
	case r == ' ', r == '\t', r == '\n', r == '\r':
		return "whitespace"
	case r < 0x20 || r == 0x7f:
		return "control character " + strconv.QuoteRune(r)
	}
	return "'" + string(r) + "'"
}

func (p *parser) peek(c byte) bool {
	return p.pos < len(p.src) && p.src[p.pos] == c
}

func (p *parser) peekStr(s string) bool {
	return strings.HasPrefix(p.src[p.pos:], s)
}

func (p *parser) expect(c byte, expected string) {
	if !p.peek(c) {
		p.unexpected(expected)
	}
	p.pos++
}

// skipS skips blank space (RFC 9535 "S": space, tab, LF, CR).
func (p *parser) skipS() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) enter() {
	p.depth++
	if p.depth > MaxNesting {
		p.fail(p.pos, "the query is nested too deeply (more than "+strconv.Itoa(MaxNesting)+" levels)")
	}
}

func (p *parser) leave() { p.depth-- }

// segments parses *(S segment). Blank space not followed by a segment is
// left for the caller.
func (p *parser) segments() []segment {
	var segs []segment
	for {
		save := p.pos
		p.skipS()
		if p.pos >= len(p.src) {
			p.pos = save
			return segs
		}
		switch p.src[p.pos] {
		case '[':
			segs = append(segs, segment{selectors: p.bracketed()})
		case '.':
			p.pos++
			if p.peek('.') {
				p.pos++
				seg := segment{descendant: true}
				switch {
				case p.peek('['):
					seg.selectors = p.bracketed()
				case p.peek('*'):
					p.pos++
					seg.selectors = []selector{wildcardSel{}}
				case p.atNameFirst():
					seg.selectors = []selector{nameSel{p.memberName()}}
				default:
					p.unexpected("expected a member name, '*' or '[' after '..'")
				}
				segs = append(segs, seg)
				continue
			}
			switch {
			case p.peek('*'):
				p.pos++
				segs = append(segs, segment{selectors: []selector{wildcardSel{}}})
			case p.atNameFirst():
				segs = append(segs, segment{selectors: []selector{nameSel{p.memberName()}}})
			default:
				p.unexpected("expected a member name or '*' after '.'")
			}
		default:
			p.pos = save
			return segs
		}
	}
}

// atNameFirst reports whether a member-name-shorthand starts here.
func (p *parser) atNameFirst() bool {
	if p.pos >= len(p.src) {
		return false
	}
	r, size := utf8.DecodeRuneInString(p.src[p.pos:])
	if r == utf8.RuneError && size == 1 {
		return false
	}
	return isNameFirst(r)
}

func isNameFirst(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' ||
		r >= 0x80 && r <= 0xD7FF || r >= 0xE000 && r <= 0x10FFFF
}

// memberName parses member-name-shorthand.
func (p *parser) memberName() string {
	start := p.pos
	for p.pos < len(p.src) {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		if !isNameFirst(r) && !(r >= '0' && r <= '9') {
			break
		}
		p.pos += size
	}
	return p.src[start:p.pos]
}

// bracketed parses "[" S selector *(S "," S selector) S "]".
func (p *parser) bracketed() []selector {
	p.pos++ // '['
	var sels []selector
	for {
		p.skipS()
		sels = append(sels, p.selector())
		p.skipS()
		if p.peek(',') {
			p.pos++
			continue
		}
		if p.peek(']') {
			p.pos++
			return sels
		}
		p.unexpected("expected ',' or ']'")
	}
}

func (p *parser) selector() selector {
	if p.pos >= len(p.src) {
		p.unexpected("expected a selector")
	}
	switch c := p.src[p.pos]; {
	case c == '\'' || c == '"':
		return nameSel{p.stringLiteral()}
	case c == '*':
		p.pos++
		return wildcardSel{}
	case c == '?':
		p.pos++
		p.enter()
		p.skipS()
		start := p.pos
		e := p.logical(p.orExpr(), start)
		p.leave()
		return filterSel{e}
	case c == ':' || c == '-' || isDigit(c):
		return p.indexOrSlice()
	}
	p.unexpected("expected a selector (name, '*', index, slice or '?' filter)")
	return nil
}

func (p *parser) indexOrSlice() selector {
	var s sliceSel
	if !p.peek(':') {
		s.start, s.hasStart = p.integer(), true
		p.skipS()
		if !p.peek(':') {
			return indexSel{s.start}
		}
	}
	p.pos++ // ':'
	p.skipS()
	if p.peek('-') || p.pos < len(p.src) && isDigit(p.src[p.pos]) {
		s.end, s.hasEnd = p.integer(), true
		p.skipS()
	}
	if p.peek(':') {
		p.pos++
		p.skipS()
		if p.peek('-') || p.pos < len(p.src) && isDigit(p.src[p.pos]) {
			s.step, s.hasStep = p.integer(), true
		}
	}
	return s
}

// integer parses int = "0" / (["-"] DIGIT1 *DIGIT) within the I-JSON
// exact range.
func (p *parser) integer() int64 {
	start := p.pos
	if p.peek('-') {
		p.pos++
	}
	ds := p.pos
	for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
		p.pos++
	}
	digits := p.src[ds:p.pos]
	switch {
	case digits == "":
		p.unexpected("expected a digit")
	case digits[0] == '0' && len(digits) > 1:
		p.fail(start, "an integer must not have leading zeros")
	case digits == "0" && ds > start:
		p.fail(start, "-0 is not a valid index")
	}
	n, err := strconv.ParseInt(p.src[start:p.pos], 10, 64)
	if err != nil || n > maxExactInt || n < -maxExactInt {
		p.fail(start, "the integer "+p.src[start:p.pos]+" is out of range (at most 2^53-1 in magnitude)")
	}
	return n
}

// stringLiteral parses a single- or double-quoted string literal.
func (p *parser) stringLiteral() string {
	quote := p.src[p.pos]
	start := p.pos
	p.pos++
	var b strings.Builder
	for {
		if p.pos >= len(p.src) {
			p.fail(start, "unterminated string literal")
		}
		c := p.src[p.pos]
		switch {
		case c == quote:
			p.pos++
			return b.String()
		case c == '\\':
			p.escape(quote, &b)
		case c < 0x20:
			p.fail(p.pos, "control character "+strconv.QuoteRune(rune(c))+" must be escaped in a string literal")
		default:
			r, size := utf8.DecodeRuneInString(p.src[p.pos:])
			if r == utf8.RuneError && size == 1 {
				p.fail(p.pos, "invalid UTF-8 in string literal")
			}
			b.WriteString(p.src[p.pos : p.pos+size])
			p.pos += size
		}
	}
}

func (p *parser) escape(quote byte, b *strings.Builder) {
	start := p.pos
	p.pos++ // '\'
	if p.pos >= len(p.src) {
		p.fail(start, "unterminated string literal")
	}
	c := p.src[p.pos]
	p.pos++
	switch c {
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case 'n':
		b.WriteByte('\n')
	case 'r':
		b.WriteByte('\r')
	case 't':
		b.WriteByte('\t')
	case '/', '\\':
		b.WriteByte(c)
	case '\'', '"':
		if c != quote {
			p.fail(start, "invalid escape \\"+string(c)+" (only the enclosing quote can be escaped)")
		}
		b.WriteByte(c)
	case 'u':
		r := p.hex4(start)
		switch {
		case utf16.IsSurrogate(r) && r < 0xDC00:
			if !p.peekStr(`\u`) {
				p.fail(start, "a high surrogate escape must be followed by a low surrogate escape")
			}
			p.pos += 2
			lo := p.hex4(start)
			if lo < 0xDC00 || lo > 0xDFFF {
				p.fail(start, "a high surrogate escape must be followed by a low surrogate escape")
			}
			r = utf16.DecodeRune(r, lo)
		case utf16.IsSurrogate(r):
			p.fail(start, "lone low surrogate escape")
		}
		b.WriteRune(r)
	default:
		p.fail(start, "invalid escape sequence in string literal")
	}
}

func (p *parser) hex4(start int) rune {
	if p.pos+4 > len(p.src) {
		p.fail(start, "\\u must be followed by four hex digits")
	}
	var r rune
	for i := range 4 {
		c := p.src[p.pos+i]
		var v byte
		switch {
		case isDigit(c):
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			p.fail(start, "\\u must be followed by four hex digits")
		}
		r = r<<4 | rune(v)
	}
	p.pos += 4
	return r
}

// ---- filter expressions ----

// logical converts e to a logical expression, or fails: literals and
// ValueType function results cannot be tested on their own.
func (p *parser) logical(e expr, off int) expr {
	switch x := e.(type) {
	case literalExpr:
		p.fail(off, "a literal cannot be used as a test; compare it with something")
	case queryExpr:
		return existExpr{x.q}
	case *funcExpr:
		if x.fn.result == valueType {
			p.fail(off, "the result of "+x.fn.name+"() is a value and must be compared")
		}
		return funcTestExpr{x}
	}
	return e
}

func (p *parser) orExpr() expr {
	start := p.pos
	first := p.andExpr()
	var es []expr
	for {
		save := p.pos
		p.skipS()
		if !p.peekStr("||") {
			p.pos = save
			break
		}
		if es == nil {
			es = []expr{p.logical(first, start)}
		}
		p.pos += 2
		p.skipS()
		off := p.pos
		es = append(es, p.logical(p.andExpr(), off))
	}
	if es == nil {
		return first
	}
	return orExpr{es}
}

func (p *parser) andExpr() expr {
	start := p.pos
	first := p.basicExpr()
	var es []expr
	for {
		save := p.pos
		p.skipS()
		if !p.peekStr("&&") {
			p.pos = save
			break
		}
		if es == nil {
			es = []expr{p.logical(first, start)}
		}
		p.pos += 2
		p.skipS()
		off := p.pos
		es = append(es, p.logical(p.basicExpr(), off))
	}
	if es == nil {
		return first
	}
	return andExpr{es}
}

// basicExpr parses paren-expr, comparison-expr or test-expr. A bare
// literal, query or function is returned raw for the caller to convert.
func (p *parser) basicExpr() expr {
	if p.peek('!') {
		p.pos++
		p.skipS()
		off := p.pos
		if p.peek('(') {
			return notExpr{p.parenExpr()}
		}
		e := p.primary()
		if _, ok := e.(literalExpr); ok {
			p.fail(off, "'!' must be followed by a query, a function or '('")
		}
		return notExpr{p.logical(e, off)}
	}
	if p.peek('(') {
		return p.parenExpr()
	}
	start := p.pos
	left := p.primary()
	save := p.pos
	p.skipS()
	op := p.comparisonOp()
	if op == "" {
		p.pos = save
		return left
	}
	p.checkComparable(left, start)
	p.skipS()
	rstart := p.pos
	right := p.primary()
	p.checkComparable(right, rstart)
	return compareExpr{op: op, l: left, r: right}
}

func (p *parser) parenExpr() expr {
	p.enter()
	p.pos++ // '('
	p.skipS()
	start := p.pos
	e := p.logical(p.orExpr(), start)
	p.skipS()
	p.expect(')', "expected ')'")
	p.leave()
	return e
}

func (p *parser) comparisonOp() string {
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if p.peekStr(op) {
			p.pos += len(op)
			return op
		}
	}
	return ""
}

func (p *parser) checkComparable(e expr, off int) {
	switch x := e.(type) {
	case literalExpr:
	case queryExpr:
		if !x.q.singular() {
			p.fail(off, "only a singular query (names and indexes only) can be compared")
		}
	case *funcExpr:
		if x.fn.result != valueType {
			p.fail(off, "the result of "+x.fn.name+"() is "+x.fn.result.String()+" and cannot be compared")
		}
	default:
		p.fail(off, "expected a literal, a singular query or a function to compare")
	}
}

// primary parses a literal, a filter query or a function expression.
func (p *parser) primary() expr {
	if p.pos >= len(p.src) {
		p.unexpected("expected an expression")
	}
	switch c := p.src[p.pos]; {
	case c == '@' || c == '$':
		p.pos++
		return queryExpr{&query{relative: c == '@', segments: p.segments()}}
	case c == '\'' || c == '"':
		return literalExpr{p.stringLiteral()}
	case c == '-' || isDigit(c):
		return literalExpr{p.number()}
	case c >= 'a' && c <= 'z':
		start := p.pos
		for p.pos < len(p.src) && (p.src[p.pos] >= 'a' && p.src[p.pos] <= 'z' ||
			isDigit(p.src[p.pos]) || p.src[p.pos] == '_') {
			p.pos++
		}
		name := p.src[start:p.pos]
		if p.peek('(') {
			return p.function(name, start)
		}
		switch name {
		case "true":
			return literalExpr{true}
		case "false":
			return literalExpr{false}
		case "null":
			return literalExpr{nil}
		}
		p.fail(start, "unknown name "+strconv.Quote(name)+": expected true, false, null or a function call")
	}
	p.unexpected("expected a literal, '@', '$', a function or '('")
	return nil
}

// number parses number = (int / "-0") [ frac ] [ exp ].
func (p *parser) number() json.Number {
	start := p.pos
	if p.peek('-') {
		p.pos++
	}
	ds := p.pos
	for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
		p.pos++
	}
	if p.pos == ds {
		p.unexpected("expected a digit")
	}
	if p.src[ds] == '0' && p.pos-ds > 1 {
		p.fail(start, "a number must not have leading zeros")
	}
	if p.peek('.') {
		p.pos++
		fs := p.pos
		for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
			p.pos++
		}
		if p.pos == fs {
			p.unexpected("expected a digit after '.'")
		}
	}
	if p.peek('e') || p.peek('E') {
		p.pos++
		if p.peek('+') || p.peek('-') {
			p.pos++
		}
		es := p.pos
		for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
			p.pos++
		}
		if p.pos == es {
			p.unexpected("expected a digit in the exponent")
		}
	}
	return json.Number(p.src[start:p.pos])
}

// function parses function-name "(" S [args] S ")" and checks the
// argument types (RFC 9535 §2.4.3).
func (p *parser) function(name string, start int) expr {
	fn := functions[name]
	if fn == nil {
		p.fail(start, "unknown function "+name+"()")
	}
	p.enter()
	p.pos++ // '('
	f := &funcExpr{fn: fn}
	p.skipS()
	if !p.peek(')') {
		for {
			p.skipS()
			off := p.pos
			arg := p.orExpr()
			i := len(f.args)
			if i >= len(fn.params) {
				p.fail(off, name+"() takes "+plural(len(fn.params), "argument"))
			}
			f.args = append(f.args, p.argument(arg, fn.params[i], name, off))
			p.skipS()
			if p.peek(',') {
				p.pos++
				continue
			}
			break
		}
	}
	p.expect(')', "expected ',' or ')'")
	if len(f.args) != len(fn.params) {
		p.fail(start, name+"() takes "+plural(len(fn.params), "argument"))
	}
	p.leave()
	if (name == "match" || name == "search") && len(f.args) == 2 {
		if lit, ok := f.args[1].(literalExpr); ok {
			f.reSet = true
			if s, ok := lit.value.(string); ok {
				re, err := compileIRegexp(s, name == "match")
				f.re, f.reErr = re, err != nil
			} else {
				f.reErr = true
			}
		}
	}
	return f
}

// argument checks one function argument against its parameter type.
func (p *parser) argument(arg expr, want ftype, name string, off int) expr {
	switch want {
	case valueType:
		switch x := arg.(type) {
		case literalExpr:
			return arg
		case queryExpr:
			if x.q.singular() {
				return arg
			}
			p.fail(off, name+"() needs a value here; the query is not singular")
		case *funcExpr:
			if x.fn.result == valueType {
				return arg
			}
		}
		p.fail(off, name+"() needs a value here (a literal, a singular query or a function returning a value)")
	case logicalType:
		if _, ok := arg.(literalExpr); ok {
			p.fail(off, name+"() needs a logical expression here, not a literal")
		}
		return p.logical(arg, off)
	case nodesType:
		switch x := arg.(type) {
		case queryExpr:
			return arg
		case *funcExpr:
			if x.fn.result == nodesType {
				return arg
			}
		}
		p.fail(off, name+"() needs a query here")
	}
	return nil
}

func plural(n int, word string) string {
	s := strconv.Itoa(n) + " " + word
	if n != 1 {
		s += "s"
	}
	return s
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
