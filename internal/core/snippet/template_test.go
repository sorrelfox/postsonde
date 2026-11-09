package snippet

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// modeVars are the variables of the mode cases: token and pw are secret.
var modeVars = template.MapScope{Label: "test", Variables: map[string]template.Variable{
	"base":  {Value: "https://api.test"},
	"host":  {Value: "api.test"},
	"id":    {Value: "7"},
	"q":     {Value: "a b"},
	"user":  {Value: "ann"},
	"token": {Value: "t0ken-s3cret", Secret: true},
	"pw":    {Value: "pw-s3cret", Secret: true},
}}

// modeSecrets are the values no templated snippet and no masked resolved
// snippet may hold: the secret variables and the values typed in auths.
var modeSecrets = []string{"t0ken-s3cret", "pw-s3cret", "typed-s3cret", "typed-key-s3cret"}

type modeCase struct {
	name string
	req  projectfile.HTTPRequest
	auth projectfile.Auth
}

func modeCases() []modeCase {
	base := func(url string) projectfile.HTTPRequest { return httpReq("GET", url) }
	return []modeCase{
		{name: "variables", req: with(base("{{base}}/users/:id?q={{q}}&t={{token}}#frag"), func(r *projectfile.HTTPRequest) {
			r.Method = "POST"
			r.PathParams = []projectfile.KeyValue{kv("id", "{{id}}")}
			r.Headers = []projectfile.KeyValue{kv("X-User", "{{user}}"), kv("X-Token", "{{token}}"), kv("X-Uuid", "{{$uuid}}")}
			r.Body = raw(projectfile.RawJSON, `{"user":"{{user}}","token":"{{token}}"}`)
		})},
		{name: "host_variable", req: base("https://{{host}}/items/{{id}}")},
		{name: "userinfo_variables", req: base("https://{{user}}:{{pw}}@api.test/me")},
		{name: "basic_typed", req: base("https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthBasic, Username: "{{user}}", Password: "typed-s3cret"}},
		{name: "basic_references", req: base("https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthBasic, Username: "{{user}}", Password: "{{pw}}"}},
		{name: "bearer_reference", req: base("https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{token}}"}},
		{name: "bearer_typed", req: base("https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "typed-s3cret"}},
		{name: "apikey_query_typed", req: base("https://api.test/me?page=1"),
			auth: projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "api_key", Value: "typed-key-s3cret", In: projectfile.APIKeyInQuery}},
		{name: "apikey_query_reference", req: base("https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "api_key", Value: "{{token}}", In: projectfile.APIKeyInQuery}},
		{name: "digest_typed", req: base("https://api.test/me"),
			auth: projectfile.Auth{Type: projectfile.AuthDigest, Username: "ann", Password: "typed-s3cret"}},
		{name: "typed_authorization_row", req: with(base("https://api.test/me"), func(r *projectfile.HTTPRequest) {
			r.Headers = []projectfile.KeyValue{kv("Authorization", "Bearer typed-s3cret"), kv("Proxy-Authorization", "Basic {{token}}")}
		})},
	}
}

// buildMode builds c resolved (variables filled in) or templated.
func buildMode(t *testing.T, c modeCase, templated bool) (request.HTTPBuild, []string) {
	t.Helper()
	var secrets []string
	resolver := template.Resolver{Scopes: template.Chain{modeVars}}
	expand := func(s string) (string, error) {
		res := resolver.Render(s)
		for _, r := range res.Refs {
			if r.Secret && r.Resolved {
				secrets = append(secrets, r.Value)
			}
		}
		return res.Text, res.Err()
	}
	auth := c.auth
	if auth.Type == "" {
		auth.Type = projectfile.AuthNone
	}
	if templated {
		expand = KeepReferences
		auth, _ = TemplateAuth(auth)
	}
	b, err := request.BuildHTTP(c.req, auth, request.HTTPOptions{Expand: expand})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return b, append(secrets, b.AuthSecrets...)
}

func modeSnippet(t *testing.T, lang Language, c modeCase, templated, reveal bool) string {
	t.Helper()
	b, secrets := buildMode(t, c, templated)
	var r Request
	var err error
	if templated {
		if r, err = FromTemplateBuild(b); err == nil {
			r, _ = r.MaskedTemplate(TypedSecrets(c.auth))
		}
	} else if r, err = FromBuild(b, nil); err == nil && !reveal {
		r, _ = r.Masked(secrets)
	}
	if err != nil {
		t.Fatalf("FromBuild: %v", err)
	}
	code, err := lang.Generate(r)
	if err != nil {
		t.Fatalf("%s: %v", lang.ID, err)
	}
	return code
}

// TestModesGolden: the snippet panel's two modes (GEN-12, GEN-13) for the
// same requests in every language, resolved (secrets masked) and
// templated, in testdata/modes/<language>/<name>.{resolved,templated}.golden;
// regenerate with go test ./internal/core/snippet -run TestModesGolden
// -update. Neither holds a secret.
func TestModesGolden(t *testing.T) {
	for _, lang := range Languages() {
		for _, c := range modeCases() {
			for _, mode := range []string{"resolved", "templated"} {
				t.Run(lang.ID+"/"+c.name+"/"+mode, func(t *testing.T) { checkModeGolden(t, lang, c, mode) })
			}
		}
	}
}

// uuidPattern matches a UUID, as {{$uuid}} makes them.
var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func checkModeGolden(t *testing.T, lang Language, c modeCase, mode string) {
	got := modeSnippet(t, lang, c, mode == "templated", false)
	// A fresh {{$uuid}} each build.
	if mode == "resolved" {
		got = uuidPattern.ReplaceAllString(got, "<uuid>")
	}
	for _, s := range modeSecrets {
		if strings.Contains(got, s) {
			t.Errorf("%s snippet holds %q:\n%s", mode, s, got)
		}
	}
	got += "\n"
	path := filepath.Join("testdata", "modes", lang.ID, c.name+"."+mode+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s:\n%s\nwant:\n%s", mode, got, want)
	}
}

// A reveal shows the secrets of the resolved build, in every language.
func TestModesReveal(t *testing.T) {
	c := modeCases()[0]
	for _, lang := range Languages() {
		if got := modeSnippet(t, lang, c, false, true); !strings.Contains(got, "t0ken-s3cret") {
			t.Errorf("revealed %s snippet lacks the secret:\n%s", lang.ID, got)
		}
	}
	got := modeSnippet(t, languages[0], c, false, true)
	for _, want := range []string{"t=t0ken-s3cret", "X-Token: t0ken-s3cret", `"token":"t0ken-s3cret"`} {
		if !strings.Contains(got, want) {
			t.Errorf("revealed snippet lacks %s:\n%s", want, got)
		}
	}
}

func TestTemplateAuth(t *testing.T) {
	prefix := "Token"
	for _, tt := range []struct {
		name   string
		in     projectfile.Auth
		want   projectfile.Auth
		masked bool
	}{
		{"none", projectfile.Auth{Type: projectfile.AuthNone}, projectfile.Auth{Type: projectfile.AuthNone}, false},
		{"disabled", projectfile.Auth{Type: projectfile.AuthBearer, Token: "x", Disabled: true},
			projectfile.Auth{Type: projectfile.AuthBearer, Token: "x", Disabled: true}, false},
		{"basic typed", projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: "p"},
			projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: Mask}, true},
		{"basic reference", projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: "{{p}}"},
			projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: "{{p}}"}, false},
		{"bearer typed elsewhere", projectfile.Auth{Type: projectfile.AuthBearer, LocalSecrets: []string{"token"}},
			projectfile.Auth{Type: projectfile.AuthBearer, Token: Mask}, true},
		{"apikey", projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "k", Value: "abc{{x}}"},
			projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "k", Value: Mask}, true},
		{"jwt header", projectfile.Auth{Type: projectfile.AuthJWT, JWTSecret: "s", Prefix: &prefix, Key: "X-JWT"},
			standIn(projectfile.APIKeyInHeader, "X-JWT", "Token "+Mask), true},
		{"jwt query", projectfile.Auth{Type: projectfile.AuthJWT, KeyFile: "k.pem", In: projectfile.APIKeyInQuery},
			standIn(projectfile.APIKeyInQuery, "access_token", Mask), true},
		{"jwt without key", projectfile.Auth{Type: projectfile.AuthJWT},
			projectfile.Auth{Type: projectfile.AuthJWT}, false},
		{"oauth2", projectfile.Auth{Type: projectfile.AuthOAuth2, TokenURL: "https://idp/token", ClientSecret: "cs"},
			standIn(projectfile.APIKeyInHeader, "Authorization", "Bearer "+Mask), true},
		{"oauth1 header", projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", ConsumerSecret: "cs"},
			standIn(projectfile.APIKeyInHeader, "Authorization", "OAuth "+Mask), true},
		{"oauth1 query", projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", In: projectfile.APIKeyInQuery},
			standIn(projectfile.APIKeyInQuery, "oauth_signature", Mask), true},
		{"oauth1 body", projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", In: projectfile.AuthInBody},
			projectfile.Auth{Type: projectfile.AuthNone}, false},
		{"awsv4", projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "ak", SecretKey: "sk"},
			standIn(projectfile.APIKeyInHeader, "Authorization", "AWS4-HMAC-SHA256 "+Mask), true},
		{"awsv4 query", projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "ak", In: projectfile.APIKeyInQuery},
			standIn(projectfile.APIKeyInQuery, "X-Amz-Signature", Mask), true},
		{"hawk", projectfile.Auth{Type: projectfile.AuthHawk, HawkID: "id", HawkKey: "k"},
			standIn(projectfile.APIKeyInHeader, "Authorization", "Hawk "+Mask), true},
		{"netrc", projectfile.Auth{Type: projectfile.AuthNetrc}, projectfile.Auth{Type: projectfile.AuthNetrc}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, masked := TemplateAuth(tt.in)
			if got.Type != tt.want.Type || got.Key != tt.want.Key || got.Value != tt.want.Value || got.In != tt.want.In ||
				got.Password != tt.want.Password || got.Token != tt.want.Token || got.Disabled != tt.want.Disabled || masked != tt.masked {
				t.Errorf("TemplateAuth = %+v, %v; want %+v, %v", got, masked, tt.want, tt.masked)
			}
		})
	}
}

