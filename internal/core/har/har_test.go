package har_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/har"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

var t0 = time.Date(2026, 9, 18, 9, 30, 0, 123_000_000, time.UTC)

// entries are history entries of every shape: a JSON POST with timings on
// a new TLS connection, a reused connection after a redirect with cookies,
// a binary clipped response without timings (an older entry), a failure
// and a cancelled send.
func entries() []history.Entry {
	mask := httpclient.Mask
	return []history.Entry{
		{
			Summary: history.Summary{
				ID: "h_1", Time: t0, Kind: history.KindHTTP, RequestID: "req_1", RequestName: "Create order",
				Method: "POST", URL: "https://api.test/orders?dry=1&tag=a%20b", Outcome: history.OutcomeOK, StatusCode: 201, Status: "201 Created",
				DurationMs: 182, RequestSize: 18, ResponseSize: 11, Masked: true,
			},
			Request: history.RequestSnapshot{
				Method: "POST", URL: "https://api.test/orders?dry=1&tag=a%20b", Proto: "HTTP/2.0",
				Headers: []history.Header{
					{Name: "Authorization", Value: "Bearer " + mask},
					{Name: "Content-Type", Value: "application/json"},
				},
				Body: history.Body{Text: `{"sku":"A","n":2}` + "\n", Size: 18},
			},
			Response: &history.ResponseSnapshot{
				Proto: "HTTP/2.0", StatusCode: 201, Status: "201 Created", FinalURL: "https://api.test/orders?dry=1&tag=a%20b",
				Headers:     []history.Header{{Name: "Content-Type", Value: "application/json"}, {Name: "Location", Value: "/orders/9"}},
				ContentType: "application/json",
				Body:        history.Body{Text: `{"id":9}` + "\n\n\n", Size: 11},
				Timings:     &history.Timings{DNS: 12.5, Connect: 20.25, TLS: 31.125, Send: 0.5, Wait: 110, Receive: 2.75, Total: 182.2},
				RemoteAddr:  "192.0.2.7:443", LocalAddr: "10.0.0.2:53122",
			},
		},
		{
			Summary: history.Summary{
				ID: "h_2", Time: t0.Add(2 * time.Second), Kind: history.KindHTTP, RequestID: "req_2", RequestName: "Login form",
				Method: "POST", URL: "http://[2001:db8::1]:8080/login", Outcome: history.OutcomeOK, StatusCode: 200, Status: "200 OK",
				DurationMs: 40, RequestSize: 27, ResponseSize: 2,
			},
			Request: history.RequestSnapshot{
				Method: "POST", URL: "http://[2001:db8::1]:8080/login", Proto: "HTTP/1.1",
				Headers: []history.Header{
					{Name: "Content-Type", Value: "application/x-www-form-urlencoded"},
					{Name: "Cookie", Value: "sid=abc; theme=dark"},
				},
				Body: history.Body{Text: "user=ada&note=a+b%26c", Size: 21},
			},
			Response: &history.ResponseSnapshot{
				Proto: "HTTP/1.1", StatusCode: 200, Status: "200 OK", FinalURL: "http://[2001:db8::1]:8080/home", Redirects: 1,
				Headers: []history.Header{
					{Name: "Set-Cookie", Value: "sid=def; Path=/; Expires=Sat, 19 Sep 2026 10:00:00 GMT; HttpOnly; Secure"},
					{Name: "Content-Type", Value: "text/plain"},
				},
				ContentType: "text/plain",
				Body:        history.Body{Text: "ok", Size: 2},
				Timings:     &history.Timings{DNS: -1, Connect: -1, TLS: -1, Send: 0.1, Wait: 8, Receive: 0.2, Total: 40},
				RemoteAddr:  "[2001:db8::1]:8080", LocalAddr: "[2001:db8::2]:50000",
			},
		},
		{
			Summary: history.Summary{
				ID: "h_3", Time: t0.Add(3 * time.Second), Kind: history.KindReplay,
				Method: "GET", URL: "https://cdn.test/logo.png", Outcome: history.OutcomeOK, StatusCode: 200, Status: "200 OK",
				DurationMs: 55, ResponseSize: 70000,
			},
			Request: history.RequestSnapshot{Method: "GET", URL: "https://cdn.test/logo.png", Proto: "HTTP/1.1", Headers: []history.Header{}},
			Response: &history.ResponseSnapshot{
				Proto: "HTTP/1.1", StatusCode: 200, Status: "200 OK", FinalURL: "https://cdn.test/logo.png",
				Headers: []history.Header{}, Body: history.Body{Text: "iVBORw0KGgo=", Base64: true, Size: 70000, Clipped: true},
			},
			Trimmed: false,
		},
		{
			Summary: history.Summary{
				ID: "h_4", Time: t0.Add(4 * time.Second), Kind: history.KindGraphQL, RequestID: "req_3", RequestName: "Search",
				Method: "POST", URL: "https://down.test/graphql", Outcome: history.OutcomeError, ErrorCode: "http.connect_failed",
				Error: "Could not connect to down.test.",
			},
			Request: history.RequestSnapshot{Method: "POST", URL: "https://down.test/graphql", Headers: []history.Header{{Name: "Content-Type", Value: "application/json"}},
				Body: history.Body{Text: `{"query":"{ a }"}`, Size: 17}},
		},
		{
			Summary: history.Summary{
				ID: "h_5", Time: t0.Add(5 * time.Second), Kind: history.KindHTTP, RequestID: "req_4", RequestName: "Slow",
				Method: "GET", URL: "https://slow.test/", Outcome: history.OutcomeCancelled, DurationMs: 0,
			},
			Request: history.RequestSnapshot{Method: "GET", URL: "https://slow.test/", Headers: []history.Header{}},
		},
	}
}

