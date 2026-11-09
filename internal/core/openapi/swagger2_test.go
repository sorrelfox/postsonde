package openapi

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// swagger wraps paths and more top-level YAML in a minimal Swagger 2.0
// document.
func swagger(rest string) string {
	return "swagger: '2.0'\ninfo: {title: T, version: '1'}\n" + rest
}

func opOf(t *testing.T, d *Document, method, path string) *Operation {
	t.Helper()
	for _, o := range d.Operations() {
		if o.Method == method && o.Path == path {
			return o
		}
	}
	t.Fatalf("no operation %s %s", method, path)
	return nil
}

func paramOf(t *testing.T, o *Operation, name string) *Parameter {
	t.Helper()
	for _, p := range o.Parameters {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("%s %s has no parameter %s", o.Method, o.Path, name)
	return nil
}

func TestSwaggerServers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, src string
		want      []string
		code      string
	}{
		{"host, base path and schemes", "host: api.example:8080\nbasePath: /v1/\nschemes: [http, https]\n", []string{"http://api.example:8080/v1", "https://api.example:8080/v1"}, ""},
		{"root base path", "host: api.example\nbasePath: /\nschemes: [https]\n", []string{"https://api.example"}, ""},
		{"no schemes", "host: api.example\n", []string{"https://api.example"}, CodeMissingField},
		{"no host", "basePath: /v2\n", []string{"/v2"}, ""},
		{"base path without slash", "host: h\nbasePath: v2\nschemes: [http]\n", []string{"http://h/v2"}, CodeInvalidValue},
		{"host with a path", "host: h/x\nbasePath: /v2\n", []string{"/v2"}, CodeInvalidValue},
		{"bad scheme", "host: h\nschemes: [ftp, wss]\n", []string{"wss://h"}, CodeInvalidValue},
		{"neither", "", []string{"/"}, ""},
	}
	for _, c := range cases {
		d, diags := parseOK(t, swagger(c.src+"paths: {}\n"))
		var got []string
		for _, s := range d.Servers {
			got = append(got, s.URL)
		}
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: servers = %v, want %v", c.name, got, c.want)
		}
		if c.code == "" && len(diags) > 0 || c.code != "" && findDiag(diags, c.code) == nil {
			t.Errorf("%s: diagnostics:%s", c.name, diagList(diags))
		}
	}
	// Servers point at host; an operation's schemes become its servers.
	d, _ := parseOK(t, swagger(`host: h
schemes: [https]
paths:
  /a:
    get:
      schemes: [http]
      responses: {'200': {description: OK}}
    put:
      schemes: [https]
      responses: {'200': {description: OK}}
`))
	if p := d.Servers[0].Pos; p.Pointer != "#/host" || p.Line != 3 {
		t.Errorf("server pos = %+v", p)
	}
	get, put := opOf(t, d, "GET", "/a"), opOf(t, d, "PUT", "/a")
	if len(get.Servers) != 1 || get.Servers[0].URL != "http://h" || get.Servers[0].Pos.Pointer != "#/paths/~1a/get/schemes" {
		t.Errorf("get servers = %+v", get.Servers)
	}
	if put.Servers != nil {
		t.Errorf("put servers = %+v", put.Servers)
	}
}

