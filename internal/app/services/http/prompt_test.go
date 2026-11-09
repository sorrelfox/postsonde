package http

import (
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// promptErrOf returns the prompts of a template.prompt error, failing the
// test for any other error.
func promptErrOf(t *testing.T, err error) []template.Prompt {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != template.CodePrompt || e.Kind != errs.InvalidArgument {
		t.Fatalf("err = %v, want %s", err, template.CodePrompt)
	}
	var out []template.Prompt
	if err := json.Unmarshal([]byte(e.Details["prompts"]), &out); err != nil {
		t.Fatalf("details.prompts = %q: %v", e.Details["prompts"], err)
	}
	return out
}

// treeContains reports the files under dir whose contents hold s.
func treeContains(t *testing.T, dir, s string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), s) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

// DYN-07: {{$prompt}} values are asked for before sending (one error with
// every label), reach the wire when answered, and are never stored: a
// secret one is masked in the timeline and the history, and no project,
// environment or local file holds either.
func TestSendPrompts(t *testing.T) {
	t.Parallel()
	srv := echoServer(t)
	ws, _, _, _ := envWorkspace(t, srv.URL)
	st, opt := historyStore(t)

	src := httpSource(`{{base}}/items?id={{$prompt "Item id" 1}}`)
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Env", Value: `{{$prompt "API token" secret}}`}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Text: `{"token":"{{$prompt "API token"}}","again":"{{ $prompt "Item id" }}"}`}
	src.Workspace = ws
	f := newFixture(t, src, nil, opt)

	// Unanswered: nothing is sent, and every label is listed once.
	_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	got := promptErrOf(t, err)
	want := []template.Prompt{{Label: "Item id", Default: "1"}, {Label: "API token", Secret: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("prompts = %+v, want %+v", got, want)
	}
	// A partial answer asks for the rest.
	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1", Prompts: map[string]string{"Item id": "77"}})
	if got := promptErrOf(t, err); !slices.Equal(got, want[1:]) {
		t.Fatalf("prompts = %+v, want %+v", got, want[1:])
	}

	const token, itemID = "pr0mpted-t0ken", "item-4242"
	echo := f.sendEcho(t, SendInputDTO{RequestID: "req_1", Prompts: map[string]string{"API token": token, "Item id": itemID}})
	if echo["uri"] != "/items?id="+itemID || echo["env"] != token ||
		echo["body"] != `{"token":"`+token+`","again":"`+itemID+`"}` {
		t.Fatalf("server received %v", echo)
	}

	sums := waitEntries(t, st, 1)
	if !sums[0].Masked {
		t.Errorf("summary not masked: %+v", sums[0])
	}
	e, err := st.Get(sums[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Request.Body.Text != `{"token":"`+httpclient.Mask+`","again":"`+itemID+`"}` {
		t.Errorf("history body = %q", e.Request.Body.Text)
	}
	if files := historyFiles(t, st); strings.Contains(files, token) {
		t.Errorf("the history files hold the secret prompt:\n%s", files)
	}
	for _, dir := range []string{ws.Root, ws.LocalDir} {
		for _, v := range []string{token, itemID} {
			if hits := treeContains(t, dir, v); len(hits) > 0 {
				t.Errorf("%q is stored in %v", v, hits)
			}
		}
	}
}

// The secret prompt is masked in the timeline, and prompts come before
// undefined variables: answering them may be all a send needs.
func TestSendPromptsTimelineAndOrder(t *testing.T) {
	t.Parallel()
	srv := echoServer(t)
	src := httpSource(srv.URL + "/x")
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Env", Value: `{{$prompt Pass secret}}`}, {Name: "X-Other", Value: "{{nope}}"}}
	f := newFixture(t, src, nil)

	_, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1"})
	promptErrOf(t, err)
	_, err = f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1", Prompts: map[string]string{"Pass": "s3cr3t-pass"}})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != template.CodeUnresolved || e.Details["names"] != "nope" {
		t.Fatalf("err = %v, want %s", err, template.CodeUnresolved)
	}

	echo := f.sendEcho(t, SendInputDTO{RequestID: "req_1", AllowUnresolved: true, Prompts: map[string]string{"Pass": "s3cr3t-pass"}})
	if echo["env"] != "s3cr3t-pass" {
		t.Fatalf("server received %v", echo)
	}
	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_1", AllowUnresolved: true, Prompts: map[string]string{"Pass": "s3cr3t-pass"}})
	if err != nil {
		t.Fatal(err)
	}
	if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	for i, hop := range f.rec.response(t, started.OpID).Timeline.Hops {
		if strings.Contains(hop.Request.Head, "s3cr3t-pass") || !strings.Contains(hop.Request.Head, "X-Env: "+httpclient.Mask) {
			t.Errorf("hop %d head:\n%s", i, hop.Request.Head)
		}
	}
}

// A run asks once: every send of it uses the answers it started with.
func TestRepeatPrompts(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Token"))
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	src := httpSource(srv.URL)
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Token", Value: `{{$prompt Token}}`}}
	f := newFixture(t, src, nil)

	_, err := f.svc.Repeat(t.Context(), RepeatInputDTO{RequestID: "req_1", Count: 3})
	if got := promptErrOf(t, err); !slices.Equal(got, []template.Prompt{{Label: "Token"}}) {
		t.Fatalf("prompts = %+v", got)
	}
	started, err := f.svc.Repeat(t.Context(), RepeatInputDTO{RequestID: "req_1", Count: 3, Prompts: map[string]string{"Token": "once"}})
	if err != nil {
		t.Fatal(err)
	}
	if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, []string{"once", "once", "once"}) {
		t.Errorf("server received %q", seen)
	}
}

// Previews send nothing and ask nothing: the prompt stays as written and
// is listed with the undefined names. Streams refuse it.
func TestPromptsInPreviewsAndStreams(t *testing.T) {
	t.Parallel()
	src := httpSource("http://example.test/")
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Id", Value: `{{$prompt Id}}`}}
	f := newFixture(t, src, nil)
	out, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Command, "{{$prompt Id}}") || !slices.Equal(out.Unresolved, []string{"$prompt Id"}) {
		t.Errorf("curl = %q, unresolved %q", out.Command, out.Unresolved)
	}

	f.src.SSE = true
	_, err = NewStream(t.Context(), f.svc, "req_1", true)
	promptErrOf(t, err)
}
