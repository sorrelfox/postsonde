package jsonschema

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Host names for the hostname and idn-hostname formats: RFC 1123 labels,
// A-labels ("xn--…") decoded with Punycode (RFC 3492) and checked as
// U-labels, and U-labels checked against the IDNA 2008 rules (RFC 5891,
// 5892, 5893) that the standard library's Unicode tables can express:
//
//   - a label does not start with a combining mark, nor have "--" in
//     positions 3 and 4, nor start or end with a hyphen;
//   - the code points are letters (not upper or title case), digits or
//     non-enclosing marks, or the exceptions of RFC 5892 section 2.6, and
//     not Hangul Jamo;
//   - the contextual rules of RFC 5892 appendix A (ZERO WIDTH (NON-)JOINER,
//     MIDDLE DOT, Greek KERAIA, Hebrew GERESH and GERSHAYIM, KATAKANA
//     MIDDLE DOT, the two sets of Arabic-Indic digits);
//   - the Bidi rule of RFC 5893 when the name has a right-to-left label.
//
// Approximations, as the Bidi_Class and Joining_Type properties are not
// in the standard library: a character of a right-to-left script has
// Bidi class R, and a letter of a cursive-joining script joins on both
// sides. The UTS #46 mapping (case and width folding, ignored characters)
// is not applied: the name is checked as written. Unicode normalization
// is not checked.

// isHostname reports whether s is a host name; with idn, U-labels and
// the ideographic full stops as label separators are allowed too.
func isHostname(s string, idn bool) bool {
	if s == "" {
		return false
	}
	var labels []string
	if idn {
		labels = splitIDN(s)
	} else {
		labels = strings.Split(s, ".")
	}
	total := len(labels) - 1 // the dots
	uLabels := make([][]rune, 0, len(labels))
	for _, label := range labels {
		if label == "" {
			return false
		}
		if isASCII(label) {
			if len(label) > 63 || !isLDH(label) {
				return false
			}
			total += len(label)
			if len(label) >= 4 && label[2:4] == "--" {
				if !strings.EqualFold(label[:4], "xn--") {
					return false
				}
				u, ok := decodeALabel(label[4:])
				if !ok || !validULabel(u) {
					return false
				}
				uLabels = append(uLabels, u)
				continue
			}
			uLabels = append(uLabels, []rune(label))
			continue
		}
		if !idn || !utf8.ValidString(label) {
			return false
		}
		u := []rune(label)
		if !validULabel(u) {
			return false
		}
		enc, err := punyEncode(u)
		if err != nil || len(enc)+4 > 63 {
			return false
		}
		total += len(enc) + 4
		uLabels = append(uLabels, u)
	}
	return total <= 253 && bidiOK(uLabels)
}

// splitIDN splits a name at the label separators of IDNA (RFC 3490
// section 3.1): full stop, ideographic full stop, fullwidth full stop
// and halfwidth ideographic full stop.
func splitIDN(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		switch r {
		case '.', '。', '．', '｡':
			out = append(out, s[start:i])
			start = i + utf8.RuneLen(r)
		}
	}
	return append(out, s[start:])
}

