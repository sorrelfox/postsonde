package openapi

import (
	"errors"
	"os"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// FuzzParse parses random input. Parse must not panic or loop; an error
// must be a coded *errs.Error; diagnostics must have valid positions; and
// the model, cycles included, must be walkable.
func FuzzParse(f *testing.F) {
	for _, name := range []string{"petstore-2.0.yaml", "petstore-2.0.json", "petstore-3.0.yaml", "petstore-3.0.json", "petstore-3.1.yaml", "petstore-3.1.json"} {
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, s := range []string{
		"openapi: 3.1.0\ncomponents:\n  schemas:\n    A: {$ref: '#/components/schemas/B'}\n    B: {$ref: '#/components/schemas/A'}\n",
		"openapi: 3.0.0\npaths:\n  /a/{x}:\n    get:\n      parameters: [{$ref: '#/paths/~1a~1{x}/get/parameters/0'}]\n",
		"openapi: 3.1.0\ncomponents:\n  schemas:\n    N: {properties: {n: {$ref: '#/components/schemas/N'}, m: {$ref: '#'}}}\n",
		`{"openapi":"3.1.0","webhooks":{"w":{"$ref":"#/webhooks/w"}},"components":{"pathItems":{"p":{"get":{"callbacks":{"c":{"x":{"$ref":"#/components/pathItems/p"}}}}}}}}`,
		"openapi: 3.0.0\nx: &a {a: 1}\npaths:\n  /p: {<<: *a, get: {responses: {'200': {$ref: '#/x/a'}}}}\n",
		"openapi: 3.1.0\ncomponents: {schemas: {A: {$anchor: a, $ref: '#a', type: string}}}\n",
		"swagger: '2.0'\n",
		"swagger: '2.0'\nx: &p {name: a, in: body, schema: {$ref: '#/definitions/A'}}\npaths: {/a: {parameters: [*p], get: {parameters: [{$ref: '#/paths/~1a/parameters/0'}, {in: formData, name: f, type: file}]}}}\ndefinitions: {A: {$ref: '#/definitions/A'}}\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := Limits{MaxDepth: 50, MaxNodes: 5_000, MaxRefs: 5_000, MaxValueBytes: 1 << 16}
		d, diags, err := ParseLimits(data, lim)
		if err != nil {
			var e *errs.Error
			if !errors.As(err, &e) || e.Code == "" || e.Message == "" {
				t.Fatalf("bad error %T %v", err, err)
			}
			return
		}
		for _, dg := range diags {
			if dg.Pos.Line < 0 || dg.Pos.Column < 0 || dg.Message == "" || dg.Code == "" {
				t.Fatalf("bad diagnostic %+v", dg)
			}
		}
		_ = dump(d, true)
	})
}
