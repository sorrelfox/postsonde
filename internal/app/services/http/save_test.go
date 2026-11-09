package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// TestSaveBody sends requests whose bodies are binary and several
// megabytes, and saves them byte for byte under the suggested name
// (HTTP-34).
func TestSaveBody(t *testing.T) {
	t.Parallel()
	big := make([]byte, 3<<20+17)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range big {
		big[i] = byte(rng.UintN(256))
	}
	small := []byte("\x00\x01binary\xff")
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		switch r.URL.Path {
		case "/files/report":
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", `attachment; filename="Q3 report.pdf"`)
			_, _ = w.Write(big)
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(small)
		}
	}))
	defer srv.Close()

	spool := filepath.Join(t.TempDir(), "spool")
	out := t.TempDir()
	var asked []SaveDialog
	answer := ""
	pick := func(_ context.Context, d SaveDialog) (string, error) {
		asked = append(asked, d)
		return answer, nil
	}
	f := newFixture(t, httpSource(srv.URL+"/files/report"), nil, WithSaveBody(spool, pick))

	id := f.send(t)
	f.rec.wait(t, id)
	resp := f.rec.response(t, id)
	if resp.BodyID == "" || resp.FileName != "Q3 report.pdf" || !resp.BodyHex || resp.Size != int64(len(big)) {
		t.Fatalf("response: bodyId %q, fileName %q, hex %v, size %d", resp.BodyID, resp.FileName, resp.BodyHex, resp.Size)
	}
	// Large bodies are spooled, private to the user.
	files, _ := os.ReadDir(spool)
	if len(files) != 1 {
		t.Fatalf("spool = %v, want one file", files)
	}
	if info, err := files[0].Info(); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("spool file mode = %v, %v", info.Mode(), err)
	}

	// Cancelled: nothing written.
	res, err := f.svc.SaveBody(t.Context(), SaveBodyInputDTO{BodyID: resp.BodyID})
	if err != nil || res.Saved {
		t.Fatalf("cancelled save = %+v, %v", res, err)
	}
	if len(asked) != 1 || asked[0].Filename != "Q3 report.pdf" {
		t.Errorf("dialog = %+v", asked)
	}

	answer = filepath.Join(out, "saved.pdf")
	res, err = f.svc.SaveBody(t.Context(), SaveBodyInputDTO{BodyID: resp.BodyID})
	if err != nil || !res.Saved || res.Path != answer || res.Size != int64(len(big)) {
		t.Fatalf("save = %+v, %v", res, err)
	}
	if got, _ := os.ReadFile(answer); !bytes.Equal(got, big) {
		t.Errorf("saved %d bytes, not the body received", len(got))
	}
	if left, _ := os.ReadDir(out); len(left) != 1 {
		t.Errorf("out dir = %v, want only the saved file", left)
	}

	// A small body is kept in memory and saved too.
	f.src = httpSource(srv.URL + "/download")
	id = f.send(t)
	f.rec.wait(t, id)
	resp = f.rec.response(t, id)
	if resp.FileName != "download.bin" {
		t.Errorf("fileName = %q", resp.FileName)
	}
	answer = filepath.Join(out, "small.bin")
	if _, err := f.svc.SaveBody(t.Context(), SaveBodyInputDTO{BodyID: resp.BodyID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(answer); !bytes.Equal(got, small) {
		t.Errorf("small saved = %q", got)
	}

	// Unknown ID.
	_, err = f.svc.SaveBody(t.Context(), SaveBodyInputDTO{BodyID: "body_nope"})
	var e *errs.Error
	errors.As(err, &e)
	checkErr(t, e, errs.NotFound, CodeBodyGone)

	// A folder that does not exist: save_failed, nothing left behind.
	answer = filepath.Join(out, "missing", "x.bin")
	_, err = f.svc.SaveBody(t.Context(), SaveBodyInputDTO{BodyID: resp.BodyID})
	e = nil
	errors.As(err, &e)
	if e == nil || e.Code != CodeSaveFailed {
		t.Errorf("missing folder: err = %v", err)
	}

	// Shutdown removes the spool.
	_ = f.svc.ServiceShutdown()
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Errorf("spool after shutdown: %v", err)
	}
}

