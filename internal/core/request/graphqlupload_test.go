package request

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// GQL-06: the extensions go in the POST body and the GET query string,
// rendered like the variables.
func TestBuildGraphQLExtensions(t *testing.T) {
	t.Parallel()
	f := projectfile.GraphQLRequest{
		URL:        "http://h/g",
		Query:      "{ me { id } }",
		Extensions: "{\n  \"persistedQuery\": {\"version\": 1, \"sha256Hash\": \"{{hash}}\"}\n}",
	}
	o := HTTPOptions{Expand: buildExpand(map[string]string{"hash": "ab<c"})}
	b, err := BuildGraphQL(f, noAuth, o)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"query":"{ me { id } }","extensions":{"persistedQuery":{"version":1,"sha256Hash":"ab<c"}}}`
	if got := bodyText(t, b.Request.Body); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}

	f.Method = "GET"
	b, err = BuildGraphQL(f, noAuth, o)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "http://h/g?query=%7B%20me%20%7B%20id%20%7D%20%7D&extensions=%7B%22persistedQuery%22%3A%7B%22version%22%3A1%2C%22sha256Hash%22%3A%22ab%3Cc%22%7D%7D"
	if b.Request.URL != wantURL {
		t.Errorf("URL = %s\nwant  %s", b.Request.URL, wantURL)
	}

	// Blank and null send none.
	for _, text := range []string{"", " null "} {
		f.Method, f.Extensions = "", text
		b, err = BuildGraphQL(f, noAuth, HTTPOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := bodyText(t, b.Request.Body); got != `{"query":"{ me { id } }"}` {
			t.Errorf("%q: body = %s", text, got)
		}
	}
}

func TestGraphQLUploadPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"variables.file", "variables.file", true},
		{" variables.input.files.0 ", "variables.input.files.0", true},
		{"input.files.0", "variables.input.files.0", true},
		{"file", "variables.file", true},
		{"variables", "variables.variables", true},
		{"", "", false},
		{"variables..file", "", false},
		{"file.", "", false},
	}
	for _, tt := range tests {
		got, err := GraphQLUploadPath(tt.in)
		if (err == nil) != tt.ok || strings.Join(got, ".") != tt.want {
			t.Errorf("GraphQLUploadPath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestSetNull(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		v    any
		path []string
		want any
		ok   bool
	}{
		{"new member", map[string]any{"a": 1}, []string{"f"}, map[string]any{"a": 1, "f": nil}, true},
		{"replaced", map[string]any{"f": "x"}, []string{"f"}, map[string]any{"f": nil}, true},
		{"new list", map[string]any{}, []string{"fs", "1"}, map[string]any{"fs": []any{nil, nil}}, true},
		{"into list", map[string]any{"fs": []any{"a", map[string]any{}}}, []string{"fs", "1", "f"}, map[string]any{"fs": []any{"a", map[string]any{"f": nil}}}, true},
		{"null on the way", map[string]any{"in": nil}, []string{"in", "f"}, map[string]any{"in": map[string]any{"f": nil}}, true},
		{"scalar on the way", map[string]any{"in": 3}, []string{"in", "f"}, nil, false},
		{"name into a list", map[string]any{"fs": []any{}}, []string{"fs", "x"}, nil, false},
		{"negative index", map[string]any{"fs": []any{}}, []string{"fs", "-1"}, nil, false},
	}
	for _, tt := range tests {
		got, ok := setNull(tt.v, tt.path)
		if ok != tt.ok || (ok && !reflect.DeepEqual(got, tt.want)) {
			t.Errorf("%s: setNull = %v, %v; want %v, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

// GQL-07: relative file paths resolve against the project root, the
// body follows the multipart request spec, and the preflight header is
// added.
func TestBuildGraphQLUploads(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fixtures", "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := projectfile.GraphQLRequest{
		URL:        "http://h/g",
		Query:      "mutation($f: Upload!) { up(f: $f) }",
		Variables:  `{"n": 12345678901234567890, "f": null}`,
		Extensions: `{"e": true}`,
		Files: []projectfile.Part{
			{Name: "", File: ""}, // a row being filled in
			{Name: "f", File: "fixtures/a.txt"},
		},
	}
	b, err := BuildGraphQL(f, noAuth, HTTPOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	mb, ok := b.Request.Body.(*MultipartBody)
	if !ok {
		t.Fatalf("body = %T, want multipart", b.Request.Body)
	}
	_, params, err := mime.ParseMediaType(mb.ContentType())
	if err != nil || !strings.HasPrefix(mb.ContentType(), "multipart/form-data;") {
		t.Fatalf("content type = %q", mb.ContentType())
	}
	r := multipart.NewReader(strings.NewReader(bodyText(t, mb)), params["boundary"])
	var got []string
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		got = append(got, p.FormName()+"|"+p.FileName()+"|"+string(data))
	}
	want := []string{
		`operations||{"query":"mutation($f: Upload!) { up(f: $f) }","variables":{"f":null,"n":12345678901234567890},"extensions":{"e":true}}`,
		`map||{"0":["variables.f"]}`,
		`0|a.txt|hello`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parts =\n%q\nwant\n%q", got, want)
	}
	if !hasHeader(b.Request.Headers, GraphQLPreflightHeader) {
		t.Errorf("headers = %+v, want %s", b.Request.Headers, GraphQLPreflightHeader)
	}

	// Without enabled files, the JSON body and no preflight header.
	f.Files[1].Disabled = true
	b, err = BuildGraphQL(f, noAuth, HTTPOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if b.Request.Body.ContentType() != "application/json" || hasHeader(b.Request.Headers, GraphQLPreflightHeader) {
		t.Errorf("disabled files: %s %+v", b.Request.Body.ContentType(), b.Request.Headers)
	}
}

func TestBuildGraphQLUploadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		method string
		vars   string
		files  []projectfile.Part
		code   string
		row    string
	}{
		{"no path", "", "", []projectfile.Part{{File: file}}, CodeGraphQLInvalidUpload, "0"},
		{"bad path", "", "", []projectfile.Part{{Name: "variables.a..b", File: file}}, CodeGraphQLInvalidUpload, "0"},
		{"no file", "", "", []projectfile.Part{{Name: "f", File: file, Disabled: true}, {Name: "g"}}, CodeBodyFileUnset, "1"},
		{"missing file", "", "", []projectfile.Part{{Name: "f", File: filepath.Join(dir, "gone.png")}}, CodeBodyFileMissing, "0"},
		{"relative without root", "", "", []projectfile.Part{{Name: "f", File: "a.bin"}}, CodeBodyFileMissing, "0"},
		{"twice", "", "", []projectfile.Part{{Name: "f", File: file}, {Name: "variables.f", File: file}}, CodeGraphQLInvalidUpload, "1"},
		{"inside another", "", "", []projectfile.Part{{Name: "in.f", File: file}, {Name: "in", File: file}}, CodeGraphQLInvalidUpload, "1"},
		{"scalar in the way", "", `{"in": "text"}`, []projectfile.Part{{Name: "in.f", File: file}}, CodeGraphQLInvalidUpload, "0"},
		{"content type", "", "", []projectfile.Part{{Name: "f", File: file, ContentType: "text/plain\r\nX: y"}}, CodeGraphQLInvalidUpload, "0"},
		{"GET", "GET", "", []projectfile.Part{{Name: "f", File: file}}, CodeGraphQLUploadOverGET, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := projectfile.GraphQLRequest{Method: tt.method, URL: "http://h", Query: "{a}", Variables: tt.vars, Files: tt.files}
			_, err := BuildGraphQL(f, noAuth, HTTPOptions{})
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != tt.code {
				t.Fatalf("err = %v, want code %s", err, tt.code)
			}
			if e.Details[DetailRow] != tt.row || e.Details[DetailField] != "files" && tt.row != "" {
				t.Errorf("details = %v, want row %q and field files", e.Details, tt.row)
			}
		})
	}
}

func TestBuildGraphQLInvalidExtensions(t *testing.T) {
	t.Parallel()
	for _, text := range []string{`{"a":`, `[1]`} {
		f := projectfile.GraphQLRequest{URL: "http://h", Query: "{a}", Extensions: text}
		_, err := BuildGraphQL(f, noAuth, HTTPOptions{})
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeGraphQLInvalidExtensions || e.Details[DetailField] != "extensions" {
			t.Errorf("%s: err = %v", text, err)
		}
		if _, err := BuildGraphQLWS(f, noAuth, HTTPOptions{}); !errors.As(err, &e) || e.Code != CodeGraphQLInvalidExtensions {
			t.Errorf("%s: WS err = %v", text, err)
		}
	}
}
