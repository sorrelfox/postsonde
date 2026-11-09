package servers

import (
	"context"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockexample"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// A mock route from a response (MOCK-03): MockRouteFromExample
// makes the route of a response example (or a history entry the UI made
// into one with HistoryService.Example) and merges it into the routes
// being edited, for a preview; the UI applies the returned configuration
// with UpdateConfig, or creates a server with it. See
// docs/mock-routes.md#routes-from-a-response.

// Secrets returns the secret values PostSonde keeps (the open project's
// and the app's, as logexport reads them). Values may come with an error
// for those that could not be read; they are used, and the error logged.
type Secrets func(ctx context.Context) ([]string, error)

// WithSecrets masks the routes MockRouteFromExample makes with the values
// secrets returns. Without it only Authorization and
// Proxy-Authorization are masked, by name.
func WithSecrets(secrets Secrets) Option {
	return func(s *ServersService) { s.knownSecrets = secrets }
}

// MockExampleInputDTO asks for the route of a response.
type MockExampleInputDTO struct {
	// Example is the response: a request's response example, or a history
	// entry as HistoryService.Example returns it.
	Example collection.ResponseExampleDTO `json:"example"`
	// Method and URL are the request's, as typed ("{{baseUrl}}/users/:id"):
	// they give the route's method and path.
	Method string `json:"method"`
	URL    string `json:"url"`
	// Path, when not null, is the route's path pattern instead of the one
	// made from the URL.
	Path *string `json:"path"`
	// Mode is "route" (the default: a new route, nothing when a route
	// answers the same method and path), "response" (a named response of
	// that route) or "replace" (that route's own response).
	Mode string `json:"mode"`
	// Name is the name of the response added in "response" mode; blank
	// uses the example's.
	Name string `json:"name"`
	// Config is the mock server's routes now, as the editor has them;
	// empty routes for a new server.
	Config MockHTTPConfigDTO `json:"config"`
}

// MockExampleResultDTO is the preview of the route.
type MockExampleResultDTO struct {
	// Method and Path are the route's; DerivedPath is the path made from
	// the URL (Path when the input gave none).
	Method      string `json:"method"`
	Path        string `json:"path"`
	DerivedPath string `json:"derivedPath"`
	// Response is what the route sends: status, headers and body.
	Response MockResponseDTO `json:"response"`
	// Dropped are the response's header fields left out of the route.
	Dropped []MockDroppedHeaderDTO `json:"dropped"`
	// Notes say what differs from the response and why.
	Notes []MockExampleNoteDTO `json:"notes"`
	// Masked says secrets were hidden.
	Masked bool `json:"masked"`
	// Existing is the route in Config that answers the same method and
	// path; null when there is none.
	Existing *MockExistingRouteDTO `json:"existing"`
	// Status is "added", "existing" (mode route and a route answers the
	// method and path: nothing added), "response_added", "replaced" or
	// "over_limit".
	Status string `json:"status"`
	// RouteID is the ID of the route added or changed, "" when none.
	RouteID string `json:"routeId"`
	// ResponseName is the name of the response added ("response").
	ResponseName string `json:"responseName"`
	// Diagnostics are the problems of the route added or changed; such a
	// route is not served until they are fixed.
	Diagnostics []RouteDiagnosticDTO `json:"diagnostics"`
	// Config is the server's routes with the route merged in: what
	// UpdateConfig (or Create) should get when the user applies.
	Config MockHTTPConfigDTO `json:"config"`
}

// MockDroppedHeaderDTO is a header field left out of the route.
type MockDroppedHeaderDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Reason is "hop_by_hop", "content_length", "content_encoding",
	// "date" or "invalid".
	Reason string `json:"reason"`
}

