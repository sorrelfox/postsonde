package http

import (
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	coreworkspace "github.com/sorrelfox/postsonde/internal/core/workspace"
)

// TestSendChecksAssertions sends a request with assertions to a test
// server (TEST-01, TEST-06): the enabled ones are evaluated in order
// against the whole body, with their {{variables}} rendered; a failing
// or invalid assertion does not fail the send; disabled ones are skipped
// but counted in Index; secrets are masked in the results.
func TestSendChecksAssertions(t *testing.T) {
	t.Parallel()
	// A body over MaxBodyText: the DTO clips it, the assertions see it all.
	big := strings.Repeat("x", MaxBodyText+10)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=abc; Path=/")
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":"u7","token":"s3cr3t-token","pad":"%s","last":true}`, big)
	}))
	defer srv.Close()

	src := httpSource(srv.URL)
	src.FolderVariables = [][]projectfile.Variable{{
		{Name: "userId", Value: "u7"},
		{Name: "token", Value: "s3cr3t-token", Secret: true},
	}}
	src.Request.Assertions = []projectfile.Assertion{
		{Target: "status", Op: "equals", Expected: "201"},
		{Target: "status", Op: "equals", Expected: "999", Disabled: true},
		{Target: "jsonpath", Expr: "$.id", Op: "equals", Expected: "{{userId}}"},
		{Target: "jsonpath", Expr: "$.last", Op: "equals", Expected: "true"},
		{Target: "cookie", Expr: "session", Op: "equals", Expected: "abc"},
		{Target: "header", Expr: "Content-Type", Op: "contains", Expected: "xml"},
		{Target: "jsonpath", Expr: "$.token", Op: "not_equals", Expected: "{{token}}"},
		{Target: "body", Op: "contains", Expected: "{{undefinedVar}}"},
		{Target: "jsonpath", Expr: "$[", Op: "exists"},
		{Target: "header", Expr: "X-Missing", Op: "not_exists", Expected: "{{alsoUndefined}}"},
	}
	f := newFixture(t, src, nil)

	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v: failed assertions must not fail the send", done)
	}
	resp := f.rec.response(t, id)
	if !resp.BodyClipped {
		t.Fatalf("body not clipped; the test needs a body over MaxBodyText")
	}
	type want struct {
		index            int
		outcome, message string
		code             string
	}
	wants := []want{
		{0, "passed", "Status code equals 201: it was 201.", ""},
		{2, "passed", `JSONPath $.id equals "u7": it was "u7".`, ""},
		{3, "passed", "JSONPath $.last equals true: it was true.", ""},
		{4, "passed", `Cookie session equals "abc": it was "abc".`, ""},
		{5, "failed", `Expected header Content-Type to contain "xml", but it was "application/json".`, ""},
		{6, "failed", `Expected JSONPath $.token to not equal "••••••", but it was "••••••".`, ""},
		{7, "invalid", "This assertion cannot be checked. The variable {{undefinedVar}} is not defined.", "template.unresolved"},
		{8, "invalid", "", "jsonpath.invalid_query"},
		{9, "passed", "Header X-Missing does not exist.", ""},
	}
	if len(resp.Tests) != len(wants) {
		t.Fatalf("tests = %d results, want %d: %+v", len(resp.Tests), len(wants), resp.Tests)
	}
	for i, w := range wants {
		got := resp.Tests[i]
		if got.Index != w.index || got.Outcome != w.outcome || got.Code != w.code || (w.message != "" && got.Message != w.message) {
			t.Errorf("tests[%d] = %+v, want index %d %s %q %s", i, got, w.index, w.outcome, w.message, w.code)
		}
		if strings.Contains(got.Message+got.Expected+got.Actual, "s3cr3t-token") {
			t.Errorf("tests[%d] shows the secret: %+v", i, got)
		}
	}
	if got := resp.Tests[1]; got.Expected != "u7" || got.Expr != "$.id" || got.Target != "jsonpath" || got.Op != "equals" {
		t.Errorf("rendered assertion = %+v", got)
	}
}

// TestSendWithoutAssertions: a response of a request without assertions
// carries an empty list, not null; a GraphQL request's assertions are
// checked too.
func TestSendWithoutAssertions(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		fmt.Fprint(w, `{"data":{"ok":true}}`)
	}))
	defer srv.Close()

	f := newFixture(t, httpSource(srv.URL), nil)
	id := f.send(t)
	f.rec.wait(t, id)
	if tests := f.rec.response(t, id).Tests; tests == nil || len(tests) != 0 {
		t.Errorf("tests = %#v, want empty", tests)
	}

	gql := collection.HTTPSource{
		GraphQL: &projectfile.GraphQLRequest{ID: "req_1", URL: srv.URL, Query: "{ ok }", Assertions: []projectfile.Assertion{
			{Target: "jsonpath", Expr: "$.data.ok", Op: "equals", Expected: "true"},
		}},
		Auth: projectfile.Auth{Type: projectfile.AuthNone},
	}
	g := newFixture(t, gql, nil)
	id = g.send(t)
	g.rec.wait(t, id)
	if tests := g.rec.response(t, id).Tests; len(tests) != 1 || tests[0].Outcome != "passed" {
		t.Errorf("graphql tests = %+v", tests)
	}
}

