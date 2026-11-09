// Package schemacache keeps GraphQL schemas fetched by introspection on
// disk, one file per endpoint (GQL-21), so the editor's validation and
// autocomplete work offline and after a restart. It never fetches:
// introspection runs only when the user asks (GQL-10).
//
// Files live in a directory of the workspace's local side-file directory
// (DirName), named after a hash of the endpoint, and hold the schema in
// the introspection shape ({"__schema": …}, schema.MarshalIntrospection).
// A file that cannot be read or parsed counts as missing: it is a cache.
// See docs/graphql.md.
package schemacache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/storage/atomicfile"
)

// DirName is the cache directory inside a workspace's local directory.
const DirName = "graphql-schemas"

// CodeSaveFailed: the schema could not be written (kind io).
const CodeSaveFailed = "graphql.schema_cache.save_failed"

// version is the version of a cache file.
const version = 1

// Query names which introspection query a schema was fetched with.
type Query string

// Introspection queries.
const (
	QueryFull         Query = "full"
	QueryConservative Query = "conservative"
)

// Entry is a cached schema.
type Entry struct {
	// Endpoint is the URL the schema was fetched from.
	Endpoint string
	// FetchedAt is when it was fetched.
	FetchedAt time.Time
	// Query is the introspection query that returned it.
	Query Query
	// Schema is the parsed schema, never nil.
	Schema *schema.Schema
	// Problems is the number of broken references found when it was
	// parsed (schema.Problems); the schema is usable anyway.
	Problems int
}

// file is a cache file.
type file struct {
	Version       int             `json:"version"`
	Endpoint      string          `json:"endpoint"`
	FetchedAt     time.Time       `json:"fetchedAt"`
	Query         Query           `json:"query"`
	Introspection json.RawMessage `json:"introspection"`
}

// Store is the cache in one local directory.
type Store struct {
	// Dir is the workspace's local directory; the files are in
	// Dir/DirName.
	Dir string
}

// Path returns the cache file of endpoint.
func (s Store) Path(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return filepath.Join(s.Dir, DirName, hex.EncodeToString(sum[:16])+".json")
}

// Load returns the cached schema of endpoint, or nil when there is none or
// its file cannot be read, is of another endpoint or version, or does not
// parse.
func (s Store) Load(endpoint string) *Entry {
	if s.Dir == "" {
		return nil
	}
	data, err := os.ReadFile(s.Path(endpoint))
	if err != nil {
		return nil
	}
	var f file
	if json.Unmarshal(data, &f) != nil || f.Version != version || f.Endpoint != endpoint {
		return nil
	}
	sch, problems, err := Parse(f.Introspection)
	if err != nil {
		return nil
	}
	return &Entry{Endpoint: endpoint, FetchedAt: f.FetchedAt, Query: f.Query, Schema: sch, Problems: problems}
}

// Save writes e, replacing the cached schema of e.Endpoint.
func (s Store) Save(e *Entry) error {
	if s.Dir == "" || e == nil || e.Schema == nil {
		return errs.New(errs.InvalidArgument, "There is no schema to save.").WithCode(CodeSaveFailed)
	}
	intro, err := e.Schema.MarshalIntrospection()
	if err != nil {
		return err
	}
	data, err := json.Marshal(file{
		Version:       version,
		Endpoint:      e.Endpoint,
		FetchedAt:     e.FetchedAt.UTC(),
		Query:         e.Query,
		Introspection: intro,
	})
	if err != nil {
		return errs.Wrap(err, errs.Internal, "The schema could not be encoded.").WithCode(CodeSaveFailed)
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, DirName), 0o700); err != nil {
		return errs.Wrap(err, errs.IO, "The schema cache folder could not be created.").WithCode(CodeSaveFailed)
	}
	if err := atomicfile.WriteFile(s.Path(e.Endpoint), data, 0o600); err != nil {
		return errs.Wrap(err, errs.IO, "The schema could not be saved.").WithCode(CodeSaveFailed)
	}
	return nil
}

// Parse converts an introspection result to a schema, accepting one with
// broken references (schema.CodeInvalidSchema) and returning their count.
func Parse(data []byte) (*schema.Schema, int, error) {
	sch, err := schema.ParseIntrospection(data)
	if err == nil {
		return sch, 0, nil
	}
	var problems schema.Problems
	if sch != nil && errors.As(err, &problems) {
		return sch, len(problems), nil
	}
	return nil, 0, err
}

// TypeCount is the number of named types a schema declares, not counting
// the introspection types (__Schema, __Type, …).
func TypeCount(s *schema.Schema) int {
	n := 0
	for _, t := range s.Types {
		if len(t.Name) < 2 || t.Name[:2] != "__" {
			n++
		}
	}
	return n
}
