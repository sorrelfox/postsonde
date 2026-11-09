package postman_test

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
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/insomnia"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
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
	m, im, err := importer.NewRegistry(curlimport.Importer{}, insomnia.Importer{}, postman.Importer{}).Parse(context.Background(), src, "")
	if err != nil {
		t.Fatal(err)
	}
	if im.Format().ID != "postman" {
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
	for _, name := range []string{
		"shop.postman_collection.json", "legacy.postman_collection.json",
		"staging.postman_environment.json", "workspace.postman_globals.json",
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

func httpOf(t *testing.T, m *importer.Model, name string) (*projectfile.HTTPRequest, *importer.Request) {
	t.Helper()
	it := find(m.Root.Items, name)
	if it == nil || it.Request == nil {
		t.Fatalf("no request %q", name)
	}
	h, ok := it.Request.File.(*projectfile.HTTPRequest)
	if !ok {
		t.Fatalf("%q is a %T", name, it.Request.File)
	}
	return h, it.Request
}

func TestCollection(t *testing.T) {
	m := parse(t, read(t, "shop.postman_collection.json"))
	root := m.Root
	if m.Name != "Shop API" || !strings.HasPrefix(root.Description, "The shop's public API.") ||
		!strings.Contains(root.Description, "Postman pre-request script") || !strings.Contains(root.Description, "turned off in Postman") {
		t.Errorf("name %q, description %q", m.Name, root.Description)
	}
	if root.Auth.Type != projectfile.AuthBearer || root.Auth.Token != "{{token}}" {
		t.Errorf("collection auth = %+v", root.Auth)
	}
	var vars []string
	for _, v := range root.Variables {
		s := v.Name + "=" + v.Value
		if v.Secret {
			s += " (secret)"
		}
		if v.Disabled {
			s += " (off)"
		}
		vars = append(vars, s)
	}
	if strings.Join(vars, ", ") != "base_url=https://shop.example/api, page_size=20, api_key=coll-api-key-5 (secret), legacy=x (off)" {
		t.Errorf("collection variables = %v", vars)
	}
	if n := len(codes(root.Diagnostics)); n != 2 || !hasCode(root.Diagnostics, postman.CodeScript) {
		t.Errorf("collection diagnostics = %v", codes(root.Diagnostics))
	}

	var top []string
	for _, it := range root.Items {
		switch {
		case it.Folder != nil:
			top = append(top, "folder:"+it.Folder.Name)
		case it.Request != nil:
			top = append(top, string(it.Request.File.FileKind())+":"+it.Request.Name)
		default:
			top = append(top, "skipped:"+it.Skipped.Name)
		}
	}
	want := "folder:Users folder:Auth graphql:Search products graphql:Product by GET graphql:Patch product (GraphQL) " +
		"http:Run script http:Place order (XML) http:Health http:Muted body http:Charge card skipped:Placeholder"
	if strings.Join(top, " ") != want {
		t.Errorf("top = %v", top)
	}

	users := find(root.Items, "Users").Folder
	if users.Auth.Type != projectfile.AuthBearer || users.Auth.Token != "{{user_token}}" || len(users.Variables) != 1 ||
		users.Description != "Everything about users.\n\nPostman pre-request script (not run by PostSonde):\n\n```js\nconsole.log(\"users folder\");\n```" {
		t.Errorf("users = %+v", users)
	}
	if users.Source.Path != "item[0]" || users.Source.Line != 10 {
		t.Errorf("users source = %+v", users.Source)
	}
	avatars := find(root.Items, "Avatars").Folder
	if avatars.Auth.Type != projectfile.AuthAPIKey || avatars.Auth.Key != "X-API-Key" || avatars.Auth.Value != "{{api_key}}" || avatars.Auth.In != projectfile.APIKeyInHeader ||
		!avatars.Variables[0].Secret || avatars.Variables[0].Value != "s3cr3t-avatar" || avatars.Variables[1].Value != "1048576" {
		t.Errorf("avatars = %+v", avatars)
	}

	list, lr := httpOf(t, m, "List users")
	if list.URL != "{{base_url}}/users?sort=name&limit={{page_size}}&active" || len(list.Params) != 4 || !list.Params[2].Disabled || list.Params[1].Description != "Page size" {
		t.Errorf("list url %q params %+v", list.URL, list.Params)
	}
	if !list.Headers[1].Disabled || list.Auth.Type != projectfile.AuthInherit || lr.Source.Path != "item[0].item[0]" {
		t.Errorf("list = %+v at %+v", list, lr.Source)
	}
	// The folder's disableCookies reaches its requests; the request's own
	// settings are added.
	s := list.Settings
	if s.FollowRedirects == nil || *s.FollowRedirects || s.MaxRedirects == nil || *s.MaxRedirects != 3 || s.SendCookies == nil || *s.SendCookies || s.StoreCookies == nil || *s.StoreCookies {
		t.Errorf("list settings = %+v", s)
	}
	// Saved examples are response examples (HTTP-38), the credentials of
	// their Authorization headers masked.
	if ex := list.ResponseExamples; len(ex) != 3 || ex[0].Name != "Two users" || ex[0].StatusCode != 200 || ex[0].StatusText != "OK" ||
		!strings.HasPrefix(ex[0].Body, "[\n  {\"id\": 1") || ex[0].Request == nil || ex[0].Request.URL != "{{base_url}}/users" ||
		ex[0].Request.Headers[0].Value != "Bearer "+httpclient.Mask || ex[1].Name != "Example 2" || ex[1].Request != nil ||
		ex[2].Name != "Empty" || ex[2].StatusCode != 204 || ex[2].Body != "" || list.Description != "Lists users, a page at a time." || hasCode(lr.Diagnostics, postman.CodeExamples) {
		t.Errorf("list examples %+v, description %q %v", list.ResponseExamples, list.Description, codes(lr.Diagnostics))
	}

	get, gr := httpOf(t, m, "Get user")
	if get.Auth.Type != projectfile.AuthNone || len(get.PathParams) != 2 || get.PathParams[0].Value != "{{user_id}}" || get.PathParams[1].Name != "orderId" ||
		!strings.Contains(get.Description, "Postman test script") || !hasCode(gr.Diagnostics, postman.CodeScript) {
		t.Errorf("get user = %+v %v", get, codes(gr.Diagnostics))
	}

	create, cr := httpOf(t, m, "Create user")
	if create.Auth.Type != projectfile.AuthBasic || create.Auth.Username != "{{admin_user}}" || create.Auth.Password != "hunter2" {
		t.Errorf("create auth = %+v", create.Auth)
	}
	for _, s := range []string{`"{{$guid}}"`, `"{{$randomFirstName}}"`, `"{{$randomString 1}}{{$randomHex 1}}"`, `{{$timestamp}},`, `{{$randomInt}},`} {
		if !strings.Contains(create.Body.Text, s) {
			t.Errorf("create body lacks %s: %s", s, create.Body.Text)
		}
	}
	if create.Body.Type != projectfile.RawJSON || len(create.Headers) != 0 || create.URL != "{{base_url}}/users" {
		t.Errorf("create = %+v", create)
	}
	// One script: the empty test script is left out.
	if got := strings.Join(codes(cr.Diagnostics), " "); got != "postman.script postman.dynamic_converted postman.dynamic_converted" {
		t.Errorf("create diagnostics = %s", got)
	}
	// The fake data values PostSonde generates are kept as written.
	for _, s := range []string{`"{{$randomFirstName}}"`, `"{{$randomEmail}}"`} {
		if !strings.Contains(create.Body.Text, s) {
			t.Errorf("create body lacks %s: %s", s, create.Body.Text)
		}
	}

	up, ur := httpOf(t, m, "Upload avatar")
	var parts []string
	for _, p := range up.Body.Parts {
		parts = append(parts, p.Name+"="+p.Value+p.File)
	}
	if up.Body.Mode != projectfile.BodyMultipart || strings.Join(parts, " ") != "image=/Users/ada/Pictures/me.png caption=Me at {{$isoTimestamp}} extra=/Users/ada/Pictures/a.png extra=/Users/ada/Pictures/b.png draft=true" ||
		up.Body.Parts[0].ContentType != "image/png" || !up.Body.Parts[4].Disabled || !hasCode(ur.Diagnostics, postman.CodeFilePath) {
		t.Errorf("upload = %+v %v", parts, codes(ur.Diagnostics))
	}
	rep, _ := httpOf(t, m, "Replace avatar")
	if rep.Body.Mode != projectfile.BodyFile || rep.Body.Path != "/Users/ada/Pictures/new.png" || rep.Method != "PUT" {
		t.Errorf("replace = %+v", rep)
	}

	login, lgr := httpOf(t, m, "Log in")
	ls := login.Settings
	if login.Auth.In != projectfile.APIKeyInQuery || login.Auth.Key != "api_key" || login.Body.Mode != projectfile.BodyURLEncoded || len(login.Body.Fields) != 3 ||
		ls.URLEncoding != projectfile.URLEncodingNone || ls.HTTPVersion != projectfile.HTTPVersion2 || ls.VerifyTLS == nil || *ls.VerifyTLS ||
		ls.FollowOriginalMethod == nil || !*ls.FollowOriginalMethod || ls.ForwardAuthorization == nil || *ls.ForwardAuthorization || ls.SendCookies != nil {
		t.Errorf("login = %+v", login)
	}
	if !hasCode(lgr.Diagnostics, postman.CodeSettingSkipped) || !strings.Contains(lgr.Diagnostics[0].Message, "insecureHTTPParser, tlsDisabledProtocols.") {
		t.Errorf("login diagnostics = %+v", lgr.Diagnostics)
	}
	digest, _ := httpOf(t, m, "Digest login")
	if digest.Auth.Type != projectfile.AuthDigest || digest.Auth.Username != "admin" || digest.Auth.Password != "{{digest_password}}" {
		t.Errorf("digest = %+v", digest.Auth)
	}
	if signed, r := httpOf(t, m, "Signed request"); signed.Auth.Type != projectfile.AuthAWSV4 || signed.Auth.SecretKey != "aws-secret-2" || len(r.Diagnostics) != 0 {
		t.Errorf("signed request = %+v %v", signed.Auth, codes(r.Diagnostics))
	}
	// OAuth 2.0 without a grant_type is Postman's default, the
	// authorization code grant: kept, with a warning that its
	// authorization URL is not set.
	for _, name := range []string{"OAuth token"} {
		h, r := httpOf(t, m, name)
		if h.Auth.Type != projectfile.AuthOAuth2 || h.Auth.GrantType != projectfile.OAuth2AuthorizationCode ||
			h.Auth.TokenURL != "https://auth.shop.example/token" || h.Auth.ClientSecret != "oauth-client-secret-1" ||
			!hasCode(r.Diagnostics, postman.CodeAuthChanged) {
			t.Errorf("%s = %+v %v", name, h.Auth, codes(r.Diagnostics))
		}
		for _, d := range r.Diagnostics {
			if strings.Contains(d.Message, "secret-") {
				t.Errorf("diagnostic quotes a secret: %s", d.Message)
			}
		}
	}
	if _, r := httpOf(t, m, "OAuth token"); !strings.Contains(r.Diagnostics[1].Message, "proxy") {
		t.Errorf("proxy not reported: %v", r.Diagnostics)
	}

	// OAuth 1.0: parameters not in the header go in a POST's URL-encoded
	// body; the RSA key takes the consumer secret's place.
	token, tr := httpOf(t, m, "Request token")
	if a := token.Auth; a.Type != projectfile.AuthOAuth1 || a.In != projectfile.AuthInBody || a.ConsumerKey != "shop-consumer" ||
		a.ConsumerSecret != "oauth1-consumer-secret-5" || a.AccessToken != "" || a.TokenSecret != "{{oauth_token_secret}}" ||
		a.SignatureMethod != "HMAC-SHA256" || a.Realm != "shop" || a.Callback != "https://app.shop.example/cb" || a.OmitVersion || a.BodyHash || len(tr.Diagnostics) != 0 {
		t.Errorf("request token = %+v %v", a, codes(tr.Diagnostics))
	}
	partner, pr := httpOf(t, m, "Partner orders")
	if a := partner.Auth; a.Type != projectfile.AuthOAuth1 || a.In != projectfile.APIKeyInHeader || a.SignatureMethod != "RSA-SHA1" || !strings.Contains(a.ConsumerSecret, "rsa-key-6") ||
		a.TokenSecret != "" || a.AccessToken != "partner-token" || a.Nonce != "fixed-nonce" || a.Timestamp != "1700000000" || !a.OmitVersion || !a.BodyHash ||
		!hasCode(pr.Diagnostics, postman.CodeAuthChanged) || !strings.Contains(pr.Diagnostics[0].Message, "empty parameters") {
		t.Errorf("partner = %+v %v", a, pr.Diagnostics)
	}
	legacy, lpr := httpOf(t, m, "Legacy partner")
	if a := legacy.Auth; a.Type != projectfile.AuthOAuth1 || a.In != projectfile.AuthInBody || a.SignatureMethod != "RSA-SHA512" || a.ConsumerSecret != "{{legacy_key}}" ||
		len(lpr.Diagnostics) != 1 || !strings.Contains(lpr.Diagnostics[0].Message, `"1.0a"`) || !strings.Contains(lpr.Diagnostics[0].Message, "RSA-SHA512") ||
		!strings.Contains(lpr.Diagnostics[0].Message, "encode") {
		t.Errorf("legacy partner = %+v %v", a, lpr.Diagnostics)
	}
	for _, r := range []*importer.Request{tr, pr, lpr} {
		for _, d := range r.Diagnostics {
			if strings.Contains(d.Message, "secret-") || strings.Contains(d.Message, "rsa-key") {
				t.Errorf("diagnostic quotes a secret: %s", d.Message)
			}
		}
	}

	search := find(root.Items, "Search products").Request.File.(*projectfile.GraphQLRequest)
	if search.Method != "" || search.URL != "{{base_url}}/graphql" || !strings.Contains(search.Query, "products(q: $q)") || search.Variables != "{\n  \"q\": \"{{query}}\"\n}" || search.Auth.Type != projectfile.AuthInherit {
		t.Errorf("search = %+v", search)
	}
	byGet := find(root.Items, "Product by GET").Request.File.(*projectfile.GraphQLRequest)
	if byGet.Method != "GET" || byGet.Variables != "" {
		t.Errorf("by GET = %+v", byGet)
	}
	if p := find(root.Items, "Patch product (GraphQL)").Request; !hasCode(p.Diagnostics, postman.CodeGraphQLMethod) {
		t.Errorf("patch = %v", codes(p.Diagnostics))
	}

	script, _ := httpOf(t, m, "Run script")
	if script.Body.Type != projectfile.RawText || len(script.Headers) != 1 || script.Headers[0].Value != "application/javascript" {
		t.Errorf("script = %+v", script)
	}
	xml, _ := httpOf(t, m, "Place order (XML)")
	if xml.Body.Type != projectfile.RawXML || xml.URL != "{{base_url}}/orders#top" {
		t.Errorf("xml = %+v", xml)
	}
	health, _ := httpOf(t, m, "Health")
	if health.Method != "GET" || health.URL != "{{base_url}}/health" || health.Body.Mode != projectfile.BodyNone || health.Auth.Type != projectfile.AuthInherit {
		t.Errorf("health = %+v", health)
	}
	muted, mutedReq := httpOf(t, m, "Muted body")
	if muted.Body.Mode != projectfile.BodyNone || !hasCode(mutedReq.Diagnostics, postman.CodeBodySkipped) {
		t.Errorf("muted = %+v", muted.Body)
	}
	_, charge := httpOf(t, m, "Charge card")
	if got := strings.Join(codes(charge.Diagnostics), " "); got != "postman.variables_skipped postman.vault" {
		t.Errorf("charge diagnostics = %s", got)
	}
	if p := find(root.Items, "Placeholder").Skipped; p.Code != postman.CodeUnsupported || p.Source.Path != "item[10]" {
		t.Errorf("placeholder = %+v", p)
	}
	if len(m.Environments) != 0 || len(m.Diagnostics) != 0 {
		t.Errorf("environments %+v, diagnostics %v", m.Environments, codes(m.Diagnostics))
	}
}

func TestV20(t *testing.T) {
	m := parse(t, read(t, "legacy.postman_collection.json"))
	if m.Name != "Legacy Orders" || m.Root.Description != "Orders API, exported as **v2.0**." {
		t.Errorf("model = %q %q", m.Name, m.Root.Description)
	}
	if a := m.Root.Auth; a.Type != projectfile.AuthBasic || a.Username != "{{user}}" || a.Password != "legacy-pass-3" {
		t.Errorf("auth = %+v", a)
	}
	if len(m.Root.Variables) != 1 || m.Root.Variables[0].Name != "host" {
		t.Errorf("variables = %+v", m.Root.Variables)
	}
	list, lr := httpOf(t, m, "List archived orders")
	if list.URL != "https://{{host}}:8443/v1/archive/:year?page=1#list" || len(list.Params) != 2 || !list.Params[1].Disabled ||
		len(list.PathParams) != 1 || list.PathParams[0].Value != "2019" || list.Method != "GET" {
		t.Errorf("list = %+v", list)
	}
	if len(list.Headers) != 3 || list.Headers[2].Value != "{{client}}" || list.Headers[0].Name != "Accept" || !list.Headers[1].Disabled || list.Headers[1].Name != "X-Debug" {
		t.Errorf("headers = %+v", list.Headers)
	}
	if list.Settings.VerifyTLS == nil || *list.Settings.VerifyTLS || list.Body.Mode != projectfile.BodyNone || lr.Source.Path != "item[0].item[0].item[0].item[0]" {
		t.Errorf("list = %+v at %+v", list, lr.Source)
	}
	// v2.0's OAuth 1.0 object, on a folder; a number timestamp is text.
	archive := find(m.Root.Items, "Archive").Folder
	if a := archive.Auth; a.Type != projectfile.AuthOAuth1 || a.In != projectfile.APIKeyInHeader || a.ConsumerKey != "archive-key" ||
		a.ConsumerSecret != "legacy-oauth1-secret-8" || a.AccessToken != "{{archive_token}}" || a.TokenSecret != "{{archive_token_secret}}" ||
		a.SignatureMethod != "PLAINTEXT" || a.Timestamp != "1700000000" || a.OmitVersion || len(archive.Diagnostics) != 0 || list.Auth.Type != projectfile.AuthInherit {
		t.Errorf("archive auth = %+v %v", a, codes(archive.Diagnostics))
	}
	create, _ := httpOf(t, m, "Create order")
	if create.Auth.Type != projectfile.AuthBearer || create.Auth.Token != "{{order_token}}" || create.Body.Type != projectfile.RawJSON ||
		len(create.Params) != 1 || create.Params[0].Name != "source" {
		t.Errorf("create = %+v", create)
	}
	sign, sr := httpOf(t, m, "Sign order")
	if a := sign.Auth; a.Type != projectfile.AuthHawk || a.HawkID != "id" || a.HawkKey != "hawk-key-4" || sign.Body.Mode != projectfile.BodyNone ||
		hasCode(sr.Diagnostics, postman.CodeAuthUnsupported) {
		t.Errorf("sign = %+v", sign)
	}
	if s := m.Root.Items[1].Skipped; s == nil || s.Source.Path != "item[1]" {
		t.Errorf("last item = %+v", m.Root.Items[1])
	}
}

func TestEnvironments(t *testing.T) {
	m := parse(t, read(t, "staging.postman_environment.json"))
	if m.Name != "Staging" || len(m.Environments) != 1 || len(m.Root.Items) != 0 {
		t.Fatalf("model = %+v", m)
	}
	e := m.Environments[0]
	if len(e.Variables) != 4 || !e.Variables[1].Secret || e.Variables[1].Value != "stg-token-9f8e" || !e.Variables[2].Disabled || e.Variables[0].Disabled {
		t.Errorf("staging = %+v", e.Variables)
	}
	if e.Variables[3].Value != "{{$guid}}-{{$randomAvatarImage}}" || !hasCode(e.Diagnostics, postman.CodeDynamicUnsupported) {
		t.Errorf("request_id = %+v %v", e.Variables[3], codes(e.Diagnostics))
	}

	g := parse(t, read(t, "workspace.postman_globals.json"))
	ge := g.Environments[0]
	if ge.Name != "My Workspace Globals" || !ge.Variables[1].Secret || ge.Variables[2].Value != "3" || !hasCode(ge.Diagnostics, postman.CodeGlobals) {
		t.Errorf("globals = %+v", ge)
	}
	// A nameless globals file.
	n, err := postman.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(`{"values": [], "_postman_variable_scope": "globals"}`)})
	if err != nil || n.Environments[0].Name != "Globals" {
		t.Errorf("nameless globals = %+v, %v", n, err)
	}
}

