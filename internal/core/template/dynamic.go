package template

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DynamicScope is the scope name of references resolved by a dynamic value,
// shown in hover and Ref.Scope.
const DynamicScope = "Dynamic"

// Limits of dynamic value arguments.
const (
	// DefaultRandomLength is the length of $randomString and $randomHex
	// without an argument.
	DefaultRandomLength = 16
	// MaxRandomLength bounds the length argument of $randomString and
	// $randomHex.
	MaxRandomLength = 4096
	// DefaultRandomIntMax is the upper bound of $randomInt without
	// arguments (the range is 0 to 1000, inclusive).
	DefaultRandomIntMax = 1000
)

// dynamicFunc generates the value of one dynamic reference from its
// arguments. It is called once per reference.
type dynamicFunc func(g *dynamicGen, args []string) (string, error)

// dynamicFuncs are the dynamic values, by name (with the "$").
var dynamicFuncs = map[string]dynamicFunc{
	"$uuid":          dynUUID,
	"$guid":          dynUUID,
	"$randomUUID":    dynUUID,
	"$timestamp":     dynTimestamp,
	"$timestampMs":   dynTimestampMs,
	"$isoTimestamp":  dynISOTimestamp,
	"$datetime":      dynDatetime(false),
	"$localDatetime": dynDatetime(true),
	"$randomInt":     dynRandomInt,
	"$randomString":  dynRandomString,
	"$randomHex":     dynRandomHex,
	"$randomBoolean": dynRandomBoolean,
}

// DynamicNames returns the names of the dynamic values, sorted, for
// completion lists. Aliases ($guid, $randomUUID) are included.
func DynamicNames() []string {
	names := make([]string, 0, len(dynamicFuncs))
	for n := range dynamicFuncs {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// dynamicDescriptions say what each dynamic value generates, for hover.
var dynamicDescriptions = map[string]string{
	"$uuid":          "A random UUID (v4), generated at send time",
	"$guid":          "A random UUID (v4), generated at send time",
	"$randomUUID":    "A random UUID (v4), generated at send time",
	"$timestamp":     "Unix time in seconds, at send time (offset: +1d)",
	"$timestampMs":   "Unix time in milliseconds, at send time (offset: +1d)",
	"$isoTimestamp":  "ISO 8601 time in UTC, at send time (offset: +1d)",
	"$datetime":      "UTC time at send time, in a format (YYYY-MM-DD) with an offset (+1d)",
	"$localDatetime": "Local time at send time, in a format (YYYY-MM-DD) with an offset (+1d)",
	"$randomInt":     "A random integer, 0 to 1000 or MIN MAX, generated at send time",
	"$randomString":  "Random letters and digits (length 16 or N), generated at send time",
	"$randomHex":     "Random hex digits (length 16 or N), generated at send time",
	"$randomBoolean": "true or false at random, generated at send time",
}

// DynamicDescription says in a sentence what the dynamic value name (as
// listed by DynamicNames, without arguments) generates; "" for any other
// name.
func DynamicDescription(name string) string { return dynamicDescriptions[name] }

// IsDynamic reports whether name, as written between the braces (without
// the surrounding whitespace, as in Segment.Name), is a dynamic value with
// valid arguments: {{$uuid}}, {{$randomInt 1 6}}, {{$datetime YYYY-MM-DD +1d}}.
// A Resolver always resolves such a reference unless a scope defines the
// same name, so the editor can treat it as resolved without a scope. It
// generates no value.
func IsDynamic(name string) bool {
	if _, ok, err := ParsePrompt(name); ok {
		return err == nil
	}
	if _, ok, err := ParseFile(name); ok {
		return err == nil
	}
	if _, ok, err := ParseResponse(name); ok {
		return err == nil
	}
	fn, args, ok := splitDynamic(name)
	if !ok {
		return false
	}
	g := &dynamicGen{now: func() time.Time { return time.Unix(0, 0) }, rand: zeroReader{}}
	_, err := fn(g, args)
	return err == nil
}

func splitDynamic(name string) (dynamicFunc, []string, bool) {
	if !strings.HasPrefix(name, "$") {
		return nil, nil, false
	}
	fields, ok := splitArgs(name)
	if !ok || len(fields) == 0 {
		return nil, nil, false
	}
	fn, ok := dynamicFuncs[fields[0]]
	if !ok {
		return nil, nil, false
	}
	return fn, fields[1:], true
}

// splitArgs splits on whitespace; a double-quoted argument may contain
// spaces ("YYYY-MM-DD HH:mm"). An unclosed quote is not a valid call.
func splitArgs(s string) ([]string, bool) {
	toks, ok := splitTokens(s)
	if !ok {
		return nil, false
	}
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.text
	}
	return out, true
}

// dynamicGen holds the clock and the random source of one Render.
type dynamicGen struct {
	now  func() time.Time
	rand io.Reader
}

func newDynamicGen(now func() time.Time, r io.Reader) *dynamicGen {
	if now == nil {
		now = time.Now
	}
	if r == nil {
		r = rand.Reader
	}
	return &dynamicGen{now: now, rand: r}
}

// value returns the value of a dynamic reference: ok is false when name is
// not a dynamic value; err describes invalid arguments.
func (g *dynamicGen) value(name string) (value string, ok bool, err error) {
	fn, args, ok := splitDynamic(name)
	if !ok {
		return "", false, nil
	}
	v, err := fn(g, args)
	return v, true, err
}

func (g *dynamicGen) bytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(g.rand, b); err != nil {
		return nil, errors.New("no random data is available")
	}
	return b, nil
}

