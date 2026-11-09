package history

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

func entry(i int) Entry {
	return Entry{
		Summary: Summary{
			Time: time.Date(2026, 9, 19, 10, 0, i, 0, time.UTC), Kind: KindHTTP,
			RequestID: fmt.Sprintf("req_%d", i%3), RequestName: fmt.Sprintf("Request %d", i),
			Method: "GET", URL: fmt.Sprintf("https://example.test/items/%d", i),
			Outcome: OutcomeOK, StatusCode: 200, Status: "200 OK", DurationMs: 12,
		},
		Request:  RequestSnapshot{Method: "GET", URL: fmt.Sprintf("https://example.test/items/%d", i), Headers: []Header{{Name: "Accept", Value: "*/*"}}},
		Response: &ResponseSnapshot{Proto: "HTTP/1.1", StatusCode: 200, Status: "200 OK", Headers: []Header{}, Body: Body{Text: "ok", Size: 2}},
	}
}

func open(t *testing.T, dir string, lim Limits) *Store {
	t.Helper()
	s, err := Open(dir, lim)
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func appendN(t *testing.T, s *Store, from, to int) []string {
	t.Helper()
	var ids []string
	for i := from; i < to; i++ {
		sum, err := s.Append(entry(i))
		if err != nil {
			t.Fatalf("Append(%d) = %v", i, err)
		}
		ids = append(ids, sum.ID)
	}
	return ids
}

func segmentFiles(t *testing.T, dir string) []string {
	t.Helper()
	ds, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range ds {
		out = append(out, d.Name())
	}
	return out
}

func ids(p Page) []string {
	var out []string
	for _, e := range p.Entries {
		out = append(out, e.ID)
	}
	return out
}

func TestAppendGetList(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "history")
	s := open(t, dir, Limits{})
	if p := s.List(Query{}); p.Total != 0 || p.Entries == nil {
		t.Fatalf("empty list = %+v", p)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created the directory: %v", err)
	}
	all := appendN(t, s, 0, 10)
	if !strings.HasPrefix(all[0], "h_") || len(all[0]) != 18 {
		t.Errorf("id = %q", all[0])
	}
	e, err := s.Get(all[3])
	if err != nil {
		t.Fatal(err)
	}
	if e.RequestName != "Request 3" || e.Response == nil || e.Response.Body.Text != "ok" || e.Request.Headers[0].Name != "Accept" {
		t.Errorf("Get = %+v", e)
	}
	if _, err := s.Get("h_missing"); !errors.Is(err, &errs.Error{Code: CodeNotFound}) {
		t.Errorf("Get(missing) = %v", err)
	}

	// Newest first, paged.
	p := s.List(Query{Offset: 2, Limit: 3})
	if p.Total != 10 || p.All != 10 || !slices.Equal(ids(p), []string{all[7], all[6], all[5]}) {
		t.Errorf("page = %+v", p)
	}
	if p := s.List(Query{RequestID: "req_1"}); p.Total != 3 || p.Entries[0].ID != all[7] {
		t.Errorf("by request = %+v", p)
	}
	if p := s.List(Query{Text: "ITEMS/4"}); p.Total != 1 || p.Entries[0].ID != all[4] {
		t.Errorf("by text = %+v", p)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v", info.Mode().Perm())
		}
		fi, err := os.Stat(filepath.Join(dir, segmentName(1)))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("segment mode = %v, %v", fi, err)
		}
	}
}

func TestListFilters(t *testing.T) {
	t.Parallel()
	s := open(t, t.TempDir(), Limits{})
	add := func(method string, code int, outcome string) string {
		e := entry(1)
		e.Method, e.StatusCode, e.Outcome = method, code, outcome
		if outcome != OutcomeOK {
			e.Error, e.ErrorCode, e.Status = "connection refused", "network.refused", ""
		}
		sum, err := s.Append(e)
		if err != nil {
			t.Fatal(err)
		}
		return sum.ID
	}
	ok := add("GET", 200, OutcomeOK)
	notFound := add("post", 404, OutcomeOK)
	failed := add("GET", 0, OutcomeError)
	cancelled := add("PUT", 0, OutcomeCancelled)
	tests := []struct {
		q    Query
		want []string
	}{
		{Query{Status: "2xx"}, []string{ok}},
		{Query{Status: "4xx"}, []string{notFound}},
		{Query{Status: StatusError}, []string{cancelled, failed}},
		{Query{Method: "POST"}, []string{notFound}},
		{Query{Method: "get", Status: "2xx"}, []string{ok}},
		{Query{Text: "refused"}, []string{cancelled, failed}},
		{Query{Text: "404"}, []string{notFound}},
	}
	for _, tt := range tests {
		if got := ids(s.List(tt.q)); !slices.Equal(got, tt.want) {
			t.Errorf("List(%+v) = %v, want %v", tt.q, got, tt.want)
		}
	}
}