// MockExampleNoteDTO is a note about the route.
type MockExampleNoteDTO struct {
	// Severity is "warning" or "info".
	Severity string `json:"severity"`
	// Code is a mockexample.* code.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MockExistingRouteDTO is the route that answers the same method and
// path.
type MockExistingRouteDTO struct {
	Index  int    `json:"index"`
	ID     string `json:"id"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

// MockRouteFromExample makes the mock route of a response (MOCK-03): the
// method and path from the request's URL as typed ({{variables}} and
// :params become path parameters, a base URL's path is taken from the URL
// sent), the status, headers and body from the response, masked again
// with every secret PostSonde keeps (WithSecrets). Hop-by-hop fields,
// Content-Length, Date and a Content-Encoding PostSonde removed are left
// out; a body that is not UTF-8 is kept in Base64. The route is merged
// into in.Config by in.Mode. Nothing changes on any server.
//
// Errors: servers.invalid_mode.
func (s *ServersService) MockRouteFromExample(ctx context.Context, in MockExampleInputDTO) (MockExampleResultDTO, error) {
	if err := interrupted(ctx, "Making the mock route"); err != nil {
		return MockExampleResultDTO{}, err
	}
	mode := mockexample.Mode(in.Mode)
	switch mode {
	case "":
		mode = mockexample.ModeRoute
	case mockexample.ModeRoute, mockexample.ModeResponse, mockexample.ModeReplace:
	default:
		return MockExampleResultDTO{}, errs.New(errs.InvalidArgument, strconv.Quote(in.Mode)+" is not a way to add a route; use route, response or replace.").
			WithCode(CodeInvalidMode)
	}
	var values []string
	if s.knownSecrets != nil {
		v, err := s.knownSecrets(ctx)
		if err != nil {
			s.logger.Warn("some secrets could not be read to mask a mock route", "err", err)
		}
		values = v
	}
	res := mockexample.Build(mockexample.Input{
		Example: exampleOf(in.Example),
		Method:  in.Method,
		URL:     in.URL,
		Path:    in.Path,
		Masker:  httpclient.NewSecretMasker(values),
	})
	merged := mockexample.Merge(in.Config.toCore(), res, mode, in.Name)

	out := MockExampleResultDTO{
		Method: res.Method, Path: res.Path, DerivedPath: res.DerivedPath,
		Dropped: make([]MockDroppedHeaderDTO, 0, len(res.Dropped)),
		Notes:   make([]MockExampleNoteDTO, 0, len(res.Notes)),
		Masked:  res.Masked,
		Status:  merged.Status, RouteID: merged.RouteID, ResponseName: merged.ResponseName,
		Diagnostics: []RouteDiagnosticDTO{},
	}
	route := toMockRouteDTO(mockhttp.Route{Status: res.Reply.Status, Headers: res.Reply.Headers, Body: res.Reply.Body})
	out.Response = MockResponseDTO{Name: res.Reply.Name, Status: route.Status, Headers: route.Headers, Body: route.Body, Rules: []MockRuleDTO{}}
	for _, d := range res.Dropped {
		out.Dropped = append(out.Dropped, MockDroppedHeaderDTO{Name: d.Name, Value: d.Value, Reason: d.Reason})
	}
	for _, n := range res.Notes {
		out.Notes = append(out.Notes, MockExampleNoteDTO{Severity: string(n.Severity), Code: n.Code, Message: n.Message})
	}
	if e := merged.Existing; e != nil {
		out.Existing = &MockExistingRouteDTO{Index: e.Index, ID: e.ID, Method: e.Method, Path: e.Path}
	}
	if merged.RouteIndex >= 0 {
		for _, d := range toDiagnosticDTOs(merged.Config) {
			if d.RouteIndex == merged.RouteIndex {
				out.Diagnostics = append(out.Diagnostics, d)
			}
		}
	}
	if cfg := toConfigDTO(merged.Config).MockHTTP; cfg != nil {
		out.Config = *cfg
	}
	s.logger.Info("mock route made from a response", "mode", string(mode), "status", merged.Status, "masked", res.Masked)
	return out, nil
}

// exampleOf is what mockexample needs of a response example.
func exampleOf(e collection.ResponseExampleDTO) mockexample.Example {
	out := mockexample.Example{
		Name:       e.Name,
		StatusCode: e.StatusCode,
		StatusText: e.StatusText,
		Body:       e.Body,
		Base64:     e.BodyEncoding == projectfile.ExampleBodyBase64,
		Clipped:    e.BodyClipped,
		Size:       e.BodySize,
	}
	for _, h := range e.Headers {
		out.Headers = append(out.Headers, mockexample.Header{Name: h.Name, Value: h.Value})
	}
	if r := e.Request; r != nil {
		out.SentMethod, out.SentURL = r.Method, r.URL
	}
	return out
}