func TestSwaggerMediaTypes(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, swagger(`consumes: [application/json, text/xml]
produces: [application/json]
paths:
  /a:
    post:
      parameters:
        - {in: body, name: b, schema: {type: object}, x-examples: {text/xml: '<a/>'}}
      responses:
        '200': {description: OK, schema: {type: string}}
    put:
      consumes: [application/yaml]
      produces: [text/plain, application/json]
      parameters:
        - {in: body, name: b, schema: {type: object}}
      responses:
        '200':
          description: OK
          schema: {type: string}
          examples: {text/csv: 'a,b'}
        '204': {description: None}
  /b:
    post:
      consumes: []
      produces: []
      parameters:
        - {in: body, name: b, required: true, description: The thing, schema: {type: object}}
      responses:
        '200': {description: OK, schema: {type: string}}
`))
	if len(diags) > 0 {
		t.Errorf("diagnostics:%s", diagList(diags))
	}
	post, put, b := opOf(t, d, "POST", "/a"), opOf(t, d, "PUT", "/a"), opOf(t, d, "POST", "/b")
	if got := post.RequestBody.Content.Names(); strings.Join(got, ",") != "application/json,text/xml" {
		t.Errorf("post content = %v", got)
	}
	if mt, _ := post.RequestBody.Content.Get("text/xml"); string(mt.Example) != `"<a/>"` {
		t.Errorf("x-examples = %s", mt.Example)
	}
	if got := put.RequestBody.Content.Names(); strings.Join(got, ",") != "application/yaml" {
		t.Errorf("put content = %v", got)
	}
	r, _ := put.Responses.Get("200")
	if got := r.Content.Names(); strings.Join(got, ",") != "text/plain,application/json,text/csv" {
		t.Errorf("put response content = %v", got)
	}
	if mt, _ := r.Content.Get("text/csv"); !mt.Schema.HasType("string") || string(mt.Example) != `"a,b"` {
		t.Errorf("text/csv = %+v", mt)
	}
	if r, _ := put.Responses.Get("204"); len(r.Content) != 0 {
		t.Errorf("204 content = %v", r.Content.Names())
	}
	// Empty consumes and produces mean JSON.
	if rb := b.RequestBody; rb.Content.Names()[0] != "application/json" || !rb.Required || rb.Description != "The thing" {
		t.Errorf("b body = %+v", rb)
	}
	if r, _ := b.Responses.Get("200"); r.Content.Names()[0] != "application/json" {
		t.Errorf("b response = %v", r.Content.Names())
	}
}

func TestSwaggerFormData(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, swagger(`consumes: [application/json]
paths:
  /form/{id}:
    parameters:
      - {name: id, in: path, required: true, type: string}
      - {name: shared, in: formData, type: string}
      - {name: over, in: formData, type: string}
    post:
      consumes: [application/x-www-form-urlencoded, multipart/form-data]
      parameters:
        - {name: over, in: formData, type: integer, required: true}
        - {name: tags, in: formData, type: array, items: {type: string}, collectionFormat: multi}
        - {name: ids, in: formData, type: array, items: {type: integer}}
      responses: {'200': {description: OK}}
    put:
      consumes: [application/x-www-form-urlencoded]
      parameters:
        - {name: file, in: formData, type: file, description: The file}
        - {name: files, in: formData, type: array, items: {type: file}}
      responses: {'200': {description: OK}}
    patch:
      parameters:
        - {name: body, in: body, schema: {type: object}}
        - {name: f, in: formData, type: string}
      responses: {'200': {description: OK}}
`))
	post := opOf(t, d, "POST", "/form/{id}")
	if got := post.RequestBody.Content.Names(); strings.Join(got, ",") != "application/x-www-form-urlencoded,multipart/form-data" {
		t.Errorf("post content = %v", got)
	}
	mt, _ := post.RequestBody.Content.Get("application/x-www-form-urlencoded")
	s := mt.Schema
	if !s.HasType("object") || strings.Join(s.Properties.Names(), ",") != "shared,over,tags,ids" || strings.Join(s.Required, ",") != "over" {
		t.Errorf("form schema = %+v", s)
	}
	if !s.Property("over").HasType("integer") || !post.RequestBody.Required {
		t.Errorf("over = %+v", s.Property("over"))
	}
	if e, _ := mt.Encoding.Get("tags"); e == nil || !*e.Explode {
		t.Errorf("tags encoding = %+v", e)
	}
	if e, _ := mt.Encoding.Get("ids"); e == nil || *e.Explode {
		t.Errorf("ids encoding = %+v", e)
	}
	if len(post.Parameters) != 1 || post.Parameters[0].Name != "id" {
		t.Errorf("post parameters = %+v", post.Parameters)
	}
	if p := s.Property("tags").Pos; p.Pointer != "#/paths/~1form~1{id}/post/parameters/1" || p.Line != 14 {
		t.Errorf("tags pos = %+v", p)
	}

	// A file makes the body multipart, whatever consumes says.
	put := opOf(t, d, "PUT", "/form/{id}")
	if got := put.RequestBody.Content.Names(); strings.Join(got, ",") != "multipart/form-data" {
		t.Errorf("put content = %v", got)
	}
	mt, _ = put.RequestBody.Content.Get("multipart/form-data")
	if f := mt.Schema.Property("file"); !f.HasType("string") || f.Format != "binary" || f.Description != "The file" {
		t.Errorf("file = %+v", f)
	}
	if f := mt.Schema.Property("files"); !f.HasType("array") || f.Items.Format != "binary" {
		t.Errorf("files = %+v", f)
	}
	if dg := findDiag(diags, CodeInvalidValue); dg == nil || !strings.Contains(dg.Message, "multipart/form-data is used") || dg.Pos.Pointer != "#/paths/~1form~1{id}/parameters/1" {
		t.Errorf("diagnostics:%s", diagList(diags))
	}

	// body wins over formData, with a warning.
	patch := opOf(t, d, "PATCH", "/form/{id}")
	if _, ok := patch.RequestBody.Content.Get("application/json"); !ok {
		t.Errorf("patch content = %v", patch.RequestBody.Content.Names())
	}
	found := false
	for _, dg := range diags {
		found = found || strings.Contains(dg.Message, "both body and formData") && dg.Pos.Pointer == "#/paths/~1form~1{id}/parameters/1"
	}
	if !found {
		t.Errorf("diagnostics:%s", diagList(diags))
	}
}

