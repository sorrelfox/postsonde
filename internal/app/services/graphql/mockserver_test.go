package graphql

import (
	"context"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/graphqlmock"
)

// The app's GraphQL client fetches the schema of PostSonde's own GraphQL
// mock server (SSRV-08) by introspection, as the editor's Fetch schema
// button does, and gets the schema the mock serves; diagnostics then work
// against it.
func TestFetchSchemaFromGraphQLMock(t *testing.T) {
	t.Parallel()
	m := servers.NewManager(servers.Options{
		Types:       []servers.Type{graphqlmock.New(graphqlmock.Options{})},
		StopTimeout: 2 * time.Second,
		Logger:      slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { m.StopAll(context.Background()) })
	s, err := m.Add(servers.Spec{Type: graphqlmock.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if s, err = m.Start(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, "http://127.0.0.1:"+strconv.Itoa(s.Port()), t.TempDir())
	done, items := f.fetch(t)
	if done.Status != operation.StatusOK || len(items) != 1 {
		t.Fatalf("fetch done = %+v, items %+v", done, items)
	}
	if info := items[0]; !info.Cached || info.Query != "full" || info.Problems != 0 {
		t.Errorf("fetched = %+v", info)
	}
	got := f.svc.schemaFor(items[0].Key)
	want, err := schema.ParseSDL(graphqlmock.ExampleSchema)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || schema.SDL(got) != schema.SDL(want) {
		t.Fatalf("fetched schema:\n%v", got)
	}
}
