package cookies

import (
	"errors"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func wantCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != kind || e.Code != code || e.Hint == "" {
		t.Fatalf("err = %v, want kind %s code %s with a hint", err, kind, code)
	}
}

func TestSetAddsAndSends(t *testing.T) {
	t.Parallel()
	clk := newClock()
	j := New(Options{Now: clk.Now})
	changes := 0
	j.onChange = func() { changes++ }

	got, err := j.Set(nil, Cookie{Name: "sid", Value: "1", Domain: ".Example.com", HTTPOnly: true, SameSite: SameSiteLax})
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != "example.com" || got.Path != "/" || got.HostOnly || !got.Created.Equal(clk.Now()) || changes != 1 {
		t.Fatalf("stored %+v, changes %d", got, changes)
	}
	if h := j.Header(mustURL(t, "http://api.example.com/x")); h != "sid=1" {
		t.Fatalf("header = %q", h)
	}

	// Host-only: not sent to subdomains.
	if _, err := j.Set(nil, Cookie{Name: "h", Value: "2", Domain: "example.com", HostOnly: true, Path: "/v1"}); err != nil {
		t.Fatal(err)
	}
	if h := j.Header(mustURL(t, "http://example.com/v1/a")); h != "h=2; sid=1" {
		t.Fatalf("header = %q", h)
	}
	if h := j.Header(mustURL(t, "http://api.example.com/v1/a")); h != "sid=1" {
		t.Fatalf("header = %q", h)
	}
}

func TestSetReplaces(t *testing.T) {
	t.Parallel()
	clk := newClock()
	j := New(Options{Now: clk.Now})
	set(t, j, "https://example.com/", "a=1", "b=2")
	old := j.All()[0]
	clk.Advance(time.Minute)

	// Same identity, new value: creation time and header order kept.
	edited := old
	edited.Value = "one"
	got, err := j.Set(&old, edited)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created.Equal(old.Created) || !got.LastAccess.Equal(clk.Now()) {
		t.Fatalf("times %v %v", got.Created, got.LastAccess)
	}
	if h := j.Header(mustURL(t, "https://example.com/")); h != "a=one; b=2" {
		t.Fatalf("header = %q", h)
	}

	// New identity: the old cookie goes.
	renamed := got
	renamed.Name, renamed.Path = "c", "/api"
	if _, err := j.Set(&got, renamed); err != nil {
		t.Fatal(err)
	}
	if h := j.Header(mustURL(t, "https://example.com/api")); h != "c=one; b=2" {
		t.Fatalf("header = %q", h)
	}

	// Taking another cookie's identity, or adding a duplicate, fails.
	b := Cookie{Name: "b", Domain: "example.com", HostOnly: true, Path: "/"}
	_, err = j.Set(&renamed, Cookie{Name: "b", Value: "x", Domain: "example.com", HostOnly: true})
	wantCode(t, err, errs.AlreadyExists, CodeCookieExists)
	_, err = j.Set(nil, Cookie{Name: "b", Value: "x", Domain: "example.com", HostOnly: true})
	wantCode(t, err, errs.AlreadyExists, CodeCookieExists)

	// Editing a cookie that is gone fails.
	j.Delete(b)
	_, err = j.Set(&b, Cookie{Name: "b", Value: "x", Domain: "example.com", HostOnly: true})
	wantCode(t, err, errs.NotFound, CodeCookieNotFound)
}

