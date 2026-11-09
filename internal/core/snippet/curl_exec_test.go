package snippet

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
)

// received is a request as the test server read it.
type received struct {
	method, uri string
	header      http.Header
	body        []byte
}

// fixedJar is a cookie jar with fixed cookies that stores nothing.
type fixedJar []*http.Cookie

func (j fixedJar) Cookies(*url.URL) []*http.Cookie                      { return j }
func (fixedJar) SetCookieLines(*url.URL, []string) ([]string, []string) { return nil, nil }

// TestCurlSendsTheSameRequest runs the copied commands with the curl
// installed on the machine against a test server, and checks that the
// server receives what PostSonde's own client sends for the same request.
// It is skipped without curl.
func TestCurlSendsTheSameRequest(t *testing.T) {
	curlPath, err := exec.LookPath("curl")
	if err != nil || testing.Short() {
		t.Skip("curl is not installed, or -short")
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{method: r.Method, uri: r.RequestURI, header: r.Header.Clone(), body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	client := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	defer client.CloseIdleConnections()

	skip := map[string]bool{
		"digest_auth":        true, // needs a challenge
		"no_redirects_http2": true, // the test server has no h2c
	}
	for _, c := range curlCases() {
		if skip[c.name] || c.secrets != nil {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			local := func(u string) string {
				u = strings.Replace(u, "https://", "http://", 1)
				return strings.Replace(u, "api.test", host, 1)
			}
			if c.graphql != nil {
				g := *c.graphql
				g.URL = local(g.URL)
				c.graphql = &g
			} else {
				c.req.URL = local(c.req.URL)
			}
			cmd, _ := generate(t, c)
			b := buildCase(t, c)

			cl := client
			if c.cookies != nil {
				cl = client.WithCookieJar(fixedJar(c.cookies))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := cl.Send(ctx, b.Request); err != nil {
				t.Fatalf("send: %v", err)
			}
			want := <-got

			sh := exec.CommandContext(ctx, "sh", "-c", cmd+" -s -S -o /dev/null")
			sh.Env = []string{"PATH=" + strings.TrimSuffix(curlPath, "/curl")}
			if out, err := sh.CombinedOutput(); err != nil {
				t.Fatalf("curl: %v\n%s\ncommand:\n%s", err, out, cmd)
			}
			var have received
			select {
			case have = <-got:
			case <-ctx.Done():
				t.Fatalf("curl sent nothing:\n%s", cmd)
			}
			compareReceived(t, cmd, have, want)
		})
	}
}

// compareReceived checks that curl's request (have) is PostSonde's
// (want), apart from the headers curl adds of its own (User-Agent,
// Accept) when the request has none, and the multipart boundary.
func compareReceived(t *testing.T, cmd string, have, want received) {
	t.Helper()
	if have.method != want.method || have.uri != want.uri {
		t.Errorf("request line %s %s, want %s %s\n%s", have.method, have.uri, want.method, want.uri, cmd)
	}
	multi := strings.HasPrefix(want.header.Get("Content-Type"), "multipart/")
	for _, name := range []string{"User-Agent", "Accept"} {
		if _, ok := want.header[name]; !ok {
			delete(have.header, name)
		}
	}
	if multi {
		for _, h := range []http.Header{have.header, want.header} {
			mt, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
			h.Set("Content-Type", mt)
			h.Del("Content-Length")
		}
	}
	names := func(h http.Header) []string {
		var out []string
		for k, v := range h {
			out = append(out, k+": "+strings.Join(v, " | "))
		}
		slices.Sort(out)
		return out
	}
	if h, w := names(have.header), names(want.header); !slices.Equal(h, w) {
		t.Errorf("headers\n%s\nwant\n%s\ncommand:\n%s", strings.Join(h, "\n"), strings.Join(w, "\n"), cmd)
	}
	if !multi {
		if string(have.body) != string(want.body) {
			t.Errorf("body %q, want %q\n%s", have.body, want.body, cmd)
		}
		return
	}
	if h, w := parts(t, have), parts(t, want); !slices.Equal(h, w) {
		t.Errorf("parts\n%s\nwant\n%s\ncommand:\n%s", strings.Join(h, "\n"), strings.Join(w, "\n"), cmd)
	}
}

// parts describes the parts of a multipart request.
func parts(t *testing.T, r received) []string {
	t.Helper()
	raw := string(r.body)
	// The boundary is the first line without its leading "--".
	line, _, _ := strings.Cut(raw, "\r\n")
	partReader := multipart.NewReader(strings.NewReader(raw), strings.TrimPrefix(line, "--"))
	var out []string
	for {
		p, err := partReader.NextPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("multipart: %v", err)
		}
		data, _ := io.ReadAll(p)
		out = append(out, p.Header.Get("Content-Disposition")+" | "+p.Header.Get("Content-Type")+" | "+string(data))
	}
}
