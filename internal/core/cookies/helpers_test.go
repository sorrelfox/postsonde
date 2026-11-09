package cookies

import (
	"net/url"
	"sync"
	"testing"
	"time"
)

// clock is a manual clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2021, time.January, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func mustURL(t testing.TB, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func parseURLForFuzz(s string) (*url.URL, error) { return url.Parse(s) }

// set stores lines as the response to rawURL and fails on rejections.
func set(t *testing.T, j *Jar, rawURL string, lines ...string) {
	t.Helper()
	if _, rejected := j.SetCookieLines(mustURL(t, rawURL), lines); len(rejected) > 0 {
		t.Fatalf("SetCookieLines(%s, %q) rejected %q", rawURL, lines, rejected)
	}
}

// reject stores line as the response to rawURL and checks it is rejected
// with reason.
func reject(t *testing.T, j *Jar, rawURL, line string, reason Reason) {
	t.Helper()
	stored, rejected := j.SetCookieLines(mustURL(t, rawURL), []string{line})
	if len(stored) != 0 || len(rejected) != 1 {
		t.Fatalf("SetCookieLines(%s, %q) = stored %q, rejected %q; want rejected", rawURL, line, stored, rejected)
	}
	c, _, _ := parseSetCookie(line)
	if want := c.name + ": " + string(reason); rejected[0] != want {
		t.Fatalf("SetCookieLines(%s, %q) rejected %q, want %q", rawURL, line, rejected[0], want)
	}
}

// header checks the Cookie header the jar sends to rawURL.
func header(t *testing.T, j *Jar, rawURL, want string) {
	t.Helper()
	if got := j.Header(mustURL(t, rawURL)); got != want {
		t.Errorf("Cookie header for %s = %q, want %q", rawURL, got, want)
	}
}
