package cookies

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDomainMatch(t *testing.T) {
	t.Parallel()
	// RFC 6265 §5.1.3.
	tests := []struct {
		s, d string
		want bool
	}{
		{"example.com", "example.com", true},
		{"www.example.com", "example.com", true},
		{"a.b.example.com", "example.com", true},
		{"example.com", "www.example.com", false},
		{"notexample.com", "example.com", false},
		{"example.com.au", "example.com", false},
		{"127.0.0.1", "127.0.0.1", true},
		{"1.127.0.0.1", "0.0.1", true}, // a host name, not an IP address
		{"10.0.0.1", "0.0.1", false},   // an IP address matches only itself
		{"::1", "::1", true},
		{"localhost", "localhost", true},
		{"api.localhost", "localhost", true},
	}
	for _, tt := range tests {
		if got := domainMatch(tt.s, tt.d); got != tt.want {
			t.Errorf("domainMatch(%q, %q) = %v, want %v", tt.s, tt.d, got, tt.want)
		}
	}
}

func TestPathMatch(t *testing.T) {
	t.Parallel()
	// RFC 6265 §5.1.4.
	defaults := map[string]string{
		"":           "/",
		"relative":   "/",
		"/":          "/",
		"/page":      "/",
		"/docs/":     "/docs",
		"/docs/page": "/docs",
		"/a/b/c":     "/a/b",
		"/a//b":      "/a/",
	}
	for in, want := range defaults {
		if got := defaultPath(in); got != want {
			t.Errorf("defaultPath(%q) = %q, want %q", in, got, want)
		}
	}
	matches := []struct {
		req, cookie string
		want        bool
	}{
		{"/", "/", true},
		{"/docs", "/docs", true},
		{"/docs/", "/docs", true},
		{"/docs/web", "/docs", true},
		{"/docs/web", "/docs/", true},
		{"/docsets", "/docs", false},
		{"/fr/docs", "/docs", false},
		{"/Docs", "/docs", false},
		{"/docs", "/docs/", false},
		{"/anything", "/", true},
	}
	for _, tt := range matches {
		if got := pathMatch(tt.req, tt.cookie); got != tt.want {
			t.Errorf("pathMatch(%q, %q) = %v, want %v", tt.req, tt.cookie, got, tt.want)
		}
	}
}

func TestPublicSuffix(t *testing.T) {
	t.Parallel()
	for d, want := range map[string]bool{
		"":                 true,
		"com":              true,
		"internal":         true,
		"com.":             true,
		"localhost":        false,
		"example.com":      false,
		"co.uk":            true,
		"com.au":           true,
		"ne.jp":            true,
		"example.co.uk":    false,
		"github.io":        true,
		"user.github.io":   false,
		"co.example":       false,
		"docs.example.com": false,
	} {
		if got := isPublicSuffix(d); got != want {
			t.Errorf("isPublicSuffix(%q) = %v, want %v", d, got, want)
		}
	}
	for d, want := range map[string]string{
		"example.com":       "example.com",
		"a.b.example.com":   "example.com",
		"www.example.co.uk": "example.co.uk",
		"x.y.github.io":     "y.github.io",
		"localhost":         "localhost",
		"10.0.0.1":          "10.0.0.1",
	} {
		if got := siteKey(d); got != want {
			t.Errorf("siteKey(%q) = %q, want %q", d, got, want)
		}
	}
}

