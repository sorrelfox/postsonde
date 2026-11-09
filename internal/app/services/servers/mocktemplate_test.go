package servers

import (
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
)

func TestMockTemplated(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	cfg := &MockHTTPConfigDTO{Routes: []MockRouteDTO{{
		Enabled: true, Method: "GET", Path: "/users/:id", Status: 200, Templated: true,
		Body: MockBodyDTO{Mode: "raw", Type: "text", Text: "user {{request.params.id}}"},
		Responses: []MockResponseDTO{{
			Name: "raw", Status: 200, Templated: false,
			Body:  MockBodyDTO{Mode: "raw", Type: "text", Text: "user {{request.params.id}}"},
			Rules: []MockRuleDTO{{Source: "query", Name: "raw", Op: "exists"}},
		}},
	}}}
	created, err := svc.Create(t.Context(), CreateInputDTO{Type: mockhttp.TypeName, Config: ConfigDTO{MockHTTP: cfg}})
	if err != nil {
		t.Fatal(err)
	}
	if r := created.Config.MockHTTP.Routes[0]; !r.Templated || r.Responses[0].Templated {
		t.Errorf("templated flags = %v, %v", r.Templated, r.Responses[0].Templated)
	}
	running, err := svc.Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := get(t, running.BoundPort, "/users/7"); status != 200 || body != "user 7" {
		t.Errorf("GET /users/7 = %d %s", status, body)
	}
	if status, body := get(t, running.BoundPort, "/users/7?raw"); status != 200 || body != "user {{request.params.id}}" {
		t.Errorf("GET /users/7?raw = %d %s", status, body)
	}

	names, err := svc.MockTemplateNames(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	has := func(name, source string) bool {
		return slices.ContainsFunc(names, func(n MockTemplateNameDTO) bool {
			return n.Name == name && n.Source == source && n.Description != ""
		})
	}
	if !has("request.params.", MockTemplateRequest) || !has("request.method", MockTemplateRequest) ||
		!has("$uuid", MockTemplateDynamic) || !has("$randomFirstName", MockTemplateDynamic) {
		t.Errorf("names = %+v", names)
	}
}
