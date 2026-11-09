package graphql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// fileFixture is a service whose one request, "gql", has the schema file
// stored in *stored, resolved against root.
type fileFixture struct {
	svc    *GraphQLService
	root   string
	stored string
	picked string
}

func newFileFixture(t *testing.T, root string) *fileFixture {
	t.Helper()
	f := &fileFixture{root: root}
	f.svc = New(
		func(_ context.Context, id string) (string, error) {
			if id != "gql" {
				return "", errs.New(errs.NotFound, "no such request")
			}
			return "https://api.test/graphql", nil
		},
		nil,
		func() (string, error) { return t.TempDir(), nil },
		nil, nil,
		WithSchemaFiles(
			func(context.Context, string) (string, error) { return f.stored, nil },
			func() string { return f.root },
			func(_ context.Context, d FileDialog) (string, error) {
				if d.Directory != f.root || !strings.Contains(d.FilterPattern, "*.graphqls") {
					t.Errorf("dialog = %+v", d)
				}
				return f.picked, nil
			}))
	return f
}

func (f *fileFixture) info(t *testing.T) SchemaInfoDTO {
	t.Helper()
	info, err := f.svc.SchemaInfo(t.Context(), SchemaInputDTO{RequestID: "gql"})
	if err != nil {
		t.Fatalf("SchemaInfo = %v", err)
	}
	return info
}

// writeFile writes src to path with a modification time mod seconds from
// now, so a rewrite of the same size is still seen as a change.
func writeFile(t *testing.T, path, src string, mod int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Duration(mod) * time.Second)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

const petsSDL = `"Pets and their owners."
type Query {
  "A pet by ID."
  pet(id: ID!): Pet
  pets(kind: Kind = DOG): [Pet!]!
}
type Pet { id: ID! name: String! kind: Kind owner: Owner }
type Owner { name: String @deprecated(reason: "Use fullName.") fullName: String }
enum Kind { DOG CAT }
`

func TestSchemaFileServesTheEditor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "schemas", "pets.graphql")
	writeFile(t, path, petsSDL, -60)
	f := newFileFixture(t, root)
	f.stored = "schemas/pets.graphql"

	info := f.info(t)
	if info.Source != SourceFile || info.File != "schemas/pets.graphql" || !info.Cached || info.Key != fileKey(path) ||
		info.TypeCount != 4 || info.Error != nil || info.Problems != 0 || info.ModifiedAt == "" || info.FetchedAt != info.ModifiedAt ||
		info.Endpoint != "https://api.test/graphql" {
		t.Fatalf("SchemaInfo = %+v", info)
	}

	// Diagnostics, completion and the docs explorer use the file's schema.
	diag, err := f.svc.Diagnostics(t.Context(), DiagnosticsInputDTO{Key: info.Key, Text: "{ pet(id: 1) { name nope } }"})
	if err != nil || !diag.Schema || len(diag.Diagnostics) != 1 || !strings.Contains(diag.Diagnostics[0].Message, `"nope"`) {
		t.Fatalf("Diagnostics = %+v, %v", diag, err)
	}
	comp, err := f.svc.Complete(t.Context(), CompleteInputDTO{Key: info.Key, Text: "{ pet(id: 1) { ", Offset: 15})
	if err != nil || !comp.Schema || !hasLabel(comp.Items, "owner") || !hasLabel(comp.Items, "kind") {
		t.Fatalf("Complete = %+v, %v", comp, err)
	}
	over, err := f.svc.DocsOverview(t.Context(), DocsInputDTO{Key: info.Key})
	if err != nil || !over.Schema || over.Description != "" || len(over.Types) < 4 {
		t.Fatalf("DocsOverview = %+v, %v", over, err)
	}
	owner, err := f.svc.DocsType(t.Context(), DocsTypeInputDTO{Key: info.Key, Name: "Owner"})
	if err != nil || !owner.Found || len(owner.Fields) != 2 || !owner.Fields[0].Deprecated {
		t.Fatalf("DocsType(Owner) = %+v, %v", owner, err)
	}

	// A change on disk is picked up without Refresh.
	writeFile(t, path, petsSDL+"type Vet { name: String }\nextend type Query { vets: [Vet] }\n", -30)
	if info := f.info(t); info.TypeCount != 5 {
		t.Fatalf("after a change: %+v", info)
	}
	comp, _ = f.svc.Complete(t.Context(), CompleteInputDTO{Key: info.Key, Text: "{ ", Offset: 2})
	if !hasLabel(comp.Items, "vets") {
		t.Fatalf("the changed schema is not used by Complete: %+v", comp.Items)
	}

	// Refresh reads the file even when it looks unchanged.
	f.svc.mu.Lock()
	before := f.svc.files[path]
	f.svc.mu.Unlock()
	re, err := f.svc.ReloadSchemaFile(t.Context(), SchemaInputDTO{RequestID: "gql"})
	f.svc.mu.Lock()
	after := f.svc.files[path]
	f.svc.mu.Unlock()
	if err != nil || re.TypeCount != 5 || before == after {
		t.Fatalf("ReloadSchemaFile = %+v, %v (re-read: %v)", re, err, before != after)
	}
}

