package servers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/graphqlmock"
)

// The GraphQL mock server (SSRV-08) through the service: a schema file
// relative to the project, read from the project folder or an allowed
// directory only (PRV-10), the editor's description, and a query. The
// app's GraphQL client introspecting it is in internal/app/services/graphql.

const catalogSDL = `type Query { product(id: ID!): Product products(first: Int): [Product!]! }
type Product { id: ID! name: String! price: Float }
scalar DateTime`

func describeExample(t *testing.T) graphqlmock.Description {
	t.Helper()
	d, err := graphqlmock.Describe(graphqlmock.Config{
		SchemaText: catalogSDL, Introspection: true, ListLength: 2,
		Overrides: []graphqlmock.Override{{Enabled: true, Target: "Product.nope", Value: "1"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGraphQLMockThroughService(t *testing.T) {
	t.Parallel()
	project, allowed, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{project, allowed, outside} {
		if err := os.WriteFile(filepath.Join(dir, "catalog.graphql"), []byte(catalogSDL), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	picked := filepath.Join(project, "schema", "catalog.graphql")
	svc := newService(nil, slog.New(slog.DiscardHandler), core.Options{StopTimeout: time.Second},
		WithProjectRoot(func() string { return project }), WithAllowedDirs(func() []string { return []string{allowed} }),
		WithFilePicker(func(context.Context, FileDialog) (string, error) { return picked, nil }))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })

	if got, err := svc.PickGraphQLSchemaFile(t.Context()); err != nil || got != "schema/catalog.graphql" {
		t.Errorf("PickGraphQLSchemaFile = %q, %v", got, err)
	}

	for _, tc := range []struct {
		name    string
		file    string
		allowed bool
	}{
		{"project-relative", "catalog.graphql", true},
		{"allowed directory", filepath.Join(allowed, "catalog.graphql"), true},
		{"outside", filepath.Join(outside, "catalog.graphql"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := GraphQLMockConfigDTO{SchemaFile: tc.file, Introspection: true, ListLength: 2,
				Overrides: []GraphQLMockOverrideDTO{{Enabled: true, Target: "Query.product", Args: `{"id": "1"}`, Value: `{"name": "Teapot"}`}}}
			d, derr := svc.DescribeGraphQLMock(t.Context(), cfg)
			created, err := svc.Create(t.Context(), CreateInputDTO{Type: graphqlmock.TypeName, Config: ConfigDTO{GraphQLMock: &cfg}})
			if err != nil {
				t.Fatal(err)
			}
			running, err := svc.Start(t.Context(), created.ID)
			if !tc.allowed {
				for _, err := range []error{err, derr} {
					if e, ok := err.(*errs.Error); !ok || e.Code != fileaccess.CodeNotAllowed {
						t.Errorf("start or describe = %v", err)
					}
				}
				return
			}
			if err != nil || derr != nil {
				t.Fatalf("start = %v, describe = %v", err, derr)
			}
			if d.Format != "sdl" || d.Source != "file" || d.QueryFields != 2 || len(d.Targets) != 6 || d.Targets[0].Target != "Query.product" ||
				d.Targets[0].Args[0] != "id" || !d.Targets[0].Root || d.Targets[5].Target != "DateTime" {
				t.Errorf("description = %+v", d)
			}
			if running.Config.GraphQLMock == nil || running.Config.GraphQLMock.Path != "/graphql" {
				t.Errorf("config = %+v", running.Config)
			}
			resp, err := http.Post("http://127.0.0.1:"+strconv.Itoa(running.BoundPort)+"/graphql", "application/json",
				strings.NewReader(`{"query": "{ product(id: 1) { __typename name } }"}`))
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != `{"data":{"product":{"__typename":"Product","name":"Teapot"}}}` {
				t.Errorf("response = %s", body)
			}
			if _, err := svc.UpdateConfig(t.Context(), created.ID, ConfigDTO{}); err == nil {
				t.Error("UpdateConfig without the GraphQL settings succeeded")
			}
			_, _ = svc.Stop(t.Context(), created.ID)
		})
	}
}
