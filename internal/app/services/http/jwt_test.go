package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

func hasCode(err error, code string) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Code == code
}

// hs256Verifies reports whether tok is an HS256 JWT signed with secret.
func hs256Verifies(t *testing.T, tok, secret string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", tok)
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(parts[0] + "." + parts[1]))
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !hmac.Equal(sig, m.Sum(nil)) {
		t.Fatalf("the JWT does not verify: %s", tok)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSendJWT (AUTH-16): a request with a jwt auth inherited from its
// folder sends a token signed at send time, with the request's variables
// (a secret environment variable as the secret); the token and the secret
// are masked in the timeline, the header preview and the copied curl
// command, and the preview of the Auth tab builds the same kind of token.
func TestSendJWT(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	env, err := f.envs.Create("dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.envs.SaveVariables(env.ID, []environments.Variable{
		{Name: "jwtSecret", Value: "dev-signing-secret", Secret: true},
		{Name: "user", Value: "ada"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.envs.SetActive(env.ID); err != nil {
		t.Fatal(err)
	}
	f.write(t, "api/_folder.json", `{"kind": "folder", "id": "fld_api", "name": "API", "auth": {"type": "jwt", "key": "X-Key", "prefix": "", "secret": "{{jwtSecret}}", "payload": "{\"sub\": \"{{user}}\", \"n\": {{$randomInt}}}", "issuedAt": true, "expiresIn": 60, "jwtId": true}}`)
	f.write(t, "api/me.json", `{"kind": "http", "id": "req_me", "name": "Me", "method": "GET", "url": "`+f.servers.URL+`/me", "auth": {"type": "inherit"}}`)
	f.reload(t)

	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_me"})
	if err != nil {
		t.Fatal(err)
	}
	if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, started.OpID)
	// "<uri>|<Authorization>|<basic>|<X-Key>"
	fields := strings.Split(resp.Body, "|")
	if len(fields) != 4 || fields[0] != "/me" || fields[1] != "" {
		t.Fatalf("server got %q", resp.Body)
	}
	tok := fields[3]
	p := hs256Verifies(t, tok, "dev-signing-secret")
	if p["sub"] != "ada" || p["exp"].(float64)-p["iat"].(float64) != 60 || len(p["jti"].(string)) != 36 {
		t.Errorf("payload = %v", p)
	}

	// Masked in the timeline, by value.
	hops := resp.Timeline.Hops
	sent := hops[len(hops)-1].Request
	found := false
	for _, h := range sent.Headers {
		if h.Name == "X-Key" {
			found = true
			if h.Value != httpclient.Mask {
				t.Errorf("timeline X-Key = %q", h.Value)
			}
		}
	}
	if !found || strings.Contains(sent.Head, tok) || !resp.Timeline.Masked {
		t.Errorf("timeline shows the token: %+v", sent)
	}

	// The header preview and the curl command mask their own token.
	pv, err := f.svc.HeaderPreview(t.Context(), HeaderPreviewInputDTO{RequestID: "req_me"})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range pv.Headers {
		if h.Name == "X-Key" && (h.Value != httpclient.Mask || !h.Masked || h.Source != PreviewFromAuth) {
			t.Errorf("preview X-Key = %+v", h)
		}
	}
	c, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_me"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Command, "-H 'X-Key: "+httpclient.Mask+"'") || strings.Contains(c.Command, "eyJ") || !c.Masked {
		t.Errorf("masked curl = %s", c.Command)
	}
	c, err = f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_me", IncludeSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(c.Command, "X-Key: eyJ")
	if i < 0 {
		t.Fatalf("curl with secrets = %s", c.Command)
	}
	hs256Verifies(t, strings.TrimSuffix(strings.Fields(c.Command[i+len("X-Key: "):])[0], "'"), "dev-signing-secret")

	// The Auth tab's preview.
	jp, err := f.svc.JWTPreview(t.Context(), JWTPreviewInputDTO{RequestID: "req_me"})
	if err != nil {
		t.Fatal(err)
	}
	if jp.Algorithm != "HS256" || jp.FolderName != "API" || !strings.Contains(jp.Payload, `"sub": "ada"`) || len(jp.Unresolved) != 0 {
		t.Errorf("preview = %+v", jp)
	}
	hs256Verifies(t, jp.Token, "dev-signing-secret")

	// Another auth type: nothing to preview.
	f.write(t, "api/_folder.json", `{"kind": "folder", "id": "fld_api", "name": "API", "auth": {"type": "bearer", "token": "x"}}`)
	f.reload(t)
	if _, err := f.svc.JWTPreview(t.Context(), JWTPreviewInputDTO{RequestID: "req_me"}); !hasCode(err, CodeNoJWT) {
		t.Errorf("preview of bearer = %v", err)
	}
}
