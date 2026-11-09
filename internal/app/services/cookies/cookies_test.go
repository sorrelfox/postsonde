package cookies

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/workspace"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || errs.KindOf(err) != kind || e.Code != code || e.Hint == "" {
		t.Fatalf("err = %v, want kind %s code %q and a hint", err, kind, code)
	}
}

// setup opens a new project and returns its manager, the Jars and the
// service over them.
func setup(t *testing.T) (*workspace.Manager, *Jars, *CookiesService) {
	t.Helper()
	m := workspace.New(tempDir(t))
	if _, err := m.Create(filepath.Join(tempDir(t), "shop-api"), "Shop API"); err != nil {
		t.Fatal(err)
	}
	jars := NewJars(core.NewRegistry(core.Options{SaveDelay: time.Hour}), m)
	t.Cleanup(func() { _ = jars.CloseAll() })
	return m, jars, New(jars, nil)
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestNoWorkspace(t *testing.T) {
	t.Parallel()
	jars := NewJars(core.NewRegistry(core.Options{}), workspace.New(tempDir(t)))
	svc := New(jars, nil)
	ctx := t.Context()
	_, err := svc.List(ctx)
	checkCode(t, err, errs.NotFound, workspace.CodeNoWorkspace)
	_, err = svc.Set(ctx, SetInputDTO{Cookie: CookieDTO{Name: "a", Value: "1", Domain: "example.com"}})
	checkCode(t, err, errs.NotFound, workspace.CodeNoWorkspace)
	_, err = jars.Header(mustURL(t, "http://example.com/"))
	checkCode(t, err, errs.NotFound, workspace.CodeNoWorkspace)
}

// TestManagerEditsReachTheNextRequest checks CK-02: cookies
// added, edited and deleted through the service are what the next request
// sends, whether the send path uses the Cookie header from Jars or the jar
// attached to the HTTP client.
func TestManagerEditsReachTheNextRequest(t *testing.T) {
	t.Parallel()
	_, jars, svc := setup(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.Header().Add("Set-Cookie", "session=s1; Path=/")
		}
		io.WriteString(w, r.Header.Get("Cookie"))
	}))
	defer srv.Close()
	u := mustURL(t, srv.URL+"/api/items")
	host := u.Hostname()

	send := func(path string) string {
		t.Helper()
		jar, err := jars.Jar()
		if err != nil {
			t.Fatal(err)
		}
		c := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{}).WithCookieJar(jar)
		defer c.CloseIdleConnections()
		resp, err := c.Send(ctx, httpclient.Request{Method: http.MethodGet, URL: srv.URL + path, Settings: httpclient.DefaultSettings()})
		if err != nil {
			t.Fatal(err)
		}
		return string(resp.Body)
	}
	header := func() string {
		t.Helper()
		h, err := jars.Header(u)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	// A response stores a cookie; the manager lists it.
	if got := send("/login"); got != "" {
		t.Fatalf("first request sent %q", got)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Cookies) != 1 || list.Cookies[0].Name != "session" || list.Cookies[0].Domain != host || !list.Cookies[0].HostOnly || list.Problem != nil {
		t.Fatalf("list = %+v", list)
	}
	session := list.Cookies[0]

	// Edit its value: the next request sends the new one.
	edited := session
	edited.Value = "edited"
	got, err := svc.Set(ctx, SetInputDTO{Key: session.Key, Cookie: edited})
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != session.Key || got.Created != session.Created {
		t.Fatalf("edited = %+v, was %+v", got, session)
	}
	if h := header(); h != "session=edited" {
		t.Fatalf("header after edit = %q", h)
	}
	if body := send("/api/items"); body != "session=edited" {
		t.Fatalf("request after edit sent %q", body)
	}

	// Add one for a path: sent first on that path only.
	added, err := svc.Set(ctx, SetInputDTO{Cookie: CookieDTO{Name: "theme", Value: "dark", Domain: host, HostOnly: true, Path: "/api", SameSite: "Lax"}})
	if err != nil {
		t.Fatal(err)
	}
	if h := header(); h != "theme=dark; session=edited" {
		t.Fatalf("header after add = %q", h)
	}
	if body := send("/other"); body != "session=edited" {
		t.Fatalf("request outside the path sent %q", body)
	}

	// Rename it (a new identity): the old name is no longer sent.
	renamed := added
	renamed.Name = "mode"
	renamed, err = svc.Set(ctx, SetInputDTO{Key: added.Key, Cookie: renamed})
	if err != nil {
		t.Fatal(err)
	}
	if h := header(); h != "mode=dark; session=edited" {
		t.Fatalf("header after rename = %q", h)
	}

	// Delete one, then clear the domain.
	if err := svc.Delete(ctx, session.Key); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, session.Key); err != nil {
		t.Fatalf("deleting again: %v", err)
	}
	if body := send("/api/items"); body != "mode=dark" {
		t.Fatalf("request after delete sent %q", body)
	}
	if n, err := svc.DeleteDomain(ctx, host); err != nil || n != 1 {
		t.Fatalf("DeleteDomain = %d, %v", n, err)
	}
	if body := send("/api/items"); body != "" {
		t.Fatalf("request after clearing the domain sent %q", body)
	}

	// Set-Cookie through Jars (the send path's other entry point), then
	// Clear.
	stored, rejected, err := jars.SetCookieLines(u, []string{"a=1", "b=2; Domain=example.org"})
	if err != nil || len(stored) != 1 || len(rejected) != 1 || rejected[0] != "b: domain_mismatch" {
		t.Fatalf("SetCookieLines = %q, %q, %v", stored, rejected, err)
	}
	if err := svc.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if h := header(); h != "" {
		t.Fatalf("header after clear = %q", h)
	}
}