// isLDH: letters, digits and hyphens, not starting or ending with a
// hyphen.
func isLDH(label string) bool {
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if !(c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// decodeALabel decodes the Punycode after "xn--": it must decode, hold a
// non-ASCII character and encode back to itself.
func decodeALabel(body string) ([]rune, bool) {
	u, err := punyDecode(body)
	if err != nil || isASCII(string(u)) {
		return nil, false
	}
	enc, err := punyEncode(u)
	if err != nil || !strings.EqualFold(enc, body) {
		return nil, false
	}
	return u, true
}

// validULabel checks a label's code points and contextual rules (RFC 5891
// section 5.4, RFC 5892).
func validULabel(u []rune) bool {
	if len(u) == 0 || u[0] == '-' || u[len(u)-1] == '-' {
		return false
	}
	if len(u) >= 4 && u[2] == '-' && u[3] == '-' {
		return false
	}
	if unicode.In(u[0], unicode.Mn, unicode.Mc, unicode.Me) {
		return false
	}
	for i, r := range u {
		switch idnaProperty(r) {
		case idnaPValid:
		case idnaContextJ:
			if !contextJ(u, i) {
				return false
			}
		case idnaContextO:
			if !contextO(u, i) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

type idnaProp int

const (
	idnaDisallowed idnaProp = iota
	idnaPValid
	idnaContextJ
	idnaContextO
)

// idnaProperty approximates the derived property of RFC 5892.
func idnaProperty(r rune) idnaProp {
	switch r {
	// Exceptions (RFC 5892 section 2.6).
	case 0x00DF, 0x03C2, 0x06FD, 0x06FE, 0x0F0B, 0x3007:
		return idnaPValid
	case 0x00B7, 0x0375, 0x05F3, 0x05F4, 0x30FB:
		return idnaContextO
	case 0x0640, 0x07FA, 0x302E, 0x302F, 0x3031, 0x3032, 0x3033, 0x3034, 0x3035, 0x303B:
		return idnaDisallowed
	case 0x200C, 0x200D:
		return idnaContextJ
	}
	switch {
	case r >= 0x0660 && r <= 0x0669, r >= 0x06F0 && r <= 0x06F9:
		return idnaContextO
	case r < utf8.RuneSelf:
		if r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return idnaPValid
		}
		return idnaDisallowed
	case r >= 0x1100 && r <= 0x11FF, r >= 0xA960 && r <= 0xA97F, r >= 0xD7B0 && r <= 0xD7FF:
		return idnaDisallowed // Hangul Jamo (OldHangulJamo)
	case unicode.In(r, unicode.Ll, unicode.Lo, unicode.Lm, unicode.Mn, unicode.Mc, unicode.Nd):
		return idnaPValid
	}
	return idnaDisallowed
}

// viramas are the code points with Canonical_Combining_Class Virama (9).
var viramas = []rune{
	0x094D, 0x09CD, 0x0A4D, 0x0ACD, 0x0B4D, 0x0BCD, 0x0C4D, 0x0CCD, 0x0D3B, 0x0D3C,
	0x0D4D, 0x0DCA, 0x0E3A, 0x0EBA, 0x0F84, 0x1039, 0x103A, 0x1714, 0x1715, 0x1734,
	0x17D2, 0x1A60, 0x1B44, 0x1BAA, 0x1BAB, 0x1BF2, 0x1BF3, 0x2D7F, 0xA806, 0xA82C,
	0xA8C4, 0xA953, 0xA9C0, 0xAAF6, 0xABED, 0x10A3F, 0x11046, 0x11070, 0x1107F, 0x110B9,
	0x11133, 0x11134, 0x111C0, 0x11235, 0x112EA, 0x1134D, 0x113CE, 0x113CF, 0x113D0, 0x11442,
	0x114C2, 0x115BF, 0x1163F, 0x116B6, 0x1172B, 0x11839, 0x1193D, 0x1193E, 0x119E0, 0x11A34,
	0x11A47, 0x11A99, 0x11C3F, 0x11D44, 0x11D45, 0x11D97, 0x11F41, 0x11F42, 0x1612F,
}

func isVirama(r rune) bool {
	_, found := slices.BinarySearch(viramas, r)
	return found
}

// joiningScripts are the cursive scripts whose letters join (Joining_Type
// D, L or R); every letter of them is taken as joining on both sides.
var joiningScripts = []*unicode.RangeTable{
	unicode.Arabic, unicode.Syriac, unicode.Nko, unicode.Mongolian, unicode.Mandaic,
	unicode.Manichaean, unicode.Psalter_Pahlavi, unicode.Phags_Pa, unicode.Adlam,
	unicode.Hanifi_Rohingya, unicode.Sogdian, unicode.Old_Uyghur, unicode.Chorasmian,
}

func isJoining(r rune) bool {
	return unicode.IsLetter(r) && unicode.In(r, joiningScripts...)
}

// isTransparent approximates Joining_Type T.
func isTransparent(r rune) bool {
	return r != 0x200C && r != 0x200D && unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf)
}

// contextJ: the rules of RFC 5892 appendix A.1 and A.2.
func contextJ(u []rune, i int) bool {
	if i > 0 && isVirama(u[i-1]) {
		return true
	}
	if u[i] == 0x200D {
		return false
	}
	// ZERO WIDTH NON-JOINER between joining letters:
	// (L|D) T* ZWNJ T* (R|D).
	j := i - 1
	for j >= 0 && isTransparent(u[j]) {
		j--
	}
	k := i + 1
	for k < len(u) && isTransparent(u[k]) {
		k++
	}
	return j >= 0 && k < len(u) && isJoining(u[j]) && isJoining(u[k])
}

// contextO: the rules of RFC 5892 appendix A.3 to A.9.
func contextO(u []rune, i int) bool {
	switch r := u[i]; {
	case r == 0x00B7: // MIDDLE DOT: between two 'l'
		return i > 0 && i+1 < len(u) && u[i-1] == 'l' && u[i+1] == 'l'
	case r == 0x0375: // Greek KERAIA: before a Greek character
		return i+1 < len(u) && unicode.Is(unicode.Greek, u[i+1])
	case r == 0x05F3, r == 0x05F4: // Hebrew GERESH, GERSHAYIM: after a Hebrew character
		return i > 0 && unicode.Is(unicode.Hebrew, u[i-1])
	case r == 0x30FB: // KATAKANA MIDDLE DOT: with Hiragana, Katakana or Han
		for _, c := range u {
			if c != 0x30FB && unicode.In(c, unicode.Hiragana, unicode.Katakana, unicode.Han) {
				return true
			}
		}
		return false
	case r >= 0x0660 && r <= 0x0669: // not with Extended Arabic-Indic digits
		return !slices.ContainsFunc(u, func(c rune) bool { return c >= 0x06F0 && c <= 0x06F9 })
	case r >= 0x06F0 && r <= 0x06F9: // not with Arabic-Indic digits
		return !slices.ContainsFunc(u, func(c rune) bool { return c >= 0x0660 && c <= 0x0669 })
	}
	return false
}

// ---- Bidi rule (RFC 5893) ----

type bidiClass int

const (
	bidiL bidiClass = iota
	bidiR           // R or AL
	bidiAN
	bidiEN
	bidiNSM
	bidiOther // ES, CS, ET, ON, BN
)

// rtlScripts are the scripts written right to left.
var rtlScripts = []*unicode.RangeTable{
	unicode.Hebrew, unicode.Arabic, unicode.Syriac, unicode.Thaana, unicode.Nko,
	unicode.Samaritan, unicode.Mandaic, unicode.Adlam, unicode.Hanifi_Rohingya,
	unicode.Mende_Kikakui, unicode.Imperial_Aramaic, unicode.Phoenician, unicode.Kharoshthi,
	unicode.Old_South_Arabian, unicode.Old_North_Arabian, unicode.Avestan,
	unicode.Inscriptional_Parthian, unicode.Inscriptional_Pahlavi, unicode.Psalter_Pahlavi,
	unicode.Old_Turkic, unicode.Old_Hungarian, unicode.Manichaean, unicode.Nabataean,
	unicode.Palmyrene, unicode.Hatran, unicode.Lydian, unicode.Meroitic_Cursive,
	unicode.Meroitic_Hieroglyphs, unicode.Cypriot, unicode.Sogdian, unicode.Old_Sogdian,
	unicode.Elymaic, unicode.Chorasmian, unicode.Yezidi, unicode.Old_Uyghur,
}

// arabicNumbers are the code points of Bidi class AN.
var arabicNumbers = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x0600, Hi: 0x0605, Stride: 1}, {Lo: 0x0660, Hi: 0x0669, Stride: 1},
		{Lo: 0x066B, Hi: 0x066C, Stride: 1}, {Lo: 0x06DD, Hi: 0x06DD, Stride: 1},
		{Lo: 0x0890, Hi: 0x0891, Stride: 1}, {Lo: 0x08E2, Hi: 0x08E2, Stride: 1},
	},
	R32: []unicode.Range32{
		{Lo: 0x10D30, Hi: 0x10D39, Stride: 1}, {Lo: 0x10D40, Hi: 0x10D49, Stride: 1},
		{Lo: 0x10E60, Hi: 0x10E7E, Stride: 1},
	},
}

