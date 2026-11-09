package projectfile

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestAuthMembers checks what each auth type writes: its own members only,
// defaults left out, and every member of a type this version does not know.
func TestAuthMembers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty type is inherit", `{"type": ""}`, `{"type":"inherit"}`},
		{"none drops other members", `{"type": "none", "username": "u", "token": "t", "disabled": true, "localSecrets": ["token"]}`, `{"type":"none"}`},
		{"inherit keeps unknown members", `{"type": "inherit", "zNote": 1, "key": "k"}`, `{"type":"inherit","zNote":1}`},
		{
			"basic",
			`{"type": "basic", "token": "t", "password": "{{pw}}", "username": "zoë", "disabled": true}`,
			`{"type":"basic","username":"zoë","password":"{{pw}}","disabled":true}`,
		},
		{
			"basic with a local password",
			`{"type": "basic", "username": "u", "localSecrets": ["token", "password"]}`,
			`{"type":"basic","username":"u","localSecrets":["password"]}`,
		},
		{"bearer default prefix left out", `{"type": "bearer", "prefix": "Bearer", "token": "{{t}}"}`, `{"type":"bearer","token":"{{t}}"}`},
		{"bearer empty prefix kept", `{"type": "bearer", "prefix": "", "token": "{{t}}"}`, `{"type":"bearer","prefix":"","token":"{{t}}"}`},
		{"bearer custom prefix", `{"type": "bearer", "prefix": "Token", "localSecrets": ["token"]}`, `{"type":"bearer","prefix":"Token","localSecrets":["token"]}`},
		{"apikey header default left out", `{"type": "apikey", "in": "header", "key": "X-Key", "value": "{{k}}"}`, `{"type":"apikey","key":"X-Key","value":"{{k}}"}`},
		{"apikey query", `{"type": "apikey", "in": "query", "key": "k", "password": "p"}`, `{"type":"apikey","key":"k","in":"query"}`},
		{"apikey unknown location kept", `{"type": "apikey", "in": "cookie", "key": "k"}`, `{"type":"apikey","key":"k","in":"cookie"}`},
		{
			"jwt keeps its members only",
			`{"type": "jwt", "algorithm": "RS256", "keyFile": "keys/k.pem", "secretBase64": true, "jwtHeader": "{\"kid\":\"1\"}", "payload": "{}", "issuedAt": true, "notBefore": true, "jwtId": true, "expiresIn": 300, "prefix": "Bearer", "in": "header", "key": "", "token": "t", "localSecrets": ["secret"]}`,
			`{"type":"jwt","algorithm":"RS256","keyFile":"keys/k.pem","secretBase64":true,"jwtHeader":"{\"kid\":\"1\"}","payload":"{}","issuedAt":true,"notBefore":true,"jwtId":true,"expiresIn":300,"localSecrets":["secret"]}`,
		},
		{
			"jwt in the query with a secret reference",
			`{"type": "jwt", "in": "query", "key": "jwt", "prefix": "", "secret": "{{jwtSecret}}", "disabled": true}`,
			`{"type":"jwt","prefix":"","key":"jwt","in":"query","secret":"{{jwtSecret}}","disabled":true}`,
		},
		{
			"netrc keeps its file only",
			`{"type": "netrc", "netrcFile": "~/.netrc-work", "username": "u", "password": "p", "localSecrets": ["password"], "disabled": true}`,
			`{"type":"netrc","netrcFile":"~/.netrc-work","disabled":true}`,
		},
		{"netrc without a file", `{"type": "netrc", "keyFile": "k"}`, `{"type":"netrc"}`},
		{
			"oauth2 keeps its members only",
			`{"type": "oauth2", "tokenUrl": "https://idp.test/token", "clientId": "app", "clientSecret": "", "clientAuth": "header", "scope": "a b", "audience": "api", "resource": "https://api.test", "state": "s", "tokenParams": [{"name": "x", "value": "1"}], "prefix": "Bearer", "in": "header", "key": "", "token": "t", "consumerKey": "k", "localSecrets": ["clientSecret", "token"], "disabled": true}`,
			`{"type":"oauth2","grantType":"client_credentials","tokenUrl":"https://idp.test/token","clientId":"app","scope":"a b","audience":"api","resource":"https://api.test","state":"s","tokenParams":[{"name":"x","value":"1"}],"disabled":true,"localSecrets":["clientSecret"]}`,
		},
		{
			"oauth2 password grant with OpenID Connect",
			`{"type": "oauth2", "grantType": "password", "tokenUrl": "https://idp.test/token", "clientId": "app", "username": "ada", "password": "", "issuer": "https://idp.test", "idToken": true, "localSecrets": ["password"]}`,
			`{"type":"oauth2","username":"ada","grantType":"password","tokenUrl":"https://idp.test/token","clientId":"app","issuer":"https://idp.test","idToken":true,"localSecrets":["password"]}`,
		},
		{
			"oauth2 authorization code with its endpoints",
			`{"type": "oauth2", "grantType": "authorization_code", "redirectUrl": "http://127.0.0.1:8765/callback", "authUrl": "https://idp.test/authorize", "tokenUrl": "https://idp.test/token", "clientId": "app"}`,
			`{"type":"oauth2","grantType":"authorization_code","tokenUrl":"https://idp.test/token","authUrl":"https://idp.test/authorize","redirectUrl":"http://127.0.0.1:8765/callback","clientId":"app"}`,
		},
		{
			"oauth2 in the query with a body client auth",
			`{"type": "oauth2", "grantType": "authorization_code", "clientAuth": "body", "in": "query", "key": "token", "prefix": "", "clientSecret": "{{cs}}"}`,
			`{"type":"oauth2","prefix":"","key":"token","in":"query","grantType":"authorization_code","clientSecret":"{{cs}}","clientAuth":"body"}`,
		},
		{
			"unknown type keeps every member",
			`{"type": "edgegrid", "authId": "r", "username": "u", "password": "p", "prefix": "Bearer", "in": "header", "localSecrets": ["x"]}`,
			`{"type":"edgegrid","username":"u","password":"p","prefix":"Bearer","in":"header","localSecrets":["x"],"authId":"r"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a Auth
			if err := json.Unmarshal([]byte(tt.in), &a); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
			// Writing is stable.
			var again Auth
			if err := json.Unmarshal(got, &again); err != nil {
				t.Fatal(err)
			}
			if second, _ := json.Marshal(again); string(second) != string(got) {
				t.Errorf("second write %s differs from %s", second, got)
			}
		})
	}
}

func TestAuthSecretHelpers(t *testing.T) {
	tests := []struct {
		auth   Auth
		member string
	}{
		{Auth{Type: AuthBasic, Password: "p"}, "password"},
		{Auth{Type: AuthBearer, Token: "p"}, "token"},
		{Auth{Type: AuthAPIKey, Value: "p"}, "value"},
		{Auth{Type: AuthJWT, JWTSecret: "p"}, "secret"},
		{Auth{Type: AuthOAuth2, ClientSecret: "p", Password: "q"}, "clientSecret"},
		{Auth{Type: AuthNone}, ""},
		{Auth{Type: AuthInherit}, ""},
		{Auth{Type: "edgegrid", Password: "p"}, ""},
	}
	for _, tt := range tests {
		if got := tt.auth.SecretMember(); got != tt.member {
			t.Errorf("%s: SecretMember = %q, want %q", tt.auth.Type, got, tt.member)
		}
		wantSecret := ""
		if tt.member != "" {
			wantSecret = "p"
		}
		if got := tt.auth.Secret(); got != wantSecret {
			t.Errorf("%s: Secret = %q, want %q", tt.auth.Type, got, wantSecret)
		}
		changed := tt.auth.WithSecret("new")
		if tt.member == "" {
			if !reflect.DeepEqual(changed, tt.auth) {
				t.Errorf("%s: WithSecret changed a type without a secret", tt.auth.Type)
			}
		} else if changed.Secret() != "new" {
			t.Errorf("%s: WithSecret did not set the secret", tt.auth.Type)
		}
		local := tt.auth
		local.LocalSecrets = []string{tt.member}
		if got := local.SecretIsLocal(); got != (tt.member != "") {
			t.Errorf("%s: SecretIsLocal = %v", tt.auth.Type, got)
		}
	}
	if p := (Auth{Type: AuthBearer}).BearerPrefix(); p != DefaultBearerPrefix {
		t.Errorf("default prefix = %q", p)
	}
	if p := (Auth{Type: AuthBearer, Prefix: ptr("")}).BearerPrefix(); p != "" {
		t.Errorf("empty prefix = %q", p)
	}
	if IsKnownAuthType("edgegrid") || !IsKnownAuthType("") || !IsKnownAuthType(AuthAPIKey) {
		t.Error("IsKnownAuthType")
	}
}
