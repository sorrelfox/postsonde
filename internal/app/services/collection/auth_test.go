package collection

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	core "github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

const typedToken = "tok-Zoë-s3cr3t"

func secretsFile(t *testing.T, svc *CollectionService, id string) string {
	t.Helper()
	ws := svc.manager.Current()
	if ws == nil {
		t.Fatal("no workspace")
	}
	return requestsecrets.New(ws.LocalDir).Path(id)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// TestAuthSecretsNeverInProjectFile saves each scheme with a typed secret
// and checks the bytes of the project file, the side file, and what
// Request returns before and after the write and in a fresh service.
func TestAuthSecretsNeverInProjectFile(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		auth   AuthDTO
		member string
	}{
		{"basic", AuthDTO{Type: "basic", Username: "zoë", Password: typedToken}, "password"},
		{"bearer", AuthDTO{Type: "bearer", Prefix: ptrTo("Token"), Token: typedToken}, "token"},
		{"apikey", AuthDTO{Type: "apikey", Key: "X-Key", Value: typedToken, In: "query"}, "value"},
		{"jwt", AuthDTO{Type: "jwt", Secret: typedToken, JWT: &JWTAuthDTO{Algorithm: "HS512", Payload: `{"sub":"u"}`, IssuedAt: true}}, "secret"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, manager, dir, _, _ := newAutosaveService(t)
			req := getRequest(t, svc, "req_health")
			req.Auth = tt.auth
			update(t, svc, req, 1)

			// Pending: the typed value comes back, marked local.
			pending := getRequest(t, svc, "req_health")
			if got := secretOf(pending.Auth); got != typedToken || !slices.Equal(pending.Auth.LocalSecrets, []string{tt.member}) {
				t.Errorf("pending auth = %+v", pending.Auth)
			}

			if err := svc.Flush(t.Context(), "req_health"); err != nil {
				t.Fatal(err)
			}
			data := readFile(t, dir, "health.json")
			if strings.Contains(data, typedToken) || strings.Contains(data, "s3cr3t") {
				t.Errorf("the project file holds the secret:\n%s", data)
			}
			if !strings.Contains(data, `"localSecrets": [`) || !strings.Contains(data, `"`+tt.member+`"`) {
				t.Errorf("the project file does not mark the secret local:\n%s", data)
			}
			side := secretsFile(t, svc, "req_health")
			sideData, err := os.ReadFile(side)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(sideData), typedToken) {
				t.Errorf("side file = %s", sideData)
			}
			if runtime.GOOS != "windows" {
				if fi, _ := os.Stat(side); fi.Mode().Perm() != 0o600 {
					t.Errorf("side file mode = %v", fi.Mode().Perm())
				}
			}
			if strings.HasPrefix(side, dir) {
				t.Errorf("side file %s is inside the project", side)
			}

			// A fresh service reads it back.
			fresh := New(manager, nil, nil)
			if got := secretOf(getRequest(t, fresh, "req_health").Auth); got != typedToken {
				t.Errorf("reloaded secret = %q", got)
			}
		})
	}
}