// TestRFC6265Examples follows the examples of RFC 6265 §3.1.
func TestRFC6265Examples(t *testing.T) {
	t.Parallel()

	t.Run("host-only", func(t *testing.T) {
		j := New(Options{Now: newClock().Now})
		set(t, j, "http://example.com/", "SID=31d4d96e407aad42")
		header(t, j, "http://example.com/", "SID=31d4d96e407aad42")
		header(t, j, "http://example.com/any/path", "SID=31d4d96e407aad42")
		header(t, j, "http://www.example.com/", "")
	})

	t.Run("domain and path", func(t *testing.T) {
		j := New(Options{Now: newClock().Now})
		set(t, j, "http://example.com/login", "SID=31d4d96e407aad42; Path=/; Domain=example.com")
		header(t, j, "http://example.com/", "SID=31d4d96e407aad42")
		header(t, j, "http://www.example.com/x", "SID=31d4d96e407aad42")
		header(t, j, "http://a.b.example.com/x", "SID=31d4d96e407aad42")
		header(t, j, "http://example.org/", "")
	})

	t.Run("secure and httponly", func(t *testing.T) {
		clk := newClock()
		j := New(Options{Now: clk.Now})
		set(t, j, "https://example.com/", "SID=31d4d96e407aad42; Path=/; Secure; HttpOnly")
		clk.Advance(time.Second)
		set(t, j, "https://example.com/", "lang=en-US; Path=/; Domain=example.com")
		header(t, j, "https://example.com/", "SID=31d4d96e407aad42; lang=en-US")
		header(t, j, "http://example.com/", "lang=en-US")
		header(t, j, "https://www.example.com/", "lang=en-US")
	})

	t.Run("expires and deletion", func(t *testing.T) {
		clk := newClock() // 2021-01-01
		j := New(Options{Now: clk.Now})
		set(t, j, "https://example.com/", "SID=31d4d96e407aad42; Path=/; Secure; HttpOnly",
			"lang=en-US; Path=/; Domain=example.com")
		set(t, j, "https://example.com/", "lang=en-US; Expires=Wed, 09 Jun 2021 10:18:14 GMT")
		// The last line has no Path: the default path of "/" is "/", and no
		// Domain: host-only. It is a different cookie from the domain one.
		header(t, j, "https://example.com/", "SID=31d4d96e407aad42; lang=en-US; lang=en-US")
		set(t, j, "https://example.com/", "lang=; Expires=Sun, 06 Nov 1994 08:49:37 GMT")
		header(t, j, "https://example.com/", "SID=31d4d96e407aad42; lang=en-US")
		set(t, j, "https://example.com/", "lang=; Domain=example.com; Path=/; Expires=Sun, 06 Nov 1994 08:49:37 GMT")
		header(t, j, "https://example.com/", "SID=31d4d96e407aad42")
	})

	t.Run("expiry time passes", func(t *testing.T) {
		clk := newClock()
		j := New(Options{Now: clk.Now})
		set(t, j, "http://example.com/", "lang=en-US; Expires=Wed, 09 Jun 2021 10:18:14 GMT")
		header(t, j, "http://example.com/", "lang=en-US")
		clk.Advance(time.Date(2021, 6, 9, 10, 18, 13, 0, time.UTC).Sub(clk.Now()))
		header(t, j, "http://example.com/", "lang=en-US")
		clk.Advance(time.Second)
		header(t, j, "http://example.com/", "")
		if n := j.Len(); n != 0 {
			t.Errorf("Len = %d after expiry, want 0", n)
		}
	})
}