// intn returns a uniform value in [0, n).
func (g *dynamicGen) intn(n *big.Int) (*big.Int, error) {
	v, err := rand.Int(g.rand, n)
	if err != nil {
		return nil, errors.New("no random data is available")
	}
	return v, nil
}

func noArgs(args []string) error {
	if len(args) > 0 {
		return errors.New("it takes no arguments")
	}
	return nil
}

func dynUUID(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	b, err := g.bytes(16)
	if err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 9562 variant
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// offsetTime is the current time moved by the optional offset argument.
func (g *dynamicGen) offsetTime(args []string) (time.Time, error) {
	t := g.now()
	switch len(args) {
	case 0:
		return t, nil
	case 1:
		return applyOffset(t, args[0])
	}
	return time.Time{}, errors.New("it takes at most an offset such as +1d")
}

func dynTimestamp(g *dynamicGen, args []string) (string, error) {
	t, err := g.offsetTime(args)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(t.Unix(), 10), nil
}

func dynTimestampMs(g *dynamicGen, args []string) (string, error) {
	t, err := g.offsetTime(args)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(t.UnixMilli(), 10), nil
}

const isoLayout = "2006-01-02T15:04:05.000Z07:00"

func dynISOTimestamp(g *dynamicGen, args []string) (string, error) {
	t, err := g.offsetTime(args)
	if err != nil {
		return "", err
	}
	return t.UTC().Format(isoLayout), nil
}

// dynDatetime is $datetime (UTC) and $localDatetime: an optional format,
// then an optional offset. Without a format it is ISO 8601.
func dynDatetime(local bool) dynamicFunc {
	return func(g *dynamicGen, args []string) (string, error) {
		format := ""
		if len(args) > 0 && !isOffset(args[0]) {
			format, args = args[0], args[1:]
		}
		t, err := g.offsetTime(args)
		if err != nil {
			return "", err
		}
		if local {
			t = t.Local()
		} else {
			t = t.UTC()
		}
		if format == "" || format == "iso8601" {
			return t.Format(isoLayout), nil
		}
		return formatTime(t, format)
	}
}

func dynRandomInt(g *dynamicGen, args []string) (string, error) {
	lo, hi := int64(0), int64(DefaultRandomIntMax)
	switch len(args) {
	case 0:
	case 2:
		var err error
		if lo, err = strconv.ParseInt(args[0], 10, 64); err != nil {
			return "", fmt.Errorf("the minimum %q is not a whole number", args[0])
		}
		if hi, err = strconv.ParseInt(args[1], 10, 64); err != nil {
			return "", fmt.Errorf("the maximum %q is not a whole number", args[1])
		}
		if lo > hi {
			return "", errors.New("the minimum is greater than the maximum")
		}
	default:
		return "", errors.New("it takes no arguments or a minimum and a maximum")
	}
	span := new(big.Int).Sub(big.NewInt(hi), big.NewInt(lo))
	span.Add(span, big.NewInt(1))
	v, err := g.intn(span)
	if err != nil {
		return "", err
	}
	return v.Add(v, big.NewInt(lo)).String(), nil
}

func lengthArg(args []string) (int, error) {
	switch len(args) {
	case 0:
		return DefaultRandomLength, nil
	case 1:
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > MaxRandomLength {
			return 0, fmt.Errorf("the length must be a whole number from 1 to %d", MaxRandomLength)
		}
		return n, nil
	}
	return 0, errors.New("it takes at most a length")
}

const alphanumeric = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func dynRandomString(g *dynamicGen, args []string) (string, error) {
	n, err := lengthArg(args)
	if err != nil {
		return "", err
	}
	out := make([]byte, n)
	// Rejection sampling keeps every character equally likely: 248 is the
	// largest multiple of 62 that fits a byte.
	const limit = 256 - 256%len(alphanumeric)
	buf := make([]byte, n+n/4+8)
	for i := 0; i < n; {
		if _, err := io.ReadFull(g.rand, buf); err != nil {
			return "", errors.New("no random data is available")
		}
		for _, c := range buf {
			if int(c) >= limit {
				continue
			}
			out[i] = alphanumeric[int(c)%len(alphanumeric)]
			if i++; i == n {
				break
			}
		}
	}
	return string(out), nil
}

func dynRandomHex(g *dynamicGen, args []string) (string, error) {
	n, err := lengthArg(args)
	if err != nil {
		return "", err
	}
	b, err := g.bytes((n + 1) / 2)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:n], nil
}

func dynRandomBoolean(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	b, err := g.bytes(1)
	if err != nil {
		return "", err
	}
	return strconv.FormatBool(b[0]&1 == 1), nil
}

