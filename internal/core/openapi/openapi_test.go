package openapi

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func parseOK(t *testing.T, src string) (*Document, []Diagnostic) {
	t.Helper()
	d, diags, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return d, diags
}

func findDiag(diags []Diagnostic, code string) *Diagnostic {
	for i := range diags {
		if diags[i].Code == code {
			return &diags[i]
		}
	}
	return nil
}

func diagList(diags []Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString("\n  " + string(d.Severity) + " " + d.Code + " " + d.String())
	}
	return b.String()
}

func schemaOf(t *testing.T, d *Document, name string) *Schema {
	t.Helper()
	s, ok := d.Components.Schemas.Get(name)
	if !ok || s == nil {
		t.Fatalf("no schema %q", name)
	}
	return s
}

// TestFixtures parses the 3.0 and 3.1 petstores in YAML and JSON: no
// diagnostics, the same model from both forms, and a golden dump with
// positions.
func TestFixtures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"petstore-2.0", "petstore-3.0", "petstore-3.1"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var dumps []string
			for _, ext := range []string{"yaml", "json"} {
				data, err := os.ReadFile("testdata/" + name + "." + ext)
				if err != nil {
					t.Fatal(err)
				}
				d, diags, err := Parse(data)
				if err != nil {
					t.Fatalf("%s: %v", ext, err)
				}
				if len(diags) > 0 {
					t.Errorf("%s: unexpected diagnostics:%s", ext, diagList(diags))
				}
				golden.Assert(t, name+"."+ext, []byte(dump(d, true)))
				dumps = append(dumps, dump(d, false))
			}
			if dumps[0] != dumps[1] {
				t.Errorf("YAML and JSON give different models:\n--- yaml\n%s\n--- json\n%s", dumps[0], dumps[1])
			}
		})
	}
}

// TestAnchorsFixture parses the YAML reader's OpenAPI fixture, which
// shares objects through anchors and aliases.
func TestAnchorsFixture(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/anchors-3.0.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, diags, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		t.Errorf("diagnostics:%s", diagList(diags))
	}
	golden.Assert(t, "anchors-3.0.yaml", []byte(dump(d, true)))
}

func findOp(t *testing.T, d *Document, id string) *Operation {
	t.Helper()
	for _, o := range d.Operations() {
		if o.OperationID == id {
			return o
		}
	}
	t.Fatalf("no operation %s", id)
	return nil
}

func TestPetstore30Model(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/petstore-3.0.json")
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if d.Is31() || d.OpenAPI != "3.0.3" || d.Info.Title != "Petstore 3.0" || d.Info.Contact.Email != "api@petstore.example" {
		t.Errorf("header: %q %v %+v", d.OpenAPI, d.Is31(), d.Info)
	}
	if got := d.Servers[0].DefaultURL(); got != "https://petstore.example/v1" {
		t.Errorf("server = %q", got)
	}
	if got := len(d.Operations()); got != 7 {
		t.Errorf("%d operations, want 7", got)
	}

	pet := schemaOf(t, d, "Pet")
	if id := pet.Property("id"); id.ExclusiveMinimum != "0" || id.Minimum != "" || !id.ReadOnly {
		t.Errorf("Pet.id = %+v", id)
	}
	if tag := pet.Property("tag"); !slices.Equal(tag.Type, []string{"string", "null"}) {
		t.Errorf("Pet.tag type = %v", tag.Type)
	}
	if dsc := pet.Discriminator; dsc == nil || dsc.PropertyName != "petType" || len(dsc.Mapping) != 2 {
		t.Errorf("discriminator = %+v", dsc)
	}
	cat, dog := schemaOf(t, d, "Cat"), schemaOf(t, d, "Dog")
	if cat.AllOf[0] != pet || dog.AllOf[0] != pet {
		t.Error("allOf does not share Pet")
	}
	if items := schemaOf(t, d, "Pets").Items; len(items.OneOf) != 2 || items.OneOf[0] != cat {
		t.Error("Pets.items.oneOf")
	}
	user := schemaOf(t, d, "User")
	if pet.Property("owner") != user || user.Property("pets").Items != pet || !pet.Recursive || !user.Recursive || schemaOf(t, d, "Error").Recursive {
		t.Error("Pet <-> User recursion")
	}

	list := findOp(t, d, "listPets")
	if len(list.Parameters) != 3 || list.Parameters[2].Name != "X-Trace-Id" || list.Parameters[2].In != InHeader {
		t.Errorf("listPets params = %+v", list.Parameters)
	}
	limit := list.Parameters[0]
	if string(limit.Example) != "20" || limit.Schema.ExclusiveMaximum != "100" || limit.Schema.Maximum != "" {
		t.Errorf("limit = %+v %+v", limit, limit.Schema)
	}
	if st := list.Parameters[1]; st.EffectiveExplode() || len(st.Examples) != 2 || string(st.Examples[1].Value.Value) != `["available","pending"]` {
		t.Errorf("status = %+v", st)
	}
	if got := list.Responses.Names(); !slices.Equal(got, []string{"200", "4XX", "default"}) {
		t.Errorf("responses = %v", got)
	}
	r200 := list.Responses[0].Value
	if len(r200.Headers) != 2 || r200.Headers[1].Value.Description != "Calls per hour allowed by the user" {
		t.Errorf("headers = %+v", r200.Headers)
	}
	sec := list.EffectiveSecurity(d)
	if len(sec) != 1 || sec[0][0].Name != "petstore_auth" || !slices.Equal(sec[0][0].Value, []string{"write:pets", "read:pets"}) {
		t.Errorf("security = %v", sec)
	}

	create := findOp(t, d, "createPet")
	if got := create.RequestBody.Content.Names(); !slices.Equal(got, []string{"application/json", "application/x-www-form-urlencoded"}) {
		t.Errorf("content = %v", got)
	}
	if mt := create.RequestBody.Content[0].Value; mt.Schema != pet || string(mt.Example) != `{"name":"Rex","petType":"dog"}` {
		t.Errorf("body = %+v", mt)
	}
	if links := create.Responses[0].Value.Links; len(links) != 1 || links[0].Name != "GetPetById" {
		t.Errorf("links = %+v", links)
	}
	if cb, _ := create.Callbacks.Get("petAdopted"); cb == nil || cb.Paths[0].Operations[0].RequestBody.Content[0].Value.Schema != pet {
		t.Error("callback")
	}

	del := findOp(t, d, "deletePet")
	if !del.Deprecated || len(del.Security) != 2 || len(del.Parameters) != 2 || del.Parameters[0].Name != "petId" {
		t.Errorf("deletePet = %+v", del)
	}
	upload := findOp(t, d, "uploadPhoto")
	if p := upload.Parameters[1]; p.In != InCookie || p.EffectiveStyle() != "form" {
		t.Errorf("cookie = %+v", p)
	}
	enc := upload.RequestBody.Content[0].Value.Encoding
	if len(enc) != 1 || enc[0].Value.ContentType != "image/png, image/jpeg" || len(enc[0].Value.Headers) != 1 {
		t.Errorf("encoding = %+v", enc)
	}
	if me := findOp(t, d, "getMe"); len(me.Servers) != 1 || me.Servers[0].URL != "https://accounts.petstore.example/v1" {
		t.Errorf("getMe servers = %+v", me.Servers)
	}
	inv := findOp(t, d, "getInventory")
	if ap := inv.Responses[0].Value.Content[0].Value.Schema.AdditionalProperties; ap == nil || !ap.HasType("integer") {
		t.Errorf("additionalProperties = %+v", ap)
	}
	auth, _ := d.Components.SecuritySchemes.Get("petstore_auth")
	if auth.Flows.Implicit == nil || auth.Flows.ClientCredentials.RefreshURL == "" || auth.Flows.Implicit.Scopes[0].Name != "write:pets" {
		t.Errorf("petstore_auth = %+v", auth.Flows)
	}
	if oidc, _ := d.Components.SecuritySchemes.Get("oidc"); oidc.OpenIDConnectURL == "" {
		t.Error("oidc")
	}
}

