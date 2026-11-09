package history

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// NAV-02: every filter of the history panel, alone and together.
func TestListPanelFilters(t *testing.T) {
	t.Parallel()
	s := open(t, t.TempDir(), Limits{})
	day := func(d, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, time.UTC) }
	add := func(mut func(*Entry)) string {
		e := entry(1)
		mut(&e)
		sum, err := s.Append(e)
		if err != nil {
			t.Fatal(err)
		}
		return sum.ID
	}
	old := add(func(e *Entry) { e.Time, e.Environment = day(20, 9), "staging" })
	gql := add(func(e *Entry) {
		e.Time, e.Kind, e.Method, e.RequestID, e.Environment = day(21, 9), KindGraphQL, "POST", "req_gql", "prod"
	})
	replay := add(func(e *Entry) { e.Time, e.Kind, e.RequestID, e.RequestName = day(22, 9), KindReplay, "", "" })
	server := add(func(e *Entry) {
		e.Time, e.StatusCode, e.Status, e.Environment = day(22, 18), 503, "503 Service Unavailable", "staging"
	})

	tests := []struct {
		name string
		q    Query
		want []string
	}{
		{"kind http", Query{Kind: KindHTTP}, []string{server, old}},
		{"kind graphql", Query{Kind: KindGraphQL}, []string{gql}},
		{"kind replay", Query{Kind: KindReplay}, []string{replay}},
		{"environment", Query{Environment: "staging"}, []string{server, old}},
		{"environment none matches", Query{Environment: "dev"}, nil},
		{"since", Query{Since: day(22, 0)}, []string{server, replay}},
		{"until excluded", Query{Until: day(21, 9)}, []string{old}},
		{"date range", Query{Since: day(21, 0), Until: day(22, 12)}, []string{replay, gql}},
		{"5xx", Query{Status: "5xx"}, []string{server}},
		{"this request only", Query{RequestID: "req_gql"}, []string{gql}},
		{"text finds environment", Query{Text: "PROD"}, []string{gql}},
		{"filters together", Query{Kind: KindHTTP, Environment: "staging", Status: "2xx"}, []string{old}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ids(s.List(tt.q)); !slices.Equal(got, tt.want) {
				t.Errorf("List(%+v) = %v, want %v", tt.q, got, tt.want)
			}
		})
	}
	if p := s.List(Query{}); !slices.Equal(p.Environments, []string{"prod", "staging"}) {
		t.Errorf("Environments = %v", p.Environments)
	}
}

// Paging walks the matching entries newest first, without gaps or
// repeats, and counts them all.
func TestListPaging(t *testing.T) {
	t.Parallel()
	s := open(t, t.TempDir(), Limits{})
	all := appendN(t, s, 0, 120)
	var seen []string
	for off := 0; ; off += 50 {
		p := s.List(Query{RequestID: "req_0", Offset: off, Limit: 50})
		if p.Total != 40 || p.All != 120 {
			t.Fatalf("page at %d: total %d, all %d", off, p.Total, p.All)
		}
		seen = append(seen, ids(p)...)
		if len(p.Entries) < 50 {
			break
		}
	}
	var want []string
	for i := 119; i >= 0; i-- {
		if i%3 == 0 {
			want = append(want, all[i])
		}
	}
	if !slices.Equal(seen, want) {
		t.Errorf("pages = %v, want %v", seen, want)
	}
	if p := s.List(Query{}); len(p.Entries) != DefaultPageSize {
		t.Errorf("default page = %d", len(p.Entries))
	}
	if p := s.List(Query{Offset: 500}); len(p.Entries) != 0 || p.Total != 120 {
		t.Errorf("past the end = %+v", p)
	}
}

// Deep search looks in headers, bodies and the template, read from disk;
// the summary search does not.
func TestListDeepSearch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := open(t, dir, Limits{SegmentBytes: 2048})
	appendN(t, s, 0, 20)
	e := entry(99)
	e.Request.Headers = append(e.Request.Headers, Header{Name: "X-Trace", Value: "trace-needle"})
	e.Response.Body = Body{Text: `{"answer":"body-needle"}`, Size: 24}
	e.Template = &Template{URL: "{{base}}/items/{{tmpl-needle}}", Headers: []Field{{Name: "X-Env", Value: "{{env}}"}}}
	target, err := s.Append(e)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, s, 20, 40)
	for _, text := range []string{"trace-needle", "x-trace: trace", "BODY-NEEDLE", "tmpl-needle", "x-env: {{env}}"} {
		if p := s.List(Query{Text: text}); p.Total != 0 {
			t.Errorf("summary search found %q: %v", text, ids(p))
		}
		if p := s.List(Query{Text: text, Deep: true}); p.Total != 1 || p.Entries[0].ID != target.ID {
			t.Errorf("deep search %q = %v", text, ids(p))
		}
	}
	// It reopens from disk the same way.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	re := open(t, dir, Limits{SegmentBytes: 2048})
	if p := re.List(Query{Text: "body-needle", Deep: true}); p.Total != 1 {
		t.Errorf("deep search after reopening = %v", ids(p))
	}
}