func TestDomainAttribute(t *testing.T) {
	t.Parallel()
	j := New(Options{Now: newClock().Now})
	set(t, j, "http://www.example.com/", "host=1")
	set(t, j, "http://www.example.com/", "dom=2; Domain=.EXAMPLE.com")
	set(t, j, "http://www.example.com/", "self=3; Domain=www.example.com")
	header(t, j, "http://www.example.com/", "host=1; dom=2; self=3")
	header(t, j, "http://WWW.Example.COM:8080/", "host=1; dom=2; self=3")
	header(t, j, "http://example.com/", "dom=2")
	header(t, j, "http://a.www.example.com/", "dom=2; self=3")
	header(t, j, "http://notexample.com/", "")

	reject(t, j, "http://www.example.com/", "x=1; Domain=example.org", ReasonDomainMismatch)
	reject(t, j, "http://www.example.com/", "x=1; Domain=a.www.example.com", ReasonDomainMismatch)
	reject(t, j, "http://www.example.com/", "x=1; Domain=ample.com", ReasonDomainMismatch)

	// Public suffixes.
	reject(t, j, "http://www.example.com/", "x=1; Domain=com", ReasonPublicSuffix)
	reject(t, j, "http://www.example.co.uk/", "x=1; Domain=co.uk", ReasonPublicSuffix)
	reject(t, j, "https://me.github.io/", "x=1; Domain=github.io", ReasonPublicSuffix)
	set(t, j, "http://www.example.co.uk/", "ok=1; Domain=example.co.uk")
	header(t, j, "http://shop.example.co.uk/", "ok=1")
	// A Domain equal to the host is host-only, even for a public suffix.
	set(t, j, "http://co.uk/", "ps=1; Domain=co.uk")
	header(t, j, "http://co.uk/", "ps=1")
	header(t, j, "http://example.co.uk/", "ok=1")
	// localhost subdomains may share cookies.
	set(t, j, "http://app.localhost:3000/", "lh=1; Domain=localhost")
	header(t, j, "http://api.localhost:4000/", "lh=1")

	// IP addresses are host-only.
	set(t, j, "http://127.0.0.1:8080/", "ip=1; Domain=127.0.0.1")
	header(t, j, "http://127.0.0.1/", "ip=1")
	reject(t, j, "http://10.0.0.1/", "x=1; Domain=0.0.1", ReasonDomainMismatch)
	set(t, j, "http://[::1]:8080/", "v6=1")
	header(t, j, "http://[::1]/", "v6=1")
	for _, c := range j.All() {
		if c.Name == "ip" && !c.HostOnly {
			t.Errorf("cookie for an IP address is not host-only: %+v", c)
		}
	}
}

func TestDefaultPathAndPathAttribute(t *testing.T) {
	t.Parallel()
	j := New(Options{Now: newClock().Now})
	set(t, j, "http://example.com/docs/page?q=/x", "def=1")
	set(t, j, "http://example.com/docs/page", "rel=2; Path=relative")
	set(t, j, "http://example.com/", "abs=3; Path=/api/v1")
	header(t, j, "http://example.com/docs", "def=1; rel=2")
	header(t, j, "http://example.com/docs/other", "def=1; rel=2")
	header(t, j, "http://example.com/docsets", "")
	header(t, j, "http://example.com/", "")
	header(t, j, "http://example.com/api/v1/users", "abs=3")
	header(t, j, "http://example.com/api/v10", "")

	// The path as sent: httpclient keeps it in URL.Opaque.
	u := &url.URL{Scheme: "http", Host: "example.com", Opaque: "/api/v1/a%2Fb", Path: "/api/v1/a/b"}
	if got := j.Header(u); got != "abs=3" {
		t.Errorf("Header(opaque URL) = %q, want abs=3", got)
	}
}

func TestSecure(t *testing.T) {
	t.Parallel()
	j := New(Options{Now: newClock().Now})
	reject(t, j, "http://example.com/", "s=1; Secure", ReasonInsecure)
	set(t, j, "https://example.com/", "s=1; Secure")
	header(t, j, "https://example.com/", "s=1")
	header(t, j, "http://example.com/", "")
	header(t, j, "wss://example.com/", "s=1")
	header(t, j, "ws://example.com/", "")

	// Localhost and loopback addresses are secure contexts over http.
	set(t, j, "http://localhost:8080/", "l=1; Secure")
	header(t, j, "http://localhost:9090/", "l=1")
	set(t, j, "http://127.0.0.1/", "l4=1; Secure")
	header(t, j, "http://127.0.0.1/", "l4=1")
	set(t, j, "http://[::1]/", "l6=1; Secure")
	header(t, j, "http://[::1]/", "l6=1")
	set(t, j, "http://api.localhost/", "sub=1; Secure")
	header(t, j, "http://api.localhost/", "sub=1")
	reject(t, j, "http://10.0.0.1/", "x=1; Secure", ReasonInsecure)

	// Leave Secure cookies alone: an insecure channel cannot overwrite or
	// shadow a Secure cookie of the same name.
	set(t, j, "https://www.example.com/", "sid=secure; Secure; Domain=example.com; Path=/")
	reject(t, j, "http://www.example.com/", "sid=evil; Domain=example.com; Path=/", ReasonOverwriteSecure)
	reject(t, j, "http://www.example.com/", "sid=evil", ReasonOverwriteSecure)
	reject(t, j, "http://sub.www.example.com/x/y", "sid=evil; Path=/x", ReasonOverwriteSecure)
	set(t, j, "http://www.example.com/", "other=ok")
	set(t, j, "https://www.example.com/", "sid=replaced; Secure; Domain=example.com; Path=/")
	header(t, j, "https://www.example.com/", "sid=replaced; other=ok")
}