func TestPetstore31Model(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/petstore-3.1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Is31() || d.Info.License.Identifier != "MIT" || d.Info.Summary == "" {
		t.Errorf("header: %+v", d.Info)
	}
	if got := d.Servers[0].DefaultURL(); got != "https://api.petstore.example:443/v2" {
		t.Errorf("default URL = %q", got)
	}
	if got := d.Servers[0].Expand(map[string]string{"env": "staging"}); got != "https://staging.petstore.example:443/v2" {
		t.Errorf("expanded URL = %q", got)
	}

	pet := schemaOf(t, d, "Pet")
	if got := pet.Property("tag").Type; !slices.Equal(got, []string{"string", "null"}) {
		t.Errorf("tag type = %v", got)
	}
	if got := pet.Property("id").ExclusiveMinimum; got != "0" {
		t.Errorf("exclusiveMinimum = %q", got)
	}
	if got := pet.Property("name").Examples; len(got) != 2 || string(got[0]) != `"Rex"` {
		t.Errorf("examples = %s", got)
	}
	if ap := pet.AdditionalProperties; ap == nil || ap.Bool == nil || *ap.Bool {
		t.Errorf("additionalProperties = %+v", ap)
	}
	// $ref with a sibling description, through an $anchor.
	price := pet.Property("price")
	if price.Description != "What the pet costs" || len(price.AllOf) != 1 || price.AllOf[0] != schemaOf(t, d, "Money") {
		t.Errorf("price = %+v", price)
	}
	if got := schemaOf(t, d, "Money").Property("currency").Const; string(got) != `"EUR"` {
		t.Errorf("const = %s", got)
	}

	tree := schemaOf(t, d, "TreeNode")
	if !tree.Recursive || tree.Property("children").Items != tree {
		t.Error("TreeNode is not a recursive shared schema")
	}
	cat := schemaOf(t, d, "Category")
	ref, _ := cat.Defs.Get("CategoryRef")
	if cat.Property("parent") != ref || ref.OneOf[0] != cat || !cat.Recursive {
		t.Error("Category/$defs recursion not resolved")
	}

	// A path item from components, with its path set on a copy.
	var byID *PathItem
	for _, p := range d.Paths {
		if p.Path == "/pets/{petId}" {
			byID = p
		}
	}
	if byID == nil || len(byID.Operations) != 2 || byID.Operations[0].Path != "/pets/{petId}" {
		t.Fatalf("/pets/{petId} = %+v", byID)
	}
	comp, _ := d.Components.PathItems.Get("PetById")
	if comp.Path != "" || comp.Operations[0].Path != "" {
		t.Error("the component path item was changed")
	}
	get := byID.Operations[0]
	if len(get.Parameters) != 1 || get.Parameters[0].Name != "petId" {
		t.Errorf("inherited parameters = %+v", get.Parameters)
	}
	// A 3.1 Reference Object's description replaces the target's on a copy.
	r404, _ := get.Responses.Get("404")
	shared, _ := d.Components.Responses.Get("Error")
	if r404.Description != "No such pet" || shared.Description != "Unexpected error" || r404.Content[0].Value != shared.Content[0].Value {
		t.Errorf("404 = %q, shared = %q", r404.Description, shared.Description)
	}

	var create *Operation
	for _, o := range d.Operations() {
		if o.OperationID == "createPet" {
			create = o
		}
	}
	if create.Security == nil || len(create.Security) != 0 || len(create.EffectiveSecurity(d)) != 0 {
		t.Errorf("createPet security = %#v", create.Security)
	}
	if mt, _ := create.RequestBody.Content.Get("application/json"); mt == nil || mt.Schema != schemaOf(t, d, "NewPet") {
		t.Error("createPet body schema")
	}
	if len(d.Webhooks) != 1 || d.Webhooks[0].Name != "newPet" || d.Webhooks[0].Value.Operations[0].Method != "POST" {
		t.Errorf("webhooks = %+v", d.Webhooks)
	}
	sub := d.Paths[len(d.Paths)-1].Operations[0]
	cb, _ := sub.Callbacks.Get("onEvent")
	if cb == nil || len(cb.Paths) != 1 || cb.Paths[0].Path != "{$request.body#/callbackUrl}" {
		t.Errorf("callbacks = %+v", sub.Callbacks)
	}
	if enc := sub.RequestBody.Content[0].Value.Encoding; len(enc) != 1 || enc[0].Name != "events" || !*enc[0].Value.Explode {
		t.Errorf("encoding = %+v", enc)
	}
}