func TestSetExpiry(t *testing.T) {
	t.Parallel()
	clk := newClock()
	j := New(Options{Now: clk.Now})
	got, err := j.Set(nil, Cookie{Name: "p", Value: "1", Domain: "example.com", Persistent: true, Expires: clk.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Hour)
	if h := j.Header(mustURL(t, "http://example.com/")); h != "" {
		t.Fatalf("expired cookie sent: %q", h)
	}
	fresh := got
	fresh.Expires = clk.Now().Add(time.Hour)
	_, err = j.Set(&got, fresh)
	wantCode(t, err, errs.NotFound, CodeCookieNotFound)

	// Session cookies drop Expires; long lifetimes are capped.
	s, err := j.Set(nil, Cookie{Name: "s", Value: "1", Domain: "example.com", Expires: clk.Now().Add(time.Hour)})
	if err != nil || !s.Expires.IsZero() {
		t.Fatalf("session cookie %+v, %v", s, err)
	}
	long, err := j.Set(nil, Cookie{Name: "l", Value: "1", Domain: "example.com", Persistent: true, Expires: clk.Now().Add(1000 * 24 * time.Hour)})
	if err != nil || !long.Expires.Equal(clk.Now().Add(MaxAge)) {
		t.Fatalf("long cookie %+v, %v", long, err)
	}
}

func TestSetValidates(t *testing.T) {
	t.Parallel()
	clk := newClock()
	j := New(Options{Now: clk.Now})
	ok := Cookie{Name: "n", Value: "v", Domain: "example.com"}
	cases := map[string]func(c *Cookie){
		"empty":          func(c *Cookie) { c.Name, c.Value = "", "" },
		"name semicolon": func(c *Cookie) { c.Name = "a;b" },
		"name equals":    func(c *Cookie) { c.Name = "a=b" },
		"name space":     func(c *Cookie) { c.Name = " a" },
		"value control":  func(c *Cookie) { c.Value = "a\nb" },
		"value semi":     func(c *Cookie) { c.Value = "a;b" },
		"too large":      func(c *Cookie) { c.Value = string(make([]byte, MaxNameValueBytes)) },
		"no domain":      func(c *Cookie) { c.Domain = " " },
		"domain url":     func(c *Cookie) { c.Domain = "http://example.com/" },
		"public suffix":  func(c *Cookie) { c.Domain = "co.uk" },
		"relative path":  func(c *Cookie) { c.Path = "api" },
		"same site":      func(c *Cookie) { c.SameSite = "Loose" },
		"expired":        func(c *Cookie) { c.Persistent, c.Expires = true, clk.Now() },
		"secure prefix":  func(c *Cookie) { c.Name = "__Secure-x" },
		"host prefix":    func(c *Cookie) { c.Name, c.Secure = "__Host-x", true },
	}
	for name, mutate := range cases {
		c := ok
		mutate(&c)
		_, err := j.Set(nil, c)
		wantCode(t, err, errs.InvalidArgument, CodeInvalidCookie)
		if j.Len() != 0 {
			t.Fatalf("%s: stored an invalid cookie", name)
		}
	}

	// Allowed: a host-only cookie on a single label, an IP (always
	// host-only), and a __Host- cookie that follows the rules.
	for _, c := range []Cookie{
		{Name: "a", Value: "1", Domain: "intranet", HostOnly: true},
		{Name: "b", Value: "1", Domain: "127.0.0.1"},
		{Name: "__Host-c", Value: "1", Domain: "example.com", HostOnly: true, Secure: true},
	} {
		got, err := j.Set(nil, c)
		if err != nil {
			t.Fatalf("Set(%+v): %v", c, err)
		}
		if !got.HostOnly {
			t.Fatalf("%s: not host-only", c.Name)
		}
	}
}

func TestDeleteDomain(t *testing.T) {
	t.Parallel()
	j := New(Options{})
	set(t, j, "http://example.com/", "a=1", "b=2; Domain=example.com")
	set(t, j, "http://other.test/", "c=3")
	if n := j.DeleteDomain(".EXAMPLE.com"); n != 2 {
		t.Fatalf("deleted %d", n)
	}
	if all := j.All(); len(all) != 1 || all[0].Name != "c" {
		t.Fatalf("left %+v", all)
	}
	if n := j.DeleteDomain("example.com"); n != 0 {
		t.Fatalf("deleted %d again", n)
	}
}