func TestPrefixes(t *testing.T) {
	t.Parallel()
	j := New(Options{Now: newClock().Now})
	set(t, j, "https://example.com/", "__Secure-a=1; Secure")
	reject(t, j, "https://example.com/", "__Secure-b=1", ReasonPrefix)
	reject(t, j, "https://example.com/", "__secure-b=1", ReasonPrefix)
	set(t, j, "https://example.com/x/", "__Host-c=1; Secure; Path=/")
	reject(t, j, "https://example.com/", "__Host-d=1; Path=/", ReasonPrefix)
	reject(t, j, "https://example.com/", "__Host-d=1; Secure; Path=/x", ReasonPrefix)
	reject(t, j, "https://example.com/x/y", "__Host-d=1; Secure", ReasonPrefix)
	reject(t, j, "https://example.com/", "__HOST-d=1; Secure; Path=/; Domain=example.com", ReasonPrefix)
	set(t, j, "https://example.com/", "__Host-d=1; Secure; Path=/")
	reject(t, j, "https://example.com/", "__Secure-x; Secure", ReasonPrefix)
}

func TestOverwriteAndDeletion(t *testing.T) {
	t.Parallel()
	clk := newClock()
	j := New(Options{Now: clk.Now})
	set(t, j, "http://example.com/", "a=1")
	clk.Advance(time.Second)
	set(t, j, "http://example.com/", "b=1")
	clk.Advance(time.Second)
	set(t, j, "http://example.com/", "a=2; HttpOnly; SameSite=Strict")
	// Replacing keeps the creation time, so the order stays.
	header(t, j, "http://example.com/", "a=2; b=1")
	all := j.All()
	if len(all) != 2 || !all[0].Created.Equal(newClock().Now()) || !all[0].HTTPOnly || all[0].SameSite != SameSiteStrict {
		t.Fatalf("All = %+v", all)
	}

	for _, line := range []string{
		"a=; Max-Age=0",
		"a=; Max-Age=-1",
		"a=; Expires=Thu, 01 Jan 1970 00:00:00 GMT",
		// Max-Age wins over Expires, in either order.
		"a=; Expires=Fri, 31 Dec 9999 23:59:59 GMT; Max-Age=0",
	} {
		set(t, j, "http://example.com/", "a=again")
		stored, _ := j.SetCookieLines(mustURL(t, "http://example.com/"), []string{line})
		if len(stored) != 1 || stored[0] != "a" {
			t.Errorf("%q: stored = %q", line, stored)
		}
		header(t, j, "http://example.com/", "b=1")
	}
	set(t, j, "http://example.com/", "a=kept; Max-Age=60; Expires=Thu, 01 Jan 1970 00:00:00 GMT")
	header(t, j, "http://example.com/", "b=1; a=kept")

	// Deleting a cookie that does not exist is fine.
	set(t, j, "http://example.com/", "nope=; Max-Age=0")
	// A cookie with another path is another cookie.
	set(t, j, "http://example.com/", "b=deep; Path=/deep; Max-Age=0")
	header(t, j, "http://example.com/", "b=1; a=kept")

	if !j.Delete(Cookie{Domain: "EXAMPLE.com", Path: "/", Name: "b", HostOnly: true}) {
		t.Error("Delete(b) = false")
	}
	if j.Delete(Cookie{Domain: "example.com", Path: "/", Name: "b", HostOnly: true}) {
		t.Error("second Delete(b) = true")
	}
	header(t, j, "http://example.com/", "a=kept")
	j.Clear()
	header(t, j, "http://example.com/", "")
}