// TestOAuth1SecretsNeverInProjectFile (AUTH-15): an oauth1 auth has two
// secret members; both typed values go to the side file, a reference
// stays in the project, and each is marked on its own.
func TestOAuth1SecretsNeverInProjectFile(t *testing.T) {
	t.Parallel()
	svc, manager, dir, _, _ := newAutosaveService(t)
	req := getRequest(t, svc, "req_health")
	req.Auth = AuthDTO{Type: "oauth1", ConsumerSecret: "consumer-s3cr3t", TokenSecret: "token-s3cr3t",
		OAuth1: &OAuth1AuthDTO{ConsumerKey: "ck", AccessToken: "tok", SignatureMethod: "HMAC-SHA256"}}
	update(t, svc, req, 1)
	if err := svc.Flush(t.Context(), "req_health"); err != nil {
		t.Fatal(err)
	}
	data := readFile(t, dir, "health.json")
	if strings.Contains(data, "s3cr3t") || !strings.Contains(data, `"consumerKey": "ck"`) {
		t.Errorf("project file:\n%s", data)
	}
	if !strings.Contains(data, `"consumerSecret",`) || !strings.Contains(data, `"tokenSecret"`) {
		t.Errorf("the project file does not mark both secrets local:\n%s", data)
	}
	sideData, err := os.ReadFile(secretsFile(t, svc, "req_health"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sideData), `"auth.consumerSecret": "consumer-s3cr3t"`) || !strings.Contains(string(sideData), `"auth.tokenSecret": "token-s3cr3t"`) {
		t.Errorf("side file = %s", sideData)
	}
	fresh := New(manager, nil, nil)
	got := getRequest(t, fresh, "req_health").Auth
	if got.ConsumerSecret != "consumer-s3cr3t" || got.TokenSecret != "token-s3cr3t" || got.OAuth1 == nil || got.OAuth1.SignatureMethod != "HMAC-SHA256" {
		t.Errorf("reloaded auth = %+v", got)
	}

	// A reference goes to the project; the typed one stays local.
	got.TokenSecret = "{{tokenSecret}}"
	got.LocalSecrets = []string{"consumerSecret"}
	req = getRequest(t, fresh, "req_health")
	req.Auth = got
	update(t, fresh, req, 2)
	if err := fresh.Flush(t.Context(), "req_health"); err != nil {
		t.Fatal(err)
	}
	data = readFile(t, dir, "health.json")
	if !strings.Contains(data, `"tokenSecret": "{{tokenSecret}}"`) || strings.Contains(data, "s3cr3t") || !strings.Contains(data, `"localSecrets": [
      "consumerSecret"
    ]`) {
		t.Errorf("project file:\n%s", data)
	}
}

// TestAWSAndHawkSecretsNeverInProjectFile (AUTH-17, AUTH-18): the AWS
// secret key and session token and the Hawk key go to the side file; the
// other members stay in the project.
func TestAWSAndHawkSecretsNeverInProjectFile(t *testing.T) {
	t.Parallel()
	svc, manager, dir, _, _ := newAutosaveService(t)
	req := getRequest(t, svc, "req_health")
	req.Auth = AuthDTO{Type: "awsv4", SecretKey: "aws-s3cr3t", SessionToken: "session-s3cr3t",
		AWS: &AWSAuthDTO{AccessKey: "AKID", Region: "us-east-1", Service: "execute-api"}}
	update(t, svc, req, 1)
	if err := svc.Flush(t.Context(), "req_health"); err != nil {
		t.Fatal(err)
	}
	data := readFile(t, dir, "health.json")
	if strings.Contains(data, "s3cr3t") || !strings.Contains(data, `"accessKey": "AKID"`) || !strings.Contains(data, `"service": "execute-api"`) ||
		!strings.Contains(data, `"secretKey",`) || !strings.Contains(data, `"sessionToken"`) {
		t.Errorf("project file:\n%s", data)
	}
	sideData, err := os.ReadFile(secretsFile(t, svc, "req_health"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sideData), `"auth.secretKey": "aws-s3cr3t"`) || !strings.Contains(string(sideData), `"auth.sessionToken": "session-s3cr3t"`) {
		t.Errorf("side file = %s", sideData)
	}
	fresh := New(manager, nil, nil)
	got := getRequest(t, fresh, "req_health").Auth
	if got.SecretKey != "aws-s3cr3t" || got.SessionToken != "session-s3cr3t" || got.AWS == nil || got.AWS.Region != "us-east-1" {
		t.Errorf("reloaded auth = %+v", got)
	}

	req = getRequest(t, fresh, "req_health")
	req.Auth = AuthDTO{Type: "hawk", HawkKey: "hawk-s3cr3t", Hawk: &HawkAuthDTO{ID: "dh37", Algorithm: "sha1", Ext: "x"}}
	update(t, fresh, req, 2)
	if err := fresh.Flush(t.Context(), "req_health"); err != nil {
		t.Fatal(err)
	}
	data = readFile(t, dir, "health.json")
	if strings.Contains(data, "s3cr3t") || !strings.Contains(data, `"hawkId": "dh37"`) || !strings.Contains(data, `"hawkKey"`) {
		t.Errorf("project file:\n%s", data)
	}
	got = getRequest(t, New(manager, nil, nil), "req_health").Auth
	if got.HawkKey != "hawk-s3cr3t" || got.Hawk == nil || got.Hawk.Algorithm != "sha1" {
		t.Errorf("reloaded auth = %+v", got)
	}
}

