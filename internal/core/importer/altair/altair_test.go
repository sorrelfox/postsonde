package altair_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/altair"
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/harimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/insomnia"
	"github.com/sorrelfox/postsonde/internal/core/importer/openapiimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// The fixtures are reconstructed from Altair's export format (window
// exports of Altair 4 and 5, collection exports of Altair 5 and 8, an
// environment), not files saved by Altair itself.

func read(t *testing.T, name string) importer.Source {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return importer.Source{Name: name, Data: data}
}

// registry holds every format, in the order the app registers them, so
// detection is tested with the others around.
func registry() *importer.Registry {
	return importer.NewRegistry(curlimport.Importer{}, insomnia.Importer{}, postman.Importer{}, openapiimport.Importer{},
		harimport.Importer{}, altair.Importer{})
}

func parse(t *testing.T, src importer.Source) *importer.Model {
	t.Helper()
	m, im, err := registry().Parse(context.Background(), src, "")
	if err != nil {
		t.Fatal(err)
	}
	if im.Format().ID != "altair" {
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
		b, _ := json.Marshal([]int{l.Line, l.Column})
		return l.Path + " @" + string(b)
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
		auth, err := json.Marshal(f.Auth)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{
			"folder": f.Name, "description": f.Description, "variables": f.Variables, "headers": f.Headers,
			"auth": json.RawMessage(auth), "at": at(f.Source), "diagnostics": diags(f.Diagnostics), "items": items,
		}
	}
	item = func(it importer.Item) any {
		switch {
		case it.Folder != nil:
			return folder(it.Folder)
		case it.Request != nil:
			g, ok := it.Request.File.(*projectfile.GraphQLRequest)
			if !ok {
				t.Fatalf("unexpected file %T", it.Request.File)
			}
			c := *g
			c.ID = "req_x"
			data, err := projectfile.Marshal(&c)
			if err != nil {
				t.Fatal(err)
			}
			return map[string]any{
				"request": it.Request.Name, "at": at(it.Request.Source),
				"file": json.RawMessage(data), "diagnostics": diags(it.Request.Diagnostics),
			}
		default:
			s := it.Skipped
			return map[string]any{"skipped": s.Kind, "name": s.Name, "code": s.Code, "reason": s.Reason, "at": at(s.Source)}
		}
	}
	var envs []any
	for _, e := range m.Environments {
		envs = append(envs, map[string]any{"environment": e.Name, "variables": e.Variables, "at": at(e.Source), "diagnostics": diags(e.Diagnostics)})
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"name": m.Name, "root": folder(&m.Root), "environments": envs, "diagnostics": diags(m.Diagnostics),
	}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestGolden(t *testing.T) {
	for _, name := range []string{
		"starwars-v4.agq", "github-v5.agq", "shop-v8.agc", "countries-flat-v5.agc", "staging-environment.json",
	} {
		t.Run(name, func(t *testing.T) {
			golden.Assert(t, name, dump(t, parse(t, read(t, name))))
		})
	}
}

// find returns the request, folder or skipped item named name, depth
// first.
func find(items []importer.Item, name string) *importer.Item {
	for i := range items {
		it := &items[i]
		if it.Request != nil && it.Request.Name == name || it.Folder != nil && it.Folder.Name == name || it.Skipped != nil && it.Skipped.Name == name {
			return it
		}
		if it.Folder != nil {
			if f := find(it.Folder.Items, name); f != nil {
				return f
			}
		}
	}
	return nil
}

func gql(t *testing.T, m *importer.Model, name string) (*projectfile.GraphQLRequest, *importer.Request) {
	t.Helper()
	it := find(m.Root.Items, name)
	if it == nil || it.Request == nil {
		t.Fatalf("no request %q", name)
	}
	g, ok := it.Request.File.(*projectfile.GraphQLRequest)
	if !ok {
		t.Fatalf("%q is a %T", name, it.Request.File)
	}
	return g, it.Request
}

func codes(ds []importer.Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return strings.Join(out, " ")
}

// outline lists a folder's items depth first: "Folder/", request names,
// "-skipped kind".
func outline(items []importer.Item, indent string) string {
	var b strings.Builder
	for _, it := range items {
		switch {
		case it.Folder != nil:
			b.WriteString(indent + it.Folder.Name + "/\n")
			b.WriteString(outline(it.Folder.Items, indent+"  "))
		case it.Request != nil:
			b.WriteString(indent + it.Request.Name + "\n")
		default:
			b.WriteString(indent + "-" + it.Skipped.Kind + "\n")
		}
	}
	return b.String()
}

func TestWindow(t *testing.T) {
	m := parse(t, read(t, "starwars-v4.agq"))
	if m.Name != "Star Wars films" || len(m.Root.Items) != 1 {
		t.Fatalf("model = %q, %d items", m.Name, len(m.Root.Items))
	}
	g, r := gql(t, m, "Star Wars films")
	if g.URL != "https://swapi-graphql.example/graphql" || !strings.HasPrefix(g.Query, "query AllFilms") || g.Variables != "{\n  \"first\": 3\n}" || g.Method != "" {
		t.Errorf("request = %+v", g)
	}
	if len(g.Headers) != 1 || g.Headers[0].Name != "X-Client" || g.Headers[0].Value != "altair" || g.Headers[0].Disabled || g.Auth.Type != projectfile.AuthInherit {
		t.Errorf("headers %+v, auth %+v", g.Headers, g.Auth)
	}
	// The subscription URL is the endpoint's: nothing to say. The script
	// is kept, turned off.
	if codes(r.Diagnostics) != altair.CodeScript || !strings.Contains(g.Description, "turned off in Altair") {
		t.Errorf("diagnostics %s, description %q", codes(r.Diagnostics), g.Description)
	}
	if r.Source.Line != 1 || r.Source.Column != 1 {
		t.Errorf("source = %+v", r.Source)
	}

	m = parse(t, read(t, "github-v5.agq"))
	g, r = gql(t, m, "Viewer repositories")
	if g.URL != "{{baseUrl}}/graphql" || g.OperationName != "Viewer" || g.Variables != "{\n  \"count\": {{count}}\n}" ||
		g.ConnectionParams != "{\n  \"authToken\": \"{{token}}\"\n}" {
		t.Errorf("request = %+v", g)
	}
	if len(g.Headers) != 2 || !g.Headers[1].Disabled || g.Headers[0].Disabled {
		t.Errorf("headers = %+v", g.Headers)
	}
	if g.Auth.Type != projectfile.AuthBearer || g.Auth.Token != "{{token}}" {
		t.Errorf("auth = %+v", g.Auth)
	}
	if got := codes(r.Diagnostics); got != "altair.subscription_url altair.transport altair.script" {
		t.Errorf("diagnostics = %s", got)
	}
	if !strings.Contains(g.Description, "<wss://stream.github-like.example/subscriptions>") || !strings.Contains(g.Description, "post-request script (not run") {
		t.Errorf("description = %q", g.Description)
	}
}

func TestCollection(t *testing.T) {
	m := parse(t, read(t, "shop-v8.agc"))
	want := "Orders/\n  Returns/\n    ReturnReasons\n    Upload label\n  Create order\n  Order updates\nCustomers/\n  Customer\nHealth\n-query\n"
	if got := outline(m.Root.Items, ""); got != want {
		t.Errorf("outline =\n%s\nwant\n%s", got, want)
	}
	if m.Name != "Shop API" || !strings.HasPrefix(m.Root.Description, "GraphQL API of the shop.\n\nAltair pre-request script") || codes(m.Root.Diagnostics) != altair.CodeScript {
		t.Errorf("root = %q %q %s", m.Name, m.Root.Description, codes(m.Root.Diagnostics))
	}

	create, r := gql(t, m, "Create order")
	if create.Auth.Type != projectfile.AuthBasic || create.Auth.Username != "{{user}}" || create.Auth.Password != "hunter2" {
		t.Errorf("auth = %+v", create.Auth)
	}
	if !strings.Contains(create.Extensions, "sha256Hash") || create.ConnectionParams != "" || create.Headers[1].Name != "X-Trace" || !create.Headers[1].Disabled {
		t.Errorf("create order = %+v", create)
	}
	if codes(r.Diagnostics) != altair.CodeSubscriptionURL || r.Source.Path != "collections[0].queries[0]" {
		t.Errorf("create order: %s at %+v", codes(r.Diagnostics), r.Source)
	}

	updates, r := gql(t, m, "Order updates")
	if updates.Auth.Type != projectfile.AuthAPIKey || updates.Auth.Key != "X-Api-Key" || updates.Auth.Value != "sk-live-order-feed" || updates.Auth.In != projectfile.APIKeyInHeader {
		t.Errorf("api key auth = %+v", updates.Auth)
	}
	if codes(r.Diagnostics) != "altair.subscription_url altair.transport" {
		t.Errorf("order updates: %s", codes(r.Diagnostics))
	}

	// OAuth 2.0 is not imported, and its token never reaches the model.
	reasons, r := gql(t, m, "ReturnReasons")
	if reasons.Auth.Type != projectfile.AuthNone || reasons.Method != "GET" || codes(r.Diagnostics) != altair.CodeAuthUnsupported {
		t.Errorf("return reasons = %+v, %s", reasons, codes(r.Diagnostics))
	}
	if bytes.Contains(dump(t, m), []byte("oauth-token-should-not-appear")) {
		t.Error("the OAuth token is in the model")
	}
	// File variables become upload rows without files; a list gets a row
	// per file, and the editor's empty row is left out.
	upload, r := gql(t, m, "Upload label")
	if codes(r.Diagnostics) != "altair.method altair.transport altair.files" || r.Diagnostics[2].Severity != importer.SeverityInfo {
		t.Errorf("upload label: %s", codes(r.Diagnostics))
	}
	var paths []string
	for _, f := range upload.Files {
		if f.File != "" || f.Disabled {
			t.Errorf("upload row %+v", f)
		}
		paths = append(paths, f.Name)
	}
	if got := strings.Join(paths, " "); got != "variables.file variables.docs.0 variables.docs.1" {
		t.Errorf("upload paths = %s", got)
	}
	customer, r := gql(t, m, "Customer")
	if customer.Auth.Type != projectfile.AuthInherit || len(r.Diagnostics) != 0 || customer.Headers[0].Value != "Bearer {{token}}" {
		t.Errorf("customer = %+v, %s", customer, codes(r.Diagnostics))
	}
}

// Sub-collections listed flat go into the collection their parentPath
// names; a missing parent or a loop leaves them where they are listed.
func TestFlatCollections(t *testing.T) {
	m := parse(t, read(t, "countries-flat-v5.agc"))
	want := "Continents/\n  Cities/\n    City\nLost/\nLoop A/\n  Loop B/\nAll countries\n"
	if got := outline(m.Root.Items, ""); got != want {
		t.Errorf("outline =\n%s\nwant\n%s", got, want)
	}
	lost := find(m.Root.Items, "Lost").Folder
	if codes(lost.Diagnostics) != altair.CodeOrphan {
		t.Errorf("lost: %s", codes(lost.Diagnostics))
	}
	loop := find(m.Root.Items, "Loop A").Folder
	if codes(loop.Diagnostics) != altair.CodeOrphan {
		t.Errorf("loop: %s", codes(loop.Diagnostics))
	}
}

func TestEnvironment(t *testing.T) {
	m := parse(t, read(t, "staging-environment.json"))
	if len(m.Environments) != 1 || m.Name != "Staging" {
		t.Fatalf("model = %+v", m)
	}
	vars := map[string]string{}
	for _, v := range m.Environments[0].Variables {
		vars[v.Name] = v.Value
	}
	if vars["count"] != "5" || vars["debug"] != "false" || vars["missing"] != "" || vars["auth"] != `{"token":"stg-token","scopes":["read","write"]}` {
		t.Errorf("variables = %v", vars)
	}

	// Altair's own environment state: the variables as JSON text.
	m = parse(t, importer.Source{Name: "env.json", Data: []byte(`{"title": "Local", "variablesJson": "{\n  \"host\": \"localhost\"\n}"}`)})
	if m.Environments[0].Name != "Local" || len(m.Environments[0].Variables) != 1 || m.Environments[0].Variables[0].Value != "localhost" {
		t.Errorf("state = %+v", m.Environments)
	}
	m = parse(t, importer.Source{Name: "env.json", Data: []byte(`{"version": 1, "type": "environment", "title": "Bad", "variablesJson": "[1]"}`)})
	if len(m.Environments[0].Variables) != 0 || codes(m.Environments[0].Diagnostics) != altair.CodeUnsupported {
		t.Errorf("bad = %+v", m.Environments)
	}
}

func TestDetect(t *testing.T) {
	im := altair.Importer{}
	cases := []struct {
		name, data string
		want       int
	}{
		{"x.json", `{"version": 1, "type": "window", "windowName": "a", "apiUrl": "https://x.test"}`, importer.Sure},
		{"x.json", "\xef\xbb\xbf{\"version\": 1, \"type\": \"collection\", \"title\": \"a\", \"queries\": []}", importer.Sure},
		{"x.json", `{"version": 1, "type": "environment", "title": "a", "variables": {}}`, importer.Sure},
		{"x.json", `{"title": "a", "queries": [{"windowName": "q", "apiUrl": "x"}]}`, importer.Likely},
		{"x.json", `{"title": "a", "variablesJson": "{}"}`, importer.Likely},
		{"x.agq", `{"query": "{ a }"}`, importer.Maybe},
		{"x.json", `{"type": "window"}`, importer.No},
		{"x.json", `{"info": {"schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"}, "item": []}`, importer.No},
		{"x.json", `{"_type": "export", "__export_format": 4, "resources": [{"_type": "request", "type": "window"}]}`, importer.No},
		{"x.agq", "curl https://x.test", importer.No},
		{"x.json", "", importer.No},
	}
	for _, c := range cases {
		if got := im.Detect(importer.Source{Name: c.name, Data: []byte(c.data)}); got != c.want {
			t.Errorf("Detect(%s, %q) = %d, want %d", c.name, c.data, got, c.want)
		}
	}
	// The other formats' fixtures are theirs.
	for dir, want := range map[string]string{
		"../postman/testdata/shop.postman_collection.json": "postman",
		"../insomnia/testdata/shop-v4.json":                "insomnia",
		"../harimport/testdata/chrome.har":                 "har",
	} {
		data, err := os.ReadFile(dir)
		if err != nil {
			t.Fatal(err)
		}
		if im.Detect(importer.Source{Name: filepath.Base(dir), Data: data}) != importer.No {
			t.Errorf("%s detected as Altair", dir)
		}
		got, err := registry().Detect(importer.Source{Name: filepath.Base(dir), Data: data})
		if err != nil || got.Format().ID != want {
			t.Errorf("%s: %v, %v", dir, got, err)
		}
	}
}

func TestParseErrors(t *testing.T) {
	im := altair.Importer{}
	cases := []struct {
		data, line, msg string
	}{
		{"{\n  \"type\": \"window\",\n  \"apiUrl\": \n}", "4", "not valid JSON"},
		{"[1, 2]", "1", "not a JSON object"},
		{`{"version": 1, "type": "backup"}`, "1", "not a window"},
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
	if _, err := im.Parse(ctx, importer.Source{Data: []byte(`{"type": "collection", "queries": []}`)}); err == nil {
		t.Error("Parse with a cancelled context succeeded")
	}
	// Another version is read as 1, with an info.
	m, err := im.Parse(context.Background(), importer.Source{Data: []byte(`{"version": 2, "type": "window", "apiUrl": "https://x.test", "query": "{ a }"}`)})
	if err != nil || codes(m.Diagnostics) != altair.CodeVersion {
		t.Errorf("version 2: %v, %+v", err, m)
	}
}