func TestSwaggerCollectionFormat(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, swagger(`paths:
  /a/{p}:
    get:
      parameters:
        - {name: p, in: path, required: true, type: array, items: {type: string}}
        - {name: csv, in: query, type: array, items: {type: string}}
        - {name: multi, in: query, type: array, items: {type: string}, collectionFormat: multi}
        - {name: ssv, in: query, type: array, items: {type: string}, collectionFormat: ssv}
        - {name: pipes, in: query, type: array, items: {type: string}, collectionFormat: pipes}
        - {name: tsv, in: query, type: array, items: {type: string}, collectionFormat: tsv}
        - {name: h, in: header, type: array, items: {type: string}, collectionFormat: multi}
        - {name: odd, in: query, type: array, items: {type: string}, collectionFormat: commas}
        - name: nested
          in: query
          type: array
          items: {type: array, items: {type: integer}, collectionFormat: pipes}
        - {name: plain, in: query, type: string, collectionFormat: multi}
      responses: {'200': {description: OK}}
`))
	op := opOf(t, d, "GET", "/a/{p}")
	cases := map[string]string{
		"p": "simple/false", "csv": "form/false", "multi": "form/true", "ssv": "spaceDelimited/false",
		"pipes": "pipeDelimited/false", "tsv": "form/false", "h": "simple/false", "odd": "form/false",
		"nested": "form/false", "plain": "form/true",
	}
	for name, want := range cases {
		p := paramOf(t, op, name)
		if got := p.EffectiveStyle() + "/" + strconv.FormatBool(p.EffectiveExplode()); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	if n := paramOf(t, op, "nested").Schema; !n.Items.HasType("array") || !n.Items.Items.HasType("integer") {
		t.Errorf("nested = %+v", n)
	}
	want := map[string]string{
		"#/paths/~1a~1{p}/get/parameters/5/collectionFormat":       CodeUnsupported,
		"#/paths/~1a~1{p}/get/parameters/6/collectionFormat":       CodeUnsupported,
		"#/paths/~1a~1{p}/get/parameters/7/collectionFormat":       CodeInvalidValue,
		"#/paths/~1a~1{p}/get/parameters/8/items/collectionFormat": CodeUnsupported,
	}
	for _, dg := range diags {
		if want[dg.Pos.Pointer] == dg.Code {
			delete(want, dg.Pos.Pointer)
		}
	}
	if len(want) > 0 || len(diags) != 4 {
		t.Errorf("missing %v in diagnostics:%s", want, diagList(diags))
	}
}

