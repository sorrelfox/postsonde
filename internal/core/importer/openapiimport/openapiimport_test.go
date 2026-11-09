package openapiimport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/insomnia"
	"github.com/sorrelfox/postsonde/internal/core/importer/openapiimport"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/openapi"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// readerFixtures are the OpenAPI reader's petstore documents.
const readerFixtures = "../../openapi/testdata"

func read(t *testing.T, path string) importer.Source {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return importer.Source{Name: filepath.Base(path), Data: data}
}

func registry() *importer.Registry {
	return importer.NewRegistry(curlimport.Importer{}, insomnia.Importer{}, postman.Importer{}, openapiimport.Importer{})
}

func parse(t *testing.T, src importer.Source) *importer.Model {
	t.Helper()
	m, im, err := registry().Parse(context.Background(), src, "")
	if err != nil {
		t.Fatal(err)
	}
	if im.Format().ID != "openapi" {
		t.Fatalf("detected %q", im.Format().ID)
	}
	return m
}

// dump renders a model as stable, readable JSON for the golden files:
// request files as PostSonde writes them.
func dump(t *testing.T, m *importer.Model) []byte {
	t.Helper()
	type diag struct {
		Code, Severity, Message, At string
	}
	at := func(l importer.Location) string {
		if l.IsZero() {
			return ""
		}
		return l.Path + " @" + strconv.Itoa(l.Line) + ":" + strconv.Itoa(l.Column)
	}
	diags := func(ds []importer.Diagnostic) []diag {
		var out []diag
		for _, d := range ds {
			out = append(out, diag{d.Code, string(d.Severity), d.Message, at(d.Source)})
		}
		return out
	}
	var item func(it importer.Item) any
	folder := func(f *importer.Folder) map[string]any {
		var items []any
		for _, it := range f.Items {
			items = append(items, item(it))
		}
		auth, err := json.Marshal(f.Auth)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{
			"folder": f.Name, "description": f.Description, "variables": f.Variables, "headers": f.Headers,
			"auth": json.RawMessage(auth), "at": at(f.Source), "diagnostics": diags(f.Diagnostics), "items": items,
		}
	}
	item = func(it importer.Item) any {
		switch {
		case it.Folder != nil:
			return folder(it.Folder)
		case it.Request != nil:
			return map[string]any{
				"request": it.Request.Name, "at": at(it.Request.Source),
				"file": json.RawMessage(marshal(t, it.Request.File)), "diagnostics": diags(it.Request.Diagnostics),
			}
		default:
			s := it.Skipped
			return map[string]any{"skipped": s.Kind, "name": s.Name, "code": s.Code, "reason": s.Reason, "at": at(s.Source)}
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"name": m.Name, "root": folder(&m.Root), "environments": len(m.Environments), "diagnostics": diags(m.Diagnostics),
	}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func marshal(t *testing.T, f projectfile.File) []byte {
	t.Helper()
	h, ok := f.(*projectfile.HTTPRequest)
	if !ok {
		t.Fatalf("unexpected file %T", f)
	}
	c := *h
	c.ID = "req_x"
	data, err := projectfile.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGolden(t *testing.T) {
	for _, name := range []string{"petstore-2.0.yaml", "petstore-3.0.yaml", "petstore-3.1.yaml"} {
		t.Run(name, func(t *testing.T) {
			golden.Assert(t, name, dump(t, parse(t, read(t, filepath.Join(readerFixtures, name)))))
		})
	}
	for _, name := range []string{"recursive.yaml", "security.yaml", "params.yaml"} {
		t.Run(name, func(t *testing.T) {
			golden.Assert(t, name, dump(t, parse(t, read(t, filepath.Join("testdata", name)))))
		})
	}
}

// The JSON and YAML forms of a document import the same, but for the
// places in the file.
func TestJSONMatchesYAML(t *testing.T) {
	for _, v := range []string{"2.0", "3.0", "3.1"} {
		y := parse(t, read(t, filepath.Join(readerFixtures, "petstore-"+v+".yaml")))
		j := parse(t, read(t, filepath.Join(readerFixtures, "petstore-"+v+".json")))
		clear := func(m *importer.Model) {
			var walk func(f *importer.Folder)
			walk = func(f *importer.Folder) {
				f.Source = importer.Location{}
				for i := range f.Diagnostics {
					f.Diagnostics[i].Source = importer.Location{}
				}
				for _, it := range f.Items {
					switch {
					case it.Folder != nil:
						walk(it.Folder)
					case it.Request != nil:
						it.Request.Source = importer.Location{}
						for i := range it.Request.Diagnostics {
							it.Request.Diagnostics[i].Source = importer.Location{}
						}
					}
				}
			}
			walk(&m.Root)
			for i := range m.Diagnostics {
				m.Diagnostics[i].Source = importer.Location{}
			}
		}
		clear(y)
		clear(j)
		if a, b := dump(t, y), dump(t, j); !bytes.Equal(a, b) {
			t.Errorf("%s: JSON and YAML differ:\n%s\n---\n%s", v, a, b)
		}
	}
}

func TestDetect(t *testing.T) {
	im := openapiimport.Importer{}
	cases := []struct {
		data string
		want int
	}{
		{"openapi: 3.1.0\ninfo: {}\n", importer.Sure},
		{"# comment\n'openapi': \"3.0.3\"\n", importer.Sure},
		{`{"openapi": "3.0.0", "info": {}}`, importer.Sure},
		{"\xef\xbb\xbf  {\n  \"info\": {},\n  \"openapi\" : \"3.1.0\"}", importer.Sure},
		{"swagger: '2.0'\npaths: {}\n", importer.Sure},
		{"swagger: 2.0 # old\ninfo: {}\n", importer.Sure},
		{`{"swagger": "2.0"}`, importer.Sure},
		{"swagger: '1.2'\npaths: {}\n", importer.Maybe},
		{`{"swagger": "1.2"}`, importer.Maybe},
		{"info: {}\nswagger: 2.0.1\npaths: {}\n", importer.Maybe},
		{"type: spec.insomnia.rest/5.0\nspec:\n  contents:\n    openapi: 3.0.0\n", importer.No},
		{`{"_type": "export", "resources": [{"contents": "openapi: 3.0.0"}]}`, importer.No},
		{"curl https://example.com\n", importer.No},
		{"", importer.No},
	}
	for _, c := range cases {
		if got := im.Detect(importer.Source{Data: []byte(c.data)}); got != c.want {
			t.Errorf("Detect(%q) = %d, want %d", c.data, got, c.want)
		}
	}
}

func TestInvalidFiles(t *testing.T) {
	cases := []struct {
		data, want string
		line       int
	}{
		{"openapi: 3.0.0\ninfo: [\n", "", 0},
		{"swagger: '1.2'\npaths: {}\n", "Swagger", 1},
		{"info: {}\nswagger: 2\npaths: {}\n", "Swagger", 2},
		{"openapi: 4.0.0\npaths: {}\n", "4.0.0", 1},
		{`{"openapi": "3.0.0", }`, "JSON", 1},
	}
	for _, c := range cases {
		_, err := openapiimport.Importer{}.Parse(context.Background(), importer.Source{Data: []byte(c.data)})
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.Parse || e.Code != importer.CodeInvalidFile || e.Details["format"] != "openapi" {
			t.Errorf("%q: err = %#v", c.data, err)
			continue
		}
		if !strings.Contains(e.Message, c.want) {
			t.Errorf("%q: message %q", c.data, e.Message)
		}
		if c.line > 0 && e.Details["line"] != strconv.Itoa(c.line) {
			t.Errorf("%q: details %v", c.data, e.Details)
		}
	}
}

func TestCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := openapiimport.Importer{}.Parse(ctx, importer.Source{Data: []byte("openapi: 3.0.0\ninfo: {title: x, version: '1'}\npaths: {}\n")})
	if errs.KindOf(err) != errs.Cancelled {
		t.Fatalf("err = %v", err)
	}
}

// The reader's diagnostics go to the operation they are in, else to the
// file; a document without servers gets an empty baseUrl and a warning.
func TestReaderDiagnosticsAndNoServer(t *testing.T) {
	doc := `openapi: 3.0.3
info: {title: Broken, version: "1"}
paths:
  /a:
    get:
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Missing'}
      responses: {'200': {description: OK}}
x-top: {$ref: 'other.yaml#/X'}
components:
  schemas:
    Unused: {$ref: '#/components/schemas/Nope'}
`
	m := parse(t, importer.Source{Name: "broken.yaml", Data: []byte(doc)})
	req := m.Root.Items[0].Request
	if req == nil {
		t.Fatalf("items = %+v", m.Root.Items)
	}
	if !hasCode(req.Diagnostics, openapi.CodeBadRef) {
		t.Errorf("request diagnostics = %+v", req.Diagnostics)
	}
	if !hasCode(m.Diagnostics, openapi.CodeBadRef) {
		t.Errorf("file diagnostics = %+v", m.Diagnostics)
	}
	if !hasCode(m.Root.Diagnostics, openapiimport.CodeNoServer) {
		t.Errorf("root diagnostics = %+v", m.Root.Diagnostics)
	}
	h := req.File.(*projectfile.HTTPRequest)
	if h.URL != "{{baseUrl}}/a" || h.Body.Mode != projectfile.BodyRaw || h.Body.Text != "" {
		t.Errorf("request = %+v", h)
	}
	if v := m.Root.Variables; len(v) != 1 || v[0].Name != "baseUrl" || v[0].Value != "" {
		t.Errorf("variables = %+v", v)
	}
}

func hasCode(ds []importer.Diagnostic, code string) bool {
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}

// Generated examples of recursive schemas end, and are valid JSON.
func TestSchemaExampleRecursive(t *testing.T) {
	doc, _, err := openapi.Parse(read(t, filepath.Join("testdata", "recursive.yaml")).Data)
	if err != nil {
		t.Fatal(err)
	}
	names := openapiimport.SchemaNames(doc)
	cases := map[string]string{
		"Node":    `{"name":"string","children":[]}`,
		"Person":  `{"name":"Ada","employer":{"title":"string","staff":[]}}`,
		"List":    `{"value":3}`,
		"Expr":    `{"op":"add","terms":[]}`,
		"Company": `{"title":"string","ceo":{"name":"Ada"},"staff":[{"name":"Ada"}]}`,
	}
	for name, want := range cases {
		s, _ := doc.Components.Schemas.Get(name)
		if !s.Recursive {
			t.Errorf("%s is not marked recursive", name)
		}
		got, notes := openapiimport.SchemaExample(s, openapiimport.ExampleOptions{Names: names})
		var buf bytes.Buffer
		if err := json.Compact(&buf, got); err != nil {
			t.Fatalf("%s: %v in %s", name, err, got)
		}
		if buf.String() != want {
			t.Errorf("%s = %s, want %s", name, buf.String(), want)
		}
		if !notes.Recursive {
			t.Errorf("%s: notes = %+v", name, notes)
		}
	}
	// The depth limit ends a deep chain.
	s, _ := doc.Components.Schemas.Get("L1")
	got, notes := openapiimport.SchemaExample(s, openapiimport.ExampleOptions{MaxDepth: 3})
	if !notes.Truncated || strings.Contains(string(got), "leaf") {
		t.Errorf("L1 = %s, %+v", got, notes)
	}
}

// Wide schemas that share subschemas without cycles stay within the
// value budget.
func TestSchemaExampleBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("openapi: 3.1.0\ninfo: {title: t, version: '1'}\npaths: {}\ncomponents:\n  schemas:\n")
	for i := 0; i < 20; i++ {
		next := "{type: string}"
		if i < 19 {
			next = "{$ref: '#/components/schemas/S" + strconv.Itoa(i+1) + "'}"
		}
		b.WriteString("    S" + strconv.Itoa(i) + ": {type: object, properties: {a: " + next + ", b: " + next + ", c: " + next + "}}\n")
	}
	doc, _, err := openapi.Parse([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := doc.Components.Schemas.Get("S0")
	got, notes := openapiimport.SchemaExample(s, openapiimport.ExampleOptions{MaxDepth: 100, MaxValues: 500})
	if !notes.Truncated || len(got) > 200_000 || !json.Valid(got) {
		t.Errorf("len %d, notes %+v, valid %v", len(got), notes, json.Valid(got))
	}
}

func TestSchemaExampleResponseDirection(t *testing.T) {
	doc, _, err := openapi.Parse(read(t, filepath.Join(readerFixtures, "petstore-3.0.yaml")).Data)
	if err != nil {
		t.Fatal(err)
	}
	names := openapiimport.SchemaNames(doc)
	user, _ := doc.Components.Schemas.Get("User")
	req, _ := openapiimport.SchemaExample(user, openapiimport.ExampleOptions{Names: names})
	resp, _ := openapiimport.SchemaExample(user, openapiimport.ExampleOptions{Names: names, Direction: openapiimport.ForResponse})
	if !strings.Contains(string(req), `"password"`) || strings.Contains(string(resp), `"password"`) {
		t.Errorf("request %s\nresponse %s", req, resp)
	}
	// Cat's discriminator value comes from the mapping.
	cat, _ := doc.Components.Schemas.Get("Cat")
	got, _ := openapiimport.SchemaExample(cat, openapiimport.ExampleOptions{Names: names, Direction: openapiimport.ForResponse})
	if !strings.Contains(string(got), `"petType": "cat"`) || !strings.Contains(string(got), `"huntingSkill": "lazy"`) {
		t.Errorf("cat = %s", got)
	}
	x := openapiimport.XMLExample(cat, got, names)
	if !strings.HasPrefix(x, `<?xml`) || !strings.Contains(x, "<Cat>") {
		t.Errorf("xml = %s", x)
	}
}

// XML examples follow the schema's xml hints: element names, attributes
// and wrapped arrays.
func TestXMLExample(t *testing.T) {
	doc, _, err := openapi.Parse(read(t, filepath.Join("testdata", "params.yaml")).Data)
	if err != nil {
		t.Fatal(err)
	}
	names := openapiimport.SchemaNames(doc)
	thing, _ := doc.Components.Schemas.Get("Thing")
	ex, _ := openapiimport.SchemaExample(thing, openapiimport.ExampleOptions{Names: names})
	got := openapiimport.XMLExample(thing, ex, names)
	want := `<?xml version="1.0" encoding="UTF-8"?>
<thing>
  <name>stringxx</name>
  <price>1</price>
  <count>-5</count>
  <tags>
    <tag>string</tag>
  </tags>
  <kind>big</kind>
  <email>user@example.com</email>
  <extra>
    <additionalProp1>true</additionalProp1>
  </extra>
</thing>
`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// A Swagger 2.0 document imports through the reader's conversion: the
// reader's diagnostics keep the Swagger document's places and go to the
// operation they are in.
func TestSwagger(t *testing.T) {
	doc := `swagger: "2.0"
info: {title: Old API, version: "1"}
host: old.example
basePath: /api
schemes: [http]
paths:
  /things:
    post:
      summary: Add a thing
      consumes: [multipart/form-data]
      parameters:
        - {name: file, in: formData, type: file, required: true}
        - {name: note, in: formData, type: string}
        - {name: ids, in: query, type: array, items: {type: integer}}
      responses: {'200': {description: OK, schema: {$ref: '#/definitions/Missing'}}}
      security: [{basic: []}]
securityDefinitions:
  basic: {type: basic}
`
	m := parse(t, importer.Source{Name: "old.yaml", Data: []byte(doc)})
	if v := m.Root.Variables; len(v) == 0 || v[0].Name != "baseUrl" || v[0].Value != "http://old.example/api" {
		t.Errorf("variables = %+v", v)
	}
	req := m.Root.Items[0].Request
	if req == nil {
		t.Fatalf("items = %+v", m.Root.Items)
	}
	h := req.File.(*projectfile.HTTPRequest)
	if h.URL != "{{baseUrl}}/things" || h.Body.Mode != projectfile.BodyMultipart || len(h.Body.Parts) != 2 || h.Auth.Type != "basic" {
		t.Errorf("request = %+v", h)
	}
	if len(h.Params) != 1 || h.Params[0].Name != "ids" {
		t.Errorf("params = %+v", h.Params)
	}
	var bad *importer.Diagnostic
	for i, d := range req.Diagnostics {
		if d.Code == openapi.CodeBadRef {
			bad = &req.Diagnostics[i]
		}
	}
	if bad == nil || !strings.Contains(bad.Message, `"#/definitions/Missing"`) || bad.Source.Line != 15 ||
		bad.Source.Path != "#/paths/~1things/post/responses/200/schema/$ref" {
		t.Errorf("diagnostics = %+v", req.Diagnostics)
	}
	if !hasCode(req.Diagnostics, openapiimport.CodeFile) {
		t.Errorf("diagnostics = %+v", req.Diagnostics)
	}
}
