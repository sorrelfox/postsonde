package history

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// panelService is a service over a history in a temporary project, with
// "now-secret" as a secret PostSonde keeps now.
func panelService(t *testing.T) (*HistoryService, *events, func(history.Entry)) {
	t.Helper()
	ws := &workspace{}
	ws.set(t.TempDir())
	ev := &events{}
	svc := New(nil, ws.localDir, ev.emit, nil, WithSecrets(func(context.Context) ([]string, error) {
		return []string{"now-secret"}, nil
	}))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	rec := Recorder(svc)()
	if rec == nil {
		t.Fatal("no recorder")
	}
	return svc, ev, rec
}

func pageIDs(p PageDTO) []string {
	var out []string
	for _, e := range p.Entries {
		out = append(out, e.ID)
	}
	return out
}

// The history panel's filters reach the store, a date must be RFC 3339,
// and a value that is secret now is neither found nor shown.
func TestListPanelFilters(t *testing.T) {
	t.Parallel()
	svc, _, rec := panelService(t)
	a := entry(1)
	a.Time, a.Environment, a.URL = time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC), "staging", "https://example.test/?k=now-secret"
	a.Request.Headers = []history.Header{{Name: "X-Old", Value: "now-secret"}}
	rec(a)
	b := entry(2)
	b.Time, b.Kind, b.RequestID = time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC), history.KindGraphQL, "req_2"
	rec(b)
	all, err := svc.List(t.Context(), ListInputDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 2 || !slices.Equal(all.Environments, []string{"staging"}) {
		t.Fatalf("all = %+v", all)
	}
	older, newer := all.Entries[1], all.Entries[0]
	if older.Environment != "staging" || older.URL != "https://example.test/?k="+httpclient.Mask {
		t.Errorf("older = %+v", older)
	}
	tests := []struct {
		name string
		in   ListInputDTO
		want []string
	}{
		{"kind", ListInputDTO{Kind: "graphql"}, []string{newer.ID}},
		{"environment", ListInputDTO{Environment: "staging"}, []string{older.ID}},
		{"since", ListInputDTO{Since: "2026-09-21T00:00:00Z"}, []string{newer.ID}},
		{"until", ListInputDTO{Until: "2026-09-21T00:00:00+02:00"}, []string{older.ID}},
		{"request", ListInputDTO{RequestID: "req_2"}, []string{newer.ID}},
		{"secret not found", ListInputDTO{Text: "now-secret"}, nil},
		{"secret not found deep", ListInputDTO{Text: "now-secret", Deep: true}, nil},
		{"deep header", ListInputDTO{Text: "x-old", Deep: true}, []string{older.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := svc.List(t.Context(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := pageIDs(p); !slices.Equal(got, tt.want) {
				t.Errorf("List(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
	if _, err := svc.List(t.Context(), ListInputDTO{Since: "yesterday"}); !errors.Is(err, &errs.Error{Code: CodeInvalidFilter}) {
		t.Errorf("bad since = %v", err)
	}
}

// Clear for one request removes only its entries and says so; Clear
// removes every entry.
func TestClearRequestAndClear(t *testing.T) {
	t.Parallel()
	svc, ev, rec := panelService(t)
	for i := range 5 {
		e := entry(i)
		if i%2 == 1 {
			e.RequestID = "req_2"
		}
		rec(e)
	}
	n, err := svc.ClearRequest(t.Context(), "req_2")
	if err != nil || n != 2 {
		t.Fatalf("ClearRequest = %d, %v", n, err)
	}
	if last := ev.list()[len(ev.list())-1]; last.Action != ActionCleared || last.RequestID != "req_2" {
		t.Errorf("event = %+v", last)
	}
	p, _ := svc.List(t.Context(), ListInputDTO{})
	if p.Total != 3 {
		t.Errorf("left = %d", p.Total)
	}
	events := len(ev.list())
	if n, err := svc.ClearRequest(t.Context(), "req_2"); n != 0 || err != nil || len(ev.list()) != events {
		t.Errorf("ClearRequest again = %d, %v, events %d", n, err, len(ev.list()))
	}
	if _, err := svc.ClearRequest(t.Context(), ""); !errors.Is(err, &errs.Error{Code: CodeInvalidFilter}) {
		t.Errorf(`ClearRequest("") = %v`, err)
	}
	if err := svc.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.List(t.Context(), ListInputDTO{}); p.Total != 0 || p.All != 0 {
		t.Errorf("after Clear = %+v", p)
	}
	if last := ev.list()[len(ev.list())-1]; last.Action != ActionCleared || last.RequestID != "" {
		t.Errorf("Clear event = %+v", last)
	}
}

// NAV-03: Restore gives the editor's form in the editor's shapes; masked
// values, and values secret now, are never in it.
func TestRestore(t *testing.T) {
	t.Parallel()
	svc, _, rec := panelService(t)
	e := entry(1)
	e.RequestName = "Create"
	e.Template = &history.Template{
		Method: "POST", URL: "{{base}}/items",
		Headers: []history.Field{{Name: "X-Token", Value: httpclient.Mask}, {Name: "X-Ref", Value: "{{ref}}"}, {Name: "X-Off", Value: "now-secret", Disabled: true}},
		Body:    &history.TemplateBody{Mode: "multipart", Parts: []history.Field{{Name: "f", File: "a.txt"}, {Name: "pw", Value: httpclient.Mask}}},
	}
	rec(e)
	p, _ := svc.List(t.Context(), ListInputDTO{})
	got, err := svc.Restore(t.Context(), p.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), httpclient.Mask) || strings.Contains(string(data), "now-secret") {
		t.Errorf("restore holds a secret or the mask: %s", data)
	}
	if !got.Templated || got.Kind != "http" || got.RequestID != "req_1" || got.RequestName != "Create" || got.Method != "POST" || got.URL != "{{base}}/items" {
		t.Errorf("restore = %+v", got)
	}
	if h := got.Headers; len(h) != 3 || h[0].Value != "" || h[1].Value != "{{ref}}" || !h[1].Enabled || h[2].Enabled {
		t.Errorf("headers = %+v", h)
	}
	if b := got.Body; b.Mode != "multipart" || len(b.Parts) != 2 || b.Parts[0].File != "a.txt" || b.Parts[1].Value != "" || b.Fields == nil {
		t.Errorf("body = %+v", b)
	}
	want := []HiddenDTO{
		{Section: "headers", Name: "X-Token", Reason: "masked"},
		{Section: "headers", Name: "X-Off", Reason: "masked"},
		{Section: "bodyParts", Name: "pw", Reason: "masked"},
	}
	if !slices.Equal(got.Hidden, want) {
		t.Errorf("hidden = %+v", got.Hidden)
	}
	if _, err := svc.Restore(t.Context(), "h_missing"); !errors.Is(err, &errs.Error{Code: history.CodeNotFound}) {
		t.Errorf("Restore(missing) = %v", err)
	}

	// Without a template: as sent, and it says so.
	old := entry(2)
	old.Request.Headers = []history.Header{{Name: "Host", Value: "example.test"}, {Name: "Accept", Value: "*/*"}}
	rec(old)
	p, _ = svc.List(t.Context(), ListInputDTO{})
	sent, err := svc.Restore(t.Context(), p.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Templated || sent.URL != "https://example.test/2" || len(sent.Headers) != 1 || sent.Headers[0].Name != "Accept" || sent.Body.Mode != "none" || sent.Hidden == nil {
		t.Errorf("as sent = %+v", sent)
	}
}