func TestSwaggerParameters(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, swagger(`consumes: [application/json]
paths:
  /a:
    parameters:
      - $ref: '#/parameters/Limit'
    get:
      parameters:
        - {name: q, in: query, type: string, format: date, default: '2024-01-01', enum: ['2024-01-01'], x-example: '2025-02-02', x-flag: 1}
        - {name: n, in: query, type: number, minimum: 1, exclusiveMinimum: true, multipleOf: 0.5, allowEmptyValue: true}
        - {name: s, in: query, schema: {type: integer}}
        - {name: Limit, in: query, type: integer}
      responses: {'200': {description: OK}}
    post:
      parameters:
        - $ref: '#/parameters/Body'
      responses: {'200': {description: OK}}
    put:
      consumes: [application/xml]
      parameters:
        - $ref: '#/parameters/Body'
        - $ref: '#/parameters/Missing'
      responses: {'200': {description: OK}}
parameters:
  Limit: {name: limit, in: query, type: integer, maximum: 10}
  Body: {name: b, in: body, required: true, schema: {$ref: '#/definitions/Thing'}}
  Form: {name: f, in: formData, type: string}
definitions:
  Thing: {type: object}
`))
	get := opOf(t, d, "GET", "/a")
	q := paramOf(t, get, "q")
	if !q.Schema.HasType("string") || q.Schema.Format != "date" || string(q.Schema.Default) != `"2024-01-01"` || len(q.Schema.Enum) != 1 ||
		string(q.Example) != `"2025-02-02"` || len(q.Extensions) != 1 {
		t.Errorf("q = %+v %+v", q, q.Schema)
	}
	if n := paramOf(t, get, "n"); n.Schema.ExclusiveMinimum != "1" || n.Schema.MultipleOf != "0.5" || !n.AllowEmptyValue {
		t.Errorf("n = %+v", n.Schema)
	}
	if s := paramOf(t, get, "s"); !s.Schema.HasType("integer") {
		t.Errorf("s = %+v", s.Schema)
	}
	limit := paramOf(t, get, "limit")
	lc, _ := d.Components.Parameters.Get("Limit")
	if limit != lc || limit.Schema.Maximum != "10" || limit.Pos.Pointer != "#/parameters/Limit" {
		t.Errorf("limit = %+v", limit)
	}
	if _, ok := d.Components.Parameters.Get("Body"); ok {
		t.Error("body parameter in components.parameters")
	}
	if _, ok := d.Components.Parameters.Get("Form"); ok {
		t.Error("formData parameter in components.parameters")
	}
	shared, ok := d.Components.RequestBodies.Get("Body")
	if !ok || !shared.Required || shared.Pos.Pointer != "#/parameters/Body" {
		t.Fatalf("requestBodies.Body = %+v", shared)
	}
	// With the document's consumes, the operation uses the shared body;
	// with others, a copy with its media types.
	if post := opOf(t, d, "POST", "/a"); post.RequestBody != shared {
		t.Errorf("post body = %+v", post.RequestBody)
	}
	put := opOf(t, d, "PUT", "/a")
	if put.RequestBody == shared || put.RequestBody.Content.Names()[0] != "application/xml" {
		t.Errorf("put body = %+v", put.RequestBody)
	}
	thing := schemaOf(t, d, "Thing")
	if mt, _ := put.RequestBody.Content.Get("application/xml"); mt.Schema != thing {
		t.Errorf("put schema = %+v", mt.Schema)
	}
	dg := findDiag(diags, CodeBadRef)
	if dg == nil || !strings.Contains(dg.Message, `"#/parameters/Missing" cannot be resolved: there is no member "Missing" at "/parameters"`) || dg.Pos.Pointer != "#/paths/~1a/put/parameters/1/$ref" || dg.Pos.Line != 23 {
		t.Errorf("diagnostics:%s", diagList(diags))
	}
}