func historyFile(es []history.Entry) *har.File {
	f := har.NewFile("1.4.0")
	page := f.AddPage("Orders", es[0].Time)
	for _, e := range es {
		he := har.FromHistory(e)
		he.Pageref = page
		f.Log.Entries = append(f.Log.Entries, he)
	}
	return f
}

func encode(t *testing.T, f *har.File) []byte {
	t.Helper()
	data, err := har.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	if problems := har.Validate(data); len(problems) > 0 {
		t.Fatalf("the file is not valid HAR 1.2:\n%s", strings.Join(problems, "\n"))
	}
	return data
}

func TestFromHistoryGolden(t *testing.T) {
	golden.Assert(t, "history.har", encode(t, historyFile(entries())))
}

func TestTimings(t *testing.T) {
	es := entries()
	e := har.FromHistory(es[0])
	tm := e.Timings
	// connect includes ssl; blocked is what no phase covers; time is the
	// sum and equals the recorded total.
	if tm.DNS != 12.5 || tm.SSL != 31.125 || tm.Connect != 51.375 || tm.Send != 0.5 || tm.Wait != 110 || tm.Receive != 2.75 {
		t.Errorf("timings = %+v", tm)
	}
	if tm.Blocked != 5.075 || e.Time != 182.2 {
		t.Errorf("blocked %v, time %v", tm.Blocked, e.Time)
	}
	if e.ServerIPAddress != "192.0.2.7" || e.Connection != "53122" {
		t.Errorf("server %q connection %q", e.ServerIPAddress, e.Connection)
	}
	// A reused connection: no dns, connect or ssl; the redirect's hop is
	// in blocked.
	e = har.FromHistory(es[1])
	if e.Timings.DNS != -1 || e.Timings.Connect != -1 || e.Timings.SSL != -1 || e.Timings.Blocked != 31.7 || e.Time != 40 {
		t.Errorf("reused timings = %+v, time %v", e.Timings, e.Time)
	}
	if e.ServerIPAddress != "2001:db8::1" || e.Connection != "50000" || !strings.Contains(e.Comment, "Followed 1 redirect to http://[2001:db8::1]:8080/home") {
		t.Errorf("entry = %+v", e)
	}
	// Without recorded phases the duration is all wait.
	e = har.FromHistory(es[2])
	if e.Timings.Wait != 55 || e.Timings.Blocked != -1 || e.Time != 55 {
		t.Errorf("old entry timings = %+v", e.Timings)
	}
}

