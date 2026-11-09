package request

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// upload is what the test server received.
type upload struct {
	contentType   string
	contentLength int64
	body          []byte
	path          string
}

// uploadServer records every request whose path is not /redirect; that one
// answers 307 to /upload, so the body must be sent again.
func uploadServer(t *testing.T) (*httptest.Server, <-chan upload) {
	t.Helper()
	got := make(chan upload, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			_, _ = io.Copy(io.Discard, r.Body)
			http.Redirect(w, r, "/upload", http.StatusTemporaryRedirect)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("server read: %v", err)
		}
		got <- upload{contentType: r.Header.Get("Content-Type"), contentLength: r.ContentLength, body: data, path: r.URL.Path}
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newClient(t *testing.T) *httpclient.Client {
	t.Helper()
	c := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func send(t *testing.T, c *httpclient.Client, url string, body httpclient.Body) *httpclient.Response {
	t.Helper()
	resp, err := c.Send(context.Background(), httpclient.Request{Method: "POST", URL: url, Body: body, Settings: httpclient.DefaultSettings()})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return resp
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type receivedPart struct {
	name, filename, contentType, disposition string
	data                                     string
}

func parseMultipart(t *testing.T, u upload) []receivedPart {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(u.contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q (%v), want multipart/form-data with a boundary", u.contentType, err)
	}
	r := multipart.NewReader(bytes.NewReader(u.body), params["boundary"])
	var parts []receivedPart
	for {
		p, err := r.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Fatalf("part read: %v", err)
		}
		parts = append(parts, receivedPart{
			name:        p.FormName(),
			filename:    p.FileName(),
			contentType: p.Header.Get("Content-Type"),
			disposition: p.Header.Get("Content-Disposition"),
			data:        string(data),
		})
	}
	return parts
}

func expandVars(vars map[string]string) Expand {
	resolver := template.Resolver{Scopes: template.Chain{template.Vars("env", vars)}}
	return func(s string) (string, error) {
		r := resolver.Render(s)
		return r.Text, r.Err()
	}
}

func TestMultipartOnTheWire(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "fixtures/avatar.png", []byte("\x89PNG\r\n\x1a\nfake"))
	writeFile(t, root, "fixtures/notes.txt", []byte("hello {{user}}\n"))
	// A quote in the file name is escaped in the Content-Disposition. A
	// Windows file name cannot have one, nor CR or LF, the other characters
	// escaped; the part names below are escaped the same way everywhere.
	outsideName, sentName := `we"ird name.JSON`, "we%22ird name.JSON"
	if runtime.GOOS == "windows" {
		outsideName, sentName = "we'ird name.JSON", "we'ird name.JSON"
	}
	outside := writeFile(t, t.TempDir(), outsideName, []byte(`{"a":1}`))
	writeFile(t, root, "data/Résumé 2026.bin", bytes.Repeat([]byte{0, 1, 2, 255}, 5000))
	writeFile(t, root, "fixtures/empty", nil)

	parts := []projectfile.Part{
		{Name: "caption", Value: "Profile photo of {{user}}"},
		{Name: "{{field}}", Value: "x"},
		{Name: "skip", Value: "no", Disabled: true},
		{}, // a row being filled in
		{Name: "image", File: "fixtures/avatar.png"},
		{Name: "notes", File: "fixtures/notes.txt", ContentType: "text/markdown; charset=utf-8"},
		{Name: "outside", File: outside},
		{Name: "cv", File: "data/Résumé 2026.bin"},
		{Name: "empty", File: "fixtures/empty"},
		{Name: "meta", Value: `{"k":"v"}`, ContentType: "application/json"},
		{Name: `quo"te\name` + "\r\nX-Injected: 1", Value: "ünïcode ✓"},
		{Name: "", Value: "only a value"},
	}
	body, err := Body(projectfile.Body{Mode: "multipart", Parts: parts}, expandVars(map[string]string{"user": "zoë", "field": "dyn"}), root)
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	mp := body.(*MultipartBody)
	if want := "multipart/form-data; boundary=" + mp.Boundary(); body.ContentType() != want || len(mp.Boundary()) < 30 {
		t.Errorf("ContentType = %q, want %q with a long random boundary", body.ContentType(), want)
	}

	srv, got := uploadServer(t)
	c := newClient(t)
	for _, path := range []string{"/upload", "/redirect"} {
		t.Run(path, func(t *testing.T) {
			resp := send(t, c, srv.URL+path, body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			u := <-got
			if u.path != "/upload" {
				t.Fatalf("path = %q", u.path)
			}
			_, n, _ := body.Open()
			if u.contentLength != int64(len(u.body)) || u.contentLength != n {
				t.Errorf("Content-Length = %d, body = %d bytes, computed length = %d", u.contentLength, len(u.body), n)
			}
			if u.contentType != body.ContentType() {
				t.Errorf("Content-Type = %q, want %q", u.contentType, body.ContentType())
			}

			want := []receivedPart{
				{name: "caption", disposition: `form-data; name="caption"`, data: "Profile photo of zoë"},
				{name: "dyn", disposition: `form-data; name="dyn"`, data: "x"},
				{name: "image", filename: "avatar.png", contentType: "image/png", disposition: `form-data; name="image"; filename="avatar.png"`, data: "\x89PNG\r\n\x1a\nfake"},
				{name: "notes", filename: "notes.txt", contentType: "text/markdown; charset=utf-8", data: "hello {{user}}\n"},
				{name: "outside", filename: sentName, contentType: "application/json", disposition: `form-data; name="outside"; filename="` + sentName + `"`, data: `{"a":1}`},
				{name: "cv", filename: "Résumé 2026.bin", contentType: "application/octet-stream", data: string(bytes.Repeat([]byte{0, 1, 2, 255}, 5000))},
				{name: "empty", filename: "empty", contentType: "application/octet-stream", data: ""},
				{name: "meta", contentType: "application/json", data: `{"k":"v"}`},
				{disposition: `form-data; name="quo%22te\name%0D%0AX-Injected: 1"`, data: "ünïcode ✓"},
				{name: "", disposition: `form-data; name=""`, data: "only a value"},
			}
			rec := parseMultipart(t, u)
			if len(rec) != len(want) {
				t.Fatalf("got %d parts, want %d: %+v", len(rec), len(want), rec)
			}
			for i, w := range want {
				g := rec[i]
				if w.disposition != "" && g.disposition != w.disposition {
					t.Errorf("part %d Content-Disposition = %q, want %q", i, g.disposition, w.disposition)
				}
				if w.name != "" && g.name != w.name {
					t.Errorf("part %d name = %q, want %q", i, g.name, w.name)
				}
				if g.filename != w.filename || g.contentType != w.contentType || g.data != w.data {
					t.Errorf("part %d = filename %q, type %q, %d bytes; want %q, %q, %d bytes", i, g.filename, g.contentType, len(g.data), w.filename, w.contentType, len(w.data))
				}
			}
		})
	}

	// Go's own form parser reads the same body.
	t.Run("ParseMultipartForm", func(t *testing.T) {
		rc, _, _ := body.Open()
		defer rc.Close()
		req := httptest.NewRequest("POST", "/", rc)
		req.Header.Set("Content-Type", body.ContentType())
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if v := req.MultipartForm.Value["caption"]; len(v) != 1 || v[0] != "Profile photo of zoë" {
			t.Errorf("caption = %q", v)
		}
		if f := req.MultipartForm.File["cv"]; len(f) != 1 || f[0].Size != 20000 || f[0].Filename != "Résumé 2026.bin" {
			t.Errorf("cv = %+v", f)
		}
	})
}

func TestMultipartWithoutParts(t *testing.T) {
	t.Parallel()
	body, err := Body(projectfile.Body{Mode: "multipart"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	rc, n, err := body.Open()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	boundary := body.(*MultipartBody).Boundary()
	// mime/multipart ends an empty body as it ends any other, after a CRLF.
	if want := "\r\n--" + boundary + "--\r\n"; string(data) != want || n != int64(len(want)) {
		t.Errorf("body = %q (%d), want %q", data, n, want)
	}
}

func TestMultipartBoundaryIsRandom(t *testing.T) {
	t.Parallel()
	a, _ := Multipart(nil, nil, "")
	b, _ := Multipart(nil, nil, "")
	if a.Boundary() == b.Boundary() {
		t.Errorf("two bodies share the boundary %q", a.Boundary())
	}
}

func TestBodyFileErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "here.txt", []byte("x"))
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		body   projectfile.Body
		root   string
		kind   errs.Kind
		code   string
		row    string
		part   string
		inText string
	}{
		{name: "missing part file", body: projectfile.Body{Mode: "multipart", Parts: []projectfile.Part{{Name: "a", Value: "1"}, {Name: "avatar", File: "gone.png"}}}, root: root, kind: errs.NotFound, code: CodeBodyFileMissing, row: "1", part: "avatar", inText: `part 2 ("avatar")`},
		{name: "part file is a folder", body: projectfile.Body{Mode: "multipart", Parts: []projectfile.Part{{Name: "d", File: "dir"}}}, root: root, kind: errs.InvalidArgument, code: CodeBodyFileUnreadable, row: "0", part: "d"},
		{name: "relative part file without a project", body: projectfile.Body{Mode: "multipart", Parts: []projectfile.Part{{Name: "h", File: "here.txt"}}}, kind: errs.NotFound, code: CodeBodyFileMissing, row: "0", part: "h"},
		{name: "control character in a part type", body: projectfile.Body{Mode: "multipart", Parts: []projectfile.Part{{Name: "t", Value: "v", ContentType: "text/plain\r\nX: y"}}}, kind: errs.InvalidArgument, code: CodeInvalidPartContentType, row: "0", part: "t"},
		{name: "unresolved variable in a part", body: projectfile.Body{Mode: "multipart", Parts: []projectfile.Part{{Name: "t", Value: "{{nope}}"}}}, kind: errs.InvalidArgument, code: "template.unresolved", row: "0", part: "t"},
		{name: "disabled missing file is fine", body: projectfile.Body{Mode: "multipart", Parts: []projectfile.Part{{Name: "x", File: "gone", Disabled: true}}}, root: root},
		{name: "file body without a file", body: projectfile.Body{Mode: "file"}, root: root, kind: errs.InvalidArgument, code: CodeBodyFileUnset},
		{name: "missing file body", body: projectfile.Body{Mode: "file", Path: "gone.bin"}, root: root, kind: errs.NotFound, code: CodeBodyFileMissing, inText: "gone.bin"},
		{name: "file body is a folder", body: projectfile.Body{Mode: "file", Path: filepath.Join(root, "dir")}, kind: errs.InvalidArgument, code: CodeBodyFileUnreadable},
		{name: "control character in the file type", body: projectfile.Body{Mode: "file", Path: "here.txt", ContentType: "a\nb"}, root: root, kind: errs.InvalidArgument, code: CodeInvalidPartContentType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, err := Body(tt.body, expandVars(nil), tt.root)
			if tt.code == "" {
				if err != nil {
					t.Fatalf("Body: %v", err)
				}
				return
			}
			if b != nil {
				t.Errorf("Body = %#v, want nil on error", b)
			}
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("error = %v, want *errs.Error", err)
			}
			if e.Kind != tt.kind || e.Code != tt.code || e.Details[DetailRow] != tt.row || e.Details[DetailPart] != tt.part {
				t.Errorf("error = kind %q code %q row %q part %q, want %q %q %q %q (%v)", e.Kind, e.Code, e.Details[DetailRow], e.Details[DetailPart], tt.kind, tt.code, tt.row, tt.part, err)
			}
			if !strings.Contains(e.Message, tt.inText) || e.Hint == "" {
				t.Errorf("message %q (hint %q) does not name %q", e.Message, e.Hint, tt.inText)
			}
		})
	}
}

