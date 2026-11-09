package template

import (
	"errors"
	mrand "math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// fixedNow is the clock of the dynamic value tests: a Saturday afternoon.
var fixedNow = time.Date(2026, 9, 19, 14, 5, 9, 123_456_789, time.UTC)

// dynResolver returns a resolver with a fixed clock and a seeded random
// source, so every run gives the same values.
func dynResolver(scopes ...Scope) Resolver {
	return Resolver{
		Scopes: scopes,
		Now:    func() time.Time { return fixedNow },
		Rand:   mrand.NewChaCha8([32]byte{1, 2, 3}),
	}
}

// renderOne renders src and fails the test on any problem.
func renderOne(t *testing.T, r Resolver, src string) string {
	t.Helper()
	res := r.Render(src)
	if err := res.Err(); err != nil {
		t.Fatalf("Render(%q).Err() = %v", src, err)
	}
	return res.Text
}

func TestDynamicTimestamps(t *testing.T) {
	t.Parallel()
	unix := func(tm time.Time) string { return strconv.FormatInt(tm.Unix(), 10) }
	ms := func(tm time.Time) string { return strconv.FormatInt(tm.UnixMilli(), 10) }
	tests := []struct {
		src, want string
	}{
		{"{{$timestamp}}", unix(fixedNow)},
		{"{{ $timestamp }}", unix(fixedNow)},
		{"{{$timestamp +1d}}", unix(fixedNow.Add(24 * time.Hour))},
		{"{{$timestamp -2h}}", unix(fixedNow.Add(-2 * time.Hour))},
		{"{{$timestampMs}}", ms(fixedNow)},
		{"{{$timestampMs +500ms}}", ms(fixedNow.Add(500 * time.Millisecond))},
		{"{{$isoTimestamp}}", "2026-09-19T14:05:09.123Z"},
		{"{{$isoTimestamp +1d}}", "2026-09-20T14:05:09.123Z"},
		{"{{$isoTimestamp -1M}}", "2026-08-19T14:05:09.123Z"},
		{"{{$isoTimestamp +1y}}", "2027-09-19T14:05:09.123Z"},
		{"{{$isoTimestamp +1w}}", "2026-09-26T14:05:09.123Z"},
		{"{{$isoTimestamp +30m}}", "2026-09-19T14:35:09.123Z"},
		{"{{$isoTimestamp -10s}}", "2026-09-19T14:04:59.123Z"},
		{"{{$isoTimestamp +1d-2h}}", "2026-09-20T12:05:09.123Z"},
		{"{{$datetime}}", "2026-09-19T14:05:09.123Z"},
		{"{{$datetime iso8601}}", "2026-09-19T14:05:09.123Z"},
		{"{{$datetime +1d}}", "2026-09-20T14:05:09.123Z"},
		{"{{$datetime YYYY-MM-DD}}", "2026-09-19"},
		{"{{$datetime YYYY-MM-DD +1d}}", "2026-09-20"},
		{`{{$datetime "YYYY-MM-DD HH:mm:ss.SSS" -1d}}`, "2026-09-18 14:05:09.123"},
		{`{{$datetime "DD/MM/YY hh:mm A"}}`, "19/09/26 02:05 PM"},
		{`{{$datetime "D.M.YYYY H:m:s h a"}}`, "19.9.2026 14:5:9 2 pm"},
		{`{{$datetime "ddd, DD MMM YYYY"}}`, "Sat, 19 Sep 2026"},
		{`{{$datetime "[Year] YYYY [at] HH"}}`, "Year 2026 at 14"},
		{"{{$datetime Z}}", "+00:00"},
		{"{{$datetime ZZ}}", "+0000"},
		{"{{$datetime X}}", unix(fixedNow)},
		{"{{$datetime x}}", ms(fixedNow)},
		{"{{$localDatetime YYYY-MM-DDTHH:mmZ}}", fixedNow.Local().Format("2006-01-02T15:04-07:00")},
		{"{{$datetime HH +0d}}", "14"},
		{"{{$datetime hh}}", "02"},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			if got := renderOne(t, dynResolver(), tt.src); got != tt.want {
				t.Errorf("Render(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestDynamicMidnightIs12AM(t *testing.T) {
	t.Parallel()
	r := dynResolver()
	r.Now = func() time.Time { return time.Date(2026, 1, 2, 0, 7, 0, 0, time.UTC) }
	if got := renderOne(t, r, `{{$datetime "h:mm a"}}`); got != "12:07 am" {
		t.Errorf("got %q, want 12:07 am", got)
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestDynamicUUID(t *testing.T) {
	t.Parallel()
	r := dynResolver()
	seen := map[string]bool{}
	for _, name := range []string{"$uuid", "$guid", "$randomUUID"} {
		for range 50 {
			v := renderOne(t, r, "{{"+name+"}}")
			if !uuidRe.MatchString(v) {
				t.Fatalf("{{%s}} = %q, not a v4 UUID", name, v)
			}
			if seen[v] {
				t.Fatalf("{{%s}} repeated %q", name, v)
			}
			seen[v] = true
		}
	}

	// Each reference gets its own value.
	res := r.Render("{{$uuid}} {{$uuid}}")
	if len(res.Refs) != 2 || res.Refs[0].Value == res.Refs[1].Value {
		t.Errorf("two references got %+v", res.Refs)
	}
	for _, ref := range res.Refs {
		if !ref.Resolved || ref.Scope != DynamicScope || ref.Secret {
			t.Errorf("ref = %+v, want resolved by %q, not secret", ref, DynamicScope)
		}
	}

	// The version and variant bits are set whatever the random bytes.
	zero := Resolver{Rand: zeroReader{}}
	if got := renderOne(t, zero, "{{$uuid}}"); got != "00000000-0000-4000-8000-000000000000" {
		t.Errorf("zero UUID = %q", got)
	}
}

func TestDynamicRandomInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src    string
		lo, hi int64
		all    bool // every value in the range must show up
	}{
		{"{{$randomInt}}", 0, DefaultRandomIntMax, false},
		{"{{$randomInt 1 6}}", 1, 6, true},
		{"{{$randomInt -5 -3}}", -5, -3, true},
		{"{{$randomInt 7 7}}", 7, 7, true},
		{"{{$randomInt -9223372036854775808 9223372036854775807}}", -1 << 63, 1<<63 - 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			r := dynResolver()
			got := map[int64]bool{}
			for range 500 {
				v := renderOne(t, r, tt.src)
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil || n < tt.lo || n > tt.hi {
					t.Fatalf("%s = %q, want an integer in [%d, %d]", tt.src, v, tt.lo, tt.hi)
				}
				got[n] = true
			}
			if tt.all && int64(len(got)) != tt.hi-tt.lo+1 {
				t.Errorf("%s gave %d distinct values, want %d", tt.src, len(got), tt.hi-tt.lo+1)
			}
			if !tt.all && len(got) < 100 {
				t.Errorf("%s gave only %d distinct values in 500 draws", tt.src, len(got))
			}
		})
	}
}

func TestDynamicRandomStrings(t *testing.T) {
	t.Parallel()
	alnum := regexp.MustCompile(`^[A-Za-z0-9]*$`)
	hexRe := regexp.MustCompile(`^[0-9a-f]*$`)
	tests := []struct {
		src  string
		re   *regexp.Regexp
		size int
	}{
		{"{{$randomString}}", alnum, DefaultRandomLength},
		{"{{$randomString 1}}", alnum, 1},
		{"{{$randomString 100}}", alnum, 100},
		{"{{$randomString 4096}}", alnum, MaxRandomLength},
		{"{{$randomHex}}", hexRe, DefaultRandomLength},
		{"{{$randomHex 7}}", hexRe, 7},
		{"{{$randomHex 64}}", hexRe, 64},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			r := dynResolver()
			a, b := renderOne(t, r, tt.src), renderOne(t, r, tt.src)
			for _, v := range []string{a, b} {
				if len(v) != tt.size || !tt.re.MatchString(v) {
					t.Fatalf("%s = %q, want %d characters matching %s", tt.src, v, tt.size, tt.re)
				}
			}
			if tt.size >= 16 && a == b {
				t.Errorf("%s repeated %q", tt.src, a)
			}
		})
	}

	// Every alphanumeric character is reachable.
	chars := renderOne(t, dynResolver(), "{{$randomString 4096}}")
	for _, c := range alphanumeric {
		if !strings.ContainsRune(chars, c) {
			t.Errorf("$randomString never produced %q", c)
		}
	}
}

func TestDynamicRandomBoolean(t *testing.T) {
	t.Parallel()
	r := dynResolver()
	seen := map[string]int{}
	for range 200 {
		seen[renderOne(t, r, "{{$randomBoolean}}")]++
	}
	if len(seen) != 2 || seen["true"] == 0 || seen["false"] == 0 {
		t.Errorf("$randomBoolean gave %v, want both true and false", seen)
	}
}

func TestDynamicDeterministic(t *testing.T) {
	t.Parallel()
	src := "{{$uuid}} {{$randomInt 1 100}} {{$randomString}} {{$randomHex 8}} {{$randomBoolean}} {{$timestamp}}"
	a := renderOne(t, dynResolver(), src)
	b := renderOne(t, dynResolver(), src)
	if a != b {
		t.Errorf("the same clock and seed gave %q and %q", a, b)
	}
}

func TestDynamicInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src, msg string
	}{
		{"{{$uuid x}}", "it takes no arguments"},
		{"{{$randomBoolean 1}}", "it takes no arguments"},
		{"{{$randomInt 5 1}}", "the minimum is greater than the maximum"},
		{"{{$randomInt a 1}}", `the minimum "a" is not a whole number`},
		{"{{$randomInt 1 b}}", `the maximum "b" is not a whole number`},
		{"{{$randomInt 1}}", "it takes no arguments or a minimum and a maximum"},
		{"{{$randomInt 1 2 3}}", "it takes no arguments or a minimum and a maximum"},
		{"{{$randomInt 1 99999999999999999999}}", `the maximum "99999999999999999999" is not a whole number`},
		{"{{$randomString 0}}", "the length must be a whole number from 1 to 4096"},
		{"{{$randomString 4097}}", "the length must be a whole number from 1 to 4096"},
		{"{{$randomHex x}}", "the length must be a whole number from 1 to 4096"},
		{"{{$randomHex 1 2}}", "it takes at most a length"},
		{"{{$timestamp 1d}}", `"1d" is not an offset such as +1d or -2h`},
		{"{{$timestamp +d}}", `"+d" is not an offset such as +1d or -2h`},
		{"{{$timestamp +1q}}", `"+1q" is not an offset such as +1d or -2h`},
		{"{{$timestamp +1}}", `"+1" is not an offset such as +1d or -2h`},
		{"{{$timestamp +1234567d}}", `"+1234567d" is not an offset such as +1d or -2h`},
		{"{{$timestamp +1d +1h}}", "it takes at most an offset such as +1d"},
		{"{{$isoTimestamp now}}", `"now" is not an offset such as +1d or -2h`},
		{"{{$datetime YYYY x}}", `"x" is not an offset such as +1d or -2h`},
		{"{{$datetime [YYYY}}", "the format has a [ without a ]"},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			res := dynResolver().Render(tt.src)
			if res.Text != tt.src {
				t.Errorf("Text = %q, want the reference left as written", res.Text)
			}
			if len(res.Problems) != 1 {
				t.Fatalf("Problems = %+v, want one", res.Problems)
			}
			p := res.Problems[0]
			name := strings.TrimSuffix(strings.TrimPrefix(tt.src, "{{"), "}}")
			if p.Kind != ProblemInvalidDynamic || p.Message != tt.msg || p.Name != name || p.Start != 0 || p.End != len(tt.src) {
				t.Errorf("problem = %+v, want %s %q for %q", p, ProblemInvalidDynamic, tt.msg, name)
			}
			err := res.Err()
			var e *errs.Error
			if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidDynamic {
				t.Fatalf("Err() = %#v, want %s", err, CodeInvalidDynamic)
			}
			if want := "The dynamic value " + tt.src + " is not valid: " + tt.msg + "."; e.Message != want {
				t.Errorf("message = %q, want %q", e.Message, want)
			}
			if IsDynamic(name) {
				t.Errorf("IsDynamic(%q) = true for invalid arguments", name)
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken") }

func TestDynamicRandomSourceFails(t *testing.T) {
	t.Parallel()
	r := Resolver{Rand: failingReader{}}
	for _, src := range []string{"{{$uuid}}", "{{$randomInt}}", "{{$randomString}}", "{{$randomHex}}", "{{$randomBoolean}}"} {
		res := r.Render(src)
		if len(res.Problems) != 1 || res.Problems[0].Kind != ProblemInvalidDynamic || res.Problems[0].Message != "no random data is available" {
			t.Errorf("Render(%q) problems = %+v", src, res.Problems)
		}
	}
}

func TestDynamicWithScopes(t *testing.T) {
	t.Parallel()
	r := dynResolver(
		Vars("Environment: dev", map[string]string{
			"$timestamp": "fixed",
			"id":         "{{$randomInt 3 3}}",
		}),
	)
	res := r.Render("{{$timestamp}}/{{id}}/{{$nope}}")
	if res.Text != "fixed/3/{{$nope}}" {
		t.Errorf("Text = %q", res.Text)
	}
	if res.Refs[0].Scope != "Environment: dev" {
		t.Errorf("a scope that defines $timestamp must win, got %+v", res.Refs[0])
	}
	if n := res.Refs[1].Nested; len(n) != 1 || n[0].Scope != DynamicScope || n[0].Value != "3" {
		t.Errorf("nested dynamic ref = %+v", n)
	}
	if len(res.Problems) != 1 || res.Problems[0].Kind != ProblemUnresolved || res.Problems[0].Name != "$nope" {
		t.Errorf("an unknown $name must be unresolved, got %+v", res.Problems)
	}
}

func TestIsDynamic(t *testing.T) {
	t.Parallel()
	valid := []string{
		"$uuid", "$guid", "$randomUUID", "$timestamp", "$timestamp +1d", "$timestampMs",
		"$isoTimestamp -1w", "$datetime", `$datetime "YYYY-MM-DD HH:mm" +1d-1h`,
		"$localDatetime YYYY", "$randomInt", "$randomInt -1 1", "$randomString 8",
		"$randomHex", "$randomBoolean", "$randomInt  1\t6",
	}
	for _, n := range valid {
		if !IsDynamic(n) {
			t.Errorf("IsDynamic(%q) = false", n)
		}
	}
	invalid := []string{
		"", "$", "uuid", "$UUID", "$nope", "$uuid x", "$randomInt 5 1", `$datetime "YYYY`,
		`$datetime "YYYY"x`, "host", " $uuid",
	}
	for _, n := range invalid {
		if IsDynamic(n) {
			t.Errorf("IsDynamic(%q) = true", n)
		}
	}

	if DynamicDescription("host") != "" {
		t.Error("DynamicDescription(host) is not empty")
	}
	names := DynamicNames()
	if !slices.IsSorted(names) || !slices.Contains(names, "$uuid") || len(names) != len(dynamicFuncs) {
		t.Errorf("DynamicNames() = %v", names)
	}
	for _, n := range names {
		if !IsDynamic(n) && n != FileName && n != ResponseName { // {{$file}} and {{$response}} need arguments
			t.Errorf("IsDynamic(%q) = false for a listed name without arguments", n)
		}
		if DynamicDescription(n) == "" {
			t.Errorf("DynamicDescription(%q) is empty", n)
		}
	}
}
