package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/har"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// ExportHAR writes the requests as they would be sent, secrets masked
// unless asked for, and lists those that cannot be built.
func TestExportHAR(t *testing.T) {
	t.Parallel()
	src := httpSource("https://api.test/items?k={{apiKey}}")
	src.Request.Name = "List items"
	src.Request.Method = "POST"
	src.Request.Headers = []projectfile.KeyValue{{Name: "X-Trace", Value: "{{missing}}"}}
	src.Request.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{"key":"{{apiKey}}"}`}
	src.Auth = projectfile.Auth{Type: projectfile.AuthBearer, Token: "b3arer-tok"}
	src.FolderVariables = [][]projectfile.Variable{{{Name: "apiKey", Value: "s3cret-key", Secret: true}}}
	jar := cookies.New(cookies.Options{})
	if _, rejected := jar.SetCookieLines(&url.URL{Scheme: "https", Host: "api.test", Path: "/"}, []string{"sid=abc; Path=/"}); len(rejected) > 0 {
		t.Fatalf("rejected %v", rejected)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "out.har")
	var dialogs []SaveDialog
	pick := func(_ context.Context, d SaveDialog) (string, error) {
		dialogs = append(dialogs, d)
		return path, nil
	}
	f := newFixture(t, src, fixedJar{jar}, WithSaveBody("", pick), WithVersion("3.1.0"))

	res, err := f.svc.ExportHAR(t.Context(), ExportHARInputDTO{Name: "Items: v2", RequestIDs: []string{"req_1", "req_gone"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || res.Entries != 1 || !res.Masked || len(res.Skipped) != 1 || res.Skipped[0].RequestID != "req_gone" || res.Skipped[0].Code != "collection.not_found" {
		t.Errorf("result = %+v", res)
	}
	if len(dialogs) != 1 || dialogs[0].Filename != "Items- v2.har" || !strings.Contains(dialogs[0].Message, "secrets masked") {
		t.Errorf("dialog = %+v", dialogs)
	}
	data := readFile(t, path)
	for _, secret := range []string{"s3cret-key", "b3arer-tok"} {
		if strings.Contains(data, secret) {
			t.Errorf("the masked file holds %q", secret)
		}
	}
	file := decodeHAR(t, data)
	e := file.Log.Entries[0]
	if file.Log.Creator.Version != "3.1.0" || file.Log.Pages[0].Title != "Items: v2" || e.Comment != "List items" || e.Response.Status != 0 ||
		e.Request.URL != "https://api.test/items?k="+httpclient.Mask || e.Request.PostData == nil || e.Request.PostData.MimeType != "application/json" {
		t.Errorf("entry = %+v", e)
	}
	var headers []string
	for _, h := range e.Request.Headers {
		headers = append(headers, h.Name+": "+h.Value)
	}
	if got := strings.Join(headers, "\n"); !strings.Contains(got, "X-Trace: {{missing}}") || !strings.Contains(got, "Authorization: Bearer "+httpclient.Mask) || !strings.Contains(got, "Cookie: sid=abc") {
		t.Errorf("headers = %s", got)
	}

	// With secrets, on request.
	res, err = f.svc.ExportHAR(t.Context(), ExportHARInputDTO{Name: "Items", RequestIDs: []string{"req_1"}, IncludeSecrets: true})
	if err != nil || res.Masked || res.Entries != 1 || !strings.Contains(dialogs[1].Message, "with their secrets") {
		t.Fatalf("with secrets = %+v, %v", res, err)
	}
	data = readFile(t, path)
	for _, want := range []string{"k=s3cret-key", "Bearer b3arer-tok", `{\"key\":\"s3cret-key\"}`} {
		if !strings.Contains(data, want) {
			t.Errorf("the file lacks %s", want)
		}
	}

	if _, err := f.svc.ExportHAR(t.Context(), ExportHARInputDTO{RequestIDs: []string{"req_gone"}}); !errors.Is(err, &errs.Error{Code: CodeExportEmpty}) {
		t.Errorf("nothing to export = %v", err)
	}
	path = ""
	if res, err := f.svc.ExportHAR(t.Context(), ExportHARInputDTO{RequestIDs: []string{"req_1"}}); err != nil || res.Saved {
		t.Errorf("cancelled = %+v, %v", res, err)
	}
	path = filepath.Join(dir, "missing", "x.har")
	if _, err := f.svc.ExportHAR(t.Context(), ExportHARInputDTO{RequestIDs: []string{"req_1"}}); !errors.Is(err, &errs.Error{Code: CodeExportFailed}) {
		t.Errorf("unwritable = %v", err)
	}
	plain := newFixture(t, src, nil)
	if _, err := plain.svc.ExportHAR(t.Context(), ExportHARInputDTO{RequestIDs: []string{"req_1"}}); !errors.Is(err, &errs.Error{Code: CodeUnavailable}) {
		t.Errorf("without a dialog = %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if problems := har.Validate(data); len(problems) > 0 {
		t.Fatalf("invalid HAR:\n%s", strings.Join(problems, "\n"))
	}
	return string(data)
}

func decodeHAR(t *testing.T, data string) *har.File {
	t.Helper()
	var f har.File
	if err := json.Unmarshal([]byte(data), &f); err != nil {
		t.Fatal(err)
	}
	return &f
}