func TestExpiry(t *testing.T) {
	t.Parallel()
	clk := newClock()
	j := New(Options{Now: clk.Now})
	set(t, j, "http://example.com/", "m=1; Max-Age=60", "session=1")
	clk.Advance(59 * time.Second)
	header(t, j, "http://example.com/", "m=1; session=1")
	clk.Advance(time.Second)
	header(t, j, "http://example.com/", "session=1")

	// Lifetimes are capped at 400 days.
	start := clk.Now()
	set(t, j, "http://example.com/", "long=1; Max-Age=999999999999", "far=1; Expires=Fri, 31 Dec 9999 23:59:59 GMT")
	for _, c := range j.All() {
		if c.Name == "session" {
			if c.Persistent || !c.Expires.IsZero() {
				t.Errorf("session cookie %+v", c)
			}
			continue
		}
		if !c.Persistent || !c.Expires.Equal(start.Add(MaxAge)) {
			t.Errorf("%s expires %v, want %v", c.Name, c.Expires, start.Add(MaxAge))
		}
	}
	clk.Advance(MaxAge)
	header(t, j, "http://example.com/", "session=1")
}

func TestHeaderOrder(t *testing.T) {
	t.Parallel()
	clk := newClock()
	j := New(Options{Now: clk.Now})
	// Same creation time: insertion order breaks the tie.
	set(t, j, "http://example.com/a/b/c", "root1=1; Path=/", "root2=2; Path=/")
	clk.Advance(time.Second)
	set(t, j, "http://example.com/", "deep=3; Path=/a/b")
	clk.Advance(time.Second)
	set(t, j, "http://example.com/", "mid=4; Path=/a", "dom=5; Domain=example.com; Path=/a")
	header(t, j, "http://example.com/a/b/c", "deep=3; mid=4; dom=5; root1=1; root2=2")
	header(t, j, "http://example.com/a", "mid=4; dom=5; root1=1; root2=2")

	// An empty name is sent as the value alone.
	set(t, j, "http://empty.example/", "justvalue")
	header(t, j, "http://empty.example/", "justvalue")
}

func TestSameSiteDoesNotRestrictSending(t *testing.T) {
	t.Parallel()
	j := New(Options{Now: newClock().Now})
	set(t, j, "https://example.com/", "s=1; SameSite=Strict", "l=2; SameSite=Lax", "n=3; SameSite=None; Secure", "u=4; SameSite=None")
	header(t, j, "https://example.com/", "s=1; l=2; n=3; u=4")
	want := map[string]SameSite{"s": SameSiteStrict, "l": SameSiteLax, "n": SameSiteNone, "u": SameSiteNone}
	for _, c := range j.All() {
		if c.SameSite != want[c.Name] {
			t.Errorf("%s SameSite = %q, want %q", c.Name, c.SameSite, want[c.Name])
		}
	}
}

func TestRejections(t *testing.T) {
	t.Parallel()
	j := New(Options{Now: newClock().Now})
	reject(t, j, "http://example.com/", "a=b\x01", ReasonInvalid)
	reject(t, j, "http://example.com/", "", ReasonInvalid)
	reject(t, j, "http://example.com/", "big="+strings.Repeat("x", MaxNameValueBytes), ReasonTooLarge)
	reject(t, j, "ftp://example.com/", "a=1", ReasonURL)
	reject(t, j, "http:///path", "a=1", ReasonURL)
	if got := j.Cookies(mustURL(t, "ftp://example.com/")); got != nil {
		t.Errorf("Cookies(ftp) = %v", got)
	}
	stored, rejected := j.SetCookieLines(mustURL(t, "http://example.com/"), []string{"ok=1", "bad=1; Domain=other.com"})
	if fmt.Sprint(stored, rejected) != "[ok] [bad: domain_mismatch]" {
		t.Errorf("SetCookieLines = %q, %q", stored, rejected)
	}
}