// zeroReader is an endless source of zero bytes, for validating arguments
// without using real randomness.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// Offsets: one or more signed terms, "+1d", "-2h", "+1d-30m".

var offsetUnits = []string{"ms", "y", "M", "w", "d", "h", "m", "s"}

// maxOffsetDigits keeps every term far from overflowing a time.Duration.
const maxOffsetDigits = 6

func isOffset(s string) bool {
	_, err := applyOffset(time.Unix(0, 0).UTC(), s)
	return err == nil
}

// applyOffset moves t by an offset such as "+1d" or "-1M+2h". Units: y
// (years), M (months), w, d, h, m (minutes), s, ms. Years and months are
// calendar steps (time.AddDate), the others fixed durations.
func applyOffset(t time.Time, s string) (time.Time, error) {
	bad := fmt.Errorf("%q is not an offset such as +1d or -2h", s)
	if s == "" {
		return time.Time{}, bad
	}
	for s != "" {
		sign := 1
		switch s[0] {
		case '+':
		case '-':
			sign = -1
		default:
			return time.Time{}, bad
		}
		s = s[1:]
		d := 0
		for d < len(s) && s[d] >= '0' && s[d] <= '9' {
			d++
		}
		if d == 0 || d > maxOffsetDigits {
			return time.Time{}, bad
		}
		n, _ := strconv.Atoi(s[:d])
		n *= sign
		s = s[d:]
		unit := ""
		for _, u := range offsetUnits {
			if strings.HasPrefix(s, u) {
				unit = u
				break
			}
		}
		s = s[len(unit):]
		switch unit {
		case "y":
			t = t.AddDate(n, 0, 0)
		case "M":
			t = t.AddDate(0, n, 0)
		case "w":
			t = t.Add(time.Duration(n) * 7 * 24 * time.Hour)
		case "d":
			t = t.Add(time.Duration(n) * 24 * time.Hour)
		case "h":
			t = t.Add(time.Duration(n) * time.Hour)
		case "m":
			t = t.Add(time.Duration(n) * time.Minute)
		case "s":
			t = t.Add(time.Duration(n) * time.Second)
		case "ms":
			t = t.Add(time.Duration(n) * time.Millisecond)
		default:
			return time.Time{}, bad
		}
	}
	return t, nil
}

// Custom formats use tokens (longest match first); text in [brackets] is
// literal, and any other character is copied as is.
var timeTokens = []struct {
	tok string
	fn  func(t time.Time) string
}{
	{"YYYY", func(t time.Time) string { return fmt.Sprintf("%04d", t.Year()) }},
	{"SSS", func(t time.Time) string { return fmt.Sprintf("%03d", t.Nanosecond()/1e6) }},
	{"MMM", func(t time.Time) string { return t.Format("Jan") }},
	{"ddd", func(t time.Time) string { return t.Format("Mon") }},
	{"YY", func(t time.Time) string { return fmt.Sprintf("%02d", t.Year()%100) }},
	{"MM", func(t time.Time) string { return fmt.Sprintf("%02d", int(t.Month())) }},
	{"DD", func(t time.Time) string { return fmt.Sprintf("%02d", t.Day()) }},
	{"HH", func(t time.Time) string { return fmt.Sprintf("%02d", t.Hour()) }},
	{"hh", func(t time.Time) string { return fmt.Sprintf("%02d", hour12(t)) }},
	{"mm", func(t time.Time) string { return fmt.Sprintf("%02d", t.Minute()) }},
	{"ss", func(t time.Time) string { return fmt.Sprintf("%02d", t.Second()) }},
	{"ZZ", func(t time.Time) string { return t.Format("-0700") }},
	{"M", func(t time.Time) string { return strconv.Itoa(int(t.Month())) }},
	{"D", func(t time.Time) string { return strconv.Itoa(t.Day()) }},
	{"H", func(t time.Time) string { return strconv.Itoa(t.Hour()) }},
	{"h", func(t time.Time) string { return strconv.Itoa(hour12(t)) }},
	{"m", func(t time.Time) string { return strconv.Itoa(t.Minute()) }},
	{"s", func(t time.Time) string { return strconv.Itoa(t.Second()) }},
	{"A", func(t time.Time) string { return t.Format("PM") }},
	{"a", func(t time.Time) string { return t.Format("pm") }},
	{"Z", func(t time.Time) string { return t.Format("-07:00") }},
	{"X", func(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }},
	{"x", func(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }},
}

func hour12(t time.Time) int {
	h := t.Hour() % 12
	if h == 0 {
		return 12
	}
	return h
}

func formatTime(t time.Time, format string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(format); {
		if format[i] == '[' {
			end := strings.IndexByte(format[i:], ']')
			if end < 0 {
				return "", errors.New("the format has a [ without a ]")
			}
			b.WriteString(format[i+1 : i+end])
			i += end + 1
			continue
		}
		matched := false
		for _, tk := range timeTokens {
			if strings.HasPrefix(format[i:], tk.tok) {
				b.WriteString(tk.fn(t))
				i += len(tk.tok)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(format[i])
			i++
		}
	}
	return b.String(), nil
}