func TestRefEscapingAndChains(t *testing.T) {
	t.Parallel()
	src := `openapi: 3.0.3
info: {title: Refs, version: "1"}
paths: {}
components:
  schemas:
    a/b: {type: string}
    t~x: {type: integer}
    sp ace: {type: boolean}
    "~1": {type: number}
    Alias1: {$ref: '#/components/schemas/Alias2'}
    Alias2: {$ref: '#/components/schemas/a~1b'}
    Obj:
      properties:
        x: {type: string, format: date}
    AliasObj: {$ref: '#/components/schemas/Obj'}
    Union:
      allOf: [{type: string, maxLength: 3}]
    Uses:
      properties:
        slash: {$ref: '#/components/schemas/a~1b'}
        tilde: {$ref: '#/components/schemas/t~0x'}
        percent: {$ref: '#/components/schemas/sp%20ace'}
        both: {$ref: '#/components/schemas/~01'}
        chain: {$ref: '#/components/schemas/Alias1'}
        index: {$ref: '#/components/schemas/Union/allOf/0'}
        through: {$ref: '#/components/schemas/AliasObj/properties/x'}
`
	d, diags := parseOK(t, src)
	if len(diags) > 0 {
		t.Fatalf("diagnostics:%s", diagList(diags))
	}
	uses := schemaOf(t, d, "Uses")
	union := schemaOf(t, d, "Union")
	obj := schemaOf(t, d, "Obj")
	tests := []struct {
		prop string
		want *Schema
	}{
		{"slash", schemaOf(t, d, "a/b")},
		{"tilde", schemaOf(t, d, "t~x")},
		{"percent", schemaOf(t, d, "sp ace")},
		{"both", schemaOf(t, d, "~1")},
		{"chain", schemaOf(t, d, "a/b")},
		{"index", union.AllOf[0]},
		{"through", obj.Property("x")},
	}
	for _, tt := range tests {
		if got := uses.Property(tt.prop); got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.prop, got, tt.want)
		}
	}
	if got := schemaOf(t, d, "Alias1"); got != schemaOf(t, d, "a/b") {
		t.Error("a component that is a reference is not the target")
	}
	if p := schemaOf(t, d, "a/b").Pos; p.Pointer != "#/components/schemas/a~1b" || p.Line != 6 || p.Column != 10 {
		t.Errorf("a/b pos = %+v", p)
	}
}

func TestRecursiveSchemas(t *testing.T) {
	t.Parallel()
	yamlSrc := `openapi: 3.1.0
info: {title: Rec, version: "1"}
paths:
  /n:
    get:
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Node'}
components:
  schemas:
    Node:
      type: object
      properties:
        next: {$ref: '#/components/schemas/Node'}
        other: {$ref: '#/components/schemas/Other'}
        list: {type: array, items: {$ref: '#/components/schemas/Node'}}
    Other:
      allOf:
        - $ref: '#/components/schemas/Node'
      properties:
        back: {$ref: '#/components/schemas/Other'}
    Expr:
      oneOf:
        - type: number
        - type: object
          properties:
            left: {$ref: '#/components/schemas/Expr'}
            right: {$ref: '#/components/schemas/Expr'}
    Root:
      properties:
        self: {$ref: '#'}
`
	jsonSrc := `{"openapi": "3.1.0", "info": {"title": "Rec", "version": "1"}, "paths": {},
 "components": {"schemas": {
  "Node": {"type": "object", "properties": {
     "next": {"$ref": "#/components/schemas/Node"},
     "other": {"$ref": "#/components/schemas/Other"},
     "list": {"type": "array", "items": {"$ref": "#/components/schemas/Node"}}}},
  "Other": {"allOf": [{"$ref": "#/components/schemas/Node"}],
            "properties": {"back": {"$ref": "#/components/schemas/Other"}}},
  "Expr": {"oneOf": [{"type": "number"}, {"type": "object", "properties": {
     "left": {"$ref": "#/components/schemas/Expr"}, "right": {"$ref": "#/components/schemas/Expr"}}}]},
  "Root": {"properties": {"self": {"$ref": "#"}}}}}}`
	for _, src := range []string{yamlSrc, jsonSrc} {
		done := make(chan struct{})
		var d *Document
		var diags []Diagnostic
		go func() {
			defer close(done)
			d, diags = parseOK(t, src)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Parse did not finish on recursive schemas")
		}
		if len(diags) > 0 {
			t.Errorf("diagnostics:%s", diagList(diags))
		}
		node, other, expr := schemaOf(t, d, "Node"), schemaOf(t, d, "Other"), schemaOf(t, d, "Expr")
		if !node.Recursive || node.Property("next") != node || node.Property("list").Items != node || node.Property("other") != other {
			t.Error("Node")
		}
		if !other.Recursive || other.AllOf[0] != node || other.Property("back") != other {
			t.Error("Other")
		}
		if !expr.Recursive || expr.OneOf[1].Property("left") != expr || expr.OneOf[1].Property("right") != expr {
			t.Error("Expr")
		}
		// A reference to the whole document reads it as a schema, once.
		if self := schemaOf(t, d, "Root").Property("self"); self == nil || self.Ref != "" {
			t.Errorf("Root.self = %+v", self)
		}
		// Walking the graph with a visited set terminates.
		if out := dump(d, false); !strings.Contains(out, "next see #/components/schemas/Node") {
			t.Errorf("dump:\n%s", out)
		}
	}
}