// TestSendChecksJSONSchema checks matches_schema assertions after a send
// (TEST-07): an inline schema, a schema file relative to the project
// root, a missing file; the schema errors carry their JSON paths, secrets
// masked.
func TestSendChecksJSONSchema(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"u7","token":"s3cr3t-token","items":[{"n":1},{"n":"two"}]}`)
	}))
	defer srv.Close()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	fileSchema := `{"type":"object","required":["id","name"],"properties":{"items":{"items":{"properties":{"n":{"type":"integer"}}}}}}`
	if err := os.WriteFile(filepath.Join(root, "schemas", "user.json"), []byte(fileSchema), 0o644); err != nil {
		t.Fatal(err)
	}

	src := httpSource(srv.URL)
	src.Workspace = &coreworkspace.Workspace{Root: root, File: &projectfile.Workspace{}}
	src.FolderVariables = [][]projectfile.Variable{{
		{Name: "token", Value: "s3cr3t-token", Secret: true},
		{Name: "dir", Value: "schemas"},
	}}
	src.Request.Assertions = []projectfile.Assertion{
		{Target: "body", Op: "matches_schema", Expected: `{"properties":{"id":{"type":"string"},"token":{"const":"{{token}}"}}}`},
		{Target: "body", Op: "matches_schema", Expected: "{{dir}}/user.json"},
		{Target: "jsonpath", Expr: "$.token", Op: "matches_schema", Expected: `{"maxLength":3}`},
		{Target: "body", Op: "matches_schema", Expected: "schemas/missing.json"},
	}
	f := newFixture(t, src, nil)
	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	tests := f.rec.response(t, id).Tests
	if len(tests) != 4 {
		t.Fatalf("tests = %+v", tests)
	}
	if got := tests[0]; got.Outcome != "passed" || len(got.SchemaErrors) != 0 || got.SchemaErrors == nil || strings.Contains(got.Expected, "s3cr3t") {
		t.Errorf("inline schema: %+v", got)
	}
	got := tests[1]
	if got.Outcome != "failed" || got.Expected != "schemas/user.json" ||
		got.Message != `Expected body to match the JSON schema in schemas/user.json, but 2 errors: $ missing required property "name"; $.items[1].n must be integer, not string.` {
		t.Errorf("file schema: %+v", got)
	}
	wantErrs := []SchemaErrorDTO{
		{Path: "$", InstancePath: "", SchemaPath: "#/required", Keyword: "required", Message: `missing required property "name"`},
		{Path: "$.items[1].n", InstancePath: "/items/1/n", SchemaPath: "#/properties/items/items/properties/n/type", Keyword: "type", Message: "must be integer, not string"},
	}
	if !reflect.DeepEqual(got.SchemaErrors, wantErrs) {
		t.Errorf("schema errors = %+v\nwant %+v", got.SchemaErrors, wantErrs)
	}
	if got := tests[2]; got.Outcome != "failed" || strings.Contains(got.Message+got.Actual, "s3cr3t") ||
		len(got.SchemaErrors) != 1 || got.SchemaErrors[0].Message != "must have at most 3 characters, not 12" {
		t.Errorf("masked value: %+v", got)
	}
	if got := tests[3]; got.Outcome != "invalid" || got.Code != CodeSchemaFileMissing || got.Message != `The schema file "schemas/missing.json" was not found.` {
		t.Errorf("missing file: %+v", got)
	}
}

func TestSchemaLoader(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "s.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := fileaccess.Policy{ProjectRoot: root}
	if data, err := schemaLoader(project)("s.json"); err != nil || string(data) != "{}" {
		t.Errorf("relative: %q, %v", data, err)
	}
	if data, err := schemaLoader(fileaccess.Policy{Allowed: []string{root}})(filepath.Join(root, "s.json")); err != nil || string(data) != "{}" {
		t.Errorf("absolute in an allowed directory without a project: %q, %v", data, err)
	}
	code := func(err error) string {
		if e, ok := errors.AsType[*errs.Error](err); ok {
			return e.Code
		}
		return ""
	}
	if _, err := schemaLoader(fileaccess.Policy{})("s.json"); code(err) != CodeSchemaFileMissing {
		t.Errorf("relative without a project: %v", err)
	}
	if _, err := schemaLoader(fileaccess.Policy{})(filepath.Join(root, "s.json")); code(err) != fileaccess.CodeNotAllowed {
		t.Errorf("absolute outside the allowed directories: %v", err)
	}
	if _, err := schemaLoader(project)("nope.json"); code(err) != CodeSchemaFileMissing {
		t.Errorf("missing: %v", err)
	}
	if _, err := schemaLoader(project)("."); code(err) != CodeSchemaFileUnreadable {
		t.Errorf("a folder: %v", err)
	}
	big := filepath.Join(root, "big.json")
	if err := os.WriteFile(big, make([]byte, MaxSchemaFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaLoader(project)("big.json"); code(err) != CodeSchemaFileUnreadable || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("too large: %v", err)
	}
}