func bidiOf(r rune) bidiClass {
	switch {
	case r >= '0' && r <= '9', r >= 0x06F0 && r <= 0x06F9, r >= 0xFF10 && r <= 0xFF19:
		return bidiEN
	case unicode.Is(arabicNumbers, r):
		return bidiAN
	case unicode.In(r, unicode.Mn, unicode.Me):
		return bidiNSM
	case unicode.In(r, rtlScripts...):
		return bidiR
	case unicode.IsLetter(r), unicode.Is(unicode.Mc, r), unicode.IsDigit(r), r == 0x0F0B, r == 0x3007:
		return bidiL
	}
	return bidiOther
}

// bidiOK applies the Bidi rule to every label when one of them has a
// right-to-left character (R, AL or AN).
func bidiOK(labels [][]rune) bool {
	rtl := false
	for _, l := range labels {
		for _, r := range l {
			if c := bidiOf(r); c == bidiR || c == bidiAN {
				rtl = true
			}
		}
	}
	if !rtl {
		return true
	}
	for _, l := range labels {
		if !bidiLabelOK(l) {
			return false
		}
	}
	return true
}

func bidiLabelOK(l []rune) bool {
	classes := make([]bidiClass, len(l))
	for i, r := range l {
		classes[i] = bidiOf(r)
	}
	// Rule 1: the first character is L, R or AL.
	first := classes[0]
	if first != bidiL && first != bidiR {
		return false
	}
	last := len(classes) - 1
	for last > 0 && classes[last] == bidiNSM {
		last--
	}
	if first == bidiR {
		// Rules 2 to 4: no L; ends with R, AL, EN or AN; not both EN and AN.
		hasEN, hasAN := false, false
		for _, c := range classes {
			switch c {
			case bidiL:
				return false
			case bidiEN:
				hasEN = true
			case bidiAN:
				hasAN = true
			}
		}
		end := classes[last]
		return !(hasEN && hasAN) && (end == bidiR || end == bidiEN || end == bidiAN)
	}
	// Rules 5 and 6: no R, AL or AN; ends with L or EN.
	for _, c := range classes {
		if c == bidiR || c == bidiAN {
			return false
		}
	}
	end := classes[last]
	return end == bidiL || end == bidiEN
}

