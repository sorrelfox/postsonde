package themes

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MaxColorLength bounds a colour value, in bytes. The longest accepted
// form, "hsla(-359.99999deg, 100.0000%, 100.0000%, 0.500000)", is well
// under it.
const MaxColorLength = 64

// ParseColor parses a theme colour and returns it normalised to lower-case
// "#rrggbb", or "#rrggbbaa" when it is not opaque. It accepts only:
//
//   - hex: #rgb, #rgba, #rrggbb, #rrggbbaa
//   - rgb()/rgba() with three numbers 0–255 or percentages, comma- or
//     space-separated, and an optional alpha (0–1 or a percentage; after a
//     comma, or after "/" in the space-separated form)
//   - hsl()/hsla() with a hue (a number, optionally "deg"), saturation and
//     lightness percentages and the same optional alpha
//   - transparent
//
// Anything else is refused: named colours other than transparent, other
// colour functions, var(), url(), expressions, and any character that
// could end or escape a CSS value (";", "}", quotes, backslashes, …).
// Surrounding spaces are ignored; nothing else is.
func ParseColor(s string) (string, error) {
	if len(s) > MaxColorLength {
		return "", fmt.Errorf("longer than %d characters", MaxColorLength)
	}
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return "", fmt.Errorf("empty")
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("#(),./%+- ", r)) {
			return "", fmt.Errorf("character %q is not allowed", r)
		}
	}
	switch {
	case v == "transparent":
		return "#00000000", nil
	case strings.HasPrefix(v, "#"):
		return parseHex(v[1:])
	}
	open := strings.IndexByte(v, '(')
	if open < 0 || !strings.HasSuffix(v, ")") {
		return "", fmt.Errorf("not a hex colour, rgb(), rgba(), hsl(), hsla() or transparent")
	}
	fn, body := v[:open], v[open+1:len(v)-1]
	if strings.ContainsAny(body, "()") {
		return "", fmt.Errorf("nested parentheses are not allowed")
	}
	args, alpha, err := splitArgs(body)
	if err != nil {
		return "", err
	}
	a := 1.0
	if alpha != "" {
		if a, err = parseAlpha(alpha); err != nil {
			return "", err
		}
	}
	var r, g, b float64
	switch fn {
	case "rgb", "rgba":
		if r, err = parseChannel(args[0]); err == nil {
			if g, err = parseChannel(args[1]); err == nil {
				b, err = parseChannel(args[2])
			}
		}
	case "hsl", "hsla":
		r, g, b, err = parseHSL(args)
	default:
		return "", fmt.Errorf("%s() is not an allowed colour function", fn)
	}
	if err != nil {
		return "", err
	}
	return format(r, g, b, a*255), nil
}

func parseHex(h string) (string, error) {
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("not a hex colour")
		}
	}
	switch len(h) {
	case 3, 4:
		var sb strings.Builder
		for _, c := range h {
			sb.WriteRune(c)
			sb.WriteRune(c)
		}
		h = sb.String()
	case 6, 8:
	default:
		return "", fmt.Errorf("a hex colour has 3, 4, 6 or 8 digits")
	}
	if len(h) == 8 && h[6:] == "ff" {
		h = h[:6]
	}
	return "#" + h, nil
}

// splitArgs splits the arguments of a colour function into its three
// components and the alpha ("" when there is none).
func splitArgs(body string) ([]string, string, error) {
	body = strings.TrimSpace(body)
	var parts []string
	alpha := ""
	if strings.Contains(body, ",") {
		if strings.Contains(body, "/") {
			return nil, "", fmt.Errorf("mixes commas and a slash")
		}
		for _, p := range strings.Split(body, ",") {
			parts = append(parts, strings.TrimSpace(p))
		}
		if len(parts) == 4 {
			alpha, parts = parts[3], parts[:3]
		}
	} else {
		main := body
		if i := strings.IndexByte(body, '/'); i >= 0 {
			main, alpha = body[:i], strings.TrimSpace(body[i+1:])
			if alpha == "" || strings.ContainsAny(alpha, " /") {
				return nil, "", fmt.Errorf("the alpha after / is not one value")
			}
		}
		parts = strings.Fields(main)
	}
	if len(parts) != 3 {
		return nil, "", fmt.Errorf("needs three values and an optional alpha")
	}
	for _, p := range parts {
		if p == "" || strings.Contains(p, " ") {
			return nil, "", fmt.Errorf("a value is missing or has spaces")
		}
	}
	return parts, alpha, nil
}

// number parses a plain decimal number: digits, an optional fraction and
// sign. No exponents, no hex, no Inf or NaN.
func number(s string) (float64, error) {
	t := strings.TrimLeft(s, "+-")
	if len(s)-len(t) > 1 || t == "" || t == "." {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	dot := false
	for _, c := range t {
		switch {
		case c == '.' && !dot:
			dot = true
		case c >= '0' && c <= '9':
		default:
			return 0, fmt.Errorf("%q is not a number", s)
		}
	}
	return strconv.ParseFloat(s, 64)
}

func percent(s string) (float64, bool, error) {
	if p, ok := strings.CutSuffix(s, "%"); ok {
		v, err := number(p)
		return v, true, err
	}
	v, err := number(s)
	return v, false, err
}

func parseChannel(s string) (float64, error) {
	v, pct, err := percent(s)
	if err != nil {
		return 0, err
	}
	if pct {
		if v < 0 || v > 100 {
			return 0, fmt.Errorf("%q is outside 0%%–100%%", s)
		}
		return v * 255 / 100, nil
	}
	if v < 0 || v > 255 {
		return 0, fmt.Errorf("%q is outside 0–255", s)
	}
	return v, nil
}

func parseAlpha(s string) (float64, error) {
	v, pct, err := percent(s)
	if err != nil {
		return 0, err
	}
	if pct {
		v /= 100
	}
	if v < 0 || v > 1 {
		return 0, fmt.Errorf("the alpha %q is outside 0–1", s)
	}
	return v, nil
}

func parseHSL(args []string) (r, g, b float64, err error) {
	hs := strings.TrimSuffix(args[0], "deg")
	h, err := number(hs)
	if err != nil {
		return 0, 0, 0, err
	}
	var sl [2]float64
	for i, p := range args[1:] {
		v, pct, err := percent(p)
		if err != nil {
			return 0, 0, 0, err
		}
		if !pct || v < 0 || v > 100 {
			return 0, 0, 0, fmt.Errorf("%q is not a percentage from 0%% to 100%%", p)
		}
		sl[i] = v / 100
	}
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	s, l := sl[0], sl[1]
	f := func(n float64) float64 {
		k := math.Mod(n+h/30, 12)
		a := s * math.Min(l, 1-l)
		return (l - a*math.Max(-1, math.Min(math.Min(k-3, 9-k), 1))) * 255
	}
	return f(0), f(8), f(4), nil
}

func format(r, g, b, a float64) string {
	c := func(v float64) int { return int(math.Round(math.Max(0, math.Min(255, v)))) }
	if c(a) == 255 {
		return fmt.Sprintf("#%02x%02x%02x", c(r), c(g), c(b))
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c(r), c(g), c(b), c(a))
}
