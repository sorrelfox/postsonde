package servers

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Sources of a MockTemplateNameDTO.
const (
	// MockTemplateRequest is a request value (request.method...).
	MockTemplateRequest = "request"
	// MockTemplateDynamic is a dynamic value ($uuid, fake data...).
	MockTemplateDynamic = "dynamic"
)

// MockTemplateNameDTO is a name a templated mock response can use
// (MOCK-06), for the body editor's completion and chips.
type MockTemplateNameDTO struct {
	// Name is the name as written between the braces. A name ending in
	// "." takes a parameter, header or JSON path after it
	// ("request.query." then "page").
	Name        string `json:"name"`
	Description string `json:"description"`
	// Source is "request" or "dynamic".
	Source string `json:"source"`
}

// MockTemplateNames returns the names a templated mock response can use:
// the request's values, then every dynamic value, as mockhttp resolves
// them. Environment and project variables are not among them.
func (s *ServersService) MockTemplateNames(ctx context.Context) ([]MockTemplateNameDTO, error) {
	req := mockhttp.RequestNames()
	dyn := template.DynamicNames()
	out := make([]MockTemplateNameDTO, 0, len(req)+len(dyn))
	for _, n := range req {
		out = append(out, MockTemplateNameDTO{Name: n.Name, Description: n.Description, Source: MockTemplateRequest})
	}
	for _, n := range dyn {
		out = append(out, MockTemplateNameDTO{Name: n, Description: template.DynamicDescription(n), Source: MockTemplateDynamic})
	}
	return out, nil
}