// No secret is found by search: entries are masked when recorded, and
// Mask hides a value that became secret since, in the summary and deep.
func TestListNeverFindsSecrets(t *testing.T) {
	t.Parallel()
	s := open(t, t.TempDir(), Limits{})
	const secret, later = "s3cr3t-token-value", "became-secret-later"
	m := httpclient.NewSecretMasker([]string{secret})
	e := entry(1)
	e.URL = "https://example.test/?key=" + secret + "&k2=" + later
	e.Request.Headers = []Header{{Name: "X-Api-Key", Value: secret}, {Name: "X-Old", Value: later}}
	e.Request.Body = Body{Text: `{"password":"` + secret + `","old":"` + later + `"}`, Size: 60}
	e.Template = &Template{URL: "{{base}}/?key=" + secret, Headers: []Field{{Name: "X-Api-Key", Value: secret}}}
	// What the recorder does before appending.
	e.URL = m.MaskText(e.URL)
	e.Request.Headers, _ = Headers([]httpclient.Header{{Name: "X-Api-Key", Value: secret}, {Name: "X-Old", Value: later}}, m, false)
	e.Request.Body, _ = Snapshot([]byte(e.Request.Body.Text), e.Request.Body.Size, m)
	MaskTemplate(e.Template, m)
	if _, err := s.Append(e); err != nil {
		t.Fatal(err)
	}
	for _, deep := range []bool{false, true} {
		for _, text := range []string{secret, "s3cr3t", "token-value"} {
			if p := s.List(Query{Text: text, Deep: deep}); p.Total != 0 {
				t.Errorf("search (deep %v) found %q", deep, text)
			}
		}
		// Found before it is known as a secret, not after.
		if p := s.List(Query{Text: later, Deep: deep}); p.Total != 1 {
			t.Errorf("search (deep %v) for a plain value = %d", deep, p.Total)
		}
		now := httpclient.NewSecretMasker([]string{secret, later})
		if p := s.List(Query{Text: later, Deep: deep, Mask: now.MaskText}); p.Total != 0 {
			t.Errorf("search (deep %v) found a value secret now", deep)
		}
	}
	// The mask itself is what is kept.
	if p := s.List(Query{Text: httpclient.Mask, Deep: true}); p.Total != 1 {
		t.Errorf("search for the mask = %d", p.Total)
	}
}

// Clearing one request's history removes its entries and nothing else,
// also after reopening.
func TestDeleteRequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := open(t, dir, Limits{})
	appendN(t, s, 0, 9)
	gone, err := s.DeleteRequest("req_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 3 {
		t.Fatalf("DeleteRequest = %v", gone)
	}
	if p := s.List(Query{RequestID: "req_1"}); p.Total != 0 {
		t.Errorf("left: %v", ids(p))
	}
	if p := s.List(Query{}); p.Total != 6 {
		t.Errorf("others = %d", p.Total)
	}
	if _, err := s.Get(gone[0]); !errors.Is(err, &errs.Error{Code: CodeNotFound}) {
		t.Errorf("Get(deleted) = %v", err)
	}
	if again, err := s.DeleteRequest("req_1"); err != nil || len(again) != 0 {
		t.Errorf("DeleteRequest again = %v, %v", again, err)
	}
	if none, err := s.DeleteRequest(""); err != nil || none != nil {
		t.Errorf(`DeleteRequest("") = %v, %v`, none, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	re := open(t, dir, Limits{})
	if p := re.List(Query{}); p.Total != 6 || re.List(Query{RequestID: "req_1"}).Total != 0 {
		t.Errorf("after reopening: %d", p.Total)
	}
}

// A trimmed entry drops the editor's texts with the bodies and says so.
func TestTrimmedEntryTemplate(t *testing.T) {
	t.Parallel()
	s := open(t, t.TempDir(), Limits{})
	e := entry(1)
	big := strings.Repeat("x", MaxEntryBytes)
	e.Request.Body = Body{Text: big, Size: int64(len(big))}
	e.Template = &Template{URL: "{{base}}", Body: &TemplateBody{Mode: "raw", Text: big}}
	sum, err := s.Append(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(sum.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Trimmed || got.Template == nil || got.Template.Body.Text != "" || !slices.Contains(got.Template.Clipped, SectionBody) || got.Template.URL != "{{base}}" {
		t.Errorf("template = %+v", got.Template)
	}
}
