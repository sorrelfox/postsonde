package jsonpath

import "strings"

// maxExp bounds decimal exponents. Larger exponents saturate: they are
// far beyond any representable magnitude, and saturating keeps the
// arithmetic in int64.
const maxExp = 1 << 50

// decimal is an exact decimal number: (-1)^neg × 0.digits × 10^exp.
// digits has no leading or trailing zeros; it is empty for zero (and then
// neg and exp are unused), so 1, 1.0, 10e-1 and -0 compare as they should.
type decimal struct {
	neg    bool
	digits string
	exp    int64
}

// parseDecimal parses a JSON number literal (int, fraction, exponent; a
// leading '+' or leading zeros are rejected). It also accepts the output
// of strconv.FormatFloat with 'g'.
func parseDecimal(s string) (decimal, bool) {
	var d decimal
	i := 0
	if i < len(s) && s[i] == '-' {
		d.neg = true
		i++
	}
	start := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	intPart := s[start:i]
	if intPart == "" || (len(intPart) > 1 && intPart[0] == '0') {
		return decimal{}, false
	}
	frac := ""
	if i < len(s) && s[i] == '.' {
		i++
		fs := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		frac = s[fs:i]
		if frac == "" {
			return decimal{}, false
		}
	}
	var exp int64
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		eneg := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			eneg = s[i] == '-'
			i++
		}
		es := i
		for i < len(s) && isDigit(s[i]) {
			if exp < maxExp {
				exp = exp*10 + int64(s[i]-'0')
			}
			i++
		}
		if i == es {
			return decimal{}, false
		}
		exp = min(exp, maxExp)
		if eneg {
			exp = -exp
		}
	}
	if i != len(s) {
		return decimal{}, false
	}
	digits := intPart + frac
	exp += int64(len(intPart))
	lead := 0
	for lead < len(digits) && digits[lead] == '0' {
		lead++
	}
	digits = strings.TrimRight(digits[lead:], "0")
	d.digits = digits
	d.exp = exp - int64(lead)
	if digits == "" {
		return decimal{}, true
	}
	return d, true
}

// cmp returns -1, 0 or +1.
func (a decimal) cmp(b decimal) int {
	as, bs := a.sign(), b.sign()
	if as != bs {
		if as < bs {
			return -1
		}
		return 1
	}
	if as == 0 {
		return 0
	}
	c := 0
	switch {
	case a.exp != b.exp:
		if a.exp < b.exp {
			c = -1
		} else {
			c = 1
		}
	default:
		c = strings.Compare(a.digits, b.digits)
	}
	if as < 0 {
		c = -c
	}
	return c
}

func (a decimal) sign() int {
	switch {
	case a.digits == "":
		return 0
	case a.neg:
		return -1
	}
	return 1
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
