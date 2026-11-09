package collection

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/fakeclock"
)

// secretsImporter is a test format whose collection has a typed auth
// secret and a secret variable on the folder, and typed request auth.
type secretsImporter struct{}

func (secretsImporter) Format() importer.Format {
	return importer.Format{ID: "secrets", Name: "Secrets"}
}

func (secretsImporter) Detect(src importer.Source) int {
	if strings.HasPrefix(string(src.Data), "SECRETS") {
		return importer.Sure
	}
	return importer.No
}

func (secretsImporter) Parse(context.Context, importer.Source) (*importer.Model, error) {
	return &importer.Model{Name: "Imported", Root: importer.Folder{
		Name:      "Imported",
		Variables: []projectfile.Variable{{Name: "importedKey", Value: "import-folder-var", Secret: true}},
		Auth:      projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: "import-folder-password"},
		Items: []importer.Item{
			{Request: &importer.Request{Name: "Get", File: &projectfile.HTTPRequest{Method: "GET", URL: "https://h/",
				Auth: projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "k", Value: "import-request-apikey"}}}},
			{Folder: &importer.Folder{Name: "Inner",
				Variables: []projectfile.Variable{{Name: "innerKey", Value: "import-inner-var", Secret: true}},
				Items: []importer.Item{{Request: &importer.Request{Name: "Q", File: &projectfile.GraphQLRequest{URL: "https://h/gql",
					Auth: projectfile.Auth{Type: projectfile.AuthDigest, Username: "u", Password: "import-gql-password"}}}}}}},
		},
	}}, nil
}

