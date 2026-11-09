package app

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/logexport"
	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	coreenvironments "github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// TestKnownSecrets checks that a copied or saved log (DBG-07) is masked
// with the secrets the stores keep: a request's auth, a secret environment
// variable, secret runtime variables (session and persisted) and a
// certificate passphrase.
func TestKnownSecrets(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	workspaces := coreworkspace.New(data)
	app := appsecrets.New(data)
	runtimeVars := runtimevars.NewRegistry(workspaces.Current, nil)
	if err := app.Set(appsecrets.ClientCertPassphraseKey("cc_1"), "cert-passphrase"); err != nil {
		t.Fatal(err)
	}

	// No project open: the app's secrets only.
	got, err := knownSecrets(workspaces, app, runtimeVars)(context.Background())
	if err != nil || !slices.Equal(got, []string{"cert-passphrase"}) {
		t.Fatalf("without a project = %q, %v", got, err)
	}

	ws, err := workspaces.Create(filepath.Join(t.TempDir(), "proj"), "Proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := requestsecrets.New(ws.LocalDir).Write("req_1", map[string]string{"auth.token": "bearer-token-1"}); err != nil {
		t.Fatal(err)
	}
	envs := coreenvironments.New(ws.Root, ws.LocalDir, false)
	env, err := envs.Create("Dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := envs.SaveVariables(env.ID, []coreenvironments.Variable{
		{Name: "host", Value: "api.example.com", Local: true, LocalValue: "localhost:8080"},
		{Name: "apiKey", Value: "env-api-key", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	// Secret global and folder variables; a global local value,
	// which is not a secret.
	if err := requestsecrets.New(ws.LocalDir).Write(ws.ID(), map[string]string{"var.globalKey": "global-var-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := requestsecrets.New(ws.LocalDir).Write("fld_1", map[string]string{"var.folderKey": "folder-var-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := localvalues.New(ws.LocalDir).Write(localvalues.Global(ws.ID()), map[string]string{"region": "local-region"}); err != nil {
		t.Fatal(err)
	}

	// Runtime variables: a secret of the session, a persisted
	// secret, and a plain value, which is not a secret.
	rt, err := runtimeVars.For(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []runtimevars.Variable{
		{Name: "sessionToken", Value: "runtime-session-secret", Secret: true},
		{Name: "keptToken", Value: "runtime-kept-secret", Secret: true, Persist: true},
		{Name: "userId", Value: "runtime-plain", Persist: true},
	} {
		if err := rt.Set(v); err != nil {
			t.Fatal(err)
		}
	}

	got, err = knownSecrets(workspaces, app, runtimeVars)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	got = slices.Compact(got)
	if want := []string{"bearer-token-1", "cert-passphrase", "env-api-key", "folder-var-secret", "global-var-secret", "runtime-kept-secret", "runtime-session-secret"}; !slices.Equal(got, want) {
		t.Errorf("knownSecrets = %q, want %q", got, want)
	}

	// Through the service: the secrets are masked, in text and JSON; local
	// values are not secrets and stay as they are.
	svc := logexport.New(knownSecrets(workspaces, app, runtimeVars), nil, nil)
	res, err := svc.Mask(context.Background(), logexport.MaskInputDTO{
		Text:   "GET https://localhost:8080/?key=env-api-key&g=global-var-secret&r=local-region token=bearer-token-1 folder-var-secret",
		Format: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "GET https://localhost:8080/?key=••••••&g=••••••&r=local-region token=•••••• ••••••"; res.Text != want {
		t.Errorf("masked = %q, want %q", res.Text, want)
	}
	res, err = svc.Mask(context.Background(), logexport.MaskInputDTO{Text: `{"url":"https://h/?k=global-var-secret","h":"folder-var-secret"}`, Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"url":"https://h/?k=••••••","h":"••••••"}`; res.Text != want {
		t.Errorf("masked JSON = %q, want %q", res.Text, want)
	}
}
