package http

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Encrypted secrets: while they are locked, a send whose scopes
// need a secret file (secret global variables, an active environment with
// secret variables) is refused with secretstore.locked and a hint, before
// anything is sent; one that needs none is sent. After the unlock the
// secrets are sent as before.
func TestSendRefusedWhileSecretsLocked(t *testing.T) {
	t.Parallel()
	srv := echoServer(t)
	ws, store, dev, _ := envWorkspace(t, srv.URL)
	if err := requestsecrets.New(ws.LocalDir).Write(ws.ID(), map[string]string{"var.globalKey": "gl0bal"}); err != nil {
		t.Fatal(err)
	}
	// The data directory holds the workspace's local directory.
	vault := secretstore.Open(filepath.Dir(filepath.Dir(ws.LocalDir)), secretstore.Options{Iterations: 1000})
	t.Cleanup(secretstore.Register(vault))
	if err := vault.Enable("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	vault.Lock()

	src := httpSource("{{base}}/{{path}}")
	src.Workspace = ws
	f := newFixture(t, src, nil)

	// No secret needed: sent.
	if got := f.sendEcho(t, SendInputDTO{RequestID: "req_1"}); got["uri"] != "/global" {
		t.Errorf("uri = %q", got["uri"])
	}

	refused := func(what string) {
		t.Helper()
		_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
		var e *errs.Error
		if !errors.As(err, &e) || !secretstore.IsLocked(err) || e.Hint == "" {
			t.Fatalf("%s: Send = %v", what, err)
		}
	}
	// A secret global variable.
	ws.File.Variables = append(ws.File.Variables, projectfile.Variable{Name: "globalKey", Secret: true})
	f.src.Request.URL = "{{base}}/{{globalKey}}"
	refused("secret global variable")
	// An environment with secret variables.
	f.src.Request.URL = "{{base}}/{{token}}"
	if err := store.SetActive(dev); err != nil {
		t.Fatal(err)
	}
	refused("secret environment variable")

	if err := vault.Unlock("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if got := f.sendEcho(t, SendInputDTO{RequestID: "req_1"}); got["uri"] != "/dev-secret" {
		t.Errorf("uri after unlock = %q", got["uri"])
	}
	f.src.Request.URL = "{{base}}/{{globalKey}}"
	if got := f.sendEcho(t, SendInputDTO{RequestID: "req_1"}); got["uri"] != "/gl0bal" {
		t.Errorf("uri after unlock = %q", got["uri"])
	}
}
