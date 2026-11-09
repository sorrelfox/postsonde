package graphql

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schemacache"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// recorder collects the events of one registry.
type recorder struct {
	mu    sync.Mutex
	items map[string][]SchemaInfoDTO
	done  map[string]chan operation.DoneEventDTO
}

func (r *recorder) doneChan(id string) chan operation.DoneEventDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.done[id]
	if !ok {
		ch = make(chan operation.DoneEventDTO, 1)
		r.done[id] = ch
	}
	return ch
}

func (r *recorder) emit(name string, data any) {
	switch d := data.(type) {
	case operation.DataEventDTO[SchemaInfoDTO]:
		if name != EventData {
			panic("data event named " + name)
		}
		r.mu.Lock()
		r.items[d.OpID] = append(r.items[d.OpID], d.Items...)
		r.mu.Unlock()
	case operation.DoneEventDTO:
		r.doneChan(d.OpID) <- d
	}
}

// fixture is a GraphQL service over a real HTTP service whose one request,
// "gql", points at url, caching in dir.
type fixture struct {
	svc *GraphQLService
	rec *recorder
}

func newFixture(t *testing.T, url, dir string) *fixture {
	t.Helper()
	rec := &recorder{items: map[string][]SchemaInfoDTO{}, done: map[string]chan operation.DoneEventDTO{}}
	ops := operation.NewRegistry(rec.emit, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ops.Shutdown(ctx)
	})
	load := func(_ context.Context, id string) (collection.HTTPSource, error) {
		if id != "gql" {
			return collection.HTTPSource{}, errs.New(errs.NotFound, "no such request").WithCode("collection.not_found")
		}
		return collection.HTTPSource{
			GraphQL: &projectfile.GraphQLRequest{
				ID:      "gql",
				URL:     "{{base}}/graphql",
				Method:  "GET", // introspection is POSTed anyway
				Query:   "{ a }",
				Headers: []projectfile.KeyValue{{Name: "X-Token", Value: "t"}},
			},
			Auth:            projectfile.Auth{Type: projectfile.AuthNone},
			FolderVariables: [][]projectfile.Variable{{{Name: "base", Value: url}}},
		}, nil
	}
	// The app's dialer: connections without a user operation are refused.
	dialer := netx.Chain(netx.NewDirect(netx.Options{}), netx.Guarded(netx.RequireUserOp(nil)))
	httpSvc := httpservice.New(load, nil, ops, dialer, nil, nil)
	t.Cleanup(func() { _ = httpSvc.ServiceShutdown() })
	svc := New(
		func(ctx context.Context, id string) (string, error) {
			return httpservice.GraphQLEndpoint(ctx, httpSvc, id)
		},
		func(ctx context.Context, id string) (string, Poster, error) {
			p, err := httpservice.NewGraphQLPoster(ctx, httpSvc, id)
			if err != nil {
				return "", nil, err
			}
			return p.Endpoint, p, nil
		},
		func() (string, error) { return dir, nil },
		ops, nil)
	return &fixture{svc: svc, rec: rec}
}

// fetch runs FetchSchema to the end.
func (f *fixture) fetch(t *testing.T) (operation.DoneEventDTO, []SchemaInfoDTO) {
	t.Helper()
	started, err := f.svc.FetchSchema(t.Context(), SchemaInputDTO{RequestID: "gql"})
	if err != nil {
		t.Fatalf("FetchSchema = %v", err)
	}
	select {
	case done := <-f.rec.doneChan(started.OpID):
		f.rec.mu.Lock()
		defer f.rec.mu.Unlock()
		return done, f.rec.items[started.OpID]
	case <-time.After(10 * time.Second):
		t.Fatal("the fetch did not finish")
		return operation.DoneEventDTO{}, nil
	}
}