func TestSetErrors(t *testing.T) {
	t.Parallel()
	_, _, svc := setup(t)
	ctx := t.Context()

	_, err := svc.Set(ctx, SetInputDTO{Cookie: CookieDTO{Name: "a", Value: "1", Domain: "example.com", Expires: "tomorrow"}})
	checkCode(t, err, errs.InvalidArgument, core.CodeInvalidCookie)
	_, err = svc.Set(ctx, SetInputDTO{Cookie: CookieDTO{Name: "a;", Value: "1", Domain: "example.com"}})
	checkCode(t, err, errs.InvalidArgument, core.CodeInvalidCookie)
	_, err = svc.Set(ctx, SetInputDTO{Key: "not a key!", Cookie: CookieDTO{Name: "a", Value: "1", Domain: "example.com"}})
	checkCode(t, err, errs.InvalidArgument, CodeInvalidKey)
	checkCode(t, svc.Delete(ctx, "%%"), errs.InvalidArgument, CodeInvalidKey)

	a, err := svc.Set(ctx, SetInputDTO{Cookie: CookieDTO{Name: "a", Value: "1", Domain: "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Set(ctx, SetInputDTO{Cookie: CookieDTO{Name: "a", Value: "2", Domain: "example.com"}})
	checkCode(t, err, errs.AlreadyExists, core.CodeCookieExists)
	if err := svc.Delete(ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Set(ctx, SetInputDTO{Key: a.Key, Cookie: CookieDTO{Name: "a", Value: "2", Domain: "example.com"}})
	checkCode(t, err, errs.NotFound, core.CodeCookieNotFound)
}

func TestExpiresAndPersistence(t *testing.T) {
	t.Parallel()
	m, jars, svc := setup(t)
	ctx := t.Context()
	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	c, err := svc.Set(ctx, SetInputDTO{Cookie: CookieDTO{
		Name: "p", Value: "1", Domain: ".Example.com", Path: "/v1", Secure: true, HTTPOnly: true, SameSite: "Strict", Expires: expires,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Domain != "example.com" || c.HostOnly || c.Expires != expires || c.Created == "" || c.Binary {
		t.Fatalf("stored %+v", c)
	}
	s, err := svc.Set(ctx, SetInputDTO{Cookie: CookieDTO{Name: "s", Value: "1", Domain: "example.com"}})
	if err != nil || s.Expires != "" {
		t.Fatalf("session cookie %+v, %v", s, err)
	}

	// Saved in the project's local directory and loaded again.
	if err := jars.CloseAll(); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.LocalDir()
	data, err := os.ReadFile(filepath.Join(dir, core.FileName))
	if err != nil || !strings.Contains(string(data), `"name": "p"`) {
		t.Fatalf("cookie file: %s, %v", data, err)
	}
	list, err := svc.List(ctx)
	if err != nil || len(list.Cookies) != 2 || list.Cookies[1].Expires != expires {
		t.Fatalf("list after reopening = %+v, %v", list, err)
	}
}

func TestProblem(t *testing.T) {
	t.Parallel()
	m, _, svc := setup(t)
	dir, _ := m.LocalDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, core.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if list.Problem == nil || list.Problem.Code != core.CodeFileInvalid || list.Problem.Hint == "" || len(list.Cookies) != 0 {
		t.Fatalf("list = %+v", list)
	}
}

func TestKeys(t *testing.T) {
	t.Parallel()
	c := core.Cookie{Name: "n\xff", Domain: "example.com", Path: "/a", HostOnly: true}
	d := toCookieDTO(c)
	if !d.Binary {
		t.Error("binary cookie not flagged")
	}
	back, err := decodeKey(d.Key)
	if err != nil || back.Name != c.Name || back.Domain != c.Domain || back.Path != c.Path || !back.HostOnly {
		t.Fatalf("decodeKey = %+v, %v", back, err)
	}
}