// TestOAuth2ClientSecretNeverInProjectFile (AUTH-08): the client secret
// goes to the side file; the token endpoint, client ID and scopes stay in
// the project. No token is ever written.
func TestOAuth2ClientSecretNeverInProjectFile(t *testing.T) {
	t.Parallel()
	svc, manager, dir, _, _ := newAutosaveService(t)
	req := getRequest(t, svc, "req_health")
	req.Auth = AuthDTO{Type: "oauth2", ClientSecret: "oauth2-s3cr3t", OAuth2: &OAuth2AuthDTO{
		TokenURL: "https://idp.test/token", ClientID: "app", Scope: "read write", Audience: "api",
		TokenParams: []KeyValueDTO{{Name: "tenant", Value: "t1", Enabled: true}},
	}}
	update(t, svc, req, 1)
	if err := svc.Flush(t.Context(), "req_health"); err != nil {
		t.Fatal(err)
	}
	data := readFile(t, dir, "health.json")
	if strings.Contains(data, "s3cr3t") || !strings.Contains(data, `"clientId": "app"`) || !strings.Contains(data, `"scope": "read write"`) ||
		!strings.Contains(data, `"localSecrets": [`) || !strings.Contains(data, `"clientSecret"`) || !strings.Contains(data, `"tenant"`) {
		t.Errorf("project file:\n%s", data)
	}
	sideData, err := os.ReadFile(secretsFile(t, svc, "req_health"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sideData), `"auth.clientSecret": "oauth2-s3cr3t"`) {
		t.Errorf("side file = %s", sideData)
	}
	got := getRequest(t, New(manager, nil, nil), "req_health").Auth
	if got.ClientSecret != "oauth2-s3cr3t" || got.OAuth2 == nil || got.OAuth2.TokenURL != "https://idp.test/token" || len(got.OAuth2.TokenParams) != 1 {
		t.Errorf("reloaded auth = %+v", got)
	}
}

func secretOf(a AuthDTO) string {

	switch a.Type {
	case "basic":
		return a.Password
	case "bearer":
		return a.Token
	case "apikey":
		return a.Value
	case "jwt":
		return a.Secret
	}
	return ""
}

func ptrTo[T any](v T) *T { return &v }

func TestAuthSecretLifecycle(t *testing.T) {
	t.Parallel()
	svc, manager, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()
	side := secretsFile(t, svc, "req_health")
	flush := func() {
		t.Helper()
		if err := svc.Flush(ctx, "req_health"); err != nil {
			t.Fatal(err)
		}
	}

	// A {{variable}} reference is shared: in the project, no side file.
	req := getRequest(t, svc, "req_health")
	req.Auth = AuthDTO{Type: "bearer", Token: "{{token}}"}
	update(t, svc, req, 1)
	flush()
	if data := readFile(t, dir, "health.json"); !strings.Contains(data, `"token": "{{token}}"`) || strings.Contains(data, "localSecrets") {
		t.Errorf("reference not in the project file:\n%s", data)
	}
	if fileExists(side) {
		t.Error("a reference created a side file")
	}

	// Typed: local.
	req.Auth.Token = typedToken
	update(t, svc, req, 2)
	flush()
	if !fileExists(side) {
		t.Fatal("no side file")
	}

	// On another machine (no side file), the mark stays through other edits.
	if err := os.Remove(side); err != nil {
		t.Fatal(err)
	}
	other := New(manager, nil, nil)
	missing := getRequest(t, other, "req_health")
	if missing.Auth.Token != "" || !slices.Equal(missing.Auth.LocalSecrets, []string{"token"}) {
		t.Errorf("missing secret = %+v", missing.Auth)
	}
	missing.URL = "/health2"
	update(t, other, missing, 3)
	if err := other.Flush(ctx, "req_health"); err != nil {
		t.Fatal(err)
	}
	if data := readFile(t, dir, "health.json"); !strings.Contains(data, `"localSecrets"`) {
		t.Errorf("the mark was dropped by an unrelated edit:\n%s", data)
	}
	if _, err := request.Auth(fileAuthOf(t, other, "req_health"), nil, nil); !errors.Is(err, &errs.Error{Code: request.CodeAuthSecretMissing}) {
		t.Errorf("sending without the secret: %v", err)
	}

	// Typed again, then cleared by the editor (mark removed): the side
	// file goes.
	missing.Auth.Token = typedToken
	update(t, other, missing, 4)
	if err := other.Flush(ctx, "req_health"); err != nil {
		t.Fatal(err)
	}
	if !fileExists(side) {
		t.Fatal("no side file after typing the secret again")
	}
	missing.Auth.Token = ""
	missing.Auth.LocalSecrets = []string{}
	update(t, other, missing, 5)
	if err := other.Flush(ctx, "req_health"); err != nil {
		t.Fatal(err)
	}
	if fileExists(side) {
		t.Error("clearing the secret kept the side file")
	}
	if data := readFile(t, dir, "health.json"); strings.Contains(data, "localSecrets") {
		t.Errorf("clearing the secret kept the mark:\n%s", data)
	}

	// Switching type drops the local secret of the old type.
	missing.Auth = AuthDTO{Type: "basic", Username: "u", Password: typedToken}
	update(t, other, missing, 6)
	missing.Auth.Type = "none"
	update(t, other, missing, 7)
	if err := other.Flush(ctx, "req_health"); err != nil {
		t.Fatal(err)
	}
	if fileExists(side) {
		t.Error("auth none kept a side file")
	}
	if data := readFile(t, dir, "health.json"); strings.Contains(data, typedToken) || strings.Contains(data, `"username"`) {
		t.Errorf("auth none kept members:\n%s", data)
	}
}

