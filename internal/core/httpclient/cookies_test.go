package httpclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
)

func eventDetails(events []TimelineEvent, name string) []string {
	var out []string
	for _, e := range events {
		if e.Name == name {
			out = append(out, e.Detail)
		}
	}
	return out
}

func TestCookieJar(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "session=s1; Path=/; HttpOnly")
		w.Header().Add("Set-Cookie", "bad=1; Domain=example.org")
		http.Redirect(w, r, "/step", http.StatusFound)
	})
	mux.HandleFunc("/step", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "step=2; Path=/")
		http.Redirect(w, r, "/echo", http.StatusSeeOther)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Join(r.Header.Values("Cookie"), " | "))
	})
	mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "session=; Path=/; Max-Age=0")
		io.WriteString(w, "bye")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	jar := cookies.New(cookies.Options{})
	base := newTestClient(t, TransportOptions{})
	c := base.WithCookieJar(jar)

	// Cookies set on redirects are stored and sent on the next hop.
	resp, err := c.Send(t.Context(), get(srv.URL+"/login"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Body); got != "session=s1; step=2" {
		t.Errorf("echo = %q, want session=s1; step=2", got)
	}
	if resp.RequestCookie != "session=s1; step=2" {
		t.Errorf("RequestCookie = %q", resp.RequestCookie)
	}
	if len(resp.Redirects) != 2 || resp.Redirects[0].RequestCookie != "" || resp.Redirects[1].RequestCookie != "session=s1" {
		t.Errorf("hop cookies = %+v", resp.Redirects)
	}
	stored := eventDetails(resp.Events, EventCookiesStored)
	if len(stored) != 2 || stored[0] != "stored session; ignored bad: domain_mismatch" || stored[1] != "stored step" {
		t.Errorf("cookies_stored events = %q", stored)
	}
	sent := eventDetails(resp.Events, EventCookiesSent)
	if len(sent) != 2 || sent[0] != "session" || sent[1] != "session, step" {
		t.Errorf("cookies_sent events = %q", sent)
	}
	for _, e := range resp.Events {
		if strings.Contains(e.Detail, "s1") {
			t.Errorf("event %s shows a cookie value: %q", e.Name, e.Detail)
		}
	}

	// Cookie rows come first and win over jar cookies of the same name,
	// in the row's spelling.
	resp, err = c.Send(t.Context(), get(srv.URL+"/echo", on("cookie", "session=mine; extra=1")))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Body); got != "session=mine; extra=1; step=2" {
		t.Errorf("echo with row = %q", got)
	}
	resp, err = c.Send(t.Context(), get(srv.URL+"/echo", on("Cookie", ""), on("X-Other", "1")))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Body); got != "session=s1; step=2" {
		t.Errorf("echo with empty row = %q", got)
	}

	// A client without the jar sends nothing but rows, and the jar-less
	// base client is unchanged.
	resp, err = base.Send(t.Context(), get(srv.URL+"/echo"))
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "" || resp.RequestCookie != "" || len(eventDetails(resp.Events, EventCookiesSent)) != 0 {
		t.Errorf("base client sent cookies: %q", resp.Body)
	}
	resp, err = c.WithCookieJar(nil).Send(t.Context(), get(srv.URL+"/echo", on("Cookie", "row=1")))
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "row=1" || resp.RequestCookie != "row=1" {
		t.Errorf("no jar: body %q, RequestCookie %q", resp.Body, resp.RequestCookie)
	}

	// Deletion through Max-Age=0.
	if _, err := c.Send(t.Context(), get(srv.URL+"/logout")); err != nil {
		t.Fatal(err)
	}
	resp, err = c.Send(t.Context(), get(srv.URL+"/echo"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Body); got != "step=2" {
		t.Errorf("after logout = %q", got)
	}
	names := []string{}
	for _, ck := range jar.All() {
		names = append(names, ck.Name)
	}
	if strings.Join(names, ",") != "step" {
		t.Errorf("jar = %v", names)
	}
}

// TestCookieJarCrossHost checks that host-only cookies do not follow a
// redirect to another host, while net/http still drops the Cookie row.
func TestCookieJarCrossHost(t *testing.T) {
	t.Parallel()
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("Cookie"))
	}))
	defer echo.Close()
	other := strings.Replace(echo.URL, "127.0.0.1", "localhost", 1)
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "local=1")
		http.Redirect(w, r, other+"/", http.StatusFound)
	}))
	defer start.Close()

	jar := cookies.New(cookies.Options{})
	c := newTestClient(t, TransportOptions{}).WithCookieJar(jar)
	resp, err := c.Send(t.Context(), get(start.URL+"/", on("Cookie", "row=1")))
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "" || resp.RequestCookie != "" {
		t.Errorf("other host got cookies %q (RequestCookie %q)", resp.Body, resp.RequestCookie)
	}
	if len(resp.Redirects) != 1 || resp.Redirects[0].RequestCookie != "row=1" {
		t.Errorf("first hop = %+v", resp.Redirects)
	}
	u, err := url.Parse(start.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if got := jar.Header(u); got != "local=1" {
		t.Errorf("jar for the first host = %q", got)
	}
}

// TestCookieJarPersisted sends through a jar from a Store, restarts the
// store and sends again.
func TestCookieJarPersisted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			w.Header().Add("Set-Cookie", "token=abc; Max-Age=3600; Path=/")
			w.Header().Add("Set-Cookie", "session=xyz")
		}
		io.WriteString(w, r.Header.Get("Cookie"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	base := newTestClient(t, TransportOptions{})

	store, err := cookies.Open(dir, cookies.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.WithCookieJar(store.Jar()).Send(t.Context(), get(srv.URL+"/set")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := cookies.Open(dir, cookies.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	resp, err := base.WithCookieJar(restarted.Jar()).Send(t.Context(), get(srv.URL+"/get"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Body); got != "token=abc; session=xyz" {
		t.Errorf("after restart the server got %q", got)
	}
}
