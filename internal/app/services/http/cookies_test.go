package http

import (
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// TestSendResponseCookies sends to a server that sets several cookies,
// malformed and rejected ones included, and checks the Cookies of the
// response and what the jar did (HTTP-32).
func TestSendResponseCookies(t *testing.T) {
	t.Parallel()
	lines := []string{
		"session=s1; Path=/; HttpOnly; SameSite=Lax",
		"pref=dark; Max-Age=3600; Secure",
		"old=; Expires=Thu, 01 Jan 1970 00:00:00 GMT",
		"other=1; Domain=other.example",
		"bad=1; Expires=soon; Max-Age=x",
		"",
		"__Host-x=1; Path=/",
	}
	redirected := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/start" {
			w.Header().Add("Set-Cookie", "hop=1")
			nethttp.Redirect(w, r, "/final", nethttp.StatusFound)
			return
		}
		for _, l := range lines {
			w.Header().Add("Set-Cookie", l)
		}
		fmt.Fprint(w, "ok")
	}))
	defer redirected.Close()

	jar := cookies.New(cookies.Options{})
	f := newFixture(t, httpSource(redirected.URL+"/start"), fixedJar{jar})
	id := f.send(t)
	f.rec.wait(t, id)
	got := f.rec.response(t, id).Cookies
	if len(got) != len(lines) {
		t.Fatalf("cookies = %d, want %d: %+v", len(got), len(lines), got)
	}
	type row struct{ name, jar, reason, err string }
	want := []row{
		{"session", JarStored, "", ""},
		{"pref", JarStored, "", ""}, // loopback counts as a secure channel
		{"old", JarRemoved, "", ""},
		{"other", JarRejected, string(cookies.ReasonDomainMismatch), ""},
		{"bad", JarStored, "", ""},
		{"", JarRejected, string(cookies.ReasonInvalid), reasonMessage(cookies.ReasonInvalid)},
		{"__Host-x", JarRejected, string(cookies.ReasonPrefix), ""},
	}
	for i, w := range want {
		c := got[i]
		if c.Raw != lines[i] || c.Name != w.name || c.Jar != w.jar || c.JarReason != w.reason || c.Error != w.err {
			t.Errorf("cookie %d = %+v, want %+v", i, c, w)
		}
		if w.reason != "" && c.JarMessage == "" {
			t.Errorf("cookie %d has no jar message", i)
		}
	}
	if s := got[0]; s.Value != "s1" || s.Path != "/" || !s.HTTPOnly || s.SameSite != "Lax" || s.Secure {
		t.Errorf("session = %+v", s)
	}
	if p := got[1]; !p.HasMaxAge || p.MaxAge != 3600 || !p.Secure {
		t.Errorf("pref = %+v", p)
	}
	if o := got[2]; o.Expires != "1970-01-01T00:00:00Z" {
		t.Errorf("old expires = %q", o.Expires)
	}
	if b := got[4]; len(b.Notes) != 2 {
		t.Errorf("bad notes = %q, want two", b.Notes)
	}
	if o := got[3]; o.Domain != "other.example" {
		t.Errorf("other domain = %q", o.Domain)
	}
	// The redirect's cookie was stored too, but is not the final
	// response's.
	names := map[string]bool{}
	for _, c := range jar.All() {
		names[c.Name] = true
	}
	if !names["hop"] || !names["session"] || !names["bad"] || names["old"] {
		t.Errorf("jar = %v", names)
	}
}

// TestResponseCookiesWithoutJar: parsed, no jar state; a jar record for
// other lines (a redirect's) is not used.
func TestResponseCookiesWithoutJar(t *testing.T) {
	t.Parallel()
	headers := []httpclient.Header{
		{Name: "Content-Type", Value: "text/plain"},
		{Name: "set-cookie", Value: "a=1; Domain=.Example.com; Path=/p; Expires=Wed, 09 Jun 2021 10:18:14 GMT; SameSite=Strict"},
		{Name: "Set-Cookie", Value: "b\x01=2"},
	}
	got := responseCookies(headers, nil, time.Now())
	if len(got) != 2 {
		t.Fatalf("cookies = %+v", got)
	}
	if a := got[0]; a.Name != "a" || a.Value != "1" || a.Domain != "example.com" || a.Path != "/p" ||
		a.Expires != "2021-06-09T10:18:14Z" || a.SameSite != "Strict" || a.HasMaxAge || a.Jar != JarNone || a.Error != "" {
		t.Errorf("a = %+v", a)
	}
	if b := got[1]; b.Error == "" || b.Jar != JarNone {
		t.Errorf("b = %+v", b)
	}
	rec := &jarRecorder{jar: cookies.New(cookies.Options{})}
	u, _ := url.Parse("http://example.com/")
	rec.SetCookieLines(u, []string{"hop=1"})
	if got := responseCookies(headers, rec, time.Now()); got[0].Jar != JarNone {
		t.Errorf("jar state from another hop: %+v", got[0])
	}
	if got := responseCookies(nil, rec, time.Now()); got == nil || len(got) != 0 {
		t.Errorf("no Set-Cookie: %#v, want []", got)
	}
}