func TestFromHistory(t *testing.T) {
	es := entries()
	e := har.FromHistory(es[1])
	if len(e.Request.Cookies) != 2 || e.Request.Cookies[1].Name != "theme" || e.Request.PostData == nil ||
		len(e.Request.PostData.Params) != 2 || e.Request.PostData.Params[1].Value != "a b&c" {
		t.Errorf("request = %+v", e.Request)
	}
	if c := e.Response.Cookies; len(c) != 1 || c[0].Value != "def" || !c[0].HTTPOnly || !c[0].Secure || c[0].Expires != "2026-09-19T10:00:00.000Z" {
		t.Errorf("response cookies = %+v", c)
	}
	if q := har.FromHistory(es[0]).Request.QueryString; len(q) != 2 || q[1].Value != "a b" {
		t.Errorf("query = %+v", q)
	}
	bin := har.FromHistory(es[2]).Response.Content
	if bin.Encoding != "base64" || bin.Size != 70000 || !strings.Contains(bin.Comment, "clipped") || bin.MimeType != "x-unknown" {
		t.Errorf("binary content = %+v", bin)
	}
	fail := har.FromHistory(es[3])
	if fail.Response.Status != 0 || fail.Error != "Could not connect to down.test." || fail.Response.BodySize != -1 {
		t.Errorf("failure = %+v", fail)
	}
	if c := har.FromHistory(es[4]); c.Error != "Cancelled." {
		t.Errorf("cancelled error = %q", c.Error)
	}
	if r := har.FromHistory(es[0]).Response; r.StatusText != "Created" || r.RedirectURL != "" {
		t.Errorf("status text %q", r.StatusText)
	}
}

func TestFromRequest(t *testing.T) {
	r := snippet.Request{
		Method: "POST",
		URL:    "https://api.test/upload?key=k3y",
		Headers: []snippet.Header{
			{Name: "Authorization", Value: "Bearer t0ken"},
			{Name: "X-Trace", Value: "1"},
		},
		Cookies: "sid=abc",
		Basic:   &snippet.Credentials{Username: "ada", Password: "pa55"},
		Body: snippet.Body{Kind: snippet.BodyMultipart, Parts: []snippet.Part{
			{Name: "note", Value: "hello"},
			{Name: "file", File: "/home/ada/secret/report.pdf", Filename: "report.pdf", ContentType: "application/pdf"},
		}},
		HTTPVersion: httpclient.HTTPVersion2,
	}
	masked, changed := r.Masked([]string{"k3y"})
	if !changed {
		t.Fatal("nothing masked")
	}
	f := har.NewFile("")
	page := f.AddPage("Uploads", t0)
	for _, x := range []struct {
		r    snippet.Request
		name string
	}{{masked, "Upload (masked)"}, {r, "Upload (with secrets)"}} {
		e := har.FromRequest(x.r, t0, x.name)
		e.Pageref = page
		f.Log.Entries = append(f.Log.Entries, e)
	}
	data := encode(t, f)
	golden.Assert(t, "request.har", data)

	m := f.Log.Entries[0]
	for _, secret := range []string{"k3y", "t0ken", "pa55", "/home/ada"} {
		if strings.Contains(string(mustEncodeEntry(t, m)), secret) {
			t.Errorf("the masked entry holds %q", secret)
		}
	}
	if f.Log.Creator.Name != "PostSonde" || f.Log.Creator.Version != "dev" || f.Log.Version != "1.2" {
		t.Errorf("log = %+v", f.Log)
	}
	names := func(e har.Entry) []string {
		var out []string
		for _, h := range e.Request.Headers {
			out = append(out, h.Name+": "+h.Value)
		}
		return out
	}
	if h := names(f.Log.Entries[1]); !slices.Contains(h, "Authorization: Basic YWRhOnBhNTU=") || !slices.Contains(h, "Cookie: sid=abc") {
		t.Errorf("headers = %v", h)
	}
	if h := names(m); !slices.Contains(h, "Authorization: Basic "+httpclient.Mask) {
		t.Errorf("masked headers = %v", h)
	}

	raw := har.FromRequest(snippet.Request{Method: "PUT", URL: "https://api.test/x", Body: snippet.Body{Kind: snippet.BodyData, Data: []byte("a=1&b=%20"), ContentType: "application/x-www-form-urlencoded"},
		Digest: &snippet.Credentials{Username: "u", Password: snippet.Mask}}, t0, "")
	if pd := raw.Request.PostData; pd == nil || pd.MimeType != "application/x-www-form-urlencoded" || len(pd.Params) != 2 || pd.Params[1].Value != " " || raw.Request.BodySize != 9 {
		t.Errorf("form post data = %+v", raw.Request)
	}
	if !strings.Contains(raw.Comment, "Digest") {
		t.Errorf("digest comment = %q", raw.Comment)
	}
}