func TestLimits(t *testing.T) {
	t.Parallel()
	clk := newClock()
	j := New(Options{Now: clk.Now, MaxPerSite: 3, MaxTotal: 5})
	for i := range 3 {
		set(t, j, "http://www.example.com/", fmt.Sprintf("c%d=1", i))
		clk.Advance(time.Second)
	}
	// Storing c0 again makes it the most recently used.
	set(t, j, "http://www.example.com/", "c0=again")
	clk.Advance(time.Second)
	// A cookie for another host of the same site evicts the least recently
	// used one, c1.
	set(t, j, "http://api.example.com/", "c3=1; Domain=example.com")
	clk.Advance(time.Second)
	header(t, j, "http://www.example.com/", "c0=again; c2=1; c3=1")

	// Other sites have their own limit; the total limit applies to all.
	set(t, j, "http://other.test/", "o1=1")
	clk.Advance(time.Second)
	set(t, j, "http://other.test/", "o2=1")
	clk.Advance(time.Second)
	if n := j.Len(); n != 5 {
		t.Fatalf("Len = %d, want 5", n)
	}
	// Sending to other.test makes its cookies recent; c0, c2 and c3 were
	// sent together, c0 first.
	header(t, j, "http://other.test/", "o1=1; o2=1")
	set(t, j, "http://third.test/", "t1=1")
	if n := j.Len(); n != 5 {
		t.Fatalf("Len = %d after the total limit, want 5", n)
	}
	header(t, j, "http://www.example.com/", "c2=1; c3=1")
	header(t, j, "http://third.test/", "t1=1")

	// Defaults.
	d := New(Options{})
	if d.maxPerSite != DefaultMaxPerSite || d.maxTotal != DefaultMaxTotal {
		t.Errorf("defaults = %d, %d", d.maxPerSite, d.maxTotal)
	}
	for i := range DefaultMaxPerSite + 20 {
		d.SetCookieLines(mustURL(t, "http://example.com/"), []string{fmt.Sprintf("n%d=1", i)})
	}
	if n := d.Len(); n != DefaultMaxPerSite {
		t.Errorf("Len = %d, want %d", n, DefaultMaxPerSite)
	}
}

func TestHTTPCookieJar(t *testing.T) {
	t.Parallel()
	j := New(Options{Now: newClock().Now})
	u := mustURL(t, "https://example.com/docs/page")
	j.SetCookies(u, []*http.Cookie{
		{Name: "raw", Value: "ignored", Raw: "raw=from raw; Path=/"},
		{Name: "built", Value: "v", Path: "/", Secure: true},
	})
	var got []string
	for _, c := range j.Cookies(mustURL(t, "https://example.com/")) {
		got = append(got, c.Name+"="+c.Value)
	}
	if fmt.Sprint(got) != "[raw=from raw built=v]" {
		t.Errorf("Cookies = %q", got)
	}
}

func TestStoreLines(t *testing.T) {
	t.Parallel()
	j := New(Options{})
	got := j.StoreLines(mustURL(t, "http://example.com/"), []string{"ok=1", "bad=1; Domain=other.com", "", "sec=1; Secure", "gone=1; Max-Age=0"})
	want := []Outcome{{"ok", ""}, {"bad", ReasonDomainMismatch}, {"", ReasonInvalid}, {"sec", ReasonInsecure}, {"gone", ""}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("StoreLines = %v, want %v", got, want)
	}
	if j.StoreLines(mustURL(t, "http://example.com/"), nil) != nil {
		t.Error("StoreLines(nil) != nil")
	}
}
