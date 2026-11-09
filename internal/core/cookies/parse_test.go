package cookies

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseSetCookie(t *testing.T) {
	t.Parallel()
	date := time.Date(2021, time.June, 9, 10, 18, 14, 0, time.UTC)
	tests := []struct {
		line   string
		want   setCookie
		reason Reason
	}{
		{line: "SID=31d4d96e407aad42", want: setCookie{name: "SID", value: "31d4d96e407aad42"}},
		{line: " SID = 31d4d96e407aad42 ;", want: setCookie{name: "SID", value: "31d4d96e407aad42"}},
		{line: "a=b=c", want: setCookie{name: "a", value: "b=c"}},
		{line: `q="quoted value"`, want: setCookie{name: "q", value: `"quoted value"`}},
		{line: "a=", want: setCookie{name: "a"}},
		// rfc6265bis: no "=" is a cookie with an empty name.
		{line: "justvalue; Path=/", want: setCookie{value: "justvalue", path: "/", hasPath: true}},
		{line: "=v", want: setCookie{value: "v"}},
		{
			line: "SID=1; path=/p; DOMAIN=.Example.COM; secure; HttpOnly; SameSite=lax; Max-Age=60; expires=Wed, 09 Jun 2021 10:18:14 GMT; Unknown=x",
			want: setCookie{
				name: "SID", value: "1", path: "/p", hasPath: true, domain: "example.com", hasDomain: true,
				secure: true, httpOnly: true, sameSite: SameSiteLax, maxAge: 60, hasMaxAge: true,
				expires: date, hasExpires: true,
			},
		},
		// Invalid attribute values are ignored; the last valid one wins.
		{line: "a=1; Max-Age=60; Max-Age=6x0; Max-Age=; Max-Age=+5", want: setCookie{name: "a", value: "1", maxAge: 60, hasMaxAge: true}},
		{line: "a=1; Max-Age=-5", want: setCookie{name: "a", value: "1", maxAge: -5, hasMaxAge: true}},
		{line: "a=1; Max-Age=99999999999999999999999", want: setCookie{name: "a", value: "1", maxAge: math.MaxInt64, hasMaxAge: true}},
		{line: "a=1; Expires=soon", want: setCookie{name: "a", value: "1"}},
		{line: "a=1; Domain=example.com; Domain=", want: setCookie{name: "a", value: "1", domain: "example.com", hasDomain: true}},
		// An empty or relative Path means the default path, overriding
		// earlier Path attributes.
		{line: "a=1; Path=/x; Path=relative", want: setCookie{name: "a", value: "1", hasPath: true}},
		{line: "a=1; Path", want: setCookie{name: "a", value: "1", hasPath: true}},
		{line: "a=1; SameSite=None", want: setCookie{name: "a", value: "1", sameSite: SameSiteNone}},
		{line: "a=1; SameSite=STRICT", want: setCookie{name: "a", value: "1", sameSite: SameSiteStrict}},
		{line: "a=1; SameSite=Strict; SameSite=bogus", want: setCookie{name: "a", value: "1", sameSite: SameSiteDefault}},
		{line: "a=1; Secure=yes; HTTPONLY=", want: setCookie{name: "a", value: "1", secure: true, httpOnly: true}},
		{line: "a=1; Path=/" + strings.Repeat("x", MaxAttributeValueBytes), want: setCookie{name: "a", value: "1"}},
		{line: "a=b\tc", want: setCookie{name: "a", value: "b\tc"}},

		{line: "", reason: ReasonInvalid},
		{line: "=", reason: ReasonInvalid},
		{line: " ; Path=/", reason: ReasonInvalid},
		{line: "a=b\x00c", reason: ReasonInvalid},
		{line: "a=b\x7f", reason: ReasonInvalid},
		{line: "a=" + strings.Repeat("v", MaxNameValueBytes), reason: ReasonTooLarge},
	}
	for _, tt := range tests {
		got, reason, ok := parseSetCookie(tt.line)
		if tt.reason != "" {
			if ok || reason != tt.reason {
				t.Errorf("parseSetCookie(%.60q) = ok %v, reason %q; want reason %q", tt.line, ok, reason, tt.reason)
			}
			continue
		}
		if !ok {
			t.Errorf("parseSetCookie(%.60q) failed: %q", tt.line, reason)
			continue
		}
		if got != tt.want {
			t.Errorf("parseSetCookie(%.60q) =\n %+v\nwant\n %+v", tt.line, got, tt.want)
		}
	}
	// Exactly at the limit is accepted.
	if _, _, ok := parseSetCookie("a=" + strings.Repeat("v", MaxNameValueBytes-1)); !ok {
		t.Error("cookie of MaxNameValueBytes rejected")
	}
}

