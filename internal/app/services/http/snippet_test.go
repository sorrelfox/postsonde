package http

import (
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func snippetOf(t *testing.T, f *fixture, in SnippetInputDTO) SnippetDTO {
	t.Helper()
	if in.RequestID == "" {
		in.RequestID = "req_1"
	}
	out, err := f.svc.Snippet(t.Context(), in)
	if err != nil {
		t.Fatalf("Snippet(%+v) = %v", in, err)
	}
	return out
}

func TestSnippetLanguages(t *testing.T) {
	t.Parallel()
	f := newFixture(t, httpSource("https://api.test/"), nil)
	langs := f.svc.SnippetLanguages()
	if len(langs) == 0 || langs[0] != (SnippetLanguageDTO{ID: "curl", Name: "cURL", Syntax: "shell"}) {
		t.Errorf("languages = %+v", langs)
	}
	_, err := f.svc.Snippet(t.Context(), SnippetInputDTO{RequestID: "req_1", Language: "cobol"})
	if e := (*errs.Error)(nil); !errors.As(err, &e) || e.Code != snippet.CodeUnknownLanguage {
		t.Errorf("unknown language: %v", err)
	}
	_, err = f.svc.Snippet(t.Context(), SnippetInputDTO{RequestID: "req_1", Mode: "raw"})
	if e := (*errs.Error)(nil); !errors.As(err, &e) || e.Code != CodeSnippetMode {
		t.Errorf("unknown mode: %v", err)
	}
	_, err = f.svc.Snippet(t.Context(), SnippetInputDTO{RequestID: "nope"})
	if errs.KindOf(err) != errs.NotFound {
		t.Errorf("unknown request: %v", err)
	}
}

// GEN-12, GEN-13: the panel's resolved view is what a send sends,
// with the secret variables of every level masked and local values shown;
// revealing shows exactly what the server receives, for that call only;
// the templated view keeps every reference and holds no secret and no
// local value.
func TestSnippetModesAndSecretVariables(t *testing.T) {
	t.Parallel()
	got := make(chan string, 1)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- r.URL.RawQuery + "\n" + r.Header.Get("X-Folder") + "\n" + r.Header.Get("Authorization") + "\n" + string(body)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	f := newFixture(t, secretProject(t, srv.URL), nil)

	// Resolved, the default: masked.
	masked := snippetOf(t, f, SnippetInputDTO{})
	assertNoSecret(t, "the resolved snippet", masked)
	if masked.Mode != SnippetResolved || masked.Language != "curl" || !masked.Masked || masked.Revealed {
		t.Errorf("resolved = %+v", masked)
	}
	for _, want := range []string{
		srv.URL + "/x?g=" + httpclient.Mask + "&e=" + httpclient.Mask,
		"&r=local-region&u=my-user", // local values are not secrets
		"-H 'X-Folder: " + httpclient.Mask + "'",
		"-H 'Authorization: Bearer " + httpclient.Mask + "'",
		`{"g":"` + httpclient.Mask + `","f":"` + httpclient.Mask + `"}`,
	} {
		if !strings.Contains(masked.Code, want) {
			t.Errorf("resolved snippet lacks %s:\n%s", want, masked.Code)
		}
	}

	// Revealed: what a send puts on the wire.
	if done := f.rec.wait(t, f.send(t)); done.Status != operation.StatusOK {
		t.Fatalf("send: %+v", done)
	}
	parts := strings.SplitN(<-got, "\n", 4)
	revealed := snippetOf(t, f, SnippetInputDTO{Mode: SnippetResolved, RevealSecrets: true})
	if revealed.Masked || !revealed.Revealed {
		t.Errorf("revealed = %+v", revealed)
	}
	for _, want := range []string{
		"/x?" + parts[0] + "'",
		"-H 'X-Folder: " + parts[1] + "'",
		"-H 'Authorization: " + parts[2] + "'",
		"--data-raw '" + parts[3] + "'",
	} {
		if !strings.Contains(revealed.Code, want) {
			t.Errorf("revealed snippet lacks what was sent, %s:\n%s", want, revealed.Code)
		}
	}
	for _, s := range leakSecrets {
		if !strings.Contains(revealed.Code, s) {
			t.Errorf("revealed snippet lacks %q", s)
		}
	}

	// A reveal is not kept: the next call is masked again.
	if again := snippetOf(t, f, SnippetInputDTO{}); again.Code != masked.Code || !again.Masked {
		t.Errorf("after a reveal:\n%s\nwant:\n%s", again.Code, masked.Code)
	}

	// Templated: references as written, nothing resolved, nothing to
	// reveal.
	for _, reveal := range []bool{false, true} {
		tpl := snippetOf(t, f, SnippetInputDTO{Mode: SnippetTemplated, RevealSecrets: reveal})
		assertNoSecret(t, "the templated snippet", tpl)
		if tpl.Mode != SnippetTemplated || tpl.Revealed || tpl.Masked || len(tpl.Unresolved) != 0 {
			t.Errorf("templated = %+v", tpl)
		}
		want := "curl '{{base}}/x?g={{globalKey}}&e={{envKey}}&r={{region}}&u={{user}}' \\\n" +
			"  -g \\\n" +
			"  -H 'X-Folder: {{folderKey}}' \\\n" +
			"  -H 'Authorization: Bearer {{envKey}}' \\\n" +
			"  -H 'Content-Type: application/json' \\\n" +
			`  --data-raw '{"g":"{{globalKey}}","f":"{{folderKey}}"}' \` + "\n" +
			"  -L --max-redirs 10"
		if tpl.Code != want {
			t.Errorf("templated (reveal %v):\n%s\nwant:\n%s", reveal, tpl.Code, want)
		}
		for _, local := range []string{"local-region", "my-user", "shared-region", "team-user"} {
			if strings.Contains(tpl.Code, local) {
				t.Errorf("templated snippet holds the value %q", local)
			}
		}
	}
}

// authSecretCase is an auth with its typed secrets, and what each view
// must show.
type authSecretCase struct {
	name    string
	auth    projectfile.Auth
	secrets []string // typed, never in a masked or templated snippet
	// revealed are in the revealed snippet, templated in the templated
	// one, masked in the masked one.
	revealed, templated, masked []string
}

func authSecretCases() []authSecretCase {
	bearer := "Bearer " + httpclient.Mask
	return []authSecretCase{
		{name: "basic", auth: projectfile.Auth{Type: projectfile.AuthBasic, Username: "ann", Password: "basic-pw-1"},
			secrets: []string{"basic-pw-1"}, revealed: []string{"-u ann:basic-pw-1"},
			masked: []string{"-u 'ann:" + httpclient.Mask + "'"}, templated: []string{"-u 'ann:" + httpclient.Mask + "'"}},
		{name: "basic_references", auth: projectfile.Auth{Type: projectfile.AuthBasic, Username: "{{user}}", Password: "{{pw}}"},
			secrets: []string{"var-pw-1"}, revealed: []string{"-u ann:var-pw-1"},
			masked: []string{"-u 'ann:" + httpclient.Mask + "'"}, templated: []string{"-u '{{user}}:{{pw}}'"}},
		{name: "bearer", auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "bearer-tok-1"},
			secrets: []string{"bearer-tok-1"}, revealed: []string{"Bearer bearer-tok-1"},
			masked: []string{bearer}, templated: []string{bearer}},
		{name: "bearer_reference", auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{tok}}"},
			secrets: []string{"var-tok-1"}, revealed: []string{"Bearer var-tok-1"},
			masked: []string{bearer}, templated: []string{"-H 'Authorization: Bearer {{tok}}'"}},
		{name: "apikey_header", auth: projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-Key", Value: "apikey-val-1"},
			secrets: []string{"apikey-val-1"}, revealed: []string{"-H 'X-Key: apikey-val-1'"},
			masked: []string{"-H 'X-Key: " + httpclient.Mask + "'"}, templated: []string{"-H 'X-Key: " + httpclient.Mask + "'"}},
		{name: "apikey_query", auth: projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "key", Value: "apikey-val-2", In: projectfile.APIKeyInQuery},
			secrets: []string{"apikey-val-2"}, revealed: []string{"?key=apikey-val-2"},
			masked: []string{"?key=" + httpclient.Mask}, templated: []string{"?key=" + httpclient.Mask}},
		{name: "digest", auth: projectfile.Auth{Type: projectfile.AuthDigest, Username: "ann", Password: "digest-pw-1"},
			secrets: []string{"digest-pw-1"}, revealed: []string{"--digest -u ann:digest-pw-1"},
			masked: []string{"--digest -u 'ann:" + httpclient.Mask + "'"}, templated: []string{"--digest -u 'ann:" + httpclient.Mask + "'"}},
		{name: "jwt", auth: projectfile.Auth{Type: projectfile.AuthJWT, JWTSecret: "jwt-hmac-s3cret-1", Payload: `{"sub":"ann"}`},
			secrets: []string{"jwt-hmac-s3cret-1", "eyJ"}, revealed: []string{"-H 'Authorization: Bearer eyJ"},
			masked: []string{bearer}, templated: []string{bearer}},
		{name: "jwt_query", auth: projectfile.Auth{Type: projectfile.AuthJWT, JWTSecret: "jwt-hmac-s3cret-2", Payload: `{}`, In: projectfile.APIKeyInQuery},
			secrets: []string{"jwt-hmac-s3cret-2", "eyJ"}, revealed: []string{"?access_token=eyJ"},
			masked: []string{"?access_token=" + httpclient.Mask}, templated: []string{"?access_token=" + httpclient.Mask}},
		{name: "oauth1", auth: projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", ConsumerSecret: "oauth1-cs-1", AccessToken: "at", TokenSecret: "oauth1-ts-1"},
			secrets: []string{"oauth1-cs-1", "oauth1-ts-1", "oauth_signature"}, revealed: []string{"Authorization: OAuth ", "oauth_signature="},
			masked: []string{"-H 'Authorization: OAuth " + httpclient.Mask + "'"}, templated: []string{"-H 'Authorization: OAuth " + httpclient.Mask + "'"}},
		{name: "oauth1_query", auth: projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", ConsumerSecret: "oauth1-cs-2", In: projectfile.APIKeyInQuery},
			secrets: []string{"oauth1-cs-2"}, revealed: []string{"oauth_signature="},
			masked: []string{"oauth_consumer_key=ck"}, templated: []string{"?oauth_signature=" + httpclient.Mask}},
		{name: "awsv4", auth: projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AKID", SecretKey: "aws-sk-1", SessionToken: "aws-st-1", Region: "us-east-1", Service: "s3"},
			secrets: []string{"aws-sk-1", "aws-st-1", "Signature="}, revealed: []string{"X-Amz-Security-Token: aws-st-1", "Signature="},
			masked: []string{"-H 'Authorization: AWS4-HMAC-SHA256 " + httpclient.Mask + "'"}, templated: []string{"-H 'Authorization: AWS4-HMAC-SHA256 " + httpclient.Mask + "'"}},
		{name: "awsv4_query", auth: projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AKID", SecretKey: "aws-sk-2", Region: "us-east-1", Service: "s3", In: projectfile.APIKeyInQuery},
			secrets: []string{"aws-sk-2"}, revealed: []string{"X-Amz-Signature="},
			masked: []string{"X-Amz-Credential="}, templated: []string{"?X-Amz-Signature=" + httpclient.Mask}},
		{name: "hawk", auth: projectfile.Auth{Type: projectfile.AuthHawk, HawkID: "hid", HawkKey: "hawk-key-1"},
			secrets: []string{"hawk-key-1", "mac="}, revealed: []string{"Authorization: Hawk id=\"hid\"", "mac="},
			masked: []string{"-H 'Authorization: Hawk " + httpclient.Mask + "'"}, templated: []string{"-H 'Authorization: Hawk " + httpclient.Mask + "'"}},
		{name: "netrc", auth: projectfile.Auth{Type: projectfile.AuthNetrc},
			secrets: []string{"netrc-pw-1"}, revealed: []string{"-H 'Authorization: " + request.NetrcPlaceholder + "'"},
			masked: []string{"-H 'Authorization: Basic " + httpclient.Mask + "'"}, templated: []string{"-H 'Authorization: Basic " + httpclient.Mask + "'"}},
	}
}

// GEN-13: for every auth type, the resolved view masks what the
// auth adds by default, a reveal shows it, and the templated view never
// holds a typed secret or anything made for one send. Nothing reads a
// netrc file.
func TestSnippetAuthSecrets(t *testing.T) {
	t.Parallel()
	nrc := newTempNetrc(t, "machine api.test login ann password netrc-pw-1\n", 0o600)
	for _, c := range authSecretCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := httpSource("https://api.test/me")
			src.Auth = c.auth
			src.FolderVariables = [][]projectfile.Variable{{
				{Name: "user", Value: "ann"},
				{Name: "pw", Value: "var-pw-1", Secret: true},
				{Name: "tok", Value: "var-tok-1", Secret: true},
			}}
			f := newFixture(t, src, nil, WithNetrc(nrc.env(t)))

			masked := snippetOf(t, f, SnippetInputDTO{})
			tpl := snippetOf(t, f, SnippetInputDTO{Mode: SnippetTemplated})
			revealed := snippetOf(t, f, SnippetInputDTO{RevealSecrets: true})
			if !masked.Masked || !revealed.Revealed || revealed.Masked {
				t.Errorf("masked = %+v, revealed = %+v", masked, revealed)
			}
			// Only references in the auth: nothing to mask in the templated view.
			if want := !strings.Contains(c.name, "reference"); tpl.Masked != want {
				t.Errorf("templated Masked = %v, want %v", tpl.Masked, want)
			}
			for _, s := range c.secrets {
				if strings.Contains(masked.Code, s) {
					t.Errorf("masked snippet holds %q:\n%s", s, masked.Code)
				}
				if strings.Contains(tpl.Code, s) {
					t.Errorf("templated snippet holds %q:\n%s", s, tpl.Code)
				}
			}
			check := func(what, code string, wants []string) {
				for _, want := range wants {
					if !strings.Contains(code, want) {
						t.Errorf("%s snippet lacks %s:\n%s", what, want, code)
					}
				}
			}
			check("masked", masked.Code, c.masked)
			check("revealed", revealed.Code, c.revealed)
			check("templated", tpl.Code, c.templated)
		})
	}
	if nrc.count() != 0 {
		t.Errorf("the netrc file was read %d times", nrc.count())
	}
}

// OAuth 2.0: the panel shows the session's token, masked unless
// revealed, and never asks the token endpoint; the templated view never
// holds the token or the client secret.
func TestSnippetOAuth2(t *testing.T) {
	t.Parallel()
	idp := newTokenIDP(t)
	api := apiServer(t, idp)
	f := newFixture(t, oauth2Source(api.URL, idp.URL), nil)

	// Without a token: the placeholder, nothing asked.
	before := snippetOf(t, f, SnippetInputDTO{RevealSecrets: true})
	if !strings.Contains(before.Code, "Bearer "+request.OAuth2Placeholder) || idp.count() != 0 {
		t.Errorf("before a send: %+v, issued %d", before, idp.count())
	}
	if done, _ := sendOp(t, f); done.Status != operation.StatusOK {
		t.Fatalf("send: %+v", done)
	}
	masked := snippetOf(t, f, SnippetInputDTO{})
	revealed := snippetOf(t, f, SnippetInputDTO{RevealSecrets: true})
	tpl := snippetOf(t, f, SnippetInputDTO{Mode: SnippetTemplated})
	noSecrets(t, "masked snippet", masked, "issued-token")
	noSecrets(t, "templated snippet", tpl, "issued-token")
	noSecrets(t, "revealed snippet", revealed) // the client secret is not sent
	if !masked.Masked || !strings.Contains(masked.Code, "Bearer "+httpclient.Mask) {
		t.Errorf("masked = %+v", masked)
	}
	if !strings.Contains(revealed.Code, "Bearer issued-token-1") {
		t.Errorf("revealed = %+v", revealed)
	}
	if !strings.Contains(tpl.Code, "-H 'Authorization: Bearer "+httpclient.Mask+"'") {
		t.Errorf("templated = %+v", tpl)
	}
	if idp.count() != 1 {
		t.Errorf("the snippets asked the IdP: issued %d", idp.count())
	}
}

// Previews fetch nothing: {{$file}} is not read and {{$prompt}} is not
// asked, in either mode; they stay as written.
func TestSnippetReadsAndAsksNothing(t *testing.T) {
	t.Parallel()
	ws, _, _, _ := envWorkspace(t, "https://api.test")
	writeFile(t, filepath.Join(ws.Root, "token.txt"), "t0ken-from-file")
	src := httpSource("{{base}}/items?id={{$prompt Id}}")
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Token", Value: "{{$file token.txt}}"}}
	src.Workspace = ws
	f := newFixture(t, src, nil)
	for _, mode := range []string{SnippetResolved, SnippetTemplated} {
		out := snippetOf(t, f, SnippetInputDTO{Mode: mode, RevealSecrets: true})
		if strings.Contains(out.Code, "t0ken-from-file") || !strings.Contains(out.Code, "-H 'X-Token: {{$file token.txt}}'") ||
			!strings.Contains(out.Code, "?id={{$prompt") {
			t.Errorf("%s:\n%s", mode, out.Code)
		}
	}
	res := snippetOf(t, f, SnippetInputDTO{})
	if !slices.Contains(res.Unresolved, "$prompt Id") {
		t.Errorf("unresolved = %v", res.Unresolved)
	}
}

// The templated view of a URL whose host is a variable, with path
// parameters and a jar: the URL as written, no cookies.
func TestSnippetTemplatedURL(t *testing.T) {
	t.Parallel()
	src := httpSource("https://{{host}}/users/:id?q={{q}}#top")
	src.Request.PathParams = []projectfile.KeyValue{{Name: "id", Value: "{{userId}}"}}
	src.FolderVariables = [][]projectfile.Variable{{{Name: "host", Value: "api.test"}, {Name: "userId", Value: "7"}, {Name: "q", Value: "x"}}}
	f := newFixture(t, src, fixedJar{jarWith(t, "https://api.test/", "sid=abc")})
	tpl := snippetOf(t, f, SnippetInputDTO{Mode: SnippetTemplated})
	if want := "curl 'https://{{host}}/users/{{userId}}?q={{q}}' \\\n  -g \\\n  -L --max-redirs 10"; tpl.Code != want {
		t.Errorf("templated:\n%s\nwant:\n%s", tpl.Code, want)
	}
	res := snippetOf(t, f, SnippetInputDTO{})
	if want := "curl 'https://api.test/users/7?q=x' \\\n  -b sid=abc \\\n  -L --max-redirs 10"; res.Code != want {
		t.Errorf("resolved:\n%s\nwant:\n%s", res.Code, want)
	}
}

func jarWith(t *testing.T, rawURL, line string) *cookies.Jar {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	jar := cookies.New(cookies.Options{})
	if _, rejected := jar.SetCookieLines(u, []string{line}); len(rejected) > 0 {
		t.Fatalf("rejected %v", rejected)
	}
	return jar
}