// $refs are rewritten to the components, cycles and missing targets are
// reported with the source's pointers, recursive schemas are marked.
func TestSwaggerRefs(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, swagger(`paths:
  /a:
    get:
      responses:
        '200': {$ref: '#/responses/Ok'}
        '201':
          description: Created
          schema: {$ref: '#/definitions/Node'}
        '404': {$ref: '#/responses/Nope'}
        default:
          description: Error
          schema: {$ref: '#/responses/Ok/schema'}
  /b:
    $ref: '#/paths/~1a'
responses:
  Ok:
    description: OK
    schema: {$ref: '#/definitions/Wrapper'}
definitions:
  Node:
    type: object
    properties:
      children: {type: array, items: {$ref: '#/definitions/Node'}}
      name: {$ref: '#/definitions/Wrapper/properties/label'}
  Wrapper:
    type: object
    properties:
      label: {type: string}
      loop: {$ref: '#/definitions/Loop1'}
      gone: {$ref: '#/definitions/Gone'}
      other: {$ref: 'other.yaml#/definitions/X'}
  Loop1: {$ref: '#/definitions/Loop2'}
  Loop2: {$ref: '#/definitions/Loop1'}
  a/b~c: {type: integer}
  Escaped: {$ref: '#/definitions/a~1b~0c'}
`))
	node, wrapper := schemaOf(t, d, "Node"), schemaOf(t, d, "Wrapper")
	if !node.Recursive || node.Property("children").Items != node {
		t.Errorf("node = %+v", node)
	}
	if node.Property("name") != wrapper.Property("label") {
		t.Error("deep reference not resolved")
	}
	if schemaOf(t, d, "Escaped") != schemaOf(t, d, "a/b~c") {
		t.Error("escaped reference not resolved")
	}
	get := opOf(t, d, "GET", "/a")
	ok, _ := get.Responses.Get("200")
	okc, _ := d.Components.Responses.Get("Ok")
	if ok != okc {
		t.Error("response reference not resolved")
	}
	def, _ := get.Responses.Get("default")
	if mt, _ := def.Content.Get("application/json"); mt.Schema != wrapper {
		t.Errorf("reference to a response schema = %+v", mt.Schema)
	}
	if b := d.Paths[1]; len(b.Operations) != 1 || b.Operations[0].Responses[0].Value != ok {
		t.Errorf("path item reference = %+v", b)
	}
	want := map[string]string{
		CodeRefCycle:    "#/definitions/Wrapper/properties/loop",
		CodeExternalRef: "#/definitions/Wrapper/properties/other/$ref",
	}
	for code, ptr := range want {
		if dg := findDiag(diags, code); dg == nil || dg.Pos.Pointer != ptr {
			t.Errorf("%s: diagnostics:%s", code, diagList(diags))
		}
	}
	var bad []string
	for _, dg := range diags {
		if dg.Code == CodeBadRef {
			bad = append(bad, dg.Pos.Pointer+" "+dg.Message)
		}
	}
	if len(bad) != 2 || !strings.Contains(bad[0], `#/definitions/Wrapper/properties/gone/$ref The reference "#/definitions/Gone" cannot be resolved: there is no member "Gone" at "/definitions"`) ||
		!strings.Contains(bad[1], `#/paths/~1a/get/responses/404/$ref The reference "#/responses/Nope"`) {
		t.Errorf("bad refs:\n%s", strings.Join(bad, "\n"))
	}
	if w := wrapper.Property("gone"); w.Ref != "#/definitions/Gone" {
		t.Errorf("placeholder = %+v", w)
	}
}

func TestSwaggerSchemas(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, swagger(`paths: {}
definitions:
  Pet:
    type: object
    discriminator: kind
    required: [kind]
    properties:
      kind: {type: string}
      photo: {type: file}
      nick: {type: string, x-nullable: true}
      both: {type: string, x-nullable: true, nullable: false}
      raw: {type: file, format: base64}
    additionalProperties: {type: file}
    x-custom: [1]
`))
	if len(diags) > 0 {
		t.Errorf("diagnostics:%s", diagList(diags))
	}
	pet := schemaOf(t, d, "Pet")
	if pet.Discriminator == nil || pet.Discriminator.PropertyName != "kind" {
		t.Errorf("discriminator = %+v", pet.Discriminator)
	}
	if p := pet.Property("photo"); !p.HasType("string") || p.Format != "binary" {
		t.Errorf("photo = %+v", p)
	}
	if p := pet.Property("raw"); p.Format != "base64" {
		t.Errorf("raw = %+v", p)
	}
	if p := pet.AdditionalProperties; p.Format != "binary" {
		t.Errorf("additionalProperties = %+v", p)
	}
	if p := pet.Property("nick"); !p.Nullable || !p.HasType("null") {
		t.Errorf("nick = %+v", p)
	}
	if p := pet.Property("both"); p.Nullable || len(p.Extensions) != 1 {
		t.Errorf("both = %+v", p)
	}
	if len(pet.Extensions) != 1 {
		t.Errorf("extensions = %+v", pet.Extensions)
	}
}