// fileAuthOf returns the auth of request id as the tree has it on disk.
func fileAuthOf(t *testing.T, svc *CollectionService, id string) projectfile.Auth {
	t.Helper()
	svc.mu.Lock()
	defer svc.mu.Unlock()
	_, tree, err := svc.currentLocked(true)
	if err != nil {
		t.Fatal(err)
	}
	p := fileAuth(tree.Node(id).File)
	if p == nil {
		t.Fatalf("%s has no auth", id)
	}
	return *p
}

func TestAuthSecretsFollowTreeOperations(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()
	writeFile(t, dir, "team/_folder.json", `{"kind": "folder", "id": "fld_team", "name": "Team", "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "team/login.json", `{"kind": "http", "id": "req_login", "name": "Login", "method": "POST", "body": {"mode": "none"}, "auth": {"type": "inherit"}}`)
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, "req_login")
	req.Auth = AuthDTO{Type: "basic", Username: "zoë", Password: typedToken}
	update(t, svc, req, 1)

	// Duplicate flushes the edit and copies the secret.
	res, err := svc.Duplicate(ctx, "req_login")
	if err != nil {
		t.Fatal(err)
	}
	if got := getRequest(t, svc, res.NodeID).Auth.Password; got != typedToken {
		t.Errorf("duplicate's password = %q", got)
	}

	// Duplicating the folder copies it for the request inside.
	folder, err := svc.Duplicate(ctx, "fld_team")
	if err != nil {
		t.Fatal(err)
	}
	copied := ""
	svc.mu.Lock()
	svc.tree.Walk(func(n *core.Node) bool {
		if n.Kind == core.KindRequest && n.Name == "Login" && n.Parent != nil && n.Parent.ID == folder.NodeID {
			copied = n.ID
		}
		return true
	})
	svc.mu.Unlock()
	if copied == "" || copied == "req_login" {
		t.Fatalf("no copy of the request in the copied folder (%q)", copied)
	}
	if got := getRequest(t, svc, copied).Auth.Password; got != typedToken {
		t.Errorf("folder copy's password = %q", got)
	}

	// Rename and move keep it (keyed by ID).
	if _, err := svc.Rename(ctx, RenameInputDTO{ID: "req_login", Name: "Sign in"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(ctx, MoveInputDTO{ID: "req_login", ParentID: "", Index: -1}); err != nil {
		t.Fatal(err)
	}
	if got := getRequest(t, svc, "req_login").Auth.Password; got != typedToken {
		t.Errorf("password after rename and move = %q", got)
	}

	// Delete keeps the side file, so restoring brings the password back.
	del, err := svc.Delete(ctx, "req_login")
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(secretsFile(t, svc, "req_login")) {
		t.Error("delete removed the side file")
	}
	if _, err := svc.Restore(ctx, del.Entry.ID); err != nil {
		t.Fatal(err)
	}
	if got := getRequest(t, svc, "req_login").Auth.Password; got != typedToken {
		t.Errorf("password after restore = %q", got)
	}

	// No project file ever held the password.
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), typedToken) {
			t.Errorf("%s holds the password", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthBrokenSideFile(t *testing.T) {
	t.Parallel()
	svc, manager, _, _, _ := newAutosaveService(t)
	req := getRequest(t, svc, "req_health")
	req.Auth = AuthDTO{Type: "bearer", Token: typedToken}
	update(t, svc, req, 1)
	if err := svc.Flush(t.Context(), "req_health"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsFile(t, svc, "req_health"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(manager, nil, nil).Request(t.Context(), "req_health")
	checkCode(t, err, errs.Parse, requestsecrets.CodeLocalDataFailed)
}

func TestInheritedAuth(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	writeFile(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "auth": {"type": "bearer", "token": "{{token}}"}}`)
	writeFile(t, dir, "users/admins/_folder.json", `{"kind": "folder", "id": "fld_admins", "name": "Admins", "auth": {"type": "inherit"}}`)
	writeFile(t, dir, "users/admins/list.json", `{"kind": "http", "id": "req_list", "name": "List", "method": "GET", "auth": {"type": "none"}}`)
	writeFile(t, dir, "broken/_folder.json", `{"kind": "folder", "id": `)
	writeFile(t, dir, "broken/inner.json", `{"kind": "http", "id": "req_inner", "name": "Inner", "method": "GET"}`)
	svc := New(manager, nil, nil)
	ctx := t.Context()

	// head drops the folder's auth fields, checked below.
	type headDTO struct{ Type, FolderID, FolderName string }
	head := func(d InheritedAuthDTO) headDTO {
		if d.Auth.Type != d.Type {
			t.Errorf("auth type %q, want %q", d.Auth.Type, d.Type)
		}
		return headDTO{d.Type, d.FolderID, d.FolderName}
	}
	got, err := svc.InheritedAuth(ctx, "req_list")
	if err != nil {
		t.Fatal(err)
	}
	if head(got) != (headDTO{Type: "bearer", FolderID: "fld_users", FolderName: "Users"}) || got.Auth.Token != "{{token}}" {
		t.Errorf("InheritedAuth(req_list) = %+v", got)
	}
	if got, err := svc.InheritedAuth(ctx, "req_health"); err != nil || head(got) != (headDTO{Type: "none"}) {
		t.Errorf("InheritedAuth(req_health) = %+v, %v", got, err)
	}
	_, err = svc.InheritedAuth(ctx, "req_inner")
	checkCode(t, err, errs.InvalidArgument, request.CodeAuthInheritFailed)
	// A folder inherits from the folders above it (COL-03).
	if got, err := svc.InheritedAuth(ctx, "fld_admins"); err != nil || head(got) != (headDTO{Type: "bearer", FolderID: "fld_users", FolderName: "Users"}) {
		t.Errorf("InheritedAuth(fld_admins) = %+v, %v", got, err)
	}
	if got, err := svc.InheritedAuth(ctx, "fld_users"); err != nil || head(got) != (headDTO{Type: "none"}) {
		t.Errorf("InheritedAuth(fld_users) = %+v, %v", got, err)
	}

	// A typed secret never leaves the folder's own editor: the note gets
	// "" and the local mark; references and the other fields come through.
	writeFile(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "auth": {"type": "basic", "username": "{{user}}", "password": "hand-typed-s3cret"}}`)
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = svc.InheritedAuth(ctx, "req_list")
	if err != nil || got.Auth.Username != "{{user}}" || got.Auth.Password != "" || !slices.Equal(got.Auth.LocalSecrets, []string{"password"}) {
		t.Errorf("typed secret: %+v, %v", got.Auth, err)
	}
	folder := getFolder(t, svc, "fld_users")
	folder.Auth = AuthDTO{Type: "apikey", Key: "X-Key", Value: "typed-key", In: "query"}
	if _, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: folder, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	got, err = svc.InheritedAuth(ctx, "req_list")
	if err != nil || got.Type != "apikey" || got.Auth.Key != "X-Key" || got.Auth.In != "query" || got.Auth.Value != "" || !slices.Equal(got.Auth.LocalSecrets, []string{"value"}) {
		t.Errorf("pending typed secret: %+v, %v", got.Auth, err)
	}
	_, err = svc.InheritedAuth(ctx, "fld_missing")
	checkCode(t, err, errs.NotFound, core.CodeNotFound)
}

// TestAuthDTORoundTrip maps auth of every type to a DTO, through JSON as
// the UI sends it, and back: the file encodes to the same bytes.
func TestAuthDTORoundTrip(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		`{"type": "inherit"}`,
		`{"type": "none", "zNote": true}`,
		`{"type": "basic", "username": "zoë", "password": "{{pw}}", "disabled": true}`,
		`{"type": "basic", "username": "u", "localSecrets": ["password"]}`,
		`{"type": "bearer", "prefix": "", "token": "{{token}}"}`,
		`{"type": "bearer", "prefix": "Token", "localSecrets": ["token"]}`,
		`{"type": "apikey", "key": "api_key", "in": "query", "localSecrets": ["value"]}`,
		`{"type": "apikey", "key": "X-Key", "value": "{{key}}"}`,
		`{"type": "jwt", "algorithm": "ES384", "keyFile": "keys/ec.pem", "jwtHeader": "{\"kid\": \"1\"}", "payload": "{\"sub\": \"{{user}}\"}", "issuedAt": true, "notBefore": true, "jwtId": true, "expiresIn": 300, "localSecrets": ["secret"]}`,
		`{"type": "jwt", "secret": "{{jwtSecret}}", "secretBase64": true, "prefix": "", "key": "tok", "in": "query", "disabled": true}`,
		`{"type": "edgegrid", "realm": "r", "username": "u", "password": "p", "prefix": "Bearer", "localSecrets": ["x"]}`,
		`{"type": "oauth1", "consumerKey": "ck", "consumerSecret": "{{cs}}", "accessToken": "{{tok}}", "signatureMethod": "HMAC-SHA256", "callback": "oob", "verifier": "v", "realm": "Photos", "omitVersion": true, "nonce": "n", "timestamp": "1", "bodyHash": true, "in": "body", "localSecrets": ["tokenSecret"]}`,
		`{"type": "oauth1", "consumerKey": "ck", "localSecrets": ["consumerSecret", "tokenSecret"], "disabled": true}`,
		`{"type": "awsv4", "accessKey": "AK", "secretKey": "{{sk}}", "region": "eu-west-1", "service": "s3", "unsignedPayload": true, "expires": 600, "in": "query", "localSecrets": ["sessionToken"]}`,
		`{"type": "hawk", "hawkId": "id", "hawkKey": "{{hk}}", "hawkAlgorithm": "sha1", "ext": "e", "app": "a", "dlg": "d", "payloadHash": true, "disabled": true}`,
		`{"type": "netrc", "netrcFile": "~/.netrc-work", "disabled": true}`,
		`{"type": "netrc"}`,
		`{"type": "oauth2", "grantType": "client_credentials", "tokenUrl": "https://idp.test/token", "clientId": "app", "clientSecret": "{{cs}}", "clientAuth": "body", "scope": "a b", "audience": "api", "resource": "https://api.test", "state": "s", "tokenParams": [{"name": "x", "value": "1", "disabled": true}], "prefix": "Token", "key": "X-Token", "disabled": true}`,
		`{"type": "oauth2", "grantType": "authorization_code", "tokenUrl": "https://idp.test/token", "in": "query", "localSecrets": ["clientSecret"]}`,
	} {
		file := `{"kind": "http", "id": "req_1", "name": "R", "method": "GET", "url": "", "body": {"mode": "none"}, "auth": ` + in + `}`
		f, err := projectfile.Decode([]byte(file))
		if err != nil {
			t.Fatal(err)
		}
		want, err := projectfile.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(toRequestDTO(f))
		if err != nil {
			t.Fatal(err)
		}
		var dto RequestDTO
		if err := json.Unmarshal(data, &dto); err != nil {
			t.Fatal(err)
		}
		back, err := dto.toFile()
		if err != nil {
			t.Fatal(err)
		}
		got, err := projectfile.Marshal(back)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s changed:\n%s\nwant\n%s", in, got, want)
		}
	}
}
