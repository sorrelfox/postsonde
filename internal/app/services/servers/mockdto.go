package servers

import (
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/graphqlmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
)

// The configuration DTOs of the server types that have one: the HTTP mock
// (MOCK-01, MOCK-02, MOCK-04, MOCK-05, MOCK-07). See docs/servers.md.

// ConfigDTO is a server's own configuration. Only the field of the
// server's type is used; the others are null.
type ConfigDTO struct {
	// MockHTTP is the configuration of a mock_http server.
	MockHTTP *MockHTTPConfigDTO `json:"mockHttp"`
	// WebSocket is the configuration of a websocket server.
	WebSocket *WebSocketConfigDTO `json:"webSocket"`
	// Webhook is the response of a webhook receiver.
	Webhook *WebhookConfigDTO `json:"webhook"`
	// SSE is the configuration of an sse server (left out for other
	// types).
	SSE *SSEConfigDTO `json:"sse,omitempty"`
	// UDP is the configuration of a udp server (left out for other
	// types).
	UDP *UDPConfigDTO `json:"udp,omitempty"`
	// GRPCMock is the configuration of a grpc_mock server (left out for
	// other types).
	GRPCMock *GRPCMockConfigDTO `json:"grpcMock,omitempty"`
	// SocketIO is the configuration of a socketio server (left out for
	// other types).
	SocketIO *SocketIOConfigDTO `json:"socketIo,omitempty"`
	// GraphQLMock is the configuration of a graphql_mock server (left out
	// for other types).
	GraphQLMock *GraphQLMockConfigDTO `json:"graphqlMock,omitempty"`
}

// MockHTTPConfigDTO is the routes of a mock server.
type MockHTTPConfigDTO struct {
	Routes []MockRouteDTO `json:"routes"`
	// StrictTrailingSlash: /users and /users/ are different paths.
	StrictTrailingSlash bool `json:"strictTrailingSlash"`
	// CaseInsensitive: literal path segments ignore letter case.
	CaseInsensitive bool `json:"caseInsensitive"`
}

// MockRouteDTO is one route: a method and path pattern, and its response.
type MockRouteDTO struct {
	// ID identifies the route; "" gets a new one.
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// Method is an HTTP method or "ANY".
	Method string `json:"method"`
	// Path is a pattern: "/users/:id", "/users/{id:[0-9]+}", "/files/**".
	Path string `json:"path"`
	// Status is 200–599.
	Status  int             `json:"status"`
	Headers []MockHeaderDTO `json:"headers"`
	Body    MockBodyDTO     `json:"body"`
	Delay   MockDelayDTO    `json:"delay"`
	// Responses are the route's other responses, tried in order before
	// status, headers and body (the default response).
	Responses []MockResponseDTO `json:"responses"`
	// Templated renders the default response's body and header values
	// with the request's values and dynamic values (MOCK-06).
	Templated bool `json:"templated"`
}

// MockResponseDTO is one of a route's other responses (MOCK-05, MOCK-07).
type MockResponseDTO struct {
	// Name selects it with the X-PostSonde-Response request header; unique
	// within the route, not "default"; "" cannot be selected.
	Name    string          `json:"name"`
	Status  int             `json:"status"`
	Headers []MockHeaderDTO `json:"headers"`
	Body    MockBodyDTO     `json:"body"`
	// Rules must all match; a response without rules answers only when
	// the header names it.
	Rules []MockRuleDTO `json:"rules"`
	// Templated renders the body and header values with the request's
	// values and dynamic values (MOCK-06).
	Templated bool `json:"templated"`
}

// MockRuleDTO is one condition of a response.
type MockRuleDTO struct {
	// Source is "query", "header", "body" or "json" (a JSONPath into the
	// request body).
	Source string `json:"source"`
	// Name is the query parameter, header name or JSONPath; unused for
	// "body".
	Name string `json:"name"`
	// Op is "equals", "not_equals", "contains", "not_contains", "matches",
	// "exists" or "not_exists".
	Op    string `json:"op"`
	Value string `json:"value"`
}

// MockHeaderDTO is one response header row.
type MockHeaderDTO struct {
	Enabled bool   `json:"enabled"`
	Name    string `json:"name"`
	Value   string `json:"value"`
}

// MockBodyDTO is the response body.
type MockBodyDTO struct {
	// Mode is "none" or "raw".
	Mode string `json:"mode"`
	// Type is the raw body's language ("json", "xml", "html", "text",
	// "yaml"), which gives the Content-Type unless a header sets one;
	// "base64" is a binary body written in Base64, sent decoded.
	Type string `json:"type"`
	Text string `json:"text"`
}

// MockDelayDTO holds the response back.
type MockDelayDTO struct {
	// Mode is "none", "fixed" (ms) or "random" (minMs to maxMs, both
	// included). Delays are at most 300000 ms.
	Mode  string `json:"mode"`
	Ms    int    `json:"ms"`
	MinMs int    `json:"minMs"`
	MaxMs int    `json:"maxMs"`
}