func mustEncodeEntry(t *testing.T, e har.Entry) []byte {
	t.Helper()
	f := har.NewFile("x")
	f.Log.Entries = append(f.Log.Entries, e)
	data, err := har.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestValidate(t *testing.T) {
	cases := []struct {
		data string
		want []string
	}{
		{`[]`, []string{"log: missing"}},
		{`not json`, []string{"the file is not JSON"}},
		{`{"log": {"version": "1.2", "creator": {"name": "x"}, "entries": [{"startedDateTime": "t", "time": 1,
			"request": {"method": "GET", "url": "u", "httpVersion": "h", "cookies": [], "headers": [{"name": "a"}], "queryString": [], "headersSize": -1, "bodySize": 0},
			"response": {"status": 200, "statusText": "", "httpVersion": "h", "cookies": [], "headers": [], "content": {"size": 0}, "redirectURL": "", "headersSize": -1, "bodySize": 0},
			"cache": {}, "timings": {"send": -1, "wait": 0, "receive": 0}}]}}`,
			[]string{"log.creator.version: missing", "log.entries[0].request.headers[0].value: missing", "log.entries[0].response.content.mimeType: missing", "log.entries[0].timings.send: is negative"}},
	}
	for _, c := range cases {
		got := har.Validate([]byte(c.data))
		if len(got) != len(c.want) {
			t.Errorf("Validate(%.30s) = %q, want %q", c.data, got, c.want)
			continue
		}
		for i := range got {
			if !strings.HasPrefix(got[i], c.want[i]) {
				t.Errorf("Validate(%.30s)[%d] = %q, want %q", c.data, i, got[i], c.want[i])
			}
		}
	}
}

func TestFileName(t *testing.T) {
	for in, want := range map[string]string{"Orders / v2": "Orders - v2.har", "": "requests.har", " ..a:b.. ": "a-b.har", "Ünïcode": "Ünïcode.har"} {
		if got := har.FileName(in); got != want {
			t.Errorf("FileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.har")
	if err := har.WriteFile(path, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{}" {
		t.Errorf("file = %q, %v", data, err)
	}
	if err := har.WriteFile(filepath.Join(dir, "missing", "x.har"), nil); err == nil {
		t.Error("writing into a missing folder succeeded")
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 {
		t.Errorf("files left: %v", left)
	}
}

// FromExchange says why a body of a saved exchange is cut: the request's
// at what the client records, the response's at the size limit.
func TestFromExchangeComments(t *testing.T) {
	t.Parallel()
	e := history.Entry{
		Summary: history.Summary{Time: t0, Method: "PUT", URL: "https://api.test/up", Outcome: history.OutcomeOK, StatusCode: 200},
		Request: history.RequestSnapshot{Method: "PUT", URL: "https://api.test/up",
			Body: history.Body{Text: "abc", Size: 5 << 20, Clipped: true}},
		Response: &history.ResponseSnapshot{StatusCode: 200, Status: "200 OK", Truncated: true,
			Body: history.Body{Text: "whole", Size: 5}},
	}
	got := har.FromExchange(e)
	if c := got.Request.PostData.Comment; !strings.Contains(c, "1024 KiB PostSonde records") {
		t.Errorf("request comment = %q", c)
	}
	if c := got.Response.Content.Comment; c != "The body was cut at the client's size limit." {
		t.Errorf("response comment = %q", c)
	}
	if got.Response.Content.Text != "whole" || got.Response.Content.Size != 5 {
		t.Errorf("content = %+v", got.Response.Content)
	}
}
