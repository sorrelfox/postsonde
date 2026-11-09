package http

import (
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// TestSendGraphQLUploads sends a GraphQL request with extensions (GQL-06)
// and files (GQL-07) to a test server, which parses the multipart request
// as the GraphQL multipart request spec describes it: operations first,
// then map, then the files, and each file where the map puts it.
func TestSendGraphQLUploads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	avatar := write("avatar.png", "PNG-bytes")
	a := write("a.txt", "first file")
	b := write("b.csv", "x,y\n1,2\n")

	type filePart struct{ name, filename, contentType, content string }
	type seen struct {
		contentType, preflight string
		order                  []string
		operations, fileMap    map[string]any
		files                  []filePart
	}
	got := make(chan seen, 1)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		s := seen{contentType: r.Header.Get("Content-Type"), preflight: r.Header.Get(request.GraphQLPreflightHeader)}
		partReader, err := r.MultipartReader()
		if err != nil {
			t.Errorf("multipart reader: %v", err)
			got <- s
			return
		}
		for {
			p, err := partReader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("next part: %v", err)
				break
			}
			data, _ := io.ReadAll(p)
			s.order = append(s.order, p.FormName())
			switch p.FormName() {
			case "operations":
				if err := json.Unmarshal(data, &s.operations); err != nil {
					t.Errorf("operations %q: %v", data, err)
				}
			case "map":
				if err := json.Unmarshal(data, &s.fileMap); err != nil {
					t.Errorf("map %q: %v", data, err)
				}
			default:
				s.files = append(s.files, filePart{p.FormName(), p.FileName(), p.Header.Get("Content-Type"), string(data)})
			}
		}
		got <- s
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"upload":true}}`)
	}))
	defer srv.Close()

	gql := &projectfile.GraphQLRequest{
		ID:    "req_1",
		URL:   srv.URL + "/graphql",
		Query: "mutation Upload($avatar: Upload!, $input: FilesInput!) { upload(avatar: $avatar, input: $input) }",
		// The files' positions are filled in: input.files is created, the
		// avatar's placeholder replaced.
		Variables:  `{"avatar": "placeholder", "input": {"name": "{{name}}"}}`,
		Extensions: `{"persistedQuery": {"version": 1, "sha256Hash": "abc"}}`,
		Files: []projectfile.Part{
			{Name: "variables.avatar", File: avatar},
			// A path without "variables." is inside the variables.
			{Name: "input.files.0", File: a, ContentType: "text/plain; charset=utf-8"},
			{Name: "variables.input.files.1", File: b},
			// The same file twice: one part, two paths in the map.
			{Name: "variables.input.again", File: avatar},
			{Name: "variables.ignored", File: b, Disabled: true},
		},
	}
	src := collection.HTTPSource{
		GraphQL:         gql,
		Auth:            projectfile.Auth{Type: projectfile.AuthNone},
		FolderVariables: [][]projectfile.Variable{{{Name: "name", Value: "Ada"}}},
	}
	f := newFixture(t, src, nil)

	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	s := <-got
	if want := []string{"operations", "map", "0", "1", "2"}; !reflect.DeepEqual(s.order, want) {
		t.Errorf("parts = %q, want %q", s.order, want)
	}
	if s.preflight != "true" {
		t.Errorf("%s = %q, want true", request.GraphQLPreflightHeader, s.preflight)
	}
	wantOps := map[string]any{
		"query":         gql.Query,
		"operationName": "Upload",
		"variables": map[string]any{
			"avatar": nil,
			"input":  map[string]any{"name": "Ada", "files": []any{nil, nil}, "again": nil},
		},
		"extensions": map[string]any{"persistedQuery": map[string]any{"version": float64(1), "sha256Hash": "abc"}},
	}
	if !reflect.DeepEqual(s.operations, wantOps) {
		t.Errorf("operations = %v, want %v", s.operations, wantOps)
	}
	wantMap := map[string]any{
		"0": []any{"variables.avatar", "variables.input.again"},
		"1": []any{"variables.input.files.0"},
		"2": []any{"variables.input.files.1"},
	}
	if !reflect.DeepEqual(s.fileMap, wantMap) {
		t.Errorf("map = %v, want %v", s.fileMap, wantMap)
	}
	wantFiles := []filePart{
		{"0", "avatar.png", "image/png", "PNG-bytes"},
		{"1", "a.txt", "text/plain; charset=utf-8", "first file"},
		{"2", "b.csv", "text/csv", "x,y\n1,2\n"},
	}
	if !reflect.DeepEqual(s.files, wantFiles) {
		t.Errorf("files = %+v, want %+v", s.files, wantFiles)
	}

	// A header row turns the automatic preflight header off, and so does
	// settings.omitHeaders.
	gql.Headers = []projectfile.KeyValue{{Name: "apollo-require-preflight", Value: "yes"}}
	id = f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if s = <-got; s.preflight != "yes" {
		t.Errorf("row: preflight = %q", s.preflight)
	}
	gql.Headers = nil
	gql.Settings.OmitHeaders = []string{request.GraphQLPreflightHeader}
	id = f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	if s = <-got; s.preflight != "" {
		t.Errorf("omitted: preflight = %q", s.preflight)
	}
}