// ---- Punycode (RFC 3492) ----

const (
	punyBase        = 36
	punyTMin        = 1
	punyTMax        = 26
	punySkew        = 38
	punyDamp        = 700
	punyInitialBias = 72
	punyInitialN    = 128
	punyMaxInt      = 1<<31 - 1
)

var errPunycode = errors.New("invalid punycode")

func punyAdapt(delta, numPoints int, first bool) int {
	if first {
		delta /= punyDamp
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > (punyBase-punyTMin)*punyTMax/2 {
		delta /= punyBase - punyTMin
		k += punyBase
	}
	return k + (punyBase-punyTMin+1)*delta/(delta+punySkew)
}

func punyThreshold(k, bias int) int {
	switch {
	case k <= bias:
		return punyTMin
	case k >= bias+punyTMax:
		return punyTMax
	}
	return k - bias
}

func punyDigit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c-'0') + 26
	case c >= 'a' && c <= 'z':
		return int(c - 'a')
	case c >= 'A' && c <= 'Z':
		return int(c - 'A')
	}
	return -1
}

// punyDecode decodes a Punycode string (without the "xn--" prefix).
func punyDecode(s string) ([]rune, error) {
	var out []rune
	rest := s
	if b := strings.LastIndexByte(s, '-'); b >= 0 {
		for i := 0; i < b; i++ {
			if s[i] >= utf8.RuneSelf {
				return nil, errPunycode
			}
			out = append(out, rune(s[i]))
		}
		rest = s[b+1:]
	}
	n, i, bias := punyInitialN, 0, punyInitialBias
	for pos := 0; pos < len(rest); {
		oldi, w := i, 1
		for k := punyBase; ; k += punyBase {
			if pos >= len(rest) {
				return nil, errPunycode
			}
			digit := punyDigit(rest[pos])
			pos++
			if digit < 0 || digit > (punyMaxInt-i)/w {
				return nil, errPunycode
			}
			i += digit * w
			t := punyThreshold(k, bias)
			if digit < t {
				break
			}
			if w > punyMaxInt/(punyBase-t) {
				return nil, errPunycode
			}
			w *= punyBase - t
		}
		count := len(out) + 1
		bias = punyAdapt(i-oldi, count, oldi == 0)
		if i/count > punyMaxInt-n {
			return nil, errPunycode
		}
		n += i / count
		i %= count
		if n > unicode.MaxRune || n >= 0xD800 && n <= 0xDFFF {
			return nil, errPunycode
		}
		out = slices.Insert(out, i, rune(n))
		i++
	}
	return out, nil
}

// punyEncode encodes code points as Punycode (without the "xn--" prefix).
func punyEncode(u []rune) (string, error) {
	var b strings.Builder
	for _, r := range u {
		if r < utf8.RuneSelf {
			b.WriteRune(r)
		}
	}
	basic := b.Len()
	h := basic
	if basic > 0 {
		b.WriteByte('-')
	}
	n, delta, bias := punyInitialN, 0, punyInitialBias
	for h < len(u) {
		m := rune(unicode.MaxRune + 1)
		for _, r := range u {
			if int(r) >= n && r < m {
				m = r
			}
		}
		if (int(m) - n) > (punyMaxInt-delta)/(h+1) {
			return "", errPunycode
		}
		delta += (int(m) - n) * (h + 1)
		n = int(m)
		for _, r := range u {
			if int(r) < n {
				delta++
				if delta > punyMaxInt {
					return "", errPunycode
				}
			}
			if int(r) != n {
				continue
			}
			q := delta
			for k := punyBase; ; k += punyBase {
				t := punyThreshold(k, bias)
				if q < t {
					break
				}
				b.WriteByte(punyEncodeDigit(t + (q-t)%(punyBase-t)))
				q = (q - t) / (punyBase - t)
			}
			b.WriteByte(punyEncodeDigit(q))
			bias = punyAdapt(delta, h+1, h == basic)
			delta = 0
			h++
		}
		delta++
		n++
	}
	return b.String(), nil
}

func punyEncodeDigit(d int) byte {
	if d < 26 {
		return byte('a' + d)
	}
	return byte('0' + d - 26)
}
