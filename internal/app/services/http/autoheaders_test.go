package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// previewLines writes the sent fields of p as "Name: value [source]".
func previewLines(p HeaderPreviewDTO) []string {
	out := make([]string, len(p.Headers))
	for i, h := range p.Headers {
		out[i] = h.Name + ": " + h.Value + " [" + h.Source + "]"
	}
	return out
}

// HTTP-09: the preview lists the header fields the timeline then shows as
// sent, in the same order and masked the same way, and says where each
// comes from; turning an automatic header off or overriding it changes
// the sent request.
func TestHeaderPreviewMatchesTimeline(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	jar := cookies.New(cookies.Options{})
	if _, rejected := jar.SetCookieLines(&url.URL{Scheme: "http", Host: host, Path: "/"}, []string{"sid=abc; Path=/"}); len(rejected) > 0 {
		t.Fatalf("rejected %v", rejected)
	}

	src := httpSource(srv.URL + "/items")
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Own", Value: "{{token}}"}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{"a":1}`}
	src.Request.Settings = projectfile.RequestSettings{HTTPVersion: projectfile.HTTPVersion1}
	src.Auth = projectfile.Auth{Type: projectfile.AuthBearer, Token: "b3arer"}
	src.FolderHeaders = nil
	src.FolderVariables = [][]projectfile.Variable{{{Name: "token", Value: "s3cret", Secret: true}}}
	f := newFixture(t, src, fixedJar{jar})

	check := func(t *testing.T) HeaderPreviewDTO {
		t.Helper()
		p, err := f.svc.HeaderPreview(t.Context(), HeaderPreviewInputDTO{RequestID: "req_1"})
		if err != nil {
			t.Fatalf("HeaderPreview: %v", err)
		}
		id := f.send(t)
		if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
			t.Fatalf("done = %+v", done)
		}
		sent := f.rec.response(t, id).Timeline.Hops[0].Request.Headers
		if len(sent) != len(p.Headers) {
			t.Fatalf("preview %v\nsent %v", previewLines(p), sent)
		}
		for i, h := range sent {
			if p.Headers[i].Name != h.Name || p.Headers[i].Value != h.Value {
				t.Errorf("field %d: preview %s: %s, sent %s: %s", i, p.Headers[i].Name, p.Headers[i].Value, h.Name, h.Value)
			}
		}
		return p
	}

	p := check(t)
	want := []string{
		"Host: " + host + " [url]",
		"Content-Length: 7 [body]",
		"Authorization: Bearer " + httpclient.Mask + " [auth]",
		"Content-Type: application/json [body]",
		"Cookie: sid=abc [cookies]",
		"X-Own: " + httpclient.Mask + " [request]",
	}
	if got := previewLines(p); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("preview =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(p.Suppressed) != 0 || p.HTTPVersion != "http1" || p.Digest || len(p.Unresolved) != 0 {
		t.Errorf("preview = %+v", p)
	}
	if !p.Headers[2].Masked || p.Headers[0].Masked {
		t.Errorf("masked flags = %+v", p.Headers)
	}

	// Content-Type and the auth header off, cookies off, Host overridden.
	f.src.Request.Settings.OmitHeaders = []string{"Content-Type", "Authorization"}
	f.src.Request.Settings.SendCookies = new(bool)
	f.src.Request.Headers = append(f.src.Request.Headers, projectfile.KeyValue{Name: "Host", Value: "api.test"})
	p = check(t)
	want = []string{
		"Host: api.test [request]",
		"Content-Length: 7 [body]",
		"X-Own: " + httpclient.Mask + " [request]",
	}
	if got := previewLines(p); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("preview =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var suppressed []string
	for _, s := range p.Suppressed {
		suppressed = append(suppressed, s.Name+": "+s.Value+" ["+s.Source+" "+s.Reason+"]")
	}
	wantSup := []string{
		"Authorization: Bearer " + httpclient.Mask + " [auth omitted]",
		"Content-Type: application/json [body omitted]",
		"Host: " + host + " [url overridden]",
		"Cookie: sid=abc [cookies omitted]",
	}
	if strings.Join(suppressed, "\n") != strings.Join(wantSup, "\n") {
		t.Errorf("suppressed =\n%s\nwant\n%s", strings.Join(suppressed, "\n"), strings.Join(wantSup, "\n"))
	}

	if _, err := f.svc.HeaderPreview(t.Context(), HeaderPreviewInputDTO{RequestID: "nope"}); errs.KindOf(err) != errs.NotFound {
		t.Errorf("unknown request: %v", err)
	}
}

func TestHeaderPreviewUnresolved(t *testing.T) {
	t.Parallel()
	src := httpSource("https://api.test/")
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Missing", Value: "{{missing}}"}}
	src.Auth = projectfile.Auth{Type: projectfile.AuthDigest, Username: "user", Password: "pa55word"}
	f := newFixture(t, src, nil)
	p, err := f.svc.HeaderPreview(t.Context(), HeaderPreviewInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Digest || len(p.Unresolved) != 1 || p.Unresolved[0] != "missing" || p.HTTPVersion != "auto" {
		t.Errorf("preview = %+v", p)
	}
	want := []string{"Host: api.test [url]", "X-Missing: {{missing}} [request]"}
	if got := previewLines(p); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("preview = %v", got)
	}
}