// NAV-02: the retention limit keeps the newest entries, and rotation
// removes the segment files of dropped entries, also after reopening.
func TestRetentionAndRotation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e := entry(10)
	e.ID = NewID()
	line, err := marshalLine(record{V: FormatVersion, Entry: &e})
	if err != nil {
		t.Fatal(err)
	}
	// About four entries per segment.
	lim := Limits{MaxEntries: 10, SegmentBytes: int64(len(line)) * 4}
	s := open(t, dir, lim)
	all := appendN(t, s, 0, 40)
	p := s.List(Query{Limit: 100})
	if p.Total != 10 || !slices.Equal(ids(p), reversed(all[30:])) {
		t.Fatalf("kept = %v", ids(p))
	}
	if _, err := s.Get(all[29]); err == nil {
		t.Errorf("a dropped entry is still readable")
	}
	files := segmentFiles(t, dir)
	if len(files) > 4 || len(files) < 2 {
		t.Errorf("segment files = %v", files)
	}
	var size int64
	for _, f := range files {
		fi, _ := os.Stat(filepath.Join(dir, f))
		size += fi.Size()
	}
	if size > lim.SegmentBytes*4 {
		t.Errorf("history files hold %d bytes", size)
	}
	_ = s.Close()

	re := open(t, dir, lim)
	if got := ids(re.List(Query{Limit: 100})); !slices.Equal(got, reversed(all[30:])) {
		t.Errorf("after reopening = %v", got)
	}
	// A lower limit applies at once.
	_ = re.Close()
	low := open(t, dir, Limits{MaxEntries: 3, SegmentBytes: lim.SegmentBytes})
	if got := ids(low.List(Query{})); !slices.Equal(got, reversed(all[37:])) {
		t.Errorf("with the lower limit = %v", got)
	}
}

func TestRetentionBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	big := entry(0)
	big.Response.Body = Body{Text: strings.Repeat("x", 10_000), Size: 10_000}
	s := open(t, dir, Limits{MaxBytes: 35_000, SegmentBytes: 25_000})
	var all []string
	for range 12 {
		sum, err := s.Append(big)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, sum.ID)
	}
	if got := ids(s.List(Query{})); !slices.Equal(got, reversed(all[9:])) {
		t.Errorf("kept = %v", got)
	}
	if n := len(segmentFiles(t, dir)); n > 3 {
		t.Errorf("%d segment files", n)
	}
}

func reversed(s []string) []string {
	out := slices.Clone(s)
	slices.Reverse(out)
	return out
}

func TestDeleteAndClear(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := open(t, dir, Limits{})
	all := appendN(t, s, 0, 5)
	if err := s.Delete(all[2]); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(all[2]); !errors.Is(err, &errs.Error{Code: CodeNotFound}) {
		t.Errorf("second Delete = %v", err)
	}
	want := []string{all[4], all[3], all[1], all[0]}
	if got := ids(s.List(Query{})); !slices.Equal(got, want) {
		t.Errorf("after delete = %v", got)
	}
	_ = s.Close()
	s = open(t, dir, Limits{})
	if got := ids(s.List(Query{})); !slices.Equal(got, want) {
		t.Errorf("reopened after delete = %v", got)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 || len(segmentFiles(t, dir)) != 0 {
		t.Errorf("after Clear: %d entries, files %v", s.Len(), segmentFiles(t, dir))
	}
	next := appendN(t, s, 5, 6)
	_ = s.Close()
	if got := ids(open(t, dir, Limits{}).List(Query{})); !slices.Equal(got, next) {
		t.Errorf("after Clear and a new entry = %v", got)
	}
}

// A crash in the middle of a write leaves the last line cut short: it is
// dropped and cut from the file, so the next entry is on a line of its
// own. A broken line in the middle is skipped.
func TestTruncatedLastLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := open(t, dir, Limits{})
	all := appendN(t, s, 0, 3)
	_ = s.Close()
	path := filepath.Join(dir, segmentName(1))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A garbage line in the middle, and half a line at the end.
	lines := strings.SplitAfter(string(data), "\n")
	torn := lines[0] + "{not json\n" + lines[1] + lines[2] + lines[2][:len(lines[2])/2]
	if err := os.WriteFile(path, []byte(torn), 0o600); err != nil {
		t.Fatal(err)
	}

	re := open(t, dir, Limits{})
	if re.Skipped() != 2 {
		t.Errorf("skipped = %d", re.Skipped())
	}
	if got := ids(re.List(Query{})); !slices.Equal(got, reversed(all)) {
		t.Errorf("entries = %v", got)
	}
	more := appendN(t, re, 3, 4)
	_ = re.Close()
	again := open(t, dir, Limits{})
	if again.Skipped() != 1 {
		t.Errorf("skipped after the repair = %d", again.Skipped())
	}
	if got := ids(again.List(Query{})); !slices.Equal(got, append(more, reversed(all)...)) {
		t.Errorf("entries after the repair = %v", got)
	}
	for _, id := range append(more, all...) {
		if _, err := again.Get(id); err != nil {
			t.Errorf("Get(%s) = %v", id, err)
		}
	}
}

func TestNewerFormatSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	line := `{"v":2,"entry":{"id":"h_future"}}` + "\n" + `{"v":1,"entry":{"id":"h_now","method":"GET"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, segmentName(7)), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, dir, Limits{})
	if got := ids(s.List(Query{})); !slices.Equal(got, []string{"h_now"}) || s.Skipped() != 1 {
		t.Errorf("entries = %v, skipped %d", got, s.Skipped())
	}
	// Appends go on after the highest segment.
	appendN(t, s, 0, 1)
	if _, err := os.Stat(filepath.Join(dir, segmentName(7))); err != nil {
		t.Error(err)
	}
}

func TestTrimmedEntry(t *testing.T) {
	t.Parallel()
	s := open(t, t.TempDir(), Limits{})
	e := entry(0)
	e.Request.Body = Body{Text: strings.Repeat("a", MaxEntryBytes), Size: MaxEntryBytes}
	sum, err := s.Append(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(sum.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Trimmed || got.Request.Body.Text != "" || got.Request.Body.Size != MaxEntryBytes || !got.Request.Body.Clipped || len(got.Request.Headers) != 1 {
		t.Errorf("trimmed entry = %+v", got.Request)
	}
}

func TestConcurrentUse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	reg := NewRegistry(Limits{MaxEntries: 50, SegmentBytes: 4 << 10})
	t.Cleanup(func() { _ = reg.Close() })
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			s, err := reg.Store(dir)
			if err != nil {
				t.Error(err)
				return
			}
			for i := range 40 {
				sum, err := s.Append(entry(w*100 + i))
				if err != nil {
					t.Error(err)
					return
				}
				switch i % 4 {
				case 0:
					_ = s.Delete(sum.ID)
				case 1:
					_, _ = s.Get(sum.ID)
				default:
					s.List(Query{Text: "items", Limit: 5})
				}
			}
		})
	}
	wg.Wait()
	s, _ := reg.Store(dir)
	if s.Len() != 50 {
		t.Errorf("%d entries kept", s.Len())
	}
	_ = reg.Close()
	re := open(t, filepath.Join(dir, DirName), Limits{MaxEntries: 50})
	if got, want := ids(re.List(Query{Limit: 100})), ids(s.List(Query{Limit: 100})); !slices.Equal(got, want) {
		t.Errorf("reopened: %v, want %v", got, want)
	}
	if re.Skipped() != 0 {
		t.Errorf("skipped %d lines", re.Skipped())
	}
}

// The helpers callers mask with: every secret is masked before the store
// writes anything, so it never reaches the history files.
func TestSnapshotMasks(t *testing.T) {
	t.Parallel()
	m := httpclient.NewSecretMasker([]string{"s3cret"})
	body, masked := Snapshot([]byte(`{"token":"s3cret"}`), 18, m)
	if !masked || body.Text != `{"token":"`+httpclient.Mask+`"}` || body.Size != 18 || body.Clipped {
		t.Errorf("body = %+v, masked %v", body, masked)
	}
	hs, masked := Headers([]httpclient.Header{
		{Name: "Authorization", Value: "Bearer abc", Enabled: true},
		{Name: "X-Key", Value: "k-s3cret", Enabled: true},
		{Name: "X-Off", Value: "s3cret", Enabled: false},
		{Name: "Accept", Value: "*/*", Enabled: true},
	}, m, true)
	want := []Header{{"Authorization", "Bearer " + httpclient.Mask}, {"X-Key", "k-" + httpclient.Mask}, {"Accept", "*/*"}}
	if !masked || !slices.Equal(hs, want) {
		t.Errorf("headers = %v, masked %v", hs, masked)
	}

	// A secret across the cut is masked whole before clipping.
	long := strings.Repeat("a", MaxBody-3) + "s3cret" + strings.Repeat("b", 10)
	body, _ = Snapshot([]byte(long), int64(len(long)), m)
	if strings.Contains(body.Text, "s3c") || !body.Clipped || body.Size != int64(len(long)) {
		t.Errorf("clipped body ends %q, clipped %v", body.Text[len(body.Text)-10:], body.Clipped)
	}
	// Binary bodies are kept as Base64, masked first.
	bin := append([]byte{0xff, 0xfe}, "s3cret"...)
	body, masked = Snapshot(bin, int64(len(bin)), m)
	if !masked || !body.Base64 || body.Text != "//7igKLigKLigKLigKLigKLigKI=" {
		t.Errorf("binary body = %+v", body)
	}
	// A body recorded only in part is clipped.
	body, _ = Snapshot([]byte("abc"), 10, nil)
	if !body.Clipped || body.Size != 10 || body.Text != "abc" {
		t.Errorf("partial body = %+v", body)
	}
}