func TestParseCookieDate(t *testing.T) {
	t.Parallel()
	d := func(y int, mo time.Month, day, h, m, s int) time.Time {
		return time.Date(y, mo, day, h, m, s, 0, time.UTC)
	}
	valid := []struct {
		in   string
		want time.Time
	}{
		{"Wed, 09 Jun 2021 10:18:14 GMT", d(2021, 6, 9, 10, 18, 14)},             // RFC 1123
		{"Sun, 06 Nov 1994 08:49:37 GMT", d(1994, 11, 6, 8, 49, 37)},             // RFC 6265 §3.1
		{"Sunday, 06-Nov-94 08:49:37 GMT", d(1994, 11, 6, 8, 49, 37)},            // RFC 850
		{"Sun Nov  6 08:49:37 1994", d(1994, 11, 6, 8, 49, 37)},                  // asctime
		{"Thu, 01-Jan-1970 00:00:01 GMT", d(1970, 1, 1, 0, 0, 1)},                // common
		{"wed, 9 JUNE 21 1:2:3 +0200", d(2021, 6, 9, 1, 2, 3)},                   // zone ignored
		{"09 Jun 2021 10:18:14", d(2021, 6, 9, 10, 18, 14)},                      // no weekday
		{"2021 Jun 09 10:18:14", d(2021, 6, 9, 10, 18, 14)},                      // year first: 2021 is not a day
		{"Jun 09 69 10:18:14", d(2069, 6, 9, 10, 18, 14)},                        // 00-69 → 20xx
		{"Jun 09 70 10:18:14", d(1970, 6, 9, 10, 18, 14)},                        // 70-99 → 19xx
		{"10:18:14xyz 09th Junk 2021GMT", d(2021, 6, 9, 10, 18, 14)},             // trailing non-digits
		{"Fri, 31 Dec 9999 23:59:59 GMT", d(9999, 12, 31, 23, 59, 59)},           // far future
		{"Mon, 01 Jan 1601 00:00:00 GMT", d(1601, 1, 1, 0, 0, 0)},                // earliest
		{"Tue, 29 Feb 2000 12:00:00 GMT", d(2000, 2, 29, 12, 0, 0)},              // leap day
		{"\t09;Jun,2021@10:18:14[]{}", d(2021, 6, 9, 10, 18, 14)},                // delimiters
		{"Wed, 09 Jun 2021 10:18:14 GMT 12:00:00 01", d(2021, 6, 9, 10, 18, 14)}, // first of each wins
	}
	for _, tt := range valid {
		got, ok := parseCookieDate(tt.in)
		if !ok || !got.Equal(tt.want) {
			t.Errorf("parseCookieDate(%q) = %v, %v; want %v", tt.in, got, ok, tt.want)
		}
	}
	invalid := []string{
		"",
		"soon",
		"Wed, 09 Jun 2021",           // no time
		"09 2021 10:18:14",           // no month
		"Jun 2021 10:18:14",          // no day (2021 has 4 digits)
		"09 Jun 10:18:14",            // no year
		"Wed, 32 Jun 2021 10:18:14",  // day out of range
		"Wed, 00 Jun 2021 10:18:14",  // day 0
		"Wed, 31 Jun 2021 10:18:14",  // no 31 June
		"Tue, 29 Feb 2021 12:00:00",  // not a leap year
		"Wed, 09 Jun 1600 10:18:14",  // before 1601
		"Wed, 09 Jun 2021 24:00:00",  // hour
		"Wed, 09 Jun 2021 10:60:00",  // minute
		"Wed, 09 Jun 2021 10:00:60",  // second
		"Wed, 09 Jun 2021 100:00:00", // three-digit hour is not a time
		"Wed, 09 Ju 2021 10:18:14",   // short month
		"Wed, 09 Jun 20210 10:18:14", // five-digit year
		"Wed, 09 Jun 2021 10:18",     // no seconds
	}
	for _, in := range invalid {
		if got, ok := parseCookieDate(in); ok {
			t.Errorf("parseCookieDate(%q) = %v, want failure", in, got)
		}
	}
}