func TestFileBodyOnTheWire(t *testing.T) {
	root := t.TempDir()
	data := bytes.Repeat([]byte("0123456789abcdef"), 70000) // over 1 MiB
	writeFile(t, root, "fixtures/big.JSON", data)
	srv, got := uploadServer(t)
	c := newClient(t)

	tests := []struct {
		name, path, override, want string
	}{
		{name: "relative path, type by extension", path: "fixtures/big.JSON", want: "application/json"},
		{name: "absolute path with an override", path: filepath.Join(root, "fixtures", "big.JSON"), override: "application/x-thing", want: "application/x-thing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := Body(projectfile.Body{Mode: "file", Path: tt.path, ContentType: tt.override}, nil, root)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"/upload", "/redirect"} {
				send(t, c, srv.URL+p, body)
				u := <-got
				if u.contentType != tt.want || u.contentLength != int64(len(data)) || !bytes.Equal(u.body, data) {
					t.Errorf("%s: Content-Type %q, Content-Length %d, %d bytes (equal %v); want %q, %d", p, u.contentType, u.contentLength, len(u.body), bytes.Equal(u.body, data), tt.want, len(data))
				}
			}
		})
	}
}

func TestFileChangedWhileSending(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := writeFile(t, root, "f.bin", []byte("0123456789"))

	b, err := Body(projectfile.Body{Mode: "multipart", Parts: []projectfile.Part{{Name: "f", File: "f.bin"}}}, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("012"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, _, _ := b.Open()
	_, err = io.ReadAll(rc)
	rc.Close()
	if !errors.Is(err, &errs.Error{Kind: errs.IO, Code: CodeBodyFileChanged}) {
		t.Errorf("shrunk file: error = %v, want %s", err, CodeBodyFileChanged)
	}

	fb, err := Body(projectfile.Body{Mode: "file", Path: "f.bin"}, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	rc, n, err := fb.Open()
	if err != nil || n != 3 {
		t.Fatalf("Open = %d, %v", n, err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("grown")
	f.Close()
	_, err = io.ReadAll(rc)
	rc.Close()
	if !errors.Is(err, &errs.Error{Kind: errs.IO, Code: CodeBodyFileChanged}) {
		t.Errorf("grown file: error = %v, want %s", err, CodeBodyFileChanged)
	}
}

func TestMultipartReaderClosedEarly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "f.bin", bytes.Repeat([]byte("x"), 1<<20))
	b, err := Multipart([]projectfile.Part{{Name: "f", File: "f.bin"}}, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	for range 20 {
		rc, _, _ := b.Open()
		buf := make([]byte, 100)
		_, _ = rc.Read(buf)
		rc.Close()
	}
	// The writers see the closed pipe and exit.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+2 {
		t.Errorf("goroutines = %d, were %d: the writers leak", n, before)
	}
}

func TestStoredAndResolvedFilePaths(t *testing.T) {
	t.Parallel()
	// Absolute on every system: on Windows on the current drive.
	root, err := filepath.Abs(filepath.FromSlash("/home/me/project"))
	if err != nil {
		t.Fatal(err)
	}
	in := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	for abs, want := range map[string]string{
		in("fixtures", "a.png"):        "fixtures/a.png",
		in("a.png"):                    "a.png",
		in("..", "other", "a.png"):     filepath.Join(filepath.Dir(root), "other", "a.png"),
		in("..", "project-2", "a"):     filepath.Join(filepath.Dir(root), "project-2", "a"),
		root:                           root,
		filepath.Join(root, "x", ".."): root,
	} {
		got := StoredFilePath(root, abs)
		if got != want {
			t.Errorf("StoredFilePath(%q) = %q, want %q", abs, got, want)
		}
		if back := ResolveFilePath(root, got); back != filepath.Clean(abs) {
			t.Errorf("ResolveFilePath(%q) = %q, want %q", got, back, filepath.Clean(abs))
		}
	}
	if got := StoredFilePath("", in("a")); got != in("a") {
		t.Errorf("without a project = %q", got)
	}
}

// TestLargeFileUploadMemory uploads files over 100 MB, as a binary body
// and as a multipart part, to a server that discards them, and checks that
// the process allocated only a small fraction of that: the files are
// streamed, not loaded.
func TestLargeFileUploadMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("uploads 220 MB")
	}
	const size = 110 << 20
	const limit = 16 << 20
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file: no disk space, reads as zeros.
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var mu sync.Mutex
	var received []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		mu.Lock()
		received = append(received, n)
		mu.Unlock()
	}))
	defer srv.Close()
	c := newClient(t)

	bodies := map[string]projectfile.Body{
		"file":      {Mode: "file", Path: "big.bin"},
		"multipart": {Mode: "multipart", Parts: []projectfile.Part{{Name: "a", Value: "b"}, {Name: "big", File: "big.bin"}}},
	}
	for name, pb := range bodies {
		t.Run(name, func(t *testing.T) {
			body, err := Body(pb, nil, root)
			if err != nil {
				t.Fatal(err)
			}
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			// Sample the heap while sending, for the peak.
			stop := make(chan struct{})
			peak := make(chan uint64)
			go func() {
				var m runtime.MemStats
				var max uint64
				tick := time.NewTicker(20 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-stop:
						peak <- max
						return
					case <-tick.C:
						runtime.ReadMemStats(&m)
						if m.HeapInuse > max {
							max = m.HeapInuse
						}
					}
				}
			}()
			resp := send(t, c, srv.URL+"/", body)
			close(stop)
			maxHeap := <-peak
			runtime.ReadMemStats(&after)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			mu.Lock()
			last := received[len(received)-1]
			mu.Unlock()
			_, n, _ := body.Open()
			if last != n || last < size {
				t.Errorf("server received %d bytes, want %d (over %d)", last, n, size)
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			growth := int64(maxHeap) - int64(before.HeapInuse)
			t.Logf("%s: sent %d MiB; allocated %.1f MiB in total, peak heap growth %.1f MiB", name, last>>20, float64(allocated)/(1<<20), float64(growth)/(1<<20))
			if allocated > limit {
				t.Errorf("allocated %s bytes while sending %d: the file is not streamed", strconv.FormatUint(allocated, 10), last)
			}
			if growth > limit {
				t.Errorf("heap grew by %d bytes while sending", growth)
			}
		})
	}
}
