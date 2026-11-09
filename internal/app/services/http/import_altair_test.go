package http

import (
	"encoding/json"
	"fmt"
	"io/fs"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// TestImportAltairAndSend (IMP-08) imports an Altair collection export
// through the default formats into a project and sends two of its
// requests to a GraphQL test server as the app does: the query,
// variables, extensions, headers and Basic auth (its password from the
// local side file) arrive as Altair would have sent them, and a GET
// request puts the query in the URL.
func TestImportAltairAndSend(t *testing.T) {
	t.Parallel()
	type seen struct {
		method, user, pass, requestID, trace string
		query                                string
		body                                 map[string]any
	}
	got := make(chan seen, 1)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		s := seen{method: r.Method, requestID: r.Header.Get("X-Request-Id"), trace: r.Header.Get("X-Trace"), query: r.URL.Query().Get("query")}
		s.user, s.pass, _ = r.BasicAuth()
		if r.Method == nethttp.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&s.body); err != nil {
				t.Errorf("body: %v", err)
			}
		}
		got <- s
		w.Header().Set("Content-Type", "application/graphql-response+json")
		fmt.Fprint(w, `{"data":{"ok":true}}`)
	}))
	defer srv.Close()

	f := newProjectFixture(t)
	ctx := t.Context()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "core", "importer", "altair", "testdata", "shop-v8.agc"))
	if err != nil {
		t.Fatal(err)
	}
	pre, err := f.coll.PreviewImport(ctx, collection.PreviewImportInputDTO{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if pre.Format.ID != "altair" {
		t.Fatalf("format = %+v", pre.Format)
	}
	res, err := f.coll.Import(ctx, collection.ImportInputDTO{Path: path, Digest: pre.Digest, Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Report.Counts; c.Folders != 4 || c.Requests != 6 || c.Skipped != 1 || c.Failed != 0 {
		t.Fatalf("counts = %+v", c)
	}
	ids := map[string]string{}
	for _, it := range res.Report.Items {
		if it.Kind == "request" {
			if it.FileKind != "graphql" || it.Status != "imported" {
				t.Errorf("%s = %+v", it.Name, it)
			}
			ids[it.Name] = it.NodeID
		}
	}

	if _, err := collection.SaveGlobalVariables(f.coll, ctx, []projectfile.Variable{
		{Name: "baseUrl", Value: srv.URL}, {Name: "user", Value: "alice"},
		{Name: "requestId", Value: "r-42"}, {Name: "sku", Value: "SKU-1"},
	}); err != nil {
		t.Fatal(err)
	}

	f.sendBody(t, ids["Create order"])
	s := <-got
	if s.method != "POST" || s.user != "alice" || s.pass != "hunter2" || s.requestID != "r-42" || s.trace != "" {
		t.Errorf("create order: %+v", s)
	}
	want := map[string]any{
		"query":         "mutation CreateOrder($input: OrderInput!) {\n  createOrder(input: $input) {\n    id\n    total\n  }\n}\n",
		"variables":     map[string]any{"input": map[string]any{"sku": "SKU-1", "quantity": float64(2)}},
		"operationName": "CreateOrder",
		"extensions":    map[string]any{"persistedQuery": map[string]any{"version": float64(1), "sha256Hash": "abc123"}},
	}
	if !reflect.DeepEqual(s.body, want) {
		t.Errorf("create order body = %v, want %v", s.body, want)
	}

	f.sendBody(t, ids["ReturnReasons"])
	s = <-got
	if s.method != "GET" || !strings.HasPrefix(s.query, "query ReturnReasons") || s.user != "" {
		t.Errorf("return reasons: %+v", s)
	}

	// The Basic password and the API key are in the local side files only.
	err = filepath.WalkDir(f.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(p, f.ws.LocalDir) {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, secret := range []string{"hunter2", "sk-live-order-feed", "oauth-token-should-not-appear"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s holds the secret %s", p, secret)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