func FuzzParseSetCookie(f *testing.F) {
	for _, s := range []string{
		"SID=31d4d96e407aad42; Path=/; Domain=example.com; Secure; HttpOnly",
		"lang=en-US; Expires=Wed, 09 Jun 2021 10:18:14 GMT",
		"a; Max-Age=-1; SameSite=None",
		"__Host-x=1; Path=/; Secure",
		"=; ;;; Domain=.; Path",
		"a=\x00",
	} {
		f.Add(s, "https://www.example.com/docs/page")
	}
	f.Fuzz(func(t *testing.T, line, rawURL string) {
		c, _, ok := parseSetCookie(line)
		if ok {
			if len(c.name)+len(c.value) > MaxNameValueBytes || c.name == "" && c.value == "" {
				t.Fatalf("accepted %q: %+v", line, c)
			}
			if c.hasPath && c.path != "" && c.path[0] != '/' {
				t.Fatalf("relative path kept: %q", c.path)
			}
		}
		parseCookieDate(line)
		j := New(Options{Now: newClock().Now})
		u := mustURL(t, "https://www.example.com/docs/page")
		if parsed, err := parseURLForFuzz(rawURL); err == nil {
			u = parsed
		}
		j.SetCookieLines(u, []string{line})
		h := j.Header(u)
		for i := 0; i < len(h); i++ {
			if b := h[i]; b < 0x20 && b != '\t' || b == 0x7f {
				t.Fatalf("control character in Cookie header %q", h)
			}
		}
		for _, sc := range j.All() {
			if sc.Path == "" || sc.Path[0] != '/' || sc.Domain == "" {
				t.Fatalf("stored invalid cookie %+v for %q", sc, rawURL)
			}
		}
	})
}

func TestParseSetCookieExported(t *testing.T) {
	t.Parallel()
	c, reason, ok := ParseSetCookie("SID=1; Domain=.Example.com; Path=/p; Max-Age=60; Expires=Wed, 09 Jun 2021 10:18:14 GMT; Secure; HttpOnly; SameSite=Lax")
	if !ok || reason != "" {
		t.Fatalf("ParseSetCookie failed: %q", reason)
	}
	want := SetCookie{
		Name: "SID", Value: "1", Domain: "example.com", Path: "/p", MaxAge: 60, HasMaxAge: true,
		Expires: time.Date(2021, time.June, 9, 10, 18, 14, 0, time.UTC), HasExpires: true,
		Secure: true, HTTPOnly: true, SameSite: SameSiteLax,
	}
	if c.Name != want.Name || c.Value != want.Value || c.Domain != want.Domain || c.Path != want.Path ||
		c.MaxAge != want.MaxAge || !c.HasMaxAge || !c.Expires.Equal(want.Expires) || !c.HasExpires ||
		!c.Secure || !c.HTTPOnly || c.SameSite != want.SameSite || len(c.Notes) != 0 {
		t.Errorf("ParseSetCookie = %+v, want %+v", c, want)
	}

	c, _, ok = ParseSetCookie("a=1; Expires=soon; Max-Age=x; Domain=; Path=rel; SameSite=bogus; Path=/" + strings.Repeat("x", MaxAttributeValueBytes))
	if !ok {
		t.Fatal("ParseSetCookie failed")
	}
	wantNotes := []string{
		"Expires=soon is not a date and was ignored.",
		"Max-Age=x is not a number and was ignored.",
		"An empty Domain was ignored.",
		"Path=rel does not start with /, so the default path applies.",
		"SameSite=bogus is not Strict, Lax or None, so the default applies.",
		"Path is longer than 1024 bytes and was ignored.",
	}
	if strings.Join(c.Notes, "|") != strings.Join(wantNotes, "|") {
		t.Errorf("Notes = %q, want %q", c.Notes, wantNotes)
	}

	c, reason, ok = ParseSetCookie("big=" + strings.Repeat("v", MaxNameValueBytes))
	if ok || reason != ReasonTooLarge || c.Name != "big" {
		t.Errorf("too large: ok %v, reason %q, name %q", ok, reason, c.Name)
	}
}
