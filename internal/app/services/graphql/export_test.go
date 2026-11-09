package graphql

import (
	"context"
	"errors"
	nethttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// TestSaveSchemaSDLLoadsBack checks GQL-20: a schema fetched by
// introspection is saved as SDL through the save dialog, and the file,
// used as a request's schema file (GQL-11), gives the same schema.
func TestSaveSchemaSDLLoadsBack(t *testing.T) {
	t.Parallel()
	github := readFixture(t, "github-subset.json")
	srv := newServer(t, func(string) (int, []byte) { return nethttp.StatusOK, github })
	f := newFixture(t, srv.URL, t.TempDir())
	_, items := f.fetch(t)
	if len(items) != 1 {
		t.Fatalf("fetch items = %+v", items)
	}
	fetched := items[0]

	root := t.TempDir()
	out := filepath.Join(root, "schemas", "api.graphql")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	var dialog SaveDialog
	f.svc.pickSave = func(_ context.Context, d SaveDialog) (string, error) {
		dialog = d
		return out, nil
	}
	res, err := f.svc.SaveSchemaSDL(t.Context(), SaveSchemaInputDTO{Key: fetched.Key, Name: "Users API"})
	if err != nil {
		t.Fatalf("SaveSchemaSDL = %v", err)
	}
	// The server lists the built-in scalars too; SDL leaves them out.
	if res.Path != out || res.TypeCount == 0 || res.TypeCount > fetched.TypeCount {
		t.Errorf("result = %+v, want path %s and up to %d types", res, out, fetched.TypeCount)
	}
	if dialog.Filename != "Users API.graphql" || dialog.ButtonText == "" || dialog.Title == "" {
		t.Errorf("dialog = %+v", dialog)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	sdl := string(data)
	if !strings.Contains(sdl, "type Repository implements") || !strings.HasSuffix(sdl, "\n") || strings.HasSuffix(sdl, "\n\n") {
		t.Errorf("SDL:\n%s", sdl)
	}

	// The file as a request's schema file: no problems, the same schema.
	ff := newFileFixture(t, root)
	ff.stored = "schemas/api.graphql"
	info := ff.info(t)
	if info.Error != nil || info.Problems != 0 || !info.Cached || info.TypeCount != res.TypeCount {
		t.Fatalf("file info = %+v (saved %d types)", info, res.TypeCount)
	}
	loaded := ff.svc.cached(info.Key)
	if loaded == nil {
		t.Fatal("the file gives no schema")
	}
	if got, want := schema.SDL(loaded.Schema), schema.SDL(f.svc.cached(fetched.Key).Schema); got != want {
		t.Errorf("the loaded schema differs:\n%s\n---\n%s", got, want)
	}
	if ov, err := ff.svc.DocsOverview(t.Context(), DocsInputDTO{Key: info.Key}); err != nil || !ov.Schema || len(ov.Roots) != 3 {
		t.Errorf("docs of the saved file = %+v, %v", ov, err)
	}

	// A schema file exports too, under its own name by default.
	var again SaveDialog
	ff.svc.pickSave = func(_ context.Context, d SaveDialog) (string, error) {
		again = d
		return "", nil
	}
	if res, err := ff.svc.SaveSchemaSDL(t.Context(), SaveSchemaInputDTO{Key: info.Key}); err != nil || res.Path != "" {
		t.Errorf("cancelled save = %+v, %v", res, err)
	}
	if again.Filename != "api.graphql" {
		t.Errorf("suggested %q for a schema file", again.Filename)
	}
}

func TestSaveSchemaSDLFailures(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "http://127.0.0.1:1", t.TempDir())
	if _, err := f.svc.SaveSchemaSDL(t.Context(), SaveSchemaInputDTO{Key: "http://127.0.0.1:1/graphql"}); !hasCode(err, CodeUnavailable) {
		t.Errorf("without a dialog = %v", err)
	}
	called := false
	f.svc.pickSave = func(context.Context, SaveDialog) (string, error) {
		called = true
		return "", nil
	}
	if _, err := f.svc.SaveSchemaSDL(t.Context(), SaveSchemaInputDTO{Key: "http://127.0.0.1:1/graphql"}); !hasCode(err, CodeNoSchema) {
		t.Errorf("without a schema = %v", err)
	}
	if called {
		t.Error("the dialog showed without a schema")
	}

	github := readFixture(t, "github-subset.json")
	srv := newServer(t, func(string) (int, []byte) { return nethttp.StatusOK, github })
	g := newFixture(t, srv.URL, t.TempDir())
	_, items := g.fetch(t)
	missing := filepath.Join(t.TempDir(), "no-such-dir", "s.graphql")
	g.svc.pickSave = func(context.Context, SaveDialog) (string, error) { return missing, nil }
	if _, err := g.svc.SaveSchemaSDL(t.Context(), SaveSchemaInputDTO{Key: items[0].Key}); !hasCode(err, CodeExportFailed) {
		t.Errorf("unwritable path = %v", err)
	}
}

func TestSchemaFileName(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, key, want string }{
		{"Users API", "https://api.example.com/graphql", "Users API.graphql"},
		{"", "https://api.example.com:8443/graphql", "api.example.com.graphql"},
		{"  a/b:c  ", "", "a-b-c.graphql"},
		{"", "{{baseUrl}}/graphql", "schema.graphql"},
		{"...", "", "schema.graphql"},
		{"", fileKeyPrefix + filepath.Join("x", "catalogue.graphqls"), "catalogue.graphql"},
	}
	for _, tt := range tests {
		if got := SchemaFileName(tt.name, tt.key); got != tt.want {
			t.Errorf("SchemaFileName(%q, %q) = %q, want %q", tt.name, tt.key, got, tt.want)
		}
	}
}

func hasCode(err error, code string) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Code == code
}
