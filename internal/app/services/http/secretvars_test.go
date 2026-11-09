package http

import (
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Secret global and folder variables: their local values are sent,
// masked in the timeline and hidden from the chips; one without a value on
// this computer is undefined.
func TestSendSecretGlobalAndFolderVariables(t *testing.T) {
	t.Parallel()
	srv := echoServer(t)
	ws, _, _, _ := envWorkspace(t, srv.URL)
	ws.File.Variables = append(ws.File.Variables,
		projectfile.Variable{Name: "globalKey", Secret: true},
		projectfile.Variable{Name: "elsewhere", Secret: true},
	)
	if err := requestsecrets.New(ws.LocalDir).Write(ws.ID(), map[string]string{"var.globalKey": "gl0bal-s3cret"}); err != nil {
		t.Fatal(err)
	}

	src := httpSource("{{base}}/x?k={{globalKey}}")
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Env", Value: "{{folderKey}}"}}
	src.Workspace = ws
	// As collection.LoadHTTPSource gives them: the local value merged in.
	src.FolderVariables = [][]projectfile.Variable{{{Name: "folderKey", Value: "f0lder-s3cret", Secret: true}}}
	f := newFixture(t, src, nil)

	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, id)
	if !strings.Contains(resp.Body, `"uri":"/x?k=gl0bal-s3cret"`) || !strings.Contains(resp.Body, `"env":"f0lder-s3cret"`) {
		t.Fatalf("server received %s", resp.Body)
	}
	for i, hop := range resp.Timeline.Hops {
		if strings.Contains(hop.Request.Head, "s3cret") || strings.Contains(hop.Request.Target, "s3cret") {
			t.Errorf("hop %d shows a secret:\n%s", i, hop.Request.Head)
		}
		if !strings.Contains(hop.Request.Head, "X-Env: "+httpclient.Mask) {
			t.Errorf("hop %d head:\n%s", i, hop.Request.Head)
		}
	}

	vars, err := f.svc.Variables(t.Context(), "req_1")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]VariableDTO{}
	for _, v := range vars.Variables {
		seen[v.Name] = v
	}
	for _, name := range []string{"globalKey", "folderKey"} {
		if v := seen[name]; !v.Secret || v.Value != "" || !v.Resolved {
			t.Errorf("%s = %+v", name, v)
		}
	}
	if _, ok := seen["elsewhere"]; ok {
		t.Error("a secret variable without a local value is listed")
	}

	// One without a value here is undefined, not sent as "".
	f.src.Request.URL = "{{base}}/{{elsewhere}}"
	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != template.CodeUnresolved || e.Details["names"] != "elsewhere" {
		t.Errorf("send with a missing secret: %v", err)
	}
}