func TestBadRefs(t *testing.T) {
	t.Parallel()
	const head = "openapi: 3.1.0\ninfo: {title: T, version: \"1\"}\n"
	tests := []struct {
		name      string
		src       string
		code      string
		sev       Severity
		line, col int
		msg       string
	}{
		{
			name: "missing component",
			src: head + `paths:
  /x:
    get:
      responses:
        '200':
          $ref: '#/components/responses/Missing'
`,
			code: CodeBadRef, sev: SeverityError, line: 8, col: 17,
			msg: `there is no member "components" at "/"`,
		},
		{
			name: "missing member deeper",
			src: head + `paths: {}
components:
  schemas:
    A: {type: string}
    B: {properties: {x: {$ref: '#/components/schemas/Nope'}}}
`,
			code: CodeBadRef, sev: SeverityError, line: 7, col: 32,
			msg: `there is no member "Nope" at "/components/schemas"`,
		},
		{
			name: "external file",
			src: head + `paths: {}
components:
  schemas:
    A: {$ref: 'common.yaml#/Pet'}
`,
			code: CodeExternalRef, sev: SeverityWarning, line: 6, col: 15,
			msg: "points outside this document",
		},
		{
			name: "cycle of references",
			src: head + `paths: {}
components:
  schemas:
    A: {$ref: '#/components/schemas/B'}
    B: {$ref: '#/components/schemas/C'}
    C: {$ref: '#/components/schemas/A'}
`,
			code: CodeRefCycle, sev: SeverityError, line: 6, col: 8,
			msg: "cycle of references",
		},
		{
			name: "self reference",
			src: head + `paths: {}
components:
  parameters:
    P: {$ref: '#/components/parameters/P'}
`,
			code: CodeRefCycle, sev: SeverityError, line: 6, col: 8,
		},
		{
			name: "ref not a string",
			src: head + `paths: {}
components:
  schemas:
    A: {$ref: 42}
`,
			code: CodeBadRef, sev: SeverityError, line: 6, col: 15,
			msg: "$ref must be a string",
		},
		{
			name: "pointer into a scalar",
			src: head + `paths: {}
components:
  schemas:
    A: {$ref: '#/info/title/x'}
`,
			code: CodeBadRef, sev: SeverityError, line: 6, col: 15,
			msg: `the value at "/info/title" is a string`,
		},
		{
			name: "array index out of range",
			src: head + `servers: [{url: /}]
paths: {}
components:
  schemas:
    A: {$ref: '#/servers/1'}
`,
			code: CodeBadRef, sev: SeverityError, line: 7, col: 15,
			msg: `there is no item "1" at "/servers"`,
		},
		{
			name: "unknown anchor",
			src: head + `paths: {}
components:
  schemas:
    A: {$ref: '#nowhere'}
`,
			code: CodeBadRef, sev: SeverityError, line: 6, col: 15,
			msg: "names no $anchor",
		},
		{
			name: "ref to a non-object",
			src: head + `paths:
  /x:
    get:
      responses:
        '200': {$ref: '#/info/title'}
`,
			code: CodeInvalidValue, sev: SeverityError, line: 2, col: 15,
			msg: "must be an object, not a string",
		},
		{
			name: "json positions",
			src:  "{\n  \"openapi\": \"3.0.0\",\n  \"info\": {\"title\": \"T\", \"version\": \"1\"},\n  \"paths\": {\"/x\": {\"get\": {\"responses\": {\"200\": {\"$ref\": \"#/nope\"}}}}}\n}",
			code: CodeBadRef, sev: SeverityError, line: 4, col: 58,
			msg: `there is no member "nope" at "/"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, diags := parseOK(t, tt.src)
			d := findDiag(diags, tt.code)
			if d == nil {
				t.Fatalf("no %s diagnostic in:%s", tt.code, diagList(diags))
			}
			if d.Severity != tt.sev || d.Pos.Line != tt.line || d.Pos.Column != tt.col || !strings.Contains(d.Message, tt.msg) {
				t.Errorf("got %s %s, want %s at %d:%d containing %q", d.Severity, d.String(), tt.sev, tt.line, tt.col, tt.msg)
			}
		})
	}
}

func TestBrokenRefPlaceholders(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, `openapi: 3.0.3
info: {title: T, version: "1"}
paths:
  /x/{id}:
    parameters:
      - $ref: '#/components/parameters/Missing'
    get:
      requestBody: {$ref: 'other.yaml#/Body'}
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Missing'}
`)
	if len(diags) == 0 {
		t.Fatal("no diagnostics")
	}
	op := d.Paths[0].Operations[0]
	if len(op.Parameters) != 1 || op.Parameters[0] == nil || op.Parameters[0].Pos.Line != 6 {
		t.Errorf("parameter placeholder = %+v", op.Parameters)
	}
	if op.RequestBody == nil || op.RequestBody.Pos.Line != 8 {
		t.Errorf("body placeholder = %+v", op.RequestBody)
	}
	r, _ := op.Responses.Get("200")
	if s := r.Content[0].Value.Schema; s == nil || s.Ref != "#/components/schemas/Missing" || s.Pos.Line != 14 {
		t.Errorf("schema placeholder = %+v", s)
	}
}

func TestRefSiblings(t *testing.T) {
	t.Parallel()
	const body = `info: {title: T, version: "1"}
paths: {}
components:
  schemas:
    Base: {type: string}
    WithDesc:
      $ref: '#/components/schemas/Base'
      description: Overridden
      x-note: kept out of the count
`
	d30, diags := parseOK(t, "openapi: 3.0.3\n"+body)
	if dg := findDiag(diags, CodeIgnored); dg == nil || dg.Severity != SeverityInfo || dg.Pos.Line != 8 {
		t.Errorf("3.0 siblings diagnostic missing:%s", diagList(diags))
	}
	if schemaOf(t, d30, "WithDesc") != schemaOf(t, d30, "Base") {
		t.Error("3.0: $ref with siblings is not the target")
	}
	d31, diags := parseOK(t, "openapi: 3.1.0\n"+body)
	if len(diags) > 0 {
		t.Errorf("3.1 diagnostics:%s", diagList(diags))
	}
	w := schemaOf(t, d31, "WithDesc")
	if w.Description != "Overridden" || len(w.AllOf) != 1 || w.AllOf[0] != schemaOf(t, d31, "Base") || len(w.Extensions) != 1 {
		t.Errorf("3.1 WithDesc = %+v", w)
	}
}

func TestSchema30Conversions(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, `openapi: 3.0.1
info: {title: T, version: "1"}
paths: {}
components:
  schemas:
    S:
      type: object
      properties:
        n: {type: string, nullable: true}
        lo: {type: number, minimum: 5, exclusiveMinimum: true, maximum: 9, exclusiveMaximum: false}
        tuple: {type: array, items: [{type: string}, {type: integer}]}
        big: {type: integer, maximum: 12345678901234567890, minimum: 0x10}
        free: {type: object, additionalProperties: true}
        ro: {type: string, readOnly: true, deprecated: true, default: "x"}
`)
	s := schemaOf(t, d, "S")
	if got := s.Property("n"); !slices.Equal(got.Type, []string{"string", "null"}) || !got.Nullable {
		t.Errorf("nullable: %+v", got)
	}
	lo := s.Property("lo")
	if lo.Minimum != "" || lo.ExclusiveMinimum != "5" || lo.Maximum != "9" || lo.ExclusiveMaximum != "" {
		t.Errorf("bounds: %+v", lo)
	}
	if tu := s.Property("tuple"); len(tu.PrefixItems) != 2 || tu.Items != nil {
		t.Errorf("tuple: %+v", tu)
	}
	if dg := findDiag(diags, CodeInvalidValue); dg == nil || dg.Pos.Line != 11 {
		t.Errorf("items array diagnostic:%s", diagList(diags))
	}
	if big := s.Property("big"); big.Maximum != "12345678901234567890" || big.Minimum != "16" {
		t.Errorf("numbers: %q %q", big.Maximum, big.Minimum)
	}
	if ap := s.Property("free").AdditionalProperties; ap == nil || ap.Bool == nil || !*ap.Bool {
		t.Errorf("additionalProperties: %+v", ap)
	}
	if ro := s.Property("ro"); !ro.ReadOnly || !ro.Deprecated || string(ro.Default) != `"x"` {
		t.Errorf("ro: %+v", ro)
	}
}

func TestSchema31Keywords(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, `openapi: 3.1.0
info: {title: T, version: "1"}
components:
  schemas:
    S:
      $schema: https://json-schema.org/draft/2020-12/schema
      $id: '#s'
      type: [object, 'null']
      if: {properties: {kind: {const: a}}}
      then: {required: [a]}
      else: true
      not: false
      patternProperties:
        ^x-: {type: string}
      propertyNames: {maxLength: 10}
      minProperties: 1
      contentMediaType: application/json
      contentEncoding: base64
      examples: [{kind: a}, null]
      $defs:
        Inner: {type: string, enum: [a, b]}
      $dynamicAnchor: meta
`)
	s := schemaOf(t, d, "S")
	if !slices.Equal(s.Type, []string{"object", "null"}) || s.If == nil || s.Then == nil || *s.Else.Bool != true || *s.Not.Bool != false {
		t.Errorf("S = %+v", s)
	}
	if string(s.If.Property("kind").Const) != `"a"` || !slices.Equal(s.Then.Required, []string{"a"}) {
		t.Error("if/then")
	}
	if len(s.PatternProperties) != 1 || *s.PropertyNames.MaxLength != 10 || *s.MinProperties != 1 {
		t.Error("object keywords")
	}
	if s.ContentMediaType != "application/json" || s.ContentEncoding != "base64" || s.ID != "#s" || s.Schema == "" {
		t.Error("string keywords")
	}
	if len(s.Examples) != 2 || string(s.Examples[0]) != `{"kind":"a"}` || string(s.Examples[1]) != "null" {
		t.Errorf("examples = %s", s.Examples)
	}
	if in, _ := s.Defs.Get("Inner"); in == nil || len(in.Enum) != 2 {
		t.Error("$defs")
	}
	if dg := findDiag(diags, CodeUnsupported); dg == nil || !strings.Contains(dg.Message, "$dynamicAnchor") || dg.Pos.Line != 22 {
		t.Errorf("unsupported keyword diagnostic:%s", diagList(diags))
	}
	if findDiag(diags, CodeMissingField) != nil {
		t.Errorf("a 3.1 document with only components needs no paths:%s", diagList(diags))
	}
}

func TestValidationDiagnostics(t *testing.T) {
	t.Parallel()
	_, diags := parseOK(t, `openapi: 3.0.3
info: {title: T}
servers:
  - url: https://{region}.example.com
paths:
  /items/{id}/{sub}:
    get:
      operationId: dup
      security: [{nope: []}]
      parameters:
        - {name: id, in: path, schema: {type: string}}
        - {name: extra, in: path, required: true}
        - {name: body, in: body}
        - {name: q, in: query}
        - {name: q, in: query}
      responses:
        '200': {description: ok}
        '2x0': {description: odd}
  items:
    post:
      operationId: dup
x-top: 1
unknown: 2
`)
	want := []struct {
		code      string
		line, col int
		msg       string
	}{
		{CodeMissingField, 2, 7, "info has no version"},
		{CodeMissingField, 4, 10, "{region}"},
		{CodeInvalidValue, 11, 11, `"id" must be required`},
		{CodeInvalidValue, 13, 28, `location "body"`},
		{CodeDuplicate, 15, 11, `query parameter "q" is listed twice`},
		{CodeInvalidValue, 18, 9, `"2x0"`},
		{CodePathParameter, 8, 7, `"extra" that is not in the path`},
		{CodePathParameter, 8, 7, "does not define the path parameter {sub}"},
		{CodeInvalidValue, 19, 3, `"items" does not start with /`},
		{CodeMissingField, 21, 7, "POST operation has no responses"},
		{CodeDuplicate, 21, 20, `operationId "dup"`},
		{CodeIgnored, 23, 1, `"unknown"`},
		{CodeBadRef, 9, 19, `"nope" is not defined`},
	}
	for _, w := range want {
		found := false
		for _, d := range diags {
			if d.Code == w.code && d.Pos.Line == w.line && d.Pos.Column == w.col && strings.Contains(d.Message, w.msg) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s at %d:%d %q in:%s", w.code, w.line, w.col, w.msg, diagList(diags))
		}
	}
	if len(diags) != len(want) {
		t.Errorf("%d diagnostics, want %d:%s", len(diags), len(want), diagList(diags))
	}
}

func TestParameterMerge(t *testing.T) {
	t.Parallel()
	d, _ := parseOK(t, `openapi: 3.0.3
info: {title: T, version: "1"}
paths:
  /a/{id}:
    parameters:
      - {name: id, in: path, required: true, description: path level}
      - {name: v, in: header}
    get:
      parameters:
        - {name: id, in: path, required: true, description: operation level}
        - {name: q, in: query}
      responses: {'200': {description: ok}}
    post:
      responses: {'200': {description: ok}}
`)
	get, post := d.Paths[0].Operations[0], d.Paths[0].Operations[1]
	var got []string
	for _, p := range get.Parameters {
		got = append(got, p.In+":"+p.Name+":"+p.Description)
	}
	if want := []string{"path:id:operation level", "header:v:", "query:q:"}; !slices.Equal(got, want) {
		t.Errorf("get params = %v, want %v", got, want)
	}
	if len(post.Parameters) != 2 || post.Parameters[0] != d.Paths[0].Parameters[0] {
		t.Errorf("post params = %+v", post.Parameters)
	}
	if p := get.Parameters[2]; p.EffectiveStyle() != "form" || !p.EffectiveExplode() {
		t.Error("query defaults")
	}
	if p := get.Parameters[1]; p.EffectiveStyle() != "simple" || p.EffectiveExplode() {
		t.Error("header defaults")
	}
}

func TestMergeKeysAndAliases(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, `openapi: 3.0.3
info: {title: T, version: "1"}
paths:
  /a:
    get:
      responses:
        '200': &ok
          description: Base
          content:
            application/json:
              schema: &str {type: string}
        '201':
          <<: *ok
          description: Created
        '202': *ok
components:
  schemas:
    S: *str
`)
	if len(diags) > 0 {
		t.Fatalf("diagnostics:%s", diagList(diags))
	}
	rs := d.Paths[0].Operations[0].Responses
	r200, r201, r202 := rs[0].Value, rs[1].Value, rs[2].Value
	if r201.Description != "Created" || len(r201.Content) != 1 || r201.Content[0].Value.Schema != r200.Content[0].Value.Schema {
		t.Errorf("merged response = %+v", r201)
	}
	if r202 != r200 {
		t.Error("an alias is not the same object")
	}
	if schemaOf(t, d, "S") != r200.Content[0].Value.Schema {
		t.Error("aliased schema not shared")
	}
}

func TestLaterMinorVersion(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, "openapi: 3.2.0\ninfo: {title: T, version: '1'}\npaths: {}\n")
	if d.Minor != 1 || !d.Is31() {
		t.Errorf("minor = %d", d.Minor)
	}
	if dg := findDiag(diags, CodeUnsupported); dg == nil || dg.Pos.Line != 1 || dg.Pos.Column != 10 {
		t.Errorf("diagnostics:%s", diagList(diags))
	}
	if len(d.Servers) != 1 || d.Servers[0].URL != "/" {
		t.Errorf("default servers = %+v", d.Servers)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		src       string
		lim       Limits
		code      string
		kind      errs.Kind
		line, col string
	}{
		{"invalid json", "{\"openapi\": \"3.0.0\",\n  \"info\": }", Limits{}, CodeInvalidJSON, errs.Parse, "2", "11"},
		{"json trailing comma", "{\"openapi\": \"3.0.0\",}", Limits{}, CodeInvalidJSON, errs.Parse, "1", "21"},
		{"json duplicate key", "{\"openapi\": \"3.0.0\", \"openapi\": \"3.1.0\"}", Limits{}, CodeInvalidJSON, errs.Parse, "1", "22"},
		{"invalid yaml", "openapi: 3.0.0\ninfo: [a\n", Limits{}, "yaml.invalid_yaml", errs.Parse, "", ""},
		{"swagger 1.2", "swagger: \"1.2\"\ninfo: {title: T}\n", Limits{}, CodeUnsupportedVersion, errs.Unsupported, "1", "10"},
		{"version 4", "openapi: 4.0.0\n", Limits{}, CodeUnsupportedVersion, errs.Unsupported, "1", "10"},
		{"version text", "openapi: three\n", Limits{}, CodeUnsupportedVersion, errs.Unsupported, "1", "10"},
		{"no openapi field", "info: {title: T}\n", Limits{}, CodeNotOpenAPI, errs.Parse, "1", "1"},
		{"top-level array", "[1, 2]", Limits{}, CodeNotOpenAPI, errs.Parse, "1", "1"},
		{"top-level scalar", "just text\n", Limits{}, CodeNotOpenAPI, errs.Parse, "1", "1"},
		{"empty", "", Limits{}, CodeNotOpenAPI, errs.Parse, "", ""},
		{"two documents", "openapi: 3.0.0\n---\nopenapi: 3.1.0\n", Limits{}, CodeNotOpenAPI, errs.Parse, "3", "1"},
		{"too large", "openapi: 3.0.0\n", Limits{MaxBytes: 5}, CodeTooLarge, errs.Unsupported, "", ""},
		{"json too deep", `{"a": [[[[[[1]]]]]]}`, Limits{MaxDepth: 4}, CodeTooLarge, errs.Unsupported, "1", "10"},
		{"yaml too deep", "a: [[[[[[1]]]]]]\n", Limits{MaxDepth: 4}, "yaml.too_large", errs.Unsupported, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d, diags, err := ParseLimits([]byte(tt.src), tt.lim)
			if err == nil {
				t.Fatalf("no error; doc %+v diags %v", d, diags)
			}
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("error %T %v", err, err)
			}
			if e.Code != tt.code || e.Kind != tt.kind {
				t.Errorf("code %s kind %s, want %s %s: %v", e.Code, e.Kind, tt.code, tt.kind, err)
			}
			if tt.line != "" && (e.Details["line"] != tt.line || e.Details["column"] != tt.col) {
				t.Errorf("at %s:%s, want %s:%s: %v", e.Details["line"], e.Details["column"], tt.line, tt.col, err)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	t.Parallel()
	t.Run("reference resolutions", func(t *testing.T) {
		t.Parallel()
		var b strings.Builder
		b.WriteString("openapi: 3.0.3\ninfo: {title: T, version: '1'}\npaths: {}\ncomponents:\n  schemas:\n    Base: {type: string}\n")
		for i := range 20 {
			b.WriteString("    S" + string(rune('a'+i)) + ": {$ref: '#/components/schemas/Base'}\n")
		}
		_, diags, err := ParseLimits([]byte(b.String()), Limits{MaxRefs: 5})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, d := range diags {
			if d.Code == CodeLimit {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d limit diagnostics, want 1:%s", n, diagList(diags))
		}
	})
	t.Run("long reference chain", func(t *testing.T) {
		t.Parallel()
		// References are filled in from a queue, not recursively: a chain
		// far longer than MaxDepth resolves.
		const n = 20_000
		var b strings.Builder
		b.WriteString("openapi: 3.0.3\ninfo: {title: T, version: '1'}\npaths:\n  /a:\n    get:\n      responses:\n        '200':\n          description: ok\n          content:\n            application/json:\n              schema: {$ref: '#/components/schemas/S0'}\ncomponents:\n  schemas:\n")
		for i := n - 1; i >= 0; i-- {
			b.WriteString("    S" + itoa(i) + ": {properties: {next: {$ref: '#/components/schemas/S" + itoa(i+1) + "'}}}\n")
		}
		b.WriteString("    S" + itoa(n) + ": {type: string}\n")
		d, diags, err := ParseLimits([]byte(b.String()), Limits{MaxDepth: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(diags) > 0 {
			t.Errorf("diagnostics:%s", diagList(diags[:1]))
		}
		s := d.Paths[0].Operations[0].Responses[0].Value.Content[0].Value.Schema
		steps := 0
		for s.Property("next") != nil {
			s = s.Property("next")
			steps++
		}
		if steps != n || !s.HasType("string") || s.Recursive {
			t.Errorf("%d steps to %+v", steps, s)
		}
	})
	t.Run("alias expansion in values", func(t *testing.T) {
		t.Parallel()
		src := `openapi: 3.0.3
info: {title: T, version: '1'}
paths: {}
x-a: &a [x, x, x, x, x, x, x, x, x, x]
x-b: &b [*a, *a, *a, *a, *a, *a, *a, *a, *a, *a]
x-c: &c [*b, *b, *b, *b, *b, *b, *b, *b, *b, *b]
x-d: &d [*c, *c, *c, *c, *c, *c, *c, *c, *c, *c]
x-e: &e [*d, *d, *d, *d, *d, *d, *d, *d, *d, *d]
components:
  examples:
`
		for i := range 50 {
			src += "    E" + itoa(i) + ": {value: [*e]}\n"
		}
		start := time.Now()
		_, diags, err := ParseLimits([]byte(src), Limits{MaxNodes: 20_000, MaxValueBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		if findDiag(diags, CodeInvalidValue) == nil && findDiag(diags, CodeLimit) == nil {
			t.Errorf("no diagnostic for expanded values:%s", diagList(diags))
		}
		if el := time.Since(start); el > 5*time.Second {
			t.Errorf("took %v", el)
		}
	})
	t.Run("large document", func(t *testing.T) {
		t.Parallel()
		var b strings.Builder
		b.WriteString("openapi: 3.1.0\ninfo: {title: T, version: '1'}\npaths:\n")
		for i := range 2000 {
			n := itoa(i)
			b.WriteString("  /r" + n + "/{id}:\n    parameters: [{$ref: '#/components/parameters/id'}]\n    get:\n      responses:\n        '200':\n          description: ok\n          content:\n            application/json:\n              schema: {$ref: '#/components/schemas/S" + n + "'}\n")
		}
		b.WriteString("components:\n  parameters:\n    id: {name: id, in: path, required: true, schema: {type: string}}\n  schemas:\n")
		for i := range 2000 {
			b.WriteString("    S" + itoa(i) + ": {type: object, properties: {next: {$ref: '#/components/schemas/S" + itoa((i+1)%2000) + "'}, all: {type: array, items: {$ref: '#/components/schemas/S0'}}}}\n")
		}
		start := time.Now()
		d, diags := parseOK(t, b.String())
		if len(diags) > 0 {
			t.Errorf("%d diagnostics:%s", len(diags), diagList(diags[:min(3, len(diags))]))
		}
		if len(d.Paths) != 2000 || !schemaOf(t, d, "S1999").Recursive {
			t.Error("model")
		}
		if el := time.Since(start); el > 10*time.Second {
			t.Errorf("took %v", el)
		}
	})
	t.Run("value bytes", func(t *testing.T) {
		t.Parallel()
		src := "openapi: 3.0.3\ninfo: {title: T, version: '1'}\npaths: {}\ncomponents:\n  examples:\n"
		for i := range 10 {
			src += "    E" + itoa(i) + ": {value: '" + strings.Repeat("v", 100) + "'}\n"
		}
		d, diags, err := ParseLimits([]byte(src), Limits{MaxValueBytes: 300})
		if err != nil {
			t.Fatal(err)
		}
		if findDiag(diags, CodeLimit) == nil {
			t.Errorf("no limit diagnostic:%s", diagList(diags))
		}
		if e, _ := d.Components.Examples.Get("E9"); e.Value != nil {
			t.Error("value kept over the limit")
		}
	})
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

func TestServerExpand(t *testing.T) {
	t.Parallel()
	s := Server{URL: "{scheme}://{host}/v1/{unknown}", Variables: Entries[*ServerVariable]{
		{"scheme", &ServerVariable{Default: "https"}},
		{"host", &ServerVariable{Default: "api.example.com"}},
	}}
	if got := s.DefaultURL(); got != "https://api.example.com/v1/{unknown}" {
		t.Errorf("DefaultURL = %q", got)
	}
	if got := s.Expand(map[string]string{"host": "localhost:8080", "unknown": "x"}); got != "https://localhost:8080/v1/x" {
		t.Errorf("Expand = %q", got)
	}
	if got := (Server{URL: "http://a/{open"}).DefaultURL(); got != "http://a/{open" {
		t.Errorf("unterminated = %q", got)
	}
}

func TestTypeErrorsAreDiagnostics(t *testing.T) {
	t.Parallel()
	d, diags := parseOK(t, `openapi: 3.0.3
info: [not, an, object]
servers: {url: x}
paths:
  /a:
    get:
      tags: pets
      deprecated: "true"
      parameters: {name: x}
      responses:
        '200':
          description: [a]
          content:
            application/json:
              schema: 5
              examples: []
components:
  schemas:
    S: {type: string, minLength: -1, maximum: abc, required: true}
`)
	for _, want := range []string{
		"info must be an object",
		"servers must be an array",
		"tags must be an array",
		"deprecated must be a boolean, not a string",
		"parameters must be an array",
		"description must be a string",
		"A schema must be an object",
		"examples must be an object",
		"minLength must be a non-negative integer",
		"maximum must be a number",
		"required in a schema must be an array",
	} {
		found := false
		for _, dg := range diags {
			if strings.Contains(dg.Message, want) && dg.Pos.Line > 0 {
				found = true
			}
		}
		if !found {
			t.Errorf("no diagnostic %q in:%s", want, diagList(diags))
		}
	}
	if op := d.Paths[0].Operations[0]; !op.Deprecated || op.Tags != nil {
		t.Errorf("op = %+v", op)
	}
}
