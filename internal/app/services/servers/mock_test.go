package servers

import (
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockroute"
)

func get(t *testing.T, port int, path string) (int, string) {
	t.Helper()
	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestMockServer(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)

	// Without a configuration, a mock server gets the example route.
	def, err := svc.Create(t.Context(), CreateInputDTO{Type: mockhttp.TypeName})
	if err != nil {
		t.Fatal(err)
	}
	if def.Config.MockHTTP == nil || len(def.Config.MockHTTP.Routes) != 1 || def.Config.MockHTTP.Routes[0].Path != "/hello" {
		t.Errorf("default config = %+v", def.Config)
	}

	cfg := &MockHTTPConfigDTO{Routes: []MockRouteDTO{{
		Enabled: true, Method: "GET", Path: "/users/:id", Status: 200,
		Headers: []MockHeaderDTO{{Enabled: true, Name: "X-Mock", Value: "1"}},
		Body:    MockBodyDTO{Mode: "raw", Type: "json", Text: `{"ok":true}`},
	}}}
	created, err := svc.Create(t.Context(), CreateInputDTO{Type: mockhttp.TypeName, Config: ConfigDTO{MockHTTP: cfg}})
	if err != nil {
		t.Fatal(err)
	}
	if r := created.Config.MockHTTP.Routes[0]; r.ID == "" || r.Delay.Mode != "none" || len(created.Diagnostics) != 0 {
		t.Errorf("created = %+v", created)
	}
	running, err := svc.Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := get(t, running.BoundPort, "/users/7"); status != 200 || body != `{"ok":true}` {
		t.Errorf("GET /users/7 = %d %s", status, body)
	}
	rec.waitTraffic(t, "started,connection_open,request,response")
	rec.mu.Lock()
	resp := rec.traffic[3]
	rec.mu.Unlock()
	if resp.HTTP == nil || resp.HTTP.Route == nil || resp.HTTP.Route.Pattern != "/users/:id" || resp.HTTP.Route.Params[0].Value != "7" {
		t.Errorf("response traffic = %+v", resp.HTTP)
	}

	next := created.Config.MockHTTP
	next.Routes = append(next.Routes, MockRouteDTO{ID: "rt_new", Enabled: true, Method: "GET", Path: "/new/{x", Status: 700})
	next.Routes[0].Status = 202
	updated, err := svc.UpdateConfig(t.Context(), created.ID, ConfigDTO{MockHTTP: next})
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != "running" || len(updated.Diagnostics) != 2 ||
		updated.Diagnostics[0].Code != mockroute.CodeInvalidSegment || updated.Diagnostics[0].RouteID != "rt_new" ||
		updated.Diagnostics[1].Code != mockhttp.CodeInvalidStatus {
		t.Errorf("updated = %+v", updated.Diagnostics)
	}
	if status, _ := get(t, running.BoundPort, "/users/7"); status != 202 {
		t.Errorf("GET after update = %d", status)
	}
	list, _ := svc.List(t.Context())
	if len(list[1].Diagnostics) != 2 || len(list[1].Config.MockHTTP.Routes) != 2 {
		t.Errorf("listed = %+v", list[1])
	}

	if _, err := svc.UpdateConfig(t.Context(), created.ID, ConfigDTO{}); codeOf(err) != mockhttp.CodeInvalidConfig {
		t.Errorf("UpdateConfig without routes = %v", err)
	}
	if _, err := svc.UpdateConfig(t.Context(), created.ID, ConfigDTO{MockHTTP: &MockHTTPConfigDTO{Routes: make([]MockRouteDTO, mockhttp.MaxRoutes+1)}}); codeOf(err) != mockhttp.CodeTooManyRoutes {
		t.Errorf("UpdateConfig with too many routes = %v", err)
	}
	if _, err := svc.UpdateConfig(t.Context(), "srv_missing", ConfigDTO{}); err == nil {
		t.Error("UpdateConfig on a missing server succeeded")
	}
}

