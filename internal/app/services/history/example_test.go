package history

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// TestExample makes response examples of recorded entries: the secrets
// the service knows now are masked though the entry kept them (it was
// recorded before the secret was one), and a send without a response has
// no example.
func TestExample(t *testing.T) {
	t.Parallel()
	ws := &workspace{}
	ws.set(t.TempDir())
	secrets := func(context.Context) ([]string, error) {
		return []string{"env-secret-42"}, errors.New("one side file could not be read")
	}
	svc := New(nil, ws.localDir, nil, nil, WithSecrets(secrets))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	rec := Recorder(svc)

	e := entry(1)
	e.ID = "h_00000000000000a1"
	e.Request.URL = "https://example.test/1?token=env-secret-42"
	e.Request.Headers = []history.Header{{Name: "Authorization", Value: "Bearer env-secret-42"}, {Name: "Content-Type", Value: "application/json"}}
	e.Request.Body = history.Body{Text: `{"key":"env-secret-42"}`, Size: 23}
	e.Response.Headers = []history.Header{{Name: "X-Echo", Value: "env-secret-42"}}
	e.Response.Body = history.Body{Text: "echo env-secret-42", Size: 18}
	rec()(e)
	failed := entry(2)
	failed.ID, failed.Outcome, failed.Response = "h_00000000000000a2", history.OutcomeError, nil
	rec()(failed)

	ex, err := svc.Example(t.Context(), ExampleInputDTO{ID: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ex)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "env-secret-42") {
		t.Errorf("the example holds the secret: %s", data)
	}
	if ex.Name != "200 OK" || ex.StatusCode != 200 || ex.StatusText != "OK" || ex.SavedAt == "" {
		t.Errorf("example = %+v", ex)
	}
	if ex.Body != "echo "+httpclient.Mask || ex.Request == nil || ex.Request.Body.Mode != "raw" || ex.Request.Body.Type != "json" {
		t.Errorf("body %q, request %+v", ex.Body, ex.Request)
	}
	if ex.Headers == nil || ex.Request.Headers == nil {
		t.Error("lists are null")
	}

	named, err := svc.Example(t.Context(), ExampleInputDTO{ID: e.ID, Name: "Happy path"})
	if err != nil || named.Name != "Happy path" {
		t.Errorf("named example = %q, %v", named.Name, err)
	}
	if _, err := svc.Example(t.Context(), ExampleInputDTO{ID: failed.ID}); !errors.Is(err, &errs.Error{Code: history.CodeNoResponse}) {
		t.Errorf("Example of a failed send = %v, want %s", err, history.CodeNoResponse)
	}
	if _, err := svc.Example(t.Context(), ExampleInputDTO{ID: "h_ffffffffffffffff"}); !errors.Is(err, &errs.Error{Code: history.CodeNotFound}) {
		t.Errorf("Example of an unknown entry = %v, want %s", err, history.CodeNotFound)
	}
	ws.set("")
	if _, err := svc.Example(t.Context(), ExampleInputDTO{ID: e.ID}); !errors.Is(err, &errs.Error{Code: "workspace.none_open"}) {
		t.Errorf("Example without a project = %v", err)
	}
}