func TestSwaggerResponses(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, swagger(`produces: [application/json, application/xml]
paths:
  /a:
    get:
      responses:
        '200':
          description: OK
          headers:
            X-Rate: {type: integer, description: Calls left, x-h: 1}
            X-Ids: {type: array, items: {type: integer}}
          schema: {type: array, items: {type: string}}
          examples:
            application/json: [a, b]
          x-r: true
`))
	if len(diags) > 0 {
		t.Errorf("diagnostics:%s", diagList(diags))
	}
	r, _ := opOf(t, d, "GET", "/a").Responses.Get("200")
	if strings.Join(r.Content.Names(), ",") != "application/json,application/xml" || len(r.Extensions) != 1 {
		t.Errorf("response = %+v", r)
	}
	js, _ := r.Content.Get("application/json")
	xml, _ := r.Content.Get("application/xml")
	if js.Schema != xml.Schema || string(js.Example) != `["a","b"]` || xml.Example != nil {
		t.Errorf("content = %+v %+v", js, xml)
	}
	if js.Pos.Pointer != "#/paths/~1a/get/responses/200" || js.Schema.Pos.Pointer != "#/paths/~1a/get/responses/200/schema" {
		t.Errorf("positions = %+v %+v", js.Pos, js.Schema.Pos)
	}
	h, _ := r.Headers.Get("X-Rate")
	if h.Description != "Calls left" || !h.Schema.HasType("integer") || len(h.Extensions) != 1 || h.Schema.Pos.Pointer != "#/paths/~1a/get/responses/200/headers/X-Rate" {
		t.Errorf("header = %+v", h)
	}
	if h, _ := r.Headers.Get("X-Ids"); !h.Schema.Items.HasType("integer") {
		t.Errorf("X-Ids = %+v", h.Schema)
	}
}

func TestSwaggerSecurity(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, swagger(`paths: {}
security: [{basic: []}, {oauth: [read]}]
securityDefinitions:
  basic: {type: basic, description: Login}
  key: {type: apiKey, name: X-Key, in: header}
  qkey: {type: apiKey, name: key, in: query}
  implicit: {type: oauth2, flow: implicit, authorizationUrl: 'https://a/auth', scopes: {read: Read}}
  oauth: {type: oauth2, flow: password, tokenUrl: 'https://a/token', scopes: {read: Read}}
  app: {type: oauth2, flow: application, tokenUrl: 'https://a/token'}
  code: {type: oauth2, flow: accessCode, authorizationUrl: 'https://a/auth', tokenUrl: 'https://a/token', scopes: {}}
  odd: {type: oauth2, flow: device, scopes: {}}
  noflow: {type: oauth2, scopes: {}}
  weird: {type: digest}
`))
	get := func(name string) *SecurityScheme {
		s, ok := d.Components.SecuritySchemes.Get(name)
		if !ok {
			t.Fatalf("no scheme %s", name)
		}
		return s
	}
	if s := get("basic"); s.Type != "http" || s.Scheme != "basic" || s.Description != "Login" {
		t.Errorf("basic = %+v", s)
	}
	if s := get("key"); s.Type != "apiKey" || s.Name != "X-Key" || s.In != "header" {
		t.Errorf("key = %+v", s)
	}
	if s := get("qkey"); s.In != "query" {
		t.Errorf("qkey = %+v", s)
	}
	if f := get("implicit").Flows; f.Implicit == nil || f.Implicit.AuthorizationURL != "https://a/auth" || f.Implicit.TokenURL != "" || f.Implicit.Scopes[0].Name != "read" {
		t.Errorf("implicit = %+v", f)
	}
	if f := get("oauth").Flows; f.Password == nil || f.Password.TokenURL != "https://a/token" {
		t.Errorf("password = %+v", f)
	}
	if f := get("app").Flows; f.ClientCredentials == nil || f.ClientCredentials.TokenURL != "https://a/token" || f.ClientCredentials.Scopes != nil {
		t.Errorf("application = %+v", f)
	}
	if f := get("code").Flows; f.AuthorizationCode == nil || f.AuthorizationCode.AuthorizationURL == "" || f.AuthorizationCode.TokenURL == "" {
		t.Errorf("accessCode = %+v", f)
	}
	if len(d.Security) != 2 || d.Security[1][0].Name != "oauth" {
		t.Errorf("security = %+v", d.Security)
	}
	var got []string
	for _, dg := range diags {
		got = append(got, dg.Code+" "+dg.Pos.Pointer)
	}
	want := []string{
		CodeInvalidValue + " #/securityDefinitions/odd/flow",
		CodeMissingField + " #/securityDefinitions/noflow",
		CodeInvalidValue + " #/securityDefinitions/weird/type",
	}
	for _, w := range want {
		if !strings.Contains(strings.Join(got, "\n"), w) {
			t.Errorf("no %s in diagnostics:%s", w, diagList(diags))
		}
	}
}