func TestMockConditionalResponsesEndToEnd(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t)
	cfg := &MockHTTPConfigDTO{Routes: []MockRouteDTO{{
		Enabled: true, Method: "POST", Path: "/orders", Status: 201,
		Body: MockBodyDTO{Mode: "raw", Type: "json", Text: `{"created":true}`},
		Responses: []MockResponseDTO{
			{Name: "rejected", Status: 422, Body: MockBodyDTO{Mode: "raw", Type: "json", Text: `{"error":"too big"}`},
				Rules: []MockRuleDTO{{Source: "json", Name: "$.qty", Op: "equals", Value: "1000"}}},
			{Name: "dry-run", Status: 200, Body: MockBodyDTO{Mode: "raw", Type: "text", Text: "dry run"},
				Rules: []MockRuleDTO{{Source: "query", Name: "dry", Op: "exists"}}},
			{Name: "outage", Status: 503, Body: MockBodyDTO{Mode: "none"}},
		},
	}}}
	created, err := svc.Create(t.Context(), CreateInputDTO{Type: mockhttp.TypeName, Config: ConfigDTO{MockHTTP: cfg}})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Diagnostics) != 0 || len(created.Config.MockHTTP.Routes[0].Responses) != 3 {
		t.Fatalf("created = %+v", created)
	}
	running, err := svc.Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(running.BoundPort))
	post := func(path, body string, header map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("POST", base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	tests := []struct {
		path, body string
		header     map[string]string
		status     int
		want       string
	}{
		{"/orders", `{"qty": 1}`, nil, 201, `{"created":true}`},
		{"/orders", `{"qty": 1000}`, nil, 422, `{"error":"too big"}`},
		{"/orders?dry", `{"qty": 1}`, nil, 200, "dry run"},
		{"/orders", `{"qty": 1000}`, map[string]string{mockhttp.SelectHeader: "outage"}, 503, ""},
		{"/orders", `{}`, map[string]string{mockhttp.SelectHeader: "500"}, 500, ""},
		{"/orders", `{}`, map[string]string{mockhttp.SelectHeader: "missing"}, 400, ""},
	}
	for _, tt := range tests {
		status, body := post(tt.path, tt.body, tt.header)
		if status != tt.status || (tt.status != 400 && body != tt.want) {
			t.Errorf("POST %s %s %v = %d %q, want %d %q", tt.path, tt.body, tt.header, status, body, tt.status, tt.want)
		}
		if tt.status == 400 && !strings.Contains(body, `no response \"missing\"`) {
			t.Errorf("unknown response body = %s", body)
		}
	}
	var names []string
	deadline := time.Now().Add(5 * time.Second)
	for len(names) < len(tests) && time.Now().Before(deadline) {
		names = names[:0]
		rec.mu.Lock()
		for _, tr := range rec.traffic {
			if tr.Kind == "response" && tr.HTTP != nil && tr.HTTP.Route != nil {
				names = append(names, tr.HTTP.Route.Response)
			}
		}
		rec.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	if want := []string{"default", "rejected", "dry-run", "outage", "status 500", ""}; !slices.Equal(names, want) {
		t.Errorf("responses in traffic = %q, want %q", names, want)
	}

	// A bad rule is a diagnostic with the response and rule index.
	next := created.Config.MockHTTP
	next.Routes[0].Responses[1].Rules = append(next.Routes[0].Responses[1].Rules, MockRuleDTO{Source: "json", Name: "$[", Op: "exists"})
	updated, err := svc.UpdateConfig(t.Context(), created.ID, ConfigDTO{MockHTTP: next})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v", updated.Diagnostics)
	}
	if d := updated.Diagnostics[0]; d.Code != mockhttp.CodeInvalidRule || d.Field != mockhttp.FieldRule || d.ResponseIndex != 1 || d.RuleIndex != 1 || d.HeaderIndex != -1 {
		t.Errorf("diagnostic = %+v", d)
	}
}