// RouteDiagnosticDTO is a problem with a route; a route with one is not
// served.
type RouteDiagnosticDTO struct {
	// Code is a mockroute.* or mockhttp.* code (docs/mock-routes.md,
	// docs/servers.md).
	Code       string `json:"code"`
	RouteIndex int    `json:"routeIndex"`
	RouteID    string `json:"routeId"`
	// Field is "pattern" (the path), "method", "id", "status", "header",
	// "body", "delay", "name" (a response's) or "rule".
	Field string `json:"field"`
	// Start and End are the UTF-8 byte range [start, end) in the path or
	// method; 0 otherwise.
	Start int `json:"start"`
	End   int `json:"end"`
	// ResponseIndex is the index in the route's responses when the problem
	// is in one of them, else -1 (the route and its default response).
	ResponseIndex int `json:"responseIndex"`
	// HeaderIndex is the header row for field "header", else -1.
	HeaderIndex int `json:"headerIndex"`
	// RuleIndex is the response's rule for field "rule", else -1.
	RuleIndex int    `json:"ruleIndex"`
	Message   string `json:"message"`
}

// RouteMatchDTO is the mock route that answered a request.
type RouteMatchDTO struct {
	ID      string `json:"id"`
	Method  string `json:"method"`
	Pattern string `json:"pattern"`
	// Params are the path parameters with decoded values.
	Params   []HeaderDTO `json:"params"`
	Wildcard string      `json:"wildcard"`
	// Response is the response that answered, on a response event of a
	// route with other responses or a request with X-PostSonde-Response:
	// "default", a response's name or "status 500"; "" otherwise.
	Response string `json:"response"`
}

// configToCore returns the Spec.Config of a server of type typ, or nil.
func configToCore(typ string, c ConfigDTO) any {
	if typ == mockhttp.TypeName && c.MockHTTP != nil {
		return c.MockHTTP.toCore()
	}
	if typ == wsserver.TypeName && c.WebSocket != nil {
		return c.WebSocket.toCore()
	}
	if typ == webhook.TypeName && c.Webhook != nil {
		return c.Webhook.toCore()
	}
	if typ == sseserver.TypeName && c.SSE != nil {
		return c.SSE.toCore()
	}
	if typ == udpserver.TypeName && c.UDP != nil {
		return c.UDP.toCore()
	}
	if typ == grpcmock.TypeName && c.GRPCMock != nil {
		return c.GRPCMock.toCore()
	}
	if typ == sioserver.TypeName && c.SocketIO != nil {
		return c.SocketIO.toCore()
	}
	if typ == graphqlmock.TypeName && c.GraphQLMock != nil {
		return c.GraphQLMock.toCore()
	}
	return nil
}

func (c MockHTTPConfigDTO) toCore() mockhttp.Config {
	out := mockhttp.Config{
		StrictTrailingSlash: c.StrictTrailingSlash,
		CaseInsensitive:     c.CaseInsensitive,
		Routes:              make([]mockhttp.Route, 0, len(c.Routes)),
	}
	for _, r := range c.Routes {
		route := mockhttp.Route{
			ID: r.ID, Enabled: r.Enabled, Method: r.Method, Path: r.Path, Status: r.Status, Templated: r.Templated,
			Headers: make([]mockhttp.Header, 0, len(r.Headers)),
			Body:    mockhttp.Body{Mode: r.Body.Mode, Type: r.Body.Type, Text: r.Body.Text},
			Delay:   mockhttp.Delay{Mode: r.Delay.Mode, Ms: r.Delay.Ms, MinMs: r.Delay.MinMs, MaxMs: r.Delay.MaxMs},
		}
		for _, h := range r.Headers {
			route.Headers = append(route.Headers, mockhttp.Header{Enabled: h.Enabled, Name: h.Name, Value: h.Value})
		}
		for _, alt := range r.Responses {
			resp := mockhttp.Response{
				Name: alt.Name, Status: alt.Status, Templated: alt.Templated,
				Headers: make([]mockhttp.Header, 0, len(alt.Headers)),
				Body:    mockhttp.Body{Mode: alt.Body.Mode, Type: alt.Body.Type, Text: alt.Body.Text},
				Rules:   make([]mockhttp.Rule, 0, len(alt.Rules)),
			}
			for _, h := range alt.Headers {
				resp.Headers = append(resp.Headers, mockhttp.Header{Enabled: h.Enabled, Name: h.Name, Value: h.Value})
			}
			for _, ru := range alt.Rules {
				resp.Rules = append(resp.Rules, mockhttp.Rule{Source: ru.Source, Name: ru.Name, Op: ru.Op, Value: ru.Value})
			}
			route.Responses = append(route.Responses, resp)
		}
		out.Routes = append(out.Routes, route)
	}
	return out
}