func TestSaveBodyUnavailable(t *testing.T) {
	t.Parallel()
	f := newFixture(t, httpSource("http://example.com/"), nil)
	_, err := f.svc.SaveBody(t.Context(), SaveBodyInputDTO{BodyID: "x"})
	var e *errs.Error
	errors.As(err, &e)
	checkErr(t, e, errs.Unsupported, CodeUnavailable)
}

func TestBodyStoreRetention(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "spool")
	// Leftovers of an earlier run are removed.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.body"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newBodyStore(dir)
	if _, err := os.Stat(filepath.Join(dir, "old.body")); !os.IsNotExist(err) {
		t.Errorf("leftover kept: %v", err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	big := bytes.Repeat([]byte("x"), SpoolThreshold+1)
	first := s.put(big, "a")
	var ids []string
	for range MaxKeptBodies {
		ids = append(ids, s.put([]byte("small"), "b"))
	}
	if _, _, ok, _ := s.open(first); ok {
		t.Error("oldest body kept past MaxKeptBodies")
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Errorf("spool file of a dropped body left: %v", files)
	}
	kb, r, ok, err := s.open(ids[0])
	if !ok || err != nil || kb.name != "b" {
		t.Fatalf("open = %+v, %v, %v", kb, ok, err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	if string(data) != "small" {
		t.Errorf("data = %q", data)
	}
	now = now.Add(BodyTTL + time.Second)
	if _, _, ok, _ := s.open(ids[len(ids)-1]); ok {
		t.Error("body kept past BodyTTL")
	}

	// Without a directory, large bodies stay in memory.
	m := newBodyStore("")
	id := m.put(big, "c")
	if kb, r, ok, _ := m.open(id); !ok || kb.path != "" || kb.size != int64(len(big)) {
		t.Errorf("memory store: %+v %v", kb, ok)
	} else {
		r.Close()
	}
}

func TestSuggestFileName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		disposition, url, contentType, want string
	}{
		{`attachment; filename="report.pdf"`, "https://x/y", "application/pdf", "report.pdf"},
		{`attachment; filename*=UTF-8''na%C3%AFve%20file.txt; filename="naive.txt"`, "", "", "naïve file.txt"},
		{`attachment; filename=my report.csv`, "", "", "my report.csv"},
		{`attachment; filename="../../etc/passwd"`, "", "text/plain", "passwd.txt"},
		{`attachment; filename="a\\b\\c:d?.json"`, "", "", "c_d_.json"},
		{`inline`, "https://api.example.com/v1/users/42?x=1", "application/json; charset=utf-8", "42.json"},
		{"", "https://example.com/img/logo%20big.png", "image/png", "logo big.png"},
		{"", "https://example.com/", "application/vnd.api+json", "response.json"},
		{"", "https://example.com/feed", "application/atom+xml", "feed.xml"},
		{"", "https://example.com/data", "", "data"},
		{"", "https://example.com/data", "text/x-custom", "data.txt"},
		{"", "https://example.com/data", "application/octet-stream", "data.bin"},
		{"", "https://example.com/...", "text/html", "response.html"},
		{"", "https://example.com/CON", "", "_CON"},
		{`attachment; filename="` + string(bytes.Repeat([]byte("é"), 150)) + `.zip"`, "", "", string(bytes.Repeat([]byte("é"), 98)) + ".zip"},
	}
	for _, tt := range tests {
		if got := suggestFileName(tt.disposition, tt.url, tt.contentType); got != tt.want {
			t.Errorf("suggestFileName(%q, %q, %q) = %q, want %q", tt.disposition, tt.url, tt.contentType, got, tt.want)
		}
	}
}