func TestLanguageByID(t *testing.T) {
	for _, id := range []string{"", "curl"} {
		if l, err := LanguageByID(id); err != nil || l.ID != LanguageCurl || l.Generate == nil {
			t.Errorf("LanguageByID(%q) = %+v, %v", id, l, err)
		}
	}
	if _, err := LanguageByID("cobol"); err == nil || !strings.Contains(err.Error(), "language") {
		t.Errorf("unknown language: %v", err)
	}
	langs := Languages()
	langs[0].ID = "changed"
	if Languages()[0].ID != LanguageCurl {
		t.Error("Languages shares its slice")
	}
}

func TestRestoreReferences(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.test/%7B%7Bid%7D%7D?k=%7b%7bv%7d%7d":        "https://api.test/{{id}}?k={{v}}",
		"https://api.test/x?q={{a%20b}}":                         "https://api.test/x?q={{a b}}",
		"https://api.test/x?k=" + strings.Repeat("%E2%80%A2", 6): "https://api.test/x?k=" + Mask,
		"https://api.test/%7B%7Bnot closed":                      "https://api.test/%7B%7Bnot closed",
		"https://api.test/x?q=%7B%7B%24randomInt%201%7D%7D":      "https://api.test/x?q={{$randomInt 1}}",
	} {
		if got := restoreReferences(in); got != want {
			t.Errorf("restoreReferences(%q) = %q, want %q", in, got, want)
		}
	}
	u, user := templateURL("https://{{u}}:{{p}}@{{host}}/a?b={{c}}#frag")
	if pass, _ := user.Password(); u != "https://{{host}}/a?b={{c}}" || user.Username() != "{{u}}" || pass != "{{p}}" {
		t.Errorf("templateURL = %q, %v", u, user)
	}
}
