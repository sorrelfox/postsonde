package xpath

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokKind uint8

const (
	tEOF tokKind = iota
	tSlash
	tDSlash
	tLParen
	tRParen
	tLBrack
	tRBrack
	tDot
	tDDot
	tAt
	tComma
	tDColon
	tPipe
	tPlus
	tMinus
	tEq
	tNeq
	tLt
	tLe
	tGt
	tGe
	tMul      // '*' as the multiply operator
	tOpName   // and, or, mod, div
	tNameTest // *, NCName:*, QName
	tNodeType // comment, text, processing-instruction, node (followed by '(')
	tFuncName // a QName followed by '('
	tAxisName // an NCName followed by '::'
	tLiteral
	tNumber
	tVariable
)

type token struct {
	kind   tokKind
	pos    int
	end    int
	text   string // name, literal value, operator name
	prefix string // tNameTest, tFuncName: the prefix
	num    float64
}

// describe names a token for error messages.
func (t token) describe(src string) string {
	switch t.kind {
	case tEOF:
		return "end of expression"
	case tLiteral:
		return "string literal"
	case tNumber:
		return "number " + src[t.pos:t.end]
	}
	return "'" + src[t.pos:t.end] + "'"
}

// tokenize splits src into tokens, applying the disambiguation rules of
// XPath 1.0 §3.7 for '*' and operator names.
func tokenize(src string) ([]token, *SyntaxError) {
	var toks []token
	i := 0
	for {
		for i < len(src) && isSpace(src[i]) {
			i++
		}
		if i >= len(src) {
			toks = append(toks, token{kind: tEOF, pos: len(src), end: len(src)})
			return toks, nil
		}
		// An operator is expected after a token that can end an operand.
		operatorExpected := false
		if n := len(toks); n > 0 {
			switch toks[n-1].kind {
			case tAt, tDColon, tLParen, tLBrack, tComma,
				tSlash, tDSlash, tPipe, tPlus, tMinus, tEq, tNeq, tLt, tLe, tGt, tGe, tMul, tOpName:
			default:
				operatorExpected = true
			}
		}
		start := i
		c := src[i]
		tok := token{pos: start}
		two := ""
		if i+1 < len(src) {
			two = src[i : i+2]
		}
		switch {
		case two == "//":
			tok.kind, i = tDSlash, i+2
		case c == '/':
			tok.kind, i = tSlash, i+1
		case c == '(':
			tok.kind, i = tLParen, i+1
		case c == ')':
			tok.kind, i = tRParen, i+1
		case c == '[':
			tok.kind, i = tLBrack, i+1
		case c == ']':
			tok.kind, i = tRBrack, i+1
		case two == "..":
			tok.kind, i = tDDot, i+2
		case c == '.' && (i+1 >= len(src) || !isDigit(src[i+1])):
			tok.kind, i = tDot, i+1
		case c == '@':
			tok.kind, i = tAt, i+1
		case c == ',':
			tok.kind, i = tComma, i+1
		case two == "::":
			tok.kind, i = tDColon, i+2
		case c == '|':
			tok.kind, i = tPipe, i+1
		case c == '+':
			tok.kind, i = tPlus, i+1
		case c == '-':
			tok.kind, i = tMinus, i+1
		case c == '=':
			tok.kind, i = tEq, i+1
		case two == "!=":
			tok.kind, i = tNeq, i+2
		case two == "<=":
			tok.kind, i = tLe, i+2
		case c == '<':
			tok.kind, i = tLt, i+1
		case two == ">=":
			tok.kind, i = tGe, i+2
		case c == '>':
			tok.kind, i = tGt, i+1
		case c == '*':
			if operatorExpected {
				tok.kind = tMul
			} else {
				tok.kind, tok.text = tNameTest, "*"
			}
			i++
		case c == '"' || c == '\'':
			j := strings.IndexByte(src[i+1:], c)
			if j < 0 {
				return nil, syntaxErr(src, start, "unterminated string literal")
			}
			tok.kind, tok.text = tLiteral, src[i+1:i+1+j]
			i += j + 2
		case isDigit(c) || c == '.':
			j := i
			for j < len(src) && isDigit(src[j]) {
				j++
			}
			if j < len(src) && src[j] == '.' {
				j++
				for j < len(src) && isDigit(src[j]) {
					j++
				}
			}
			f, _ := strconv.ParseFloat(src[i:j], 64) // ±Inf when out of range
			tok.kind, tok.num = tNumber, f
			i = j
		case c == '$':
			j := i + 1
			if n := ncNameLen(src[j:]); n > 0 {
				j += n
			}
			tok.kind, i = tVariable, j
		default:
			n := ncNameLen(src[i:])
			if n == 0 {
				r, _ := utf8.DecodeRuneInString(src[i:])
				return nil, syntaxErr(src, start, "unexpected character "+strconv.QuoteRune(r))
			}
			name := src[i : i+n]
			i += n
			if operatorExpected {
				switch name {
				case "and", "or", "mod", "div":
					tok.kind, tok.text = tOpName, name
				default:
					return nil, syntaxErr(src, start, "unexpected name "+strconv.Quote(name)+": expected an operator")
				}
				break
			}
			// QName or NCName:*
			prefix := ""
			if i+1 < len(src) && src[i] == ':' && src[i+1] != ':' {
				if src[i+1] == '*' {
					tok.kind, tok.prefix, tok.text = tNameTest, name, "*"
					i += 2
					break
				}
				if m := ncNameLen(src[i+1:]); m > 0 {
					prefix, name = name, src[i+1:i+1+m]
					i += 1 + m
				}
			}
			tok.prefix, tok.text = prefix, name
			j := i
			for j < len(src) && isSpace(src[j]) {
				j++
			}
			switch {
			case j < len(src) && src[j] == '(':
				tok.kind = tFuncName
				if prefix == "" {
					switch name {
					case "comment", "text", "processing-instruction", "node":
						tok.kind = tNodeType
					}
				}
			case prefix == "" && strings.HasPrefix(src[j:], "::"):
				tok.kind = tAxisName
			default:
				tok.kind = tNameTest
			}
		}
		tok.end = i
		toks = append(toks, tok)
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// ncNameLen returns the length of the NCName at the start of s, or 0.
func ncNameLen(s string) int {
	n := 0
	for n < len(s) {
		r, size := utf8.DecodeRuneInString(s[n:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		if n == 0 {
			if !isNameStart(r) {
				return 0
			}
		} else if !isNameStart(r) && !isNameChar(r) {
			break
		}
		n += size
	}
	return n
}

// isNameStart accepts '_', ASCII letters and any non-ASCII character:
// every XPath delimiter is ASCII, and names encoding/xml accepted must
// always be expressible.
func isNameStart(r rune) bool {
	return r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || (r >= 0x80 && !unicode.IsSpace(r))
}

func isNameChar(r rune) bool {
	return r == '-' || r == '.' || ('0' <= r && r <= '9')
}
