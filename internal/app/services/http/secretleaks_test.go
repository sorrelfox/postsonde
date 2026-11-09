package http

import (
	"context"
	"encoding/json"
	"io"

	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// leakSecrets are the values of the secret variables of secretProject, one
// per level of the scope chain.
var leakSecrets = []string{"gl0bal-s3cret", "env-s3cret", "f0lder-s3cret"}

// secretProject is a project whose request uses a secret variable of every
// level (global, the active environment, a folder) in its URL, headers and
// body, and a variable with a local value (not a secret) at the global and
// environment levels.
func secretProject(t *testing.T, base string) collection.HTTPSource {
	t.Helper()
	ws, err := coreworkspace.New(t.TempDir()).Create(filepath.Join(t.TempDir(), "p"), "P")
	if err != nil {
		t.Fatal(err)
	}
	ws.File.Variables = []projectfile.Variable{
		{Name: "base", Value: base},
		{Name: "globalKey", Secret: true},
		{Name: "region", Value: "shared-region"},
	}
	if err := requestsecrets.New(ws.LocalDir).Write(ws.ID(), map[string]string{"var.globalKey": "gl0bal-s3cret"}); err != nil {
		t.Fatal(err)
	}
	if err := localvalues.New(ws.LocalDir).Write(localvalues.Global(ws.ID()), map[string]string{"region": "local-region"}); err != nil {
		t.Fatal(err)
	}
	store := environments.New(ws.Root, ws.LocalDir, false)
	env, err := store.Create("Dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveVariables(env.ID, []environments.Variable{
		{Name: "envKey", Value: "env-s3cret", Secret: true},
		{Name: "user", Value: "team-user", Local: true, LocalValue: "my-user"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActive(env.ID); err != nil {
		t.Fatal(err)
	}
	src := httpSource("{{base}}/x?g={{globalKey}}&e={{envKey}}&r={{region}}&u={{user}}")
	src.Request.Name = "Leaky"
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{
		{Name: "X-Folder", Value: "{{folderKey}}"},
		{Name: "Authorization", Value: "Bearer {{envKey}}"},
	}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{"g":"{{globalKey}}","f":"{{folderKey}}"}`}
	// As collection.LoadHTTPSource gives them: the local value merged in.
	src.FolderVariables = [][]projectfile.Variable{{{Name: "folderKey", Value: "f0lder-s3cret", Secret: true}}}
	src.Workspace = ws
	return src
}

// assertNoSecret fails when what (any value, as JSON, or a string) holds
// one of leakSecrets.
func assertNoSecret(t *testing.T, where string, what any) {
	t.Helper()
	text, ok := what.(string)
	if !ok {
		data, err := json.Marshal(what)
		if err != nil {
			t.Fatal(err)
		}
		text = string(data)
	}
	for _, s := range leakSecrets {
		if strings.Contains(text, s) {
			t.Errorf("%s shows the secret %q:\n%s", where, s, text)
		}
	}
}

// TestSecretsNeverUnmasked is the check of SEC-01: the values of
// secret variables, at every level of the chain, are never shown unmasked
// by what the app shows or writes about a request: the chips and their
// hover (Variables), the response and its timeline, the history, Copy as
// cURL, the HAR export, and the error of a failed send or stream (which
// the UI, the app log and the console get). The request itself does carry
// them: the server receives the real values.
func TestSecretsNeverUnmasked(t *testing.T) {
	t.Parallel()
	got := make(chan string, 4)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- r.URL.RawQuery + " " + r.Header.Get("X-Folder") + " " + r.Header.Get("Authorization") + " " + string(body)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	src := secretProject(t, srv.URL)
	st, historyOpt := historyStore(t)
	harPath := filepath.Join(t.TempDir(), "out.har")
	pick := func(context.Context, SaveDialog) (string, error) { return harPath, nil }
	f := newFixture(t, src, nil, historyOpt, WithSaveBody("", pick))
	ctx := t.Context()

	// The chips and their hover: secrets masked, local values shown and
	// marked local.
	vars, err := f.svc.Variables(ctx, "req_1")
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "Variables", vars)
	byName := map[string]VariableDTO{}
	for _, v := range vars.Variables {
		byName[v.Name] = v
	}
	for _, name := range []string{"globalKey", "envKey", "folderKey"} {
		if v := byName[name]; !v.Secret || v.Value != "" || !v.Resolved || v.Local {
			t.Errorf("%s = %+v", name, v)
		}
	}
	if v := byName["region"]; v.Value != "local-region" || !v.Local || v.Secret || v.Scope != "Global" {
		t.Errorf("region = %+v", v)
	}
	if v := byName["user"]; v.Value != "my-user" || !v.Local || v.Scope != "Environment: Dev" {
		t.Errorf("user = %+v", v)
	}
	if v := byName["base"]; v.Local {
		t.Errorf("base = %+v", v)
	}

	// A send: the server gets the real values (and the local ones), the
	// response, its timeline and the history show them masked.
	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	received := <-got
	for _, want := range append([]string{"r=local-region", "u=my-user"}, leakSecrets...) {
		if !strings.Contains(received, want) {
			t.Errorf("the server received %q, without %q", received, want)
		}
	}
	resp := f.rec.response(t, id)
	assertNoSecret(t, "the response", resp)
	if !resp.Timeline.Masked {
		t.Error("the timeline does not say it masked something")
	}
	waitEntries(t, st, 1)
	assertNoSecret(t, "the history files", historyFiles(t, st))

	// Copy as cURL and the HAR export, masked by default.
	curl, err := f.svc.CurlCommand(ctx, CurlInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "Copy as cURL", curl)
	if !curl.Masked || !strings.Contains(curl.Command, "u=my-user") {
		t.Errorf("curl = %+v", curl)
	}
	if _, err := f.svc.ExportHAR(ctx, ExportHARInputDTO{Name: "Leaky", RequestIDs: []string{"req_1"}}); err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "the HAR file", readFile(t, harPath))

	// A failed send: the transport error repeats the URL; what the UI,
	// the log (err.Error()) and the history get is masked.
	closer := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		if c, _, err := w.(nethttp.Hijacker).Hijack(); err == nil {
			_ = c.Close()
		}
	}))
	t.Cleanup(closer.Close)
	f.src.Workspace.File.Variables[0].Value = closer.URL
	id = f.send(t)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusError || done.Error == nil {
		t.Fatalf("done = %+v", done)
	}
	assertNoSecret(t, "the error of a failed send", done)
	if !strings.Contains(strings.Join(done.Error.Causes, "\n"), httpclient.Mask) {
		t.Errorf("causes = %q, want the URL with the secrets masked", done.Error.Causes)
	}
	waitEntries(t, st, 2)
	assertNoSecret(t, "the history files after a failure", historyFiles(t, st))

	// The same for an event stream.
	f.src.SSE = true
	stream, err := NewStream(ctx, f.svc, "req_1", false)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "the stream's URL", stream.URL)
	_, err = stream.Open(netx.WithUserOp(ctx, netx.UserOp{Kind: "test", ID: "1"}), "", func(*httpclient.Response, io.Reader) error { return nil })
	if err == nil {
		t.Fatal("a stream to a server that closes the connection succeeded")
	}
	assertNoSecret(t, "the error of a stream", err.Error())
}

// A secret used as the host (a variable can be anything) is masked in the
// error that names the host: its message, details and causes.
func TestSecretHostMaskedInErrors(t *testing.T) {
	t.Parallel()
	src := httpSource("http://{{host}}:1/x")
	src.FolderVariables = [][]projectfile.Variable{{{Name: "host", Value: "127.0.0.1", Secret: true}}}
	f := newFixture(t, src, nil)
	id := f.send(t)
	done := f.rec.wait(t, id)
	if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != "netx.connection_refused" {
		t.Fatalf("done = %+v", done)
	}
	data, _ := json.Marshal(done)
	if strings.Contains(string(data), "127.0.0.1") {
		t.Errorf("the error shows the secret host:\n%s", data)
	}
	if !strings.Contains(done.Error.Message, httpclient.Mask+":1") {
		t.Errorf("message = %q", done.Error.Message)
	}
}
