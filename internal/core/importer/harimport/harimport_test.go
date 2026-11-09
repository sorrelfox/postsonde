package harimport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/harimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/insomnia"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func read(t *testing.T, name string) importer.Source {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return importer.Source{Name: name, Data: data}
}

// parse reads src through a registry of every format, so detection is
// tested with the others around.
func parse(t *testing.T, src importer.Source) *importer.Model {
	t.Helper()
	m, im, err := importer.NewRegistry(curlimport.Importer{}, insomnia.Importer{}, postman.Importer{}, harimport.Importer{}).Parse(context.Background(), src, "")
	if err != nil {
		t.Fatal(err)
	}
	if im.Format().ID != "har" {
		t.Fatalf("detected %q", im.Format().ID)
	}
	return m
}

// dump renders a model as stable, readable JSON for the golden files:
// request files as PostSonde writes them.
func dump(t *testing.T, m *importer.Model) []byte {
	t.Helper()
	type diag struct {
		Code, Severity, Message, At string
	}
	at := func(l importer.Location) string {
		if l.IsZero() {
			return ""
		}
		return l.Path + " @" + strconv.Itoa(l.Line) + ":" + strconv.Itoa(l.Column)
	}
	diags := func(ds []importer.Diagnostic) []diag {
		var out []diag
		for _, d := range ds {
			out = append(out, diag{d.Code, string(d.Severity), d.Message, at(d.Source)})
		}
		return out
	}
	var item func(it importer.Item) any
	folder := func(f *importer.Folder) map[string]any {
		var items []any
		for _, it := range f.Items {
			items = append(items, item(it))
		}
		return map[string]any{"folder": f.Name, "at": at(f.Source), "diagnostics": diags(f.Diagnostics), "items": items}
	}
	item = func(it importer.Item) any {
		switch {
		case it.Folder != nil:
			return folder(it.Folder)
		case it.Request != nil:
			return map[string]any{
				"request": it.Request.Name, "at": at(it.Request.Source),
				"file": json.RawMessage(marshal(t, it.Request.File)), "diagnostics": diags(it.Request.Diagnostics),
			}
		default:
			s := it.Skipped
			return map[string]any{"skipped": s.Kind, "name": s.Name, "code": s.Code, "reason": s.Reason, "at": at(s.Source)}
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"name": m.Name, "root": folder(&m.Root), "diagnostics": diags(m.Diagnostics)}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func marshal(t *testing.T, f projectfile.File) []byte {
	t.Helper()
	switch v := f.(type) {
	case *projectfile.HTTPRequest:
		c := *v
		c.ID = "req_x"
		f = &c
	case *projectfile.GraphQLRequest:
		c := *v
		c.ID = "req_x"
		f = &c
	case *projectfile.WebSocketRequest:
		c := *v
		c.ID = "req_x"
		f = &c
	default:
		t.Fatalf("unexpected file %T", f)
	}
	data, err := projectfile.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGolden(t *testing.T) {
	for _, name := range []string{"chrome.har", "firefox.har", "hosts.har"} {
		t.Run(name, func(t *testing.T) {
			golden.Assert(t, name, dump(t, parse(t, read(t, name))))
		})
	}
}

// requests returns the model's requests by name, depth first.
func requests(items []importer.Item, out map[string]*importer.Request) map[string]*importer.Request {
	if out == nil {
		out = map[string]*importer.Request{}
	}
	for _, it := range items {
		switch {
		case it.Folder != nil:
			requests(it.Folder.Items, out)
		case it.Request != nil:
			out[it.Request.Name] = it.Request
		}
	}
	return out
}

func codes(ds []importer.Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return strings.Join(out, " ")
}

func TestChrome(t *testing.T) {
	m := parse(t, read(t, "chrome.har"))
	if m.Name != "chrome" || len(m.Root.Items) != 11 {
		t.Fatalf("name %q, %d items", m.Name, len(m.Root.Items))
	}
	if len(m.Diagnostics) != 1 || m.Diagnostics[0].Code != harimport.CodeFiltered ||
		!strings.Contains(m.Diagnostics[0].Message, "2 static resources") || !strings.Contains(m.Diagnostics[0].Message, "1 CORS preflight request") ||
		!strings.Contains(m.Diagnostics[0].Message, "1 repeated request") {
		t.Errorf("file diagnostics = %+v", m.Diagnostics)
	}
	reqs := requests(m.Root.Items, nil)

	doc := reqs["GET /cart"].File.(*projectfile.HTTPRequest)
	if doc.URL != "https://shop.example.com/cart" || len(doc.Headers) != 3 || doc.Headers[0].Name != "accept" {
		t.Errorf("document = %+v", doc)
	}
	if c := codes(reqs["GET /cart"].Diagnostics); c != harimport.CodeCookies {
		t.Errorf("document diagnostics = %s", c)
	}
	if msg := reqs["GET /cart"].Diagnostics[0].Message; !strings.Contains(msg, "(sid, theme)") || strings.Contains(msg, "chrome-session") {
		t.Errorf("cookie message = %s", msg)
	}

	cart := reqs["GET /v1/cart"].File.(*projectfile.HTTPRequest)
	if cart.Auth.Type != projectfile.AuthBearer || cart.Auth.Token != "eyJhbGciOiJIUzI1NiJ9.chrome-bearer-7f3a" || cart.Auth.Prefix != nil ||
		len(cart.Params) != 2 || cart.Params[1].Value != "items%2Ctotals" || cart.Description != "Recorded response: 200 OK (application/json)." {
		t.Errorf("cart = %+v", cart)
	}
	for _, h := range cart.Headers {
		if strings.EqualFold(h.Name, "authorization") {
			t.Errorf("authorization kept as a header: %+v", cart.Headers)
		}
	}

	g := reqs["ProductSearch"].File.(*projectfile.GraphQLRequest)
	if g.URL != "https://api.shop.example.com/graphql" || !strings.HasPrefix(g.Query, "query ProductSearch") || g.OperationName != "ProductSearch" ||
		g.Variables != "{\n  \"term\": \"t-shirt\",\n  \"first\": 10\n}" || len(g.Headers) != 1 || g.Auth.Type != projectfile.AuthBearer {
		t.Errorf("graphql = %+v", g)
	}

	form := reqs["POST /newsletter"].File.(*projectfile.HTTPRequest)
	if form.Body.Mode != projectfile.BodyURLEncoded || len(form.Body.Fields) != 2 || form.Body.Fields[0].Value != "ada@example.com" || form.Body.Fields[1].Value != "news & offers" {
		t.Errorf("form = %+v", form.Body)
	}

	up := reqs["POST /v1/avatars"]
	avatar := up.File.(*projectfile.HTTPRequest)
	if avatar.Body.Mode != projectfile.BodyMultipart || len(avatar.Body.Parts) != 2 || avatar.Body.Parts[0].File != "me.png" ||
		avatar.Body.Parts[0].ContentType != "image/png" || avatar.Body.Parts[1].Value != "Holiday photo" || len(avatar.Headers) != 0 {
		t.Errorf("avatar = %+v", avatar)
	}
	if codes(up.Diagnostics) != harimport.CodeFilePart {
		t.Errorf("avatar diagnostics = %s", codes(up.Diagnostics))
	}

	wsr := reqs["WebSocket /socket"]
	ws := wsr.File.(*projectfile.WebSocketRequest)
	if ws.URL != "wss://realtime.shop.example.com/socket?v=2" || strings.Join(ws.Subprotocols, ",") != "cart.v2,json" ||
		len(ws.Headers) != 1 || ws.Headers[0].Name != "Origin" || len(ws.SavedMessages) != 3 ||
		ws.SavedMessages[0].Format != projectfile.MessageJSON || ws.SavedMessages[1].Text != "ping" || ws.SavedMessages[2].Format != projectfile.MessageBase64 {
		t.Errorf("websocket = %+v", ws)
	}
	if codes(wsr.Diagnostics) != harimport.CodeCookies+" "+harimport.CodeWebSocket {
		t.Errorf("websocket diagnostics = %s", codes(wsr.Diagnostics))
	}

	admin := reqs["POST /v1/admin/reindex"]
	ah := admin.File.(*projectfile.HTTPRequest)
	if ah.Auth.Type != projectfile.AuthBasic || ah.Auth.Username != "admin" || ah.Auth.Password != "reindex-pass-42" {
		t.Errorf("admin auth = %+v", ah.Auth)
	}
	if codes(admin.Diagnostics) != harimport.CodeAuth+" "+harimport.CodeAuthDropped {
		t.Errorf("admin diagnostics = %s", codes(admin.Diagnostics))
	}
	for _, d := range admin.Diagnostics {
		if strings.Contains(d.Message, "chrome-api-key") || strings.Contains(d.Message, "reindex-pass") {
			t.Errorf("a message holds a secret: %s", d.Message)
		}
	}

	digest := reqs["GET /v1/reports/daily"]
	if dh := digest.File.(*projectfile.HTTPRequest); dh.Auth.Type != projectfile.AuthInherit || len(dh.Headers) != 0 || codes(digest.Diagnostics) != harimport.CodeAuthDropped {
		t.Errorf("digest = %+v %s", dh, codes(digest.Diagnostics))
	}

	// The data: URL is a skipped item, reported where it was.
	var skipped []*importer.Skipped
	for _, it := range m.Root.Items {
		if it.Skipped != nil {
			skipped = append(skipped, it.Skipped)
		}
	}
	if len(skipped) != 1 || skipped[0].Name != "GET data: URL" || skipped[0].Code != harimport.CodeUnsupported || skipped[0].Source.Path != "log.entries[11]" || skipped[0].Source.Line == 0 {
		t.Errorf("skipped = %+v", skipped)
	}
	if h := reqs["GET localhost:9999"]; h != nil {
		t.Errorf("health named by host: %+v", h)
	}
	if h := reqs["GET /health"]; h == nil || h.File.(*projectfile.HTTPRequest).Description != "" {
		t.Errorf("health = %+v", h)
	}
}

func TestFirefox(t *testing.T) {
	m := parse(t, read(t, "firefox.har"))
	if len(m.Root.Items) != 2 || m.Root.Items[0].Folder == nil || m.Root.Items[0].Folder.Name != "Checkout – Example Shop" || m.Root.Items[1].Folder.Name != "My account" {
		t.Fatalf("root = %+v", m.Root.Items)
	}
	if f := m.Root.Items[0].Folder; f.Source.Path != "log.pages[0]" || len(f.Items) != 3 {
		t.Errorf("checkout folder = %+v", f)
	}
	reqs := requests(m.Root.Items, nil)
	order := reqs["POST /v1/orders"].File.(*projectfile.HTTPRequest)
	if order.Auth.Type != projectfile.AuthBearer || order.Auth.Token != "ff-token-5d6e" || order.Auth.Prefix == nil || *order.Auth.Prefix != "Token" ||
		order.Body.Mode != projectfile.BodyRaw || order.Body.Type != projectfile.RawJSON || len(order.Headers) != 3 {
		t.Errorf("order = %+v", order)
	}
	addr := reqs["PUT /v1/orders/9001/address"].File.(*projectfile.HTTPRequest)
	if addr.Body.Mode != projectfile.BodyURLEncoded || len(addr.Body.Fields) != 2 || addr.Body.Fields[1].Value != "Zürich" || addr.URL != "https://api.shop.example.com/v1/orders/9001/address?notify=1" {
		t.Errorf("address = %+v", addr)
	}
	me := reqs["GET /v1/me"].File.(*projectfile.HTTPRequest)
	if me.Auth.Type != projectfile.AuthAPIKey || me.Auth.Key != "X-API-Key" || me.Auth.Value != "ff-api-key-91c2" || me.Auth.In != projectfile.APIKeyInHeader {
		t.Errorf("me auth = %+v", me.Auth)
	}
	docs := reqs["POST /v1/me/documents"].File.(*projectfile.HTTPRequest)
	if docs.Body.Mode != projectfile.BodyMultipart || len(docs.Body.Parts) != 2 || docs.Body.Parts[1].File != "invoice-2026-09.pdf" || docs.Body.Parts[1].ContentType != "application/pdf" {
		t.Errorf("documents = %+v", docs.Body)
	}
}

func TestHosts(t *testing.T) {
	m := parse(t, read(t, "hosts.har"))
	var names []string
	for _, it := range m.Root.Items {
		names = append(names, it.Folder.Name)
	}
	if strings.Join(names, ",") != "api.alpha.test:8080,beta.test,Other" {
		t.Errorf("folders = %v", names)
	}
	if len(m.Diagnostics) != 1 || m.Diagnostics[0].Code != harimport.CodeVersion {
		t.Errorf("diagnostics = %+v", m.Diagnostics)
	}
	reqs := requests(m.Root.Items, nil)
	if h := reqs["GET /status"]; h == nil || h.File.(*projectfile.HTTPRequest).Method != "GET" {
		t.Errorf("status = %+v", h)
	}
	blob := reqs["PUT /blobs/7"]
	if b := blob.File.(*projectfile.HTTPRequest).Body; b.Mode != projectfile.BodyNone || codes(blob.Diagnostics) != harimport.CodeBody {
		t.Errorf("blob = %+v %s", b, codes(blob.Diagnostics))
	}
}

func TestDetect(t *testing.T) {
	im := harimport.Importer{}
	cases := []struct {
		name, data string
		want       int
	}{
		{"x.har", `{"log": {"version": "1.2", "creator": {"name": "WebInspector"}, "entries": []}}`, importer.Sure},
		{"x.json", "\xef\xbb\xbf {\n \"log\": {\n  \"entries\": []}}", importer.Sure},
		{"x.har", `{"something": "else"}`, importer.Maybe},
		{"x.json", `{"info": {"name": "x"}, "item": []}`, importer.No},
		{"x.har", "curl https://x.test", importer.No},
		{"x.har", "", importer.No},
	}
	for _, c := range cases {
		if got := im.Detect(importer.Source{Name: c.name, Data: []byte(c.data)}); got != c.want {
			t.Errorf("Detect(%s, %q) = %d, want %d", c.name, c.data, got, c.want)
		}
	}
	// A HAR file is no other format's.
	for _, other := range []importer.Importer{curlimport.Importer{}, insomnia.Importer{}, postman.Importer{}} {
		if got := other.Detect(read(t, "chrome.har")); got >= importer.Likely {
			t.Errorf("%s detects chrome.har as %d", other.Format().ID, got)
		}
	}
}

func TestParseErrors(t *testing.T) {
	im := harimport.Importer{}
	cases := []struct {
		data, line, msg string
	}{
		{"{\n  \"log\": {\n    \"entries\": [\n      {\"request\": }\n    ]\n  }\n}", "4", "not valid JSON"},
		{"[1, 2]", "1", "no log object"},
		{`{"log": {"version": "1.2"}}`, "1", "no list of entries"},
		{`{"log": {"entries": {}}}`, "1", "no list of entries"},
		{"\xff\xfe", "", "UTF-8"},
	}
	for _, c := range cases {
		_, err := im.Parse(context.Background(), importer.Source{Data: []byte(c.data)})
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != importer.CodeInvalidFile || errs.KindOf(err) != errs.Parse || !strings.Contains(e.Message, c.msg) {
			t.Errorf("Parse(%q) = %v", c.data, err)
			continue
		}
		if e.Details["line"] != c.line {
			t.Errorf("Parse(%q) line = %q, want %q", c.data, e.Details["line"], c.line)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := im.Parse(ctx, importer.Source{Data: []byte(`{"log": {"entries": []}}`)}); err == nil {
		t.Error("Parse with a cancelled context succeeded")
	}
}