func (f *fixture) info(t *testing.T) SchemaInfoDTO {
	t.Helper()
	info, err := f.svc.SchemaInfo(t.Context(), SchemaInputDTO{RequestID: "gql"})
	if err != nil {
		t.Fatalf("SchemaInfo = %v", err)
	}
	return info
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "core", "graphql", "schema", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// server answers introspection queries with body(query), counting them.
type server struct {
	*httptest.Server
	posts atomic.Int32
}

func newServer(t *testing.T, body func(query string) (int, []byte)) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		s.posts.Add(1)
		var req struct {
			Query         string `json:"query"`
			OperationName string `json:"operationName"`
		}
		if r.Method != nethttp.MethodPost || r.Header.Get("X-Token") != "t" || json.NewDecoder(r.Body).Decode(&req) != nil ||
			req.OperationName != schema.IntrospectionOperationName {
			w.WriteHeader(nethttp.StatusBadRequest)
			return
		}
		status, data := body(req.Query)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestFetchCacheAndReuseOffline(t *testing.T) {
	t.Parallel()
	github := readFixture(t, "github-subset.json")
	var current atomic.Pointer[[]byte]
	current.Store(&github)
	srv := newServer(t, func(string) (int, []byte) { return nethttp.StatusOK, *current.Load() })
	dir := t.TempDir()

	f := newFixture(t, srv.URL, dir)
	if info := f.info(t); info.Cached || info.Key != "" || info.Endpoint != srv.URL+"/graphql" {
		t.Fatalf("SchemaInfo before a fetch = %+v", info)
	}
	if srv.posts.Load() != 0 {
		t.Fatal("SchemaInfo sent a request")
	}

	done, items := f.fetch(t)
	if done.Status != operation.StatusOK || len(items) != 1 {
		t.Fatalf("fetch done = %+v, items %+v", done, items)
	}
	fetched := items[0]
	if !fetched.Cached || fetched.Key != srv.URL+"/graphql" || fetched.TypeCount == 0 || fetched.Query != "full" || fetched.FetchedAt == "" ||
		fetched.Change != ChangeNew {
		t.Errorf("fetched = %+v", fetched)
	}
	// Only a fetch's result says how the schema changed.
	fetched.Change = ""
	if info := f.info(t); !reflect.DeepEqual(info, fetched) {
		t.Errorf("SchemaInfo after the fetch = %+v, want %+v", info, fetched)
	}

	// A restart with the network off: a fresh service, the server gone.
	srv.Close()
	offline := newFixture(t, srv.URL, dir)
	if info := offline.info(t); !reflect.DeepEqual(info, fetched) {
		t.Errorf("SchemaInfo after a restart = %+v, want %+v", info, fetched)
	}
	if e := offline.svc.cached(fetched.Endpoint); e == nil || e.Schema.Type("Repository") == nil {
		t.Error("the cached schema is not usable offline")
	}
}

func TestRefreshReplacesTheCache(t *testing.T) {
	t.Parallel()
	github := readFixture(t, "github-subset.json")
	countries := readFixture(t, "countries-full.json")
	var current atomic.Pointer[[]byte]
	current.Store(&github)
	srv := newServer(t, func(string) (int, []byte) { return nethttp.StatusOK, *current.Load() })
	dir := t.TempDir()
	f := newFixture(t, srv.URL, dir)

	_, first := f.fetch(t)
	f.svc.now = func() time.Time { return time.Now().Add(time.Hour) }
	current.Store(&countries)
	done, second := f.fetch(t)
	if done.Status != operation.StatusOK || len(second) != 1 {
		t.Fatalf("refresh = %+v", done)
	}
	if second[0].TypeCount == first[0].TypeCount || second[0].FetchedAt == first[0].FetchedAt {
		t.Errorf("refresh kept the old schema: %+v then %+v", first[0], second[0])
	}
	// GQL-19: the result says whether the schema changed.
	if first[0].Change != ChangeNew || second[0].Change != ChangeChanged {
		t.Errorf("changes = %q then %q", first[0].Change, second[0].Change)
	}
	second[0].Change = ""
	if info := newFixture(t, srv.URL, dir).info(t); !reflect.DeepEqual(info, second[0]) {
		t.Errorf("cache after refresh = %+v, want %+v", info, second[0])
	}
	if _, third := f.fetch(t); len(third) != 1 || third[0].Change != ChangeUnchanged {
		t.Errorf("a refresh with the same schema = %+v", third)
	}
}

func TestFetchFallsBackToTheConservativeQuery(t *testing.T) {
	t.Parallel()
	rejected := readFixture(t, "swapi-full-rejected.json")
	swapi := readFixture(t, "swapi-conservative.json")
	srv := newServer(t, func(query string) (int, []byte) {
		if query == schema.FullIntrospectionQuery {
			return nethttp.StatusBadRequest, rejected
		}
		return nethttp.StatusOK, swapi
	})
	f := newFixture(t, srv.URL, t.TempDir())
	done, items := f.fetch(t)
	if done.Status != operation.StatusOK || len(items) != 1 || items[0].Query != "conservative" || srv.posts.Load() != 2 {
		t.Fatalf("fetch = %+v, %+v after %d posts", done, items, srv.posts.Load())
	}
}

func TestFetchFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"http status", nethttp.StatusNotFound, "<html>not here</html>", CodeHTTPStatus},
		{"not a schema", nethttp.StatusOK, `{"data": {"user": null}}`, schema.CodeNoSchema},
		{"not json", nethttp.StatusOK, "hello", schema.CodeMalformedJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newServer(t, func(string) (int, []byte) { return tt.status, []byte(tt.body) })
			dir := t.TempDir()
			f := newFixture(t, srv.URL, dir)
			done, items := f.fetch(t)
			if done.Status != operation.StatusError || done.Error == nil || done.Error.Code != tt.code || len(items) != 0 {
				t.Fatalf("done = %+v (error %+v), items %+v, want %s", done, done.Error, items, tt.code)
			}
			if _, err := os.Stat(filepath.Join(dir, schemacache.DirName)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a failed fetch wrote the cache: %v", err)
			}
		})
	}
}

func TestFetchNeedsABuildableRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "http://127.0.0.1:1", t.TempDir())
	_, err := f.svc.FetchSchema(t.Context(), SchemaInputDTO{RequestID: "missing"})
	if !errors.Is(err, &errs.Error{Kind: errs.NotFound}) {
		t.Errorf("FetchSchema(unknown id) = %v", err)
	}
	_, err = New(nil, nil, nil, nil, nil).FetchSchema(t.Context(), SchemaInputDTO{RequestID: "gql"})
	if !errors.Is(err, &errs.Error{Kind: errs.Unsupported, Code: CodeUnavailable}) {
		t.Errorf("FetchSchema without dependencies = %v", err)
	}
}
