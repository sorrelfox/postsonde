package schemacache

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "schema", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSaveAndLoad(t *testing.T) {
	t.Parallel()
	sch, problems, err := Parse(fixture(t, "github-subset.json"))
	if err != nil || problems != 0 {
		t.Fatalf("Parse = %d problems, %v", problems, err)
	}
	store := Store{Dir: t.TempDir()}
	const endpoint = "https://api.test/graphql"
	if store.Load(endpoint) != nil {
		t.Fatal("Load before Save found a schema")
	}
	at := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	if err := store.Save(&Entry{Endpoint: endpoint, FetchedAt: at, Query: QueryFull, Schema: sch}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(store.Path(endpoint))
	if err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("cache file: %v, %v", fi, err)
	}

	got := store.Load(endpoint)
	if got == nil {
		t.Fatal("Load after Save found nothing")
	}
	if !got.FetchedAt.Equal(at) || got.Query != QueryFull || got.Endpoint != endpoint {
		t.Errorf("entry = %+v", got)
	}
	if TypeCount(got.Schema) != TypeCount(sch) || TypeCount(sch) == 0 || got.Schema.Query() == nil {
		t.Errorf("types = %d, want %d", TypeCount(got.Schema), TypeCount(sch))
	}
	if store.Load("https://other.test/graphql") != nil {
		t.Error("another endpoint shares the cache")
	}
}

func TestLoadIgnoresBadFiles(t *testing.T) {
	t.Parallel()
	store := Store{Dir: t.TempDir()}
	const endpoint = "http://localhost:4000/graphql"
	if err := os.MkdirAll(filepath.Join(store.Dir, DirName), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		"not json",
		`{"version": 99, "endpoint": "http://localhost:4000/graphql", "introspection": {}}`,
		`{"version": 1, "endpoint": "http://elsewhere/graphql", "introspection": {"__schema": {"types": []}}}`,
		`{"version": 1, "endpoint": "http://localhost:4000/graphql", "introspection": {"nope": 1}}`,
	} {
		if err := os.WriteFile(store.Path(endpoint), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if e := store.Load(endpoint); e != nil {
			t.Errorf("Load(%s) = %+v, want nil", content, e)
		}
	}
	if (Store{}).Load(endpoint) != nil || (Store{}).Save(&Entry{}) == nil {
		t.Error("a store without a directory must hold nothing")
	}
}

func TestParseServerErrors(t *testing.T) {
	t.Parallel()
	if _, _, err := Parse(fixture(t, "swapi-full-rejected.json")); err == nil {
		t.Error("Parse of a rejected query succeeded")
	}
}