// toConfigDTO maps a Spec.Config; nil and unknown configurations are an
// empty ConfigDTO.
func toConfigDTO(config any) ConfigDTO {
	if ws, ok := config.(wsserver.Config); ok {
		return ConfigDTO{WebSocket: toWebSocketConfigDTO(ws)}
	}
	if wh, ok := config.(webhook.Config); ok {
		return ConfigDTO{Webhook: toWebhookConfigDTO(wh)}
	}
	if c, ok := config.(sseserver.Config); ok {
		return ConfigDTO{SSE: toSSEConfigDTO(c)}
	}
	if c, ok := config.(udpserver.Config); ok {
		return ConfigDTO{UDP: toUDPConfigDTO(c)}
	}
	if c, ok := config.(grpcmock.Config); ok {
		return ConfigDTO{GRPCMock: toGRPCMockConfigDTO(c)}
	}
	if c, ok := config.(sioserver.Config); ok {
		return ConfigDTO{SocketIO: toSocketIOConfigDTO(c)}
	}
	if c, ok := config.(graphqlmock.Config); ok {
		return ConfigDTO{GraphQLMock: toGraphQLMockConfigDTO(c)}
	}
	c, ok := config.(mockhttp.Config)
	if !ok {
		return ConfigDTO{}
	}
	out := &MockHTTPConfigDTO{
		StrictTrailingSlash: c.StrictTrailingSlash,
		CaseInsensitive:     c.CaseInsensitive,
		Routes:              make([]MockRouteDTO, 0, len(c.Routes)),
	}
	for _, r := range c.Routes {
		route := MockRouteDTO{
			ID: r.ID, Enabled: r.Enabled, Method: r.Method, Path: r.Path, Status: r.Status, Templated: r.Templated,
			Headers: make([]MockHeaderDTO, 0, len(r.Headers)),
			Body:    MockBodyDTO{Mode: r.Body.Mode, Type: r.Body.Type, Text: r.Body.Text},
			Delay:   MockDelayDTO{Mode: r.Delay.Mode, Ms: r.Delay.Ms, MinMs: r.Delay.MinMs, MaxMs: r.Delay.MaxMs},
		}
		for _, h := range r.Headers {
			route.Headers = append(route.Headers, MockHeaderDTO{Enabled: h.Enabled, Name: h.Name, Value: h.Value})
		}
		route.Responses = make([]MockResponseDTO, 0, len(r.Responses))
		for _, alt := range r.Responses {
			resp := MockResponseDTO{
				Name: alt.Name, Status: alt.Status, Templated: alt.Templated,
				Headers: make([]MockHeaderDTO, 0, len(alt.Headers)),
				Body:    MockBodyDTO{Mode: alt.Body.Mode, Type: alt.Body.Type, Text: alt.Body.Text},
				Rules:   make([]MockRuleDTO, 0, len(alt.Rules)),
			}
			for _, h := range alt.Headers {
				resp.Headers = append(resp.Headers, MockHeaderDTO{Enabled: h.Enabled, Name: h.Name, Value: h.Value})
			}
			for _, ru := range alt.Rules {
				resp.Rules = append(resp.Rules, MockRuleDTO{Source: ru.Source, Name: ru.Name, Op: ru.Op, Value: ru.Value})
			}
			route.Responses = append(route.Responses, resp)
		}
		out.Routes = append(out.Routes, route)
	}
	return ConfigDTO{MockHTTP: out}
}

// toDiagnosticDTOs checks a Spec.Config; always a non-nil slice.
func toDiagnosticDTOs(config any) []RouteDiagnosticDTO {
	out := []RouteDiagnosticDTO{}
	c, ok := config.(mockhttp.Config)
	if !ok {
		return out
	}
	for _, d := range mockhttp.Check(c) {
		out = append(out, RouteDiagnosticDTO{
			Code: d.Code, RouteIndex: d.RouteIndex, RouteID: d.RouteID, Field: d.Field,
			Start: d.Start, End: d.End, ResponseIndex: d.ResponseIndex, HeaderIndex: d.HeaderIndex, RuleIndex: d.RuleIndex, Message: d.Message,
		})
	}
	return out
}

// defaultConfigDTO is the configuration a new server of typ starts with.
func defaultConfigDTO(typ core.Type) ConfigDTO {
	ct, ok := typ.(core.ConfigurableType)
	if !ok {
		return ConfigDTO{}
	}
	c, err := ct.NormalizeConfig(nil)
	if err != nil {
		return ConfigDTO{}
	}
	return toConfigDTO(c)
}

func toRouteMatchDTO(m *core.RouteMatch) *RouteMatchDTO {
	if m == nil {
		return nil
	}
	out := &RouteMatchDTO{ID: m.ID, Method: m.Method, Pattern: m.Pattern, Wildcard: m.Wildcard, Response: m.Response, Params: make([]HeaderDTO, 0, len(m.Params))}
	for _, p := range m.Params {
		out.Params = append(out.Params, HeaderDTO{Name: p.Name, Value: p.Value})
	}
	return out
}