// The builder's diagnostics on converted objects point into the Swagger
// document: lines, columns and pointers.
func TestSwaggerDiagnosticPositions(t *testing.T) {
	t.Parallel()
	_, diags := parseOK(t, swagger(`paths:
  /a/{id}:
    get:
      operationId: same
      parameters:
        - {in: body, name: b}
        - {name: x, in: cookiez, type: string}
      responses: {'200': {description: OK}}
    put:
      operationId: same
      responses: {'200': {description: OK}}
host: h
x-ok: 1
extra: 2
`))
	want := map[string]string{
		"warning openapi.missing_field 8:11: The body parameter \"b\" has no schema. (#/paths/~1a~1{id}/get/parameters/0)":                                     "",
		"warning openapi.invalid_value 9:25: The parameter location \"cookiez\" is not path, query, header or cookie. (#/paths/~1a~1{id}/get/parameters/1/in)": "",
		"warning openapi.ignored 16:1: The field \"extra\" is not part of Swagger 2.0; it is ignored. (#/extra)":                                               "",
	}
	for _, dg := range diags {
		delete(want, string(dg.Severity)+" "+dg.Code+" "+dg.String())
	}
	if len(want) > 0 {
		t.Errorf("missing %v\nin diagnostics:%s", want, diagList(diags))
	}
	if dg := findDiag(diags, CodeDuplicate); dg == nil || dg.Pos.Pointer != "#/paths/~1a~1{id}/put/operationId" {
		t.Errorf("duplicate: diagnostics:%s", diagList(diags))
	}
	if dg := findDiag(diags, CodePathParameter); dg == nil || dg.Pos.Pointer != "#/paths/~1a~1{id}/get" || dg.Pos.Line != 6 {
		t.Errorf("path parameter: diagnostics:%s", diagList(diags))
	}
}

func TestSwaggerVersionAndLimits(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"swagger: 2.0\npaths: {}\n", `{"swagger": "2.0", "paths": {}}`} {
		d, _ := parseOK(t, src)
		if d.Swagger != "2.0" || d.OpenAPI != "3.0.3" || d.Minor != 0 {
			t.Errorf("%s: %+v", src, d)
		}
	}
	for _, src := range []string{"swagger: '3.0'\n", "swagger: [2.0]\n", "swagger: 2\n"} {
		_, _, err := Parse([]byte(src))
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeUnsupportedVersion || e.Details["line"] != "1" {
			t.Errorf("%q: err = %v", src, err)
		}
	}
	// Aliased parameters are converted once per use; the conversion is
	// bounded.
	var b strings.Builder
	b.WriteString("swagger: '2.0'\nx-p: &p {name: a, in: query, type: array, items: {type: string, enum: [a, b, c, d]}}\nx-l: &l [*p, *p, *p, *p, *p, *p, *p, *p]\npaths:\n")
	for i := 0; i < 40; i++ {
		b.WriteString("  /p" + strconv.Itoa(i) + ": {get: {parameters: *l, responses: {}}}\n")
	}
	_, _, err := ParseLimits([]byte(b.String()), Limits{MaxNodes: 500})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeTooLarge {
		t.Errorf("err = %v", err)
	}
	if _, _, err := ParseLimits([]byte(b.String()), Limits{}); err != nil {
		t.Errorf("default limits: %v", err)
	}
	// Aliases of aliases multiply; the conversion stops at the limit.
	b.Reset()
	b.WriteString("swagger: '2.0'\nx-r: &r {description: d, headers: {A: {type: string}, B: {type: string}, C: {type: string}}}\nx-rs: &rs {")
	for i := 0; i < 50; i++ {
		b.WriteString("'" + strconv.Itoa(200+i) + "': *r, ")
	}
	b.WriteString("}\nx-op: &op {responses: *rs}\nx-pi: &pi {get: *op, put: *op, post: *op, delete: *op, patch: *op}\npaths:\n")
	for i := 0; i < 500; i++ {
		b.WriteString("  /p" + strconv.Itoa(i) + ": *pi\n")
	}
	start := time.Now()
	_, _, err = ParseLimits([]byte(b.String()), Limits{MaxNodes: 20_000})
	if !errors.As(err, &e) || e.Code != CodeTooLarge || time.Since(start) > 5*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
	// Aliased schemas stay shared.
	d, diags := parseOK(t, `swagger: '2.0'
info: {title: T, version: '1'}
paths: {}
definitions:
  A: &a {type: object, properties: {x: {type: file}}}
  B: {type: array, items: *a}
`)
	if len(diags) > 0 || schemaOf(t, d, "B").Items != schemaOf(t, d, "A") {
		t.Errorf("aliases: %+v%s", schemaOf(t, d, "B").Items, diagList(diags))
	}
}
