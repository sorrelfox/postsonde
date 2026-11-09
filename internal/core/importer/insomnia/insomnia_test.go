package insomnia_test

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
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/insomnia"
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

func parse(t *testing.T, src importer.Source) *importer.Model {
	t.Helper()
	m, im, err := importer.NewRegistry(curlimport.Importer{}, insomnia.Importer{}).Parse(context.Background(), src, "")
	if err != nil {
		t.Fatal(err)
	}
	if im.Format().ID != "insomnia" {
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
		return l.Path + " @" + itoa(l.Line) + ":" + itoa(l.Column)
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
			return map[string]any{
				"request": it.Request.Name, "at": at(it.Request.Source),
				"file": json.RawMessage(marshal(t, it.Request.File)), "diagnostics": diags(it.Request.Diagnostics),
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
	case *projectfile.GRPCRequest:
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

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestGolden(t *testing.T) {
	for _, name := range []string{"shop-v4.json", "shop-v5.yaml", "shop-v4.yaml", "multi-v4.json", "env-v5.yaml", "spec-v5.yaml"} {
		t.Run(name, func(t *testing.T) {
			golden.Assert(t, name, dump(t, parse(t, read(t, name))))
		})
	}
}

// find returns the request or folder named name, depth first.
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

func codes(ds []importer.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return out
}

func hasCode(ds []importer.Diagnostic, code string) bool {
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestV4Mapping(t *testing.T) {
	m := parse(t, read(t, "shop-v4.json"))
	if m.Name != "Shop API" || m.Root.Description != "The shop's public API." {
		t.Errorf("name = %q, description = %q", m.Name, m.Root.Description)
	}
	// Folders in metaSortKey order: API (-2000), Forms (-1000), then the
	// requests; skipped items in place; empty api_spec ignored.
	var top []string
	for _, it := range m.Root.Items {
		switch {
		case it.Folder != nil:
			top = append(top, "folder:"+it.Folder.Name)
		case it.Request != nil:
			top = append(top, string(it.Request.File.FileKind())+":"+it.Request.Name)
		default:
			top = append(top, "skipped:"+it.Skipped.Kind)
		}
	}
	want := "folder:API folder:Forms graphql:Search products grpc:Get order websocket:Order feed skipped:test suite"
	if strings.Join(top, " ") != want {
		t.Errorf("top = %v", top)
	}

	users := find(m.Root.Items, "Users").Folder
	if users.Auth.Type != projectfile.AuthBearer || users.Auth.Token != "{{token}}" || users.Auth.Prefix != nil {
		t.Errorf("folder auth = %+v", users.Auth)
	}
	if len(users.Variables) != 1 || users.Variables[0].Name != "page_size" || users.Variables[0].Value != "20" {
		t.Errorf("folder variables = %+v", users.Variables)
	}
	if len(users.Headers) != 1 || users.Headers[0].Value != "{{team-id}}" {
		t.Errorf("folder headers = %+v", users.Headers)
	}
	if !strings.Contains(users.Description, "pre-request script") || !hasCode(users.Diagnostics, insomnia.CodeScript) {
		t.Errorf("folder script: %q %v", users.Description, codes(users.Diagnostics))
	}
	api := find(m.Root.Items, "API").Folder
	var names []string
	for _, v := range api.Variables {
		names = append(names, v.Name+"="+v.Value)
	}
	if strings.Join(names, " ") != "api.version=v2 api.regions.0=eu api.regions.1=us" {
		t.Errorf("nested variables = %v", names)
	}

	list := find(m.Root.Items, "List users").Request
	h := list.File.(*projectfile.HTTPRequest)
	if h.URL != "{{base_url}}/users?sort=name&limit={{page_size}}&q=a%26b" {
		t.Errorf("url = %q", h.URL)
	}
	if len(h.Params) != 4 || !h.Params[3].Disabled || h.Params[0].Name != "sort" {
		t.Errorf("params = %+v", h.Params)
	}
	if h.Auth.Type != projectfile.AuthInherit || h.Settings.FollowRedirects == nil || *h.Settings.FollowRedirects || h.Settings.URLEncoding != projectfile.URLEncodingNone {
		t.Errorf("auth %+v, settings %+v", h.Auth, h.Settings)
	}
	if list.Source.Line != 7 || list.Source.Path != "resources[0]" {
		t.Errorf("source = %+v", list.Source)
	}
	if !hasCode(list.Diagnostics, insomnia.CodeSettingSkipped) {
		t.Errorf("diagnostics = %v", codes(list.Diagnostics))
	}

	create := find(m.Root.Items, "Create user").Request
	c := create.File.(*projectfile.HTTPRequest)
	if !strings.Contains(c.Body.Text, `"id": "{{$uuid}}"`) || !strings.Contains(c.Body.Text, "{{$isoTimestamp}}") ||
		!strings.Contains(c.Body.Text, "{{user.name}}") || c.Body.Type != projectfile.RawJSON {
		t.Errorf("body = %+v", c.Body)
	}
	if c.Auth.Type != projectfile.AuthBasic || c.Auth.Username != "{{user.login}}" || c.Auth.Password != "hunter2" {
		t.Errorf("auth = %+v", c.Auth)
	}
	for _, code := range []string{insomnia.CodeAuthChanged, insomnia.CodeScript, insomnia.CodeTemplateConverted} {
		if !hasCode(create.Diagnostics, code) {
			t.Errorf("create diagnostics %v lack %s", codes(create.Diagnostics), code)
		}
	}

	me := find(m.Root.Items, "Me").Request
	mh := me.File.(*projectfile.HTTPRequest)
	if mh.Auth.Type != projectfile.AuthNone || !strings.Contains(mh.Headers[0].Value, "{% response") || mh.Headers[1].Value != "{{ _.name | upper }}" {
		t.Errorf("me = %+v", mh)
	}
	if !hasCode(me.Diagnostics, insomnia.CodeTemplateTag) || !hasCode(me.Diagnostics, insomnia.CodeTemplateExpression) {
		t.Errorf("me diagnostics = %v", codes(me.Diagnostics))
	}
	for _, d := range me.Diagnostics {
		if strings.Contains(d.Message, "b64::") {
			t.Errorf("diagnostic quotes the tag's arguments: %s", d.Message)
		}
	}

	get := find(m.Root.Items, "Get user").Request.File.(*projectfile.HTTPRequest)
	if len(get.PathParams) != 1 || get.PathParams[0].Value != "{{user_id}}" || get.Auth.Key != "Cookie" || get.Auth.Value != "session={{session}}" {
		t.Errorf("get user = %+v", get)
	}
	login := find(m.Root.Items, "Log in").Request.File.(*projectfile.HTTPRequest)
	if login.Body.Mode != projectfile.BodyURLEncoded || len(login.Body.Fields) != 2 || login.Auth.In != projectfile.APIKeyInQuery {
		t.Errorf("login = %+v", login)
	}
	avatar := find(m.Root.Items, "Upload avatar").Request
	ah := avatar.File.(*projectfile.HTTPRequest)
	if ah.Body.Mode != projectfile.BodyMultipart || ah.Body.Parts[0].File != "/Users/ada/Pictures/me.png" ||
		ah.Body.Parts[1].Value != "Me at {{$timestamp}}" || ah.Auth.Type != projectfile.AuthDigest {
		t.Errorf("avatar = %+v", ah)
	}
	imp := find(m.Root.Items, "Import CSV").Request
	ih := imp.File.(*projectfile.HTTPRequest)
	if ih.Body.Mode != projectfile.BodyFile || ih.Body.ContentType != "text/csv" || ih.Auth.Type != projectfile.AuthOAuth2 ||
		ih.Auth.GrantType != projectfile.OAuth2ClientCredentials || ih.Auth.TokenURL != "{{base_url}}/oauth/token" || ih.Auth.ClientID != "shop" ||
		ih.Auth.ClientSecret != "s3cr3t" || hasCode(imp.Diagnostics, insomnia.CodeAuthUnsupported) || hasCode(imp.Diagnostics, insomnia.CodeAuthChanged) {
		t.Errorf("import = %+v %v", ih, codes(imp.Diagnostics))
	}
	partner := find(m.Root.Items, "Partner orders").Request
	if a := partner.File.(*projectfile.HTTPRequest).Auth; a.Type != projectfile.AuthOAuth1 || a.In != "" || a.ConsumerKey != "{{partner_key}}" ||
		a.ConsumerSecret != "oauth1-consumer-s3cr3t" || a.AccessToken != "partner-token" || a.TokenSecret != "oauth1-token-s3cr3t" ||
		a.SignatureMethod != "HMAC-SHA1" || a.Nonce != "fixed-nonce" || a.Timestamp != "1700000000" || a.Callback != "https://app.shop.example/cb" ||
		a.Realm != "shop" || a.Verifier != "v123" || !a.BodyHash || a.OmitVersion || a.Disabled || len(partner.Diagnostics) != 0 {
		t.Errorf("partner = %+v %v", a, codes(partner.Diagnostics))
	}
	note := find(m.Root.Items, "Post note").Request.File.(*projectfile.HTTPRequest)
	if note.Body.Type != projectfile.RawText || len(note.Headers) != 1 || note.Headers[0].Value != "application/edn" {
		t.Errorf("note = %+v", note)
	}
	report := find(m.Root.Items, "Daily report").Request
	if a := report.File.(*projectfile.HTTPRequest).Auth; a.Type != projectfile.AuthNetrc || a.NetrcFile != "" || a.Disabled || len(report.Diagnostics) != 0 {
		t.Errorf("report = %+v %v", a, codes(report.Diagnostics))
	}

	gql := find(m.Root.Items, "Search products").Request.File.(*projectfile.GraphQLRequest)
	if gql.OperationName != "Search" || !strings.Contains(gql.Query, "products(q: $q)") ||
		gql.Variables != "{\n  \"q\": \"{{query}}\",\n  \"first\": 10\n}" || gql.Auth.Prefix == nil || *gql.Auth.Prefix != "Token" {
		t.Errorf("graphql = %+v", gql)
	}
	grpc := find(m.Root.Items, "Get order").Request
	g := grpc.File.(*projectfile.GRPCRequest)
	if g.Address != "{{grpc_host}}:443" || !g.Settings.TLS || g.Service != "shop.v1.Orders" || g.Method != "GetOrder" ||
		!strings.Contains(g.Message, "{{order_id}}") || len(g.Metadata) != 1 || !hasCode(grpc.Diagnostics, insomnia.CodeProtoSkipped) {
		t.Errorf("grpc = %+v %v", g, codes(grpc.Diagnostics))
	}
	ws := find(m.Root.Items, "Order feed").Request
	w := ws.File.(*projectfile.WebSocketRequest)
	if w.URL != "wss://{{ws_host}}/feed?channel=orders" || w.Message != `{"subscribe": "{{channel}}"}` || w.MessageFormat != projectfile.MessageJSON ||
		!hasCode(ws.Diagnostics, insomnia.CodeAuthUnsupported) {
		t.Errorf("websocket = %+v %v", w, codes(ws.Diagnostics))
	}

	// Environments: each sub-environment with the base's variables.
	var envs []string
	for _, e := range m.Environments {
		envs = append(envs, e.Name)
	}
	if strings.Join(envs, ",") != "Local (private),Staging,Production" {
		t.Fatalf("environments = %v", envs)
	}
	local, staging, prod := m.Environments[0], m.Environments[1], m.Environments[2]
	if len(local.Variables) != 4 || local.Variables[0].Value != "http://localhost:8080" || !local.Variables[1].Secret || local.Variables[1].Value != "dev-token-123" {
		t.Errorf("local = %+v", local.Variables)
	}
	if staging.Variables[0].Value != "https://staging.shop.example/v2" || staging.Variables[len(staging.Variables)-1].Name != "user_id" {
		t.Errorf("staging = %+v", staging.Variables)
	}
	if v := staging.Variables; v[0].Name != "base_url" || v[2].Name != "user.login" {
		t.Errorf("staging order = %+v", v)
	}
	if !hasCode(prod.Diagnostics, insomnia.CodeSecretValue) || prod.Variables[1].Value != "" || !prod.Variables[1].Secret || !prod.Variables[4].Disabled {
		t.Errorf("production = %+v %v", prod.Variables, codes(prod.Diagnostics))
	}
	if !hasCode(m.Diagnostics, insomnia.CodeCookies) {
		t.Errorf("file diagnostics = %v", codes(m.Diagnostics))
	}
}

func TestV5Mapping(t *testing.T) {
	m := parse(t, read(t, "shop-v5.yaml"))
	if m.Name != "Shop API v5" || m.Root.Description == "" {
		t.Errorf("model = %q %q", m.Name, m.Root.Description)
	}
	catalog := find(m.Root.Items, "Catalog").Folder
	if catalog.Auth.Type != projectfile.AuthAPIKey || catalog.Auth.Value != "{{api_key}}" || len(catalog.Variables) != 2 ||
		catalog.Variables[1].Name != "limits.page" || catalog.Headers[0].Value != "{{lang}}" {
		t.Errorf("catalog = %+v", catalog)
	}
	if catalog.Source.Path != "collection[0]" || catalog.Source.Line != 9 {
		t.Errorf("catalog source = %+v", catalog.Source)
	}
	list := find(m.Root.Items, "List products").Request
	h := list.File.(*projectfile.HTTPRequest)
	if h.URL != "{{base_url}}/products?per_page={{limits.page}}" || len(h.Params) != 2 || h.Settings.FollowRedirects == nil || !*h.Settings.FollowRedirects ||
		h.Settings.SendCookies == nil || *h.Settings.SendCookies || h.Description != "Every product, 50 per page." || list.Source.Path != "collection[0].children[0].children[0]" {
		t.Errorf("list = %+v at %+v", h, list.Source)
	}
	price := find(m.Root.Items, "Set price").Request
	p := price.File.(*projectfile.HTTPRequest)
	if p.Body.Type != projectfile.RawXML || p.Auth.Type != projectfile.AuthNone || !hasCode(price.Diagnostics, insomnia.CodeAuthUnsupported) || !hasCode(price.Diagnostics, insomnia.CodeScript) {
		t.Errorf("price = %+v %v", p, codes(price.Diagnostics))
	}
	sync := find(m.Root.Items, "Sync products").Request
	if a := sync.File.(*projectfile.HTTPRequest).Auth; a.Type != projectfile.AuthOAuth1 || !a.Disabled || a.SignatureMethod != "RSA-SHA1" ||
		!strings.Contains(a.ConsumerSecret, "rsa-sync-key") || a.TokenSecret != "" || a.AccessToken != "{{sync_token}}" ||
		!hasCode(sync.Diagnostics, insomnia.CodeAuthChanged) || !strings.Contains(sync.Diagnostics[0].Message, `"1.0a"`) {
		t.Errorf("sync = %+v %v", a, sync.Diagnostics)
	}
	gql := find(m.Root.Items, "Product query").Request.File.(*projectfile.GraphQLRequest)
	if gql.Method != "GET" || gql.Query != `{ product(id: "{{product_id}}") { name } }` || gql.Auth.Prefix != nil {
		t.Errorf("graphql = %+v", gql)
	}
	g := find(m.Root.Items, "Stock").Request
	if gr := g.File.(*projectfile.GRPCRequest); gr.Address != "{{grpc_host}}:50051" || gr.Settings.TLS || gr.Service != "shop.v1.Stock" || !hasCode(g.Diagnostics, insomnia.CodeProtoSkipped) {
		t.Errorf("grpc = %+v", gr)
	}
	ws := find(m.Root.Items, "Stock updates").Request
	if w := ws.File.(*projectfile.WebSocketRequest); w.URL != "ws://{{ws_host}}/stock?format=json" || len(w.Params) != 1 {
		t.Errorf("websocket = %+v", w)
	}
	if s := find(m.Root.Items, "Chat"); s == nil || s.Skipped == nil || s.Skipped.Kind != "socket.io request" {
		t.Errorf("chat = %+v", s)
	}
	export := find(m.Root.Items, "Stock export").Request
	if a := export.File.(*projectfile.HTTPRequest).Auth; a.Type != projectfile.AuthNetrc || !a.Disabled || len(export.Diagnostics) != 0 {
		t.Errorf("stock export = %+v %v", a, codes(export.Diagnostics))
	}
	if len(m.Environments) != 2 || m.Environments[0].Name != "Dev" || m.Environments[1].Name != "Mine" {
		t.Fatalf("environments = %+v", m.Environments)
	}
	dev := m.Environments[0].Variables
	if len(dev) != 5 || dev[0].Value != "http://localhost:8080" || dev[2].Value != "7" || dev[4].Name != "empty" || dev[4].Value != "" {
		t.Errorf("dev = %+v", dev)
	}
	mine := m.Environments[1]
	if !mine.Variables[2].Secret || !hasCode(mine.Diagnostics, insomnia.CodePrivateEnvironment) {
		t.Errorf("mine = %+v", mine)
	}
}

// The fixture of the YAML reader, a plain Insomnia 11 export.
func TestYAMLReaderFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "yaml", "testdata", "insomnia-v5.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, importer.Source{Name: "petstore.yaml", Data: data})
	create := find(m.Root.Items, "Create pet").Request
	if h := create.File.(*projectfile.HTTPRequest); h.URL != "{{base_url}}/pets" || h.Body.Type != projectfile.RawJSON || !hasCode(create.Diagnostics, insomnia.CodeScript) {
		t.Errorf("create = %+v", h)
	}
	up := find(m.Root.Items, "Upload photo").Request.File.(*projectfile.HTTPRequest)
	if up.Body.Mode != projectfile.BodyMultipart || up.Body.Parts[0].File != "/Users/me/Pictures/rex.jpg" {
		t.Errorf("upload = %+v", up.Body)
	}
	if find(m.Root.Items, "Search (GraphQL)").Request.File.FileKind() != projectfile.KindGraphQL {
		t.Error("graphql not detected")
	}
	if len(m.Environments) != 1 || m.Environments[0].Name != "Local" {
		t.Errorf("environments = %+v", m.Environments)
	}
}

func TestDetect(t *testing.T) {
	im := insomnia.Importer{}
	cases := []struct {
		data string
		want int
	}{
		{`{"_type": "export", "__export_format": 4, "resources": []}`, importer.Sure},
		{"\xef\xbb\xbf  {\n  \"__export_format\": 4\n}", importer.Sure},
		{`{"info": {"schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"}}`, importer.No},
		{"type: collection.insomnia.rest/5.0\nname: x\n", importer.Sure},
		{"# exported\ntype: \"spec.insomnia.rest/5.0\"\n", importer.Sure},
		{"type: environment.insomnia.rest/5.0\n", importer.Sure},
		{"type: collection.insomnia.rest/6.0\n", importer.Likely},
		{"_type: export\n__export_format: 4\nresources: []\n", importer.Sure},
		{"openapi: 3.1.0\ninfo:\n  title: x\n", importer.No},
		{"curl https://x.test", importer.No},
		{"", importer.No},
	}
	for _, c := range cases {
		if got := im.Detect(importer.Source{Name: "x", Data: []byte(c.data)}); got != c.want {
			t.Errorf("Detect(%q) = %d, want %d", c.data, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	im := insomnia.Importer{}
	cases := []struct {
		data string
		line string
	}{
		{"{\n  \"_type\": \"export\",\n  \"resources\": [\n    {\"_id\": }\n  ]\n}", "4"},
		{`{"_type": "export"}`, "1"},
		{`{"_type": "export", "resources": {}}`, "1"},
		{"[1, 2]", "1"},
		{"type: collection.insomnia.rest/5.0\n  bad: indent\n", "2"},
		{"name: just yaml\n", "1"},
		{"\xff\xfe", ""},
		{`{"_type": "export", "resources": []} trailing`, "1"},
	}
	for _, c := range cases {
		_, err := im.Parse(context.Background(), importer.Source{Data: []byte(c.data)})
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != importer.CodeInvalidFile || errs.KindOf(err) != errs.Parse {
			t.Errorf("Parse(%q) = %v", c.data, err)
			continue
		}
		if e.Details["line"] != c.line {
			t.Errorf("Parse(%q) line = %q, want %q (%v)", c.data, e.Details["line"], c.line, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := im.Parse(ctx, importer.Source{Data: []byte(`{"_type": "export", "resources": []}`)}); err == nil {
		t.Error("Parse with a cancelled context succeeded")
	}
}

func TestEdgeCases(t *testing.T) {
	// Orphans go to the top level; a loop of parents is skipped; an empty
	// export has nothing.
	data := `{"_type": "export", "__export_format": 5, "resources": [
		{"_id": "wrk_1", "_type": "workspace", "name": "W"},
		{"_id": "req_orphan", "_type": "request", "parentId": "fld_gone", "name": "Orphan", "url": "http://x.test"},
		{"_id": "fld_a", "_type": "request_group", "parentId": "fld_b", "name": "A"},
		{"_id": "fld_b", "_type": "request_group", "parentId": "fld_a", "name": "B"},
		{"_id": "req_in_loop", "_type": "request", "parentId": "fld_a", "name": "Looped", "url": "http://x.test"},
		{"_id": "mock_1", "_type": "mock_server", "parentId": "wrk_1", "name": "Mock"},
		{"_id": "x_1", "_type": "something_new", "parentId": "wrk_1", "name": "New"},
		{"_id": "req_blank", "_type": "request", "parentId": "wrk_1", "url": "", "method": ""}
	]}`
	m, err := insomnia.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(data)})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(m.Diagnostics, insomnia.CodeOrphan) || !hasCode(m.Diagnostics, insomnia.CodeVersion) {
		t.Errorf("diagnostics = %v", codes(m.Diagnostics))
	}
	var got []string
	for _, it := range m.Root.Items {
		switch {
		case it.Request != nil:
			got = append(got, "request:"+it.Request.Name)
		case it.Folder != nil:
			got = append(got, "folder:"+it.Folder.Name)
		default:
			got = append(got, "skipped:"+it.Skipped.Kind+":"+it.Skipped.Name)
		}
	}
	want := "skipped:mock server:Mock skipped:something_new:New request:Request request:Orphan skipped:folder:A skipped:folder:B skipped:request:Looped"
	if strings.Join(got, " ") != want {
		t.Errorf("items = %v", got)
	}
	blank := find(m.Root.Items, "Request").Request.File.(*projectfile.HTTPRequest)
	if blank.Method != "GET" || blank.Body.Mode != projectfile.BodyNone || blank.Auth.Type != projectfile.AuthInherit {
		t.Errorf("blank = %+v", blank)
	}
}

func TestTemplates(t *testing.T) {
	cases := map[string]string{
		"{{ _.a }}":                   "{{a}}",
		"{{_.a}}/{{ b }}":             "{{a}}/{{b}}",
		"{{ _['my-key'] }}":           "{{my-key}}",
		`{{ _["x"].y[2] }}`:           "{{x.y.2}}",
		"{{ _.a | upper }}":           "{{ _.a | upper }}",
		"{{ _.a + 1 }}":               "{{ _.a + 1 }}",
		"{% uuid 'v4' %}":             "{{$uuid}}",
		"{% uuid %}":                  "{{$uuid}}",
		"{% now 'millis' %}":          "{{$timestampMs}}",
		"{% now 'custom', 'YYYY' %}":  "{% now 'custom', 'YYYY' %}",
		"{% base64 'encode', 'x' %}":  "{% base64 'encode', 'x' %}",
		"a {{ unterminated":           "a {{ unterminated",
		"a {% unterminated":           "a {% unterminated",
		"{ plain } {x}":               "{ plain } {x}",
		`{"a": {{ _.n }}}`:            `{"a": {{n}}}`,
		"{{ _ }}":                     "{{ _ }}",
		"{{ _.a.b }}{% uuid 'v4' %}{": "{{a.b}}{{$uuid}}{",
	}
	for in, want := range cases {
		src := `{"_type":"export","resources":[{"_type":"workspace","_id":"w"},{"_type":"request","_id":"r","parentId":"w","name":"R","url":` + quote(in) + `}]}`
		m, err := insomnia.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(src)})
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Root.Items[0].Request.File.(*projectfile.HTTPRequest).URL; got != want {
			t.Errorf("conv(%q) = %q, want %q", in, got, want)
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestOAuth2Auth: Insomnia's OAuth 2.0 auth becomes an oauth2 auth
// (AUTH-08): the credentials in the body, a token prefix, audience and
// resource; the authorization code and implicit grants with their URLs;
// the password grant with its credentials.
func TestOAuth2Auth(t *testing.T) {
	data := `{"_type": "export", "__export_format": 4, "resources": [
		{"_id": "wrk_1", "_type": "workspace", "name": "W"},
		{"_id": "req_cc", "_type": "request", "parentId": "wrk_1", "name": "CC", "url": "http://x.test", "method": "GET",
		 "authentication": {"type": "oauth2", "grantType": "client_credentials", "accessTokenUrl": "https://idp.test/token",
		  "clientId": "app", "clientSecret": "{{ _.secret }}", "credentialsInBody": true, "tokenPrefix": "Token",
		  "scope": "a b", "audience": "api", "resource": "urn:r", "disabled": true}},
		{"_id": "req_pw", "_type": "request", "parentId": "wrk_1", "name": "PW", "url": "http://x.test", "method": "GET",
		 "authentication": {"type": "oauth2", "grantType": "password", "accessTokenUrl": "https://idp.test/token", "username": "u", "password": "p"}},
		{"_id": "req_im", "_type": "request", "parentId": "wrk_1", "name": "IM", "url": "http://x.test", "method": "GET",
		 "authentication": {"type": "oauth2", "grantType": "implicit", "authorizationUrl": "https://idp.test/authorize",
		  "redirectUrl": "http://localhost:9100/cb", "clientId": "spa", "responseType": "id_token", "scope": "profile"}},
		{"_id": "req_ac", "_type": "request", "parentId": "wrk_1", "name": "AC", "url": "http://x.test", "method": "GET",
		 "authentication": {"type": "oauth2", "grantType": "authorization_code", "accessTokenUrl": "https://idp.test/token",
		  "authorizationUrl": "https://idp.test/authorize", "redirectUrl": "http://127.0.0.1:9000/cb", "clientId": "desk", "usePkce": true}},
		{"_id": "req_web", "_type": "request", "parentId": "wrk_1", "name": "Web", "url": "http://x.test", "method": "GET",
		 "authentication": {"type": "oauth2", "grantType": "authorization_code", "accessTokenUrl": "https://idp.test/token",
		  "authorizationUrl": "https://idp.test/authorize", "redirectUrl": "https://app.example/cb"}}
	]}`
	m, err := insomnia.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(data)})
	if err != nil {
		t.Fatal(err)
	}
	cc := find(m.Root.Items, "CC").Request
	a := cc.File.(*projectfile.HTTPRequest).Auth
	if a.Type != projectfile.AuthOAuth2 || a.GrantType != projectfile.OAuth2ClientCredentials || a.ClientSecret != "{{secret}}" ||
		a.ClientAuth != projectfile.OAuth2ClientAuthBody || a.BearerPrefix() != "Token" || a.Scope != "a b" || a.Audience != "api" ||
		a.Resource != "urn:r" || !a.Disabled || len(cc.Diagnostics) != 0 {
		t.Errorf("client credentials = %+v %v", a, codes(cc.Diagnostics))
	}
	pw := find(m.Root.Items, "PW").Request
	if a := pw.File.(*projectfile.HTTPRequest).Auth; a.GrantType != projectfile.OAuth2Password || a.Username != "u" || a.Password != "p" || len(pw.Diagnostics) != 0 {
		t.Errorf("password = %+v %v", a, codes(pw.Diagnostics))
	}
	// The implicit grant: its URLs, and the ID token Insomnia's
	// responseType "id_token" sends, with a warning about the scope.
	im := find(m.Root.Items, "IM").Request
	if a := im.File.(*projectfile.HTTPRequest).Auth; a.GrantType != projectfile.OAuth2Implicit || a.AuthURL != "https://idp.test/authorize" ||
		a.RedirectURL != "http://localhost:9100/cb" || !a.IDToken || !hasCode(im.Diagnostics, insomnia.CodeAuthChanged) ||
		!strings.Contains(im.Diagnostics[0].Message, "openid") {
		t.Errorf("implicit = %+v %v", a, codes(im.Diagnostics))
	}
	// The authorization code grant is run: its URLs come along, a
	// redirect URL that is not on this computer is left out and reported.
	ac := find(m.Root.Items, "AC").Request
	if a := ac.File.(*projectfile.HTTPRequest).Auth; a.GrantType != projectfile.OAuth2AuthorizationCode || a.AuthURL != "https://idp.test/authorize" ||
		a.RedirectURL != "http://127.0.0.1:9000/cb" || len(ac.Diagnostics) != 0 {
		t.Errorf("authorization code = %+v %v", a, codes(ac.Diagnostics))
	}
	web := find(m.Root.Items, "Web").Request
	if a := web.File.(*projectfile.HTTPRequest).Auth; a.RedirectURL != "" || !hasCode(web.Diagnostics, insomnia.CodeAuthChanged) ||
		!strings.Contains(web.Diagnostics[0].Message, "app.example") {
		t.Errorf("web redirect = %+v %v", a, codes(web.Diagnostics))
	}
}