func TestDetect(t *testing.T) {
	im := postman.Importer{}
	cases := []struct {
		name, data string
		want       int
	}{
		{"x.json", `{"info": {"schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"}, "item": []}`, importer.Sure},
		{"x.json", "\xef\xbb\xbf {\"info\": {\"schema\": \"https://schema.postman.com/json/collection/v2.0.0/\"}}", importer.Sure},
		{"x.json", `{"name": "E", "values": [], "_postman_variable_scope": "environment"}`, importer.Sure},
		{"x.json", `{"info": {"_postman_id": "1", "name": "x"}, "item": []}`, importer.Likely},
		{"a.postman_collection.json", `{"info": {"name": "x"}}`, importer.Maybe},
		{"x.json", `{"info": {"name": "x"}, "item": []}`, importer.No},
		{"x.json", `{"_type": "export", "__export_format": 4, "resources": []}`, importer.No},
		{"x.json", `{"openapi": "3.1.0", "info": {"title": "x"}}`, importer.No},
		{"x.postman_collection.json", "curl https://x.test", importer.No},
		{"x.json", "", importer.No},
	}
	for _, c := range cases {
		if got := im.Detect(importer.Source{Name: c.name, Data: []byte(c.data)}); got != c.want {
			t.Errorf("Detect(%s, %q) = %d, want %d", c.name, c.data, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	im := postman.Importer{}
	cases := []struct {
		data, line, msg string
	}{
		{"{\n  \"info\": {},\n  \"item\": [\n    {\"name\": }\n  ]\n}", "4", "not valid JSON"},
		{"[1, 2]", "1", "not a JSON object"},
		{`{"info": {"schema": "https://schema.getpostman.com/json/collection/v1.0.0/collection.json"}}`, "1", "Collection v1"},
		{`{"id": "x", "name": "Old", "order": [], "requests": []}`, "1", "Collection v1"},
		{`{"version": 1, "collections": [], "environments": []}`, "1", "data dump"},
		{`{"name": "nothing"}`, "1", "not a Postman export"},
		{`{"info": {"name": "x"}, "item": {}}`, "1", "not a list"},
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
	if _, err := im.Parse(ctx, importer.Source{Data: []byte(`{"info": {}, "item": []}`)}); err == nil {
		t.Error("Parse with a cancelled context succeeded")
	}
}

func TestSchemaVersion(t *testing.T) {
	for data, want := range map[string]string{
		`{"info": {"name": "x"}, "item": []}`: "no schema version",
		`{"info": {"name": "x", "schema": "https://schema.getpostman.com/json/collection/v2.2.0/collection.json"}, "item": []}`: "not v2.0 or v2.1",
		`{"info": {"name": "x", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"}, "item": []}`: "",
	} {
		m, err := postman.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(data)})
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if len(m.Diagnostics) > 0 {
			got = m.Diagnostics[0].Message
		}
		if want == "" && got != "" || !strings.Contains(got, want) {
			t.Errorf("%s: diagnostics %v", data, m.Diagnostics)
		}
	}
}

func TestDynamicVariables(t *testing.T) {
	cases := map[string]string{
		"{{$guid}}":               "{{$guid}}",
		"{{ $timestamp }}":        "{{ $timestamp }}",
		"{{$isoTimestamp}}":       "{{$isoTimestamp}}",
		"{{$randomAlphaNumeric}}": "{{$randomString 1}}",
		"{{$randomHexadecimal}}":  "{{$randomHex 1}}",
		"{{$randomCity}}":         "{{$randomCity}}",
		"{{$randomLoremWords}}":   "{{$randomLoremWords}}",
		"{{$randomAvatarImage}}":  "{{$randomAvatarImage}}",
		"a{{b}}c{{$randomInt}}":   "a{{b}}c{{$randomInt}}",
		"{{unterminated":          "{{unterminated",
		"{{vault:key}}":           "{{vault:key}}",
	}
	for in, want := range cases {
		src := `{"info": {"name": "x"}, "item": [{"name": "R", "request": {"url": ` + quote(in) + `}}]}`
		m, err := postman.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(src)})
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Root.Items[0].Request.File.(*projectfile.HTTPRequest).URL; got != want {
			t.Errorf("conv(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSameDynamicResolves: every Postman dynamic variable kept as written
// is one PostSonde generates, so an imported request sends without
// undefined names; the unsupported ones are reported.
func TestSameDynamicResolves(t *testing.T) {
	for _, name := range postman.SameDynamicNames() {
		if !template.IsDynamic(name) {
			t.Errorf("%s is kept as written but is not a PostSonde dynamic value", name)
		}
	}
	src := `{"info": {"name": "x"}, "item": [{"name": "R", "request": {"url": "{{$randomFullName}}{{$randomAvatarImage}}"}}]}`
	m, err := postman.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	d := m.Root.Items[0].Request.Diagnostics
	if len(d) != 1 || d[0].Code != postman.CodeDynamicUnsupported || !strings.Contains(d[0].Message, "{{$randomAvatarImage}}") {
		t.Errorf("diagnostics = %+v", d)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestOAuth2Auth: Postman's OAuth 2.0 auth becomes an oauth2 auth
// (AUTH-07…10, AUTH-12): every grant with its own fields; the token
// Postman had is never imported.
func TestOAuth2Auth(t *testing.T) {
	src := `{"info": {"name": "x"}, "item": [
  {"name": "CC", "request": {"url": "https://h/", "auth": {"type": "oauth2", "oauth2": [
    {"key": "grant_type", "value": "client_credentials"},
    {"key": "accessTokenUrl", "value": "{{idp}}/oauth/token"},
    {"key": "clientId", "value": "shop"},
    {"key": "clientSecret", "value": "{{client_secret}}"},
    {"key": "scope", "value": "orders:read orders:write"},
    {"key": "client_authentication", "value": "body"},
    {"key": "addTokenTo", "value": "header"},
    {"key": "headerPrefix", "value": "Token"},
    {"key": "tokenName", "value": "mine"},
    {"key": "accessToken", "value": "postman-held-token"},
    {"key": "tokenRequestParams", "value": [
      {"key": "audience", "value": "https://api.shop", "enabled": true, "send_as": "request_body"},
      {"key": "tenant", "value": "{{tenant}}", "enabled": true, "send_as": "request_body"},
      {"key": "debug", "value": "1", "enabled": false, "send_as": "request_body"}
    ]}
  ]}}},
  {"name": "Q", "request": {"url": "https://h/", "auth": {"type": "oauth2", "oauth2": [
    {"key": "grant_type", "value": "client_credentials"},
    {"key": "accessTokenUrl", "value": "https://idp/t"},
    {"key": "addTokenTo", "value": "queryParams"},
    {"key": "tokenRequestParams", "value": [{"key": "x-trace", "value": "1", "send_as": "request_header"}]}
  ]}}},
  {"name": "PKCE", "request": {"url": "https://h/", "auth": {"type": "oauth2", "oauth2": [
    {"key": "grant_type", "value": "authorization_code_with_pkce"},
    {"key": "authUrl", "value": "https://idp/authorize"},
    {"key": "accessTokenUrl", "value": "https://idp/t"},
    {"key": "clientSecret", "value": "pkce-typed-secret"}
  ]}}},
  {"name": "Web callback", "request": {"url": "https://h/", "auth": {"type": "oauth2", "oauth2": [
    {"key": "grant_type", "value": "authorization_code"},
    {"key": "authUrl", "value": "https://idp/authorize"},
    {"key": "redirect_uri", "value": "https://oauth.pstmn.io/v1/callback"},
    {"key": "accessTokenUrl", "value": "https://idp/t"}
  ]}}},
  {"name": "Loopback", "request": {"url": "https://h/", "auth": {"type": "oauth2", "oauth2": [
    {"key": "grant_type", "value": "authorization_code"},
    {"key": "authUrl", "value": "https://idp/authorize"},
    {"key": "redirect_uri", "value": "http://localhost:8765/cb"},
    {"key": "accessTokenUrl", "value": "https://idp/t"}
  ]}}},
  {"name": "Password", "request": {"url": "https://h/", "auth": {"type": "oauth2", "oauth2": [
    {"key": "grant_type", "value": "password_credentials"},
    {"key": "accessTokenUrl", "value": "https://idp/t"},
    {"key": "username", "value": "{{user}}"},
    {"key": "password", "value": "typed-pw"}
  ]}}},
  {"name": "Implicit", "request": {"url": "https://h/", "auth": {"type": "oauth2", "oauth2": [
    {"key": "grant_type", "value": "implicit"},
    {"key": "authUrl", "value": "https://idp/authorize"},
    {"key": "redirect_uri", "value": "http://127.0.0.1:7000/cb"},
    {"key": "clientId", "value": "spa"}
  ]}}}
]}`
	m, err := postman.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	cc, r := httpOf(t, m, "CC")
	a := cc.Auth
	if a.Type != projectfile.AuthOAuth2 || a.GrantType != projectfile.OAuth2ClientCredentials || a.TokenURL != "{{idp}}/oauth/token" ||
		a.ClientID != "shop" || a.ClientSecret != "{{client_secret}}" || a.Scope != "orders:read orders:write" ||
		a.ClientAuth != projectfile.OAuth2ClientAuthBody || a.BearerPrefix() != "Token" || a.Audience != "https://api.shop" || a.In != "" ||
		len(a.TokenParams) != 2 || a.TokenParams[0].Name != "tenant" || a.TokenParams[0].Value != "{{tenant}}" || a.TokenParams[0].Disabled || !a.TokenParams[1].Disabled {
		t.Errorf("client credentials = %+v", a)
	}
	if len(r.Diagnostics) != 0 {
		t.Errorf("diagnostics = %v", r.Diagnostics)
	}
	q, r := httpOf(t, m, "Q")
	if q.Auth.In != projectfile.APIKeyInQuery || q.Auth.Prefix != nil || len(q.Auth.TokenParams) != 0 ||
		!hasCode(r.Diagnostics, postman.CodeAuthChanged) || !strings.Contains(r.Diagnostics[0].Message, "x-trace") {
		t.Errorf("query = %+v %v", q.Auth, r.Diagnostics)
	}
	// The authorization code grant is run: its authorization URL is
	// imported, and its callback when it is on this computer.
	pk, r := httpOf(t, m, "PKCE")
	if pk.Auth.GrantType != projectfile.OAuth2AuthorizationCode || pk.Auth.ClientSecret != "pkce-typed-secret" ||
		pk.Auth.AuthURL != "https://idp/authorize" || pk.Auth.RedirectURL != "" || len(r.Diagnostics) != 0 {
		t.Errorf("pkce = %+v %v", pk.Auth, r.Diagnostics)
	}
	web, r := httpOf(t, m, "Web callback")
	if web.Auth.RedirectURL != "" || !hasCode(r.Diagnostics, postman.CodeAuthChanged) || !strings.Contains(r.Diagnostics[0].Message, "oauth.pstmn.io") {
		t.Errorf("web callback = %+v %v", web.Auth, r.Diagnostics)
	}
	if lo, r := httpOf(t, m, "Loopback"); lo.Auth.RedirectURL != "http://localhost:8765/cb" || len(r.Diagnostics) != 0 {
		t.Errorf("loopback = %+v %v", lo.Auth, r.Diagnostics)
	}
	// The password and implicit grants are run.
	if pw, r := httpOf(t, m, "Password"); pw.Auth.GrantType != projectfile.OAuth2Password || pw.Auth.Username != "{{user}}" ||
		pw.Auth.Password != "typed-pw" || len(r.Diagnostics) != 0 {
		t.Errorf("password = %+v %v", pw.Auth, r.Diagnostics)
	}
	if im, r := httpOf(t, m, "Implicit"); im.Auth.GrantType != projectfile.OAuth2Implicit || im.Auth.AuthURL != "https://idp/authorize" ||
		im.Auth.RedirectURL != "http://127.0.0.1:7000/cb" || im.Auth.ClientID != "spa" || len(r.Diagnostics) != 0 {
		t.Errorf("implicit = %+v %v", im.Auth, r.Diagnostics)
	}
	for _, it := range m.Root.Items {
		if data := marshal(t, it.Request.File); strings.Contains(string(data), "postman-held-token") {
			t.Errorf("a request holds the Postman token: %s", data)
		}
	}
}

// TestJWTAuth: Postman's "JWT Bearer" auth becomes a jwt auth (AUTH-16).
func TestJWTAuth(t *testing.T) {
	src := `{"info": {"name": "x"}, "item": [
  {"name": "H", "request": {"url": "https://h/", "auth": {"type": "jwt", "jwt": [
    {"key": "algorithm", "value": "HS384"},
    {"key": "isSecretBase64Encoded", "value": true},
    {"key": "secret", "value": "{{jwt_secret}}"},
    {"key": "payload", "value": "{\"sub\": \"{{user}}\", \"iat\": {{$timestamp}}}"},
    {"key": "addTokenTo", "value": "header"},
    {"key": "headerPrefix", "value": "JWT"},
    {"key": "queryParamKey", "value": "token"},
    {"key": "header", "value": "{}"}
  ]}}},
  {"name": "Q", "request": {"url": "https://h/", "auth": {"type": "jwt", "jwt": [
    {"key": "algorithm", "value": "RS256"},
    {"key": "privateKey", "value": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----"},
    {"key": "secret", "value": "ignored"},
    {"key": "header", "value": "{\"kid\": \"k1\"}"},
    {"key": "addTokenTo", "value": "queryParam"}
  ]}}}
]}`
	m, err := postman.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	h := m.Root.Items[0].Request.File.(*projectfile.HTTPRequest).Auth
	if h.Type != projectfile.AuthJWT || h.Algorithm != "HS384" || !h.SecretBase64 || h.JWTSecret != "{{jwt_secret}}" ||
		h.Payload != `{"sub": "{{user}}", "iat": {{$timestamp}}}` || h.JWTHeader != "" || h.In != "" || h.BearerPrefix() != "JWT" {
		t.Errorf("header JWT = %+v", h)
	}
	q := m.Root.Items[1].Request.File.(*projectfile.HTTPRequest).Auth
	if q.Type != projectfile.AuthJWT || q.Algorithm != "RS256" || !strings.HasPrefix(q.JWTSecret, "-----BEGIN PRIVATE KEY-----") ||
		q.JWTHeader != `{"kid": "k1"}` || q.In != projectfile.APIKeyInQuery || q.Key != "token" || q.Prefix != nil {
		t.Errorf("query JWT = %+v", q)
	}
	if len(m.Root.Items[0].Request.Diagnostics)+len(m.Root.Items[1].Request.Diagnostics) != 0 {
		t.Errorf("diagnostics = %v %v", m.Root.Items[0].Request.Diagnostics, m.Root.Items[1].Request.Diagnostics)
	}
}