func hasLabel(items []CompletionItemDTO, label string) bool {
	for _, it := range items {
		if it.Label == label {
			return true
		}
	}
	return false
}

func TestSchemaFileProblems(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "schema.graphqls")
	f := newFileFixture(t, root)
	f.stored = "schema.graphqls"

	// Type system errors: the schema is usable, the problems are listed.
	writeFile(t, path, "type Query { a: Missing b: Int }\nunion U = Query | Int\n", -60)
	info := f.info(t)
	if !info.Cached || info.Error != nil || info.Problems != 2 || len(info.ProblemList) != 2 ||
		info.ProblemList[0] != (SchemaProblemDTO{Message: `Unknown type "Missing".`, Line: 1, Column: 17}) {
		t.Fatalf("SchemaInfo with type errors = %+v", info)
	}

	// Syntax errors: no schema, the error and the problems with positions.
	writeFile(t, path, "type Query {\n  a: Int\n  b(\n}\n", -30)
	info = f.info(t)
	if info.Cached || info.Key != "" || info.Error == nil || info.Error.Code != schema.CodeSDLSyntax ||
		len(info.ProblemList) == 0 || info.ProblemList[0].Line != 3 {
		t.Fatalf("SchemaInfo with syntax errors = %+v", info)
	}
	if e := f.svc.cached(fileKey(path)); e != nil {
		t.Fatal("a schema for a file with syntax errors")
	}

	// A missing file.
	f.stored = "gone.graphql"
	info = f.info(t)
	if info.Cached || info.Error == nil || info.Error.Code != CodeSchemaFileMissing || info.Error.Hint == "" || info.File != "gone.graphql" {
		t.Fatalf("SchemaInfo of a missing file = %+v", info)
	}

	// A relative path without a project.
	f.root = ""
	info = f.info(t)
	if info.Error == nil || info.Error.Code != CodeSchemaFileNoProject {
		t.Fatalf("SchemaInfo without a project = %+v", info)
	}

	// Without a schema file the request uses introspection.
	f.stored = ""
	info = f.info(t)
	if info.Source != SourceIntrospection || info.Cached || info.Error != nil || info.File != "" {
		t.Fatalf("SchemaInfo without a file = %+v", info)
	}
}

func TestPickSchemaFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := newFileFixture(t, root)

	f.picked = filepath.Join(root, "api", "schema.graphql")
	if got, err := f.svc.PickSchemaFile(t.Context()); err != nil || got != "api/schema.graphql" {
		t.Errorf("PickSchemaFile inside the project = %q, %v", got, err)
	}
	outside := filepath.Join(t.TempDir(), "schema.graphql")
	f.picked = outside
	if got, err := f.svc.PickSchemaFile(t.Context()); err != nil || got != outside {
		t.Errorf("PickSchemaFile outside the project = %q, %v", got, err)
	}
	f.picked = ""
	if got, err := f.svc.PickSchemaFile(t.Context()); err != nil || got != "" {
		t.Errorf("PickSchemaFile cancelled = %q, %v", got, err)
	}

	bare := New(nil, nil, nil, nil, nil)
	var e *errs.Error
	if _, err := bare.PickSchemaFile(t.Context()); !errors.As(err, &e) || e.Kind != errs.Unsupported {
		t.Errorf("PickSchemaFile without a picker = %v", err)
	}
}