// assertProjectFree fails for every file under the project folder dir that
// holds one of secrets, and returns how many files it scanned.
func assertProjectFree(t *testing.T, dir string, secrets []string) int {
	t.Helper()
	scanned := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, s := range secrets {
			if strings.Contains(string(data), s) {
				rel, _ := filepath.Rel(dir, path)
				t.Errorf("%s holds the secret %q:\n%s", rel, s, data)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return scanned
}

// TestProjectFilesNeverHoldSecrets is the check of SEC-04 and PRV-09:
// credentials entered through every save path, and plaintext secrets typed
// into project files by hand, end up in the local side files only. After
// every path has run, the project folder is scanned byte for byte and no
// secret is found in it; every secret is still usable.
func TestProjectFilesNeverHoldSecrets(t *testing.T) {
	t.Parallel()
	manager, dir := newProject(t)
	clock := fakeclock.New(clockStart)
	svc := New(manager, nil, nil, withClock(clock),
		WithImporters(importer.NewRegistry(curlimport.Importer{}, secretsImporter{})))
	ctx := t.Context()
	ws := manager.Current()

	// Plaintext secrets typed into the project by hand.
	writeFile(t, dir, "postsonde.json", `{"schemaVersion": 1, "kind": "workspace", "id": "ws_0123456789abcdef", "name": "Shop", "order": ["users"],
  "variables": [{"name": "globalKey", "value": "hand-global-var", "secret": true}]}`)
	writeFile(t, dir, "legacy.json", `{"kind": "http", "id": "req_legacy", "name": "Legacy", "method": "GET", "url": "/", "auth": {"type": "bearer", "token": "hand-bearer-token"}}`)
	writeFile(t, dir, "users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users",
  "variables": [{"name": "vault", "value": "hand-folder-var", "secret": true}, {"name": "plain", "value": "not-a-secret"}],
  "auth": {"type": "apikey", "key": "X-Api-Key", "value": "hand-folder-apikey"}}`)
	writeFile(t, dir, "environments/dev.json", `{"schemaVersion": 1, "kind": "environment", "id": "env_dev", "name": "Dev",
  "variables": [{"name": "envKey", "value": "hand-env-var", "secret": true}]}`)
	// Broken and unknown files would keep the folder from being duplicated.
	for _, name := range []string{"broken.json", "stream.json"} {
		if err := os.Remove(filepath.Join(dir, "users", name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	hand := []string{"hand-global-var", "hand-bearer-token", "hand-folder-var", "hand-folder-apikey", "hand-env-var"}

	// Loading uses them as they are and writes nothing.
	if got := getRequest(t, svc, "req_legacy"); got.Auth.Token != "hand-bearer-token" {
		t.Errorf("legacy auth = %+v", got.Auth)
	}
	if f := getFolder(t, svc, "fld_users"); f.Auth.Value != "hand-folder-apikey" || f.Variables[0].Value != "hand-folder-var" {
		t.Errorf("folder = %+v", f)
	}
	for _, rel := range []string{"postsonde.json", "legacy.json", "users/_folder.json"} {
		if data := readFile(t, dir, rel); !strings.Contains(data, "hand-") {
			t.Errorf("loading changed %s:\n%s", rel, data)
		}
	}

	// Credentials entered in the editors: every auth type of an HTTP
	// request, a GraphQL request, a folder's auth and secret variable.
	typed := map[string]AuthDTO{
		"Basic":   {Type: "basic", Username: "ada", Password: "typed-basic-password"},
		"Digest":  {Type: "digest", Username: "ada", Password: "typed-digest-password"},
		"Bearer":  {Type: "bearer", Token: "typed-bearer-token"},
		"API key": {Type: "apikey", Key: "X-Key", Value: "typed-api-key", In: "query"},
		"JWT":     {Type: "jwt", Secret: "typed-jwt-secret", JWT: &JWTAuthDTO{Payload: `{"sub":"ada"}`}},
		"OAuth 2.0": {Type: "oauth2", ClientSecret: "typed-oauth2-client-secret",
			OAuth2: &OAuth2AuthDTO{TokenURL: "https://idp.test/token", ClientID: "app", Scope: "read"}},
	}
	secrets := append([]string(nil), hand...)
	created := map[string]string{}
	for name, auth := range typed {
		res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{ParentID: "fld_users", Name: name, Kind: "http", Index: -1})
		if err != nil {
			t.Fatal(err)
		}
		req := getRequest(t, svc, res.NodeID)
		req.Auth = auth
		update(t, svc, req, 1)
		created[name] = res.NodeID
		secrets = append(secrets, auth.Password+auth.Token+auth.Value+auth.Secret+auth.ClientSecret)
	}
	gql := getRequest(t, svc, "req_me")
	gql.Auth = AuthDTO{Type: "bearer", Token: "typed-graphql-token"}
	update(t, svc, gql, 1)
	secrets = append(secrets, "typed-graphql-token")
	sse, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Events", Kind: "sse", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	sseReq := getRequest(t, svc, sse.NodeID)
	sseReq.Auth = AuthDTO{Type: "bearer", Token: "typed-sse-token"}
	update(t, svc, sseReq, 1)
	secrets = append(secrets, "typed-sse-token")
	// The handshake auth of the other protocols (AUTH-31).
	for _, kind := range []string{"websocket", "socketio", "grpc"} {
		res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Live " + kind, Kind: kind, Index: -1})
		if err != nil {
			t.Fatal(err)
		}
		req := getRequest(t, svc, res.NodeID)
		req.Auth = AuthDTO{Type: "bearer", Token: "typed-" + kind + "-token"}
		update(t, svc, req, 1)
		secrets = append(secrets, "typed-"+kind+"-token")
		if got := getRequest(t, svc, res.NodeID); got.Auth.Token != "typed-"+kind+"-token" {
			t.Errorf("%s auth = %+v", kind, got.Auth)
		}
	}

	// The password of a request's own proxy, of every kind: typed in the
	// password field, and typed into the URL.
	proxyIDs := map[string]string{}
	for _, kind := range []string{"http", "graphql", "sse", "tcp", "udp", "websocket", "socketio", "grpc"} {
		res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Proxied " + kind, Kind: kind, Index: -1})
		if err != nil {
			t.Fatal(err)
		}
		req := getRequest(t, svc, res.NodeID)
		req.Settings.Proxy = &RequestProxyDTO{Mode: "custom", URL: "socks5h://bob@proxy.lan:1080", Password: "typed-proxy-" + kind}
		update(t, svc, req, 1)
		req = getRequest(t, svc, res.NodeID)
		if req.Settings.Proxy == nil || req.Settings.Proxy.Password != "typed-proxy-"+kind {
			t.Errorf("%s proxy = %+v", kind, req.Settings.Proxy)
		}
		req.Settings.Proxy = &RequestProxyDTO{Mode: "custom", URL: "socks5h://bob:url-proxy-" + kind + "@proxy.lan:1080"}
		update(t, svc, req, 2)
		proxyIDs[kind] = res.NodeID
		secrets = append(secrets, "typed-proxy-"+kind, "url-proxy-"+kind)
	}
	writeFile(t, dir, "hand-proxy.json", `{"kind": "tcp", "id": "req_handproxy", "name": "Hand proxy", "address": "x:1",
  "settings": {"proxy": {"mode": "custom", "url": "socks5://bob:hand-proxy-url@proxy.lan", "password": "hand-proxy-password"}}}`)
	if _, err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rename(ctx, RenameInputDTO{ID: "req_handproxy", Name: "Hand proxy 2"}); err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, "hand-proxy-url", "hand-proxy-password")

	folderRes, err := svc.CreateFolder(ctx, CreateFolderInputDTO{Name: "Admin", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	admin := getFolder(t, svc, folderRes.NodeID)
	admin.Auth = AuthDTO{Type: "basic", Username: "root", Password: "typed-folder-password"}
	admin.Variables = []KeyValueDTO{{Name: "adminKey", Value: "typed-folder-var", Enabled: true, Extra: `{"secret":true}`}}
	if _, err := svc.UpdateFolder(ctx, UpdateFolderInputDTO{Folder: admin, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, "typed-folder-password", "typed-folder-var")

	// cURL import.
	for _, cmd := range []string{
		"curl -u 'ada:curl-basic-password' https://h/a",
		"curl --digest -u 'ada:curl-digest-password' https://h/b",
		"curl --oauth2-bearer curl-bearer-token https://h/c",
	} {
		if _, err := svc.ImportCurl(ctx, ImportCurlInputDTO{Text: cmd, Index: -1}); err != nil {
			t.Fatal(err)
		}
	}
	secrets = append(secrets, "curl-basic-password", "curl-digest-password", "curl-bearer-token")

	// File imports: a cURL script, and a collection with folder settings.
	script := filepath.Join(t.TempDir(), "calls.sh")
	if err := os.WriteFile(script, []byte("curl -u 'ada:file-basic-password' https://h/d\ncurl --digest -u 'ada:file-digest-password' https://h/e\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Import(ctx, ImportInputDTO{Path: script, Format: "curl", Index: -1}); err != nil {
		t.Fatal(err)
	}
	coll := filepath.Join(t.TempDir(), "coll.txt")
	if err := os.WriteFile(coll, []byte("SECRETS"), 0o600); err != nil {
		t.Fatal(err)
	}
	imported, err := svc.Import(ctx, ImportInputDTO{Path: coll, Format: "secrets", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	intoUsers, err := svc.Import(ctx, ImportInputDTO{Path: coll, Format: "secrets", ParentID: "fld_users", IntoParent: true, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range intoUsers.Report.Items {
		if item.Status == "failed" {
			t.Errorf("import into users: %+v", item)
		}
	}
	secrets = append(secrets, "file-basic-password", "file-digest-password",
		"import-folder-var", "import-folder-password", "import-request-apikey", "import-inner-var", "import-gql-password")

	// An environment's secret variable, and a rename of the environment
	// holding a hand-typed one.
	envs := environments.New(ws.Root, ws.LocalDir, false)
	prod, err := envs.Create("Prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := envs.SaveVariables(prod.ID, []environments.Variable{
		{Name: "envKey", Value: "typed-env-var", Secret: true},
		{Name: "host", Value: "shared-host", Local: true, LocalValue: "local-env-host"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := envs.Rename("env_dev", "Development"); err != nil {
		t.Fatal(err)
	}
	if _, err := envs.Rename(prod.ID, "Production"); err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, "typed-env-var")

	// Local values (SEC-02) of an environment variable (above) and
	// of a global one: the tree operations below rewrite postsonde.json.
	if _, err := SaveGlobalVariables(svc, ctx, []projectfile.Variable{
		{Name: "globalKey", Value: "hand-global-var", Secret: true},
		{Name: "region", Value: "shared-region"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := localvalues.New(ws.LocalDir).Write(localvalues.Global(ws.ID()), map[string]string{"region": "local-global-region"}); err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, "local-env-host", "local-global-region")

	// Tree operations rewrite files that hold hand-typed secrets: a rename
	// (legacy.json), reorders (postsonde.json, users/_folder.json), a move,
	// a duplicate, a delete and a restore.
	if _, err := svc.Rename(ctx, RenameInputDTO{ID: "req_legacy", Name: "Legacy API"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(ctx, MoveInputDTO{ID: created["Bearer"], ParentID: "", Index: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(ctx, MoveInputDTO{ID: "req_legacy", ParentID: "fld_users", Index: 0}); err != nil {
		t.Fatal(err)
	}
	dup, err := svc.Duplicate(ctx, "fld_users")
	if err != nil {
		t.Fatal(err)
	}
	del, err := svc.Delete(ctx, created["Digest"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Restore(ctx, del.Entry.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}
	clock.Advance(0)

	if n := assertProjectFree(t, dir, secrets); n < 15 {
		t.Errorf("scanned only %d files", n)
	}
	// Something non-secret did reach the project, so the scan is real.
	if data := readFile(t, dir, "users/_folder.json"); !strings.Contains(data, "not-a-secret") {
		t.Errorf("users/_folder.json =\n%s", data)
	}
	// Shared values stay where local values replace them on this computer.
	if data := readFile(t, dir, "postsonde.json"); !strings.Contains(data, "shared-region") {
		t.Errorf("postsonde.json =\n%s", data)
	}
	if data := readFile(t, dir, "environments/production.json"); !strings.Contains(data, "shared-host") {
		t.Errorf("environments/production.json =\n%s", data)
	}
	if prodEnv, err := envs.Get(prod.ID); err != nil || prodEnv.Variables[1].Effective() != "local-env-host" {
		t.Errorf("prod environment = %+v, %v", prodEnv, err)
	}

	// Every secret is still where the app finds it.
	if got := getRequest(t, svc, "req_legacy"); got.Auth.Token != "hand-bearer-token" {
		t.Errorf("legacy auth after the migration = %+v", got.Auth)
	}
	users := getFolder(t, svc, "fld_users")
	if users.Auth.Value != "hand-folder-apikey" || users.Variables[0].Value != "hand-folder-var" {
		t.Errorf("users after the migration = %+v", users)
	}
	if copyOf := getFolder(t, svc, dup.NodeID); copyOf.Auth.Value != "hand-folder-apikey" || copyOf.Variables[0].Value != "hand-folder-var" {
		t.Errorf("duplicate = %+v", copyOf)
	}
	for kind, id := range proxyIDs {
		got := getRequest(t, svc, id)
		if p := got.Settings.Proxy; p == nil || p.Password != "url-proxy-"+kind || p.URL != "socks5h://bob@proxy.lan:1080" {
			t.Errorf("%s proxy after the migration = %+v", kind, p)
		}
	}
	if p := getRequest(t, svc, "req_handproxy").Settings.Proxy; p == nil || p.Password != "hand-proxy-url" || p.URL != "socks5://bob@proxy.lan" {
		t.Errorf("hand-typed proxy after the migration = %+v", p)
	}
	for name, auth := range typed {
		got := getRequest(t, svc, created[name])
		if got.Auth.Password+got.Auth.Token+got.Auth.Value != auth.Password+auth.Token+auth.Value {
			t.Errorf("%s auth = %+v", name, got.Auth)
		}
	}
	if got := getFolder(t, svc, folderRes.NodeID); got.Auth.Password != "typed-folder-password" || got.Variables[0].Value != "typed-folder-var" {
		t.Errorf("admin folder = %+v", got)
	}
	global, err := requestsecrets.New(ws.LocalDir).Read(ws.ID())
	if err != nil || global["var.globalKey"] != "hand-global-var" {
		t.Errorf("global side file = %v, %v", global, err)
	}
	dev, err := envs.Get("env_dev")
	if err != nil || dev.Variables[0].Value != "hand-env-var" {
		t.Errorf("dev environment = %+v, %v", dev, err)
	}
	if got := getRequest(t, svc, sse.NodeID); got.Auth.Token != "typed-sse-token" {
		t.Errorf("sse auth = %+v", got.Auth)
	}
	if got := getFolder(t, svc, imported.NodeID); got.Auth.Password != "import-folder-password" || got.Variables[0].Value != "import-folder-var" {
		t.Errorf("imported folder = %+v", got)
	}
	src, err := LoadVariablesSource(svc, ctx, created["Basic"])
	if err != nil || len(src.FolderVariables) != 1 || src.FolderVariables[0][0].Value != "hand-folder-var" {
		t.Errorf("variables source = %+v, %v", src, err)
	}
}
