package history

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/har"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// ExportHAR writes the chosen, a request's or every entry, oldest first,
// as a valid HAR file; masked entries stay masked.
func TestExportHAR(t *testing.T) {
	t.Parallel()
	ws := &workspace{dir: t.TempDir()}
	out := t.TempDir()
	var dialogs []SaveDialog
	path := filepath.Join(out, "picked.har")
	pick := func(_ context.Context, d SaveDialog) (string, error) {
		dialogs = append(dialogs, d)
		return path, nil
	}
	svc := New(nil, ws.localDir, nil, nil, WithHARExport("2.0.1", pick))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })

	if _, err := svc.ExportHAR(t.Context(), ExportHARInputDTO{}); !errors.Is(err, &errs.Error{Code: CodeExportEmpty}) {
		t.Errorf("empty export = %v", err)
	}
	rec := Recorder(svc)
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	for i := range 3 {
		e := entry(i)
		e.Time = base.Add(time.Duration(i) * time.Second)
		if i == 1 {
			e.RequestID = "req_2"
			e.Masked = true
			e.Request.Headers = []history.Header{{Name: "Authorization", Value: "Bearer " + httpclient.Mask}}
			e.Response.Timings = &history.Timings{DNS: 1, Connect: 2, TLS: 3, Send: 4, Wait: 5, Receive: 6, Total: 25}
		}
		rec()(e)
	}

	res, err := svc.ExportHAR(t.Context(), ExportHARInputDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || res.Path != path || res.Entries != 3 || !res.Masked {
		t.Errorf("result = %+v", res)
	}
	if len(dialogs) != 1 || dialogs[0].Filename != "PostSonde history.har" || !strings.Contains(dialogs[0].Message, "3 history entries") {
		t.Errorf("dialog = %+v", dialogs)
	}
	f := readHAR(t, path)
	if f.Log.Creator.Name != "PostSonde" || f.Log.Creator.Version != "2.0.1" || len(f.Log.Entries) != 3 || len(f.Log.Pages) != 1 {
		t.Fatalf("log = %+v", f.Log)
	}
	for i, e := range f.Log.Entries {
		if want := "https://example.test/" + string(rune('0'+i)); e.Request.URL != want || e.Pageref != "page_1" {
			t.Errorf("entry %d = %s %s", i, e.Request.URL, e.Pageref)
		}
	}
	if tm := f.Log.Entries[1].Timings; tm.Connect != 5 || tm.SSL != 3 || tm.Blocked != 4 || f.Log.Entries[1].Time != 25 {
		t.Errorf("timings = %+v", tm)
	}
	if h := f.Log.Entries[1].Request.Headers; len(h) != 1 || h[0].Value != "Bearer "+httpclient.Mask {
		t.Errorf("headers = %+v", h)
	}

	// One request's entries; chosen entries; a cancelled dialog.
	res, err = svc.ExportHAR(t.Context(), ExportHARInputDTO{RequestID: "req_2", Title: "Get / item"})
	if err != nil || res.Entries != 1 || res.Masked != true || dialogs[1].Filename != "Get - item.har" {
		t.Errorf("request export = %+v, %v, %+v", res, err, dialogs[1])
	}
	p, _ := svc.List(t.Context(), ListInputDTO{})
	res, err = svc.ExportHAR(t.Context(), ExportHARInputDTO{IDs: []string{p.Entries[0].ID, p.Entries[2].ID}})
	if err != nil || res.Entries != 2 || res.Masked {
		t.Errorf("chosen export = %+v, %v", res, err)
	}
	if _, err := svc.ExportHAR(t.Context(), ExportHARInputDTO{IDs: []string{"h_nope"}}); !errors.Is(err, &errs.Error{Code: history.CodeNotFound}) {
		t.Errorf("unknown ID = %v", err)
	}
	path = ""
	if res, err := svc.ExportHAR(t.Context(), ExportHARInputDTO{}); err != nil || res.Saved {
		t.Errorf("cancelled = %+v, %v", res, err)
	}
	path = filepath.Join(out, "missing", "x.har")
	if _, err := svc.ExportHAR(t.Context(), ExportHARInputDTO{}); !errors.Is(err, &errs.Error{Code: CodeExportFailed}) {
		t.Errorf("unwritable = %v", err)
	}

	noDialog := New(nil, ws.localDir, nil, nil)
	if _, err := noDialog.ExportHAR(t.Context(), ExportHARInputDTO{}); !errors.Is(err, &errs.Error{Code: "history.unavailable"}) {
		t.Errorf("without a dialog = %v", err)
	}
}

func readHAR(t *testing.T, path string) *har.File {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if problems := har.Validate(data); len(problems) > 0 {
		t.Fatalf("invalid HAR:\n%s", strings.Join(problems, "\n"))
	}
	var f har.File
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return &f
}
