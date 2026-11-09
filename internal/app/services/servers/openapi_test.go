package servers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockopenapi"
)

const openapiFixtures = "../../../core/openapi/testdata"

// concretePath fills a generated pattern's parameters with a value.
func concretePath(pattern string) string {
	segs := strings.Split(pattern, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") {
			segs[i] = "42"
		}
	}
	return strings.Join(segs, "/")
}

func do(t *testing.T, port int, method, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port))+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// A mock server made from each petstore answers every documented operation
// with its route's status and example body.
func TestGenerateMockRoutesEndToEnd(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"petstore-3.0.yaml", "petstore-3.1.json", "petstore-2.0.yaml"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, _ := newTestService(t)
			def, err := svc.Create(t.Context(), CreateInputDTO{Type: mockhttp.TypeName})
			if err != nil {
				t.Fatal(err)
			}
			res, err := svc.GenerateMockRoutes(t.Context(), MockOpenAPIInputDTO{
				Path: filepath.Join(openapiFixtures, name), Config: *def.Config.MockHTTP,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Routes) == 0 || res.Added != len(res.Routes) || res.Kept != 1 || res.Removed != 0 ||
				len(res.Config.Routes) != len(res.Routes)+1 || res.Config.Routes[0].Path != "/hello" {
				t.Fatalf("merge into the default route: %d routes, added %d, kept %d, config %d", len(res.Routes), res.Added, res.Kept, len(res.Config.Routes))
			}
			for _, d := range res.Diagnostics {
				if d.Severity == "error" {
					t.Errorf("error diagnostic: %+v", d)
				}
			}

			updated, err := svc.UpdateConfig(t.Context(), def.ID, ConfigDTO{MockHTTP: &res.Config})
			if err != nil {
				t.Fatal(err)
			}
			if len(updated.Diagnostics) != 0 {
				t.Fatalf("generated routes have problems: %+v", updated.Diagnostics)
			}
			running, err := svc.Start(t.Context(), def.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range res.Routes {
				resp, body := do(t, running.BoundPort, r.Route.Method, concretePath(r.Route.Path))
				if resp.StatusCode != r.Route.Status {
					t.Errorf("%s (%s %s): status %d, want %d", r.Operation, r.Route.Method, r.Route.Path, resp.StatusCode, r.Route.Status)
					continue
				}
				if resp.StatusCode == http.StatusNoContent || r.Route.Body.Mode == "none" {
					continue
				}
				if body != r.Route.Body.Text {
					t.Errorf("%s: body %q, want %q", r.Operation, body, r.Route.Body.Text)
				}
				if r.Route.Body.Type == "json" && body != "" && !json.Valid([]byte(body)) {
					t.Errorf("%s: body is not JSON: %s", r.Operation, body)
				}
				if ct := resp.Header.Get("Content-Type"); r.MediaType != "" && !strings.HasPrefix(ct, strings.Split(r.MediaType, ";")[0]) {
					t.Errorf("%s: Content-Type %q for %s", r.Operation, ct, r.MediaType)
				}
			}
			// A path the document does not have is still a 404.
			if resp, _ := do(t, running.BoundPort, "GET", "/nowhere"); resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET /nowhere = %d", resp.StatusCode)
			}
		})
	}
}

func TestGenerateMockRoutesMergeAndReplace(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	path := filepath.Join(openapiFixtures, "petstore-3.0.yaml")

	mine := MockHTTPConfigDTO{Routes: []MockRouteDTO{{
		ID: "rt_mine", Enabled: true, Method: "GET", Path: "/v1/pets/:id", Status: 418,
		Headers: []MockHeaderDTO{}, Body: MockBodyDTO{Mode: "none"}, Delay: MockDelayDTO{Mode: "none"},
	}}}
	res, err := svc.GenerateMockRoutes(t.Context(), MockOpenAPIInputDTO{Path: path, Mode: "merge", Config: mine})
	if err != nil {
		t.Fatal(err)
	}
	if res.Prefix != "/v1" || res.DefaultPrefix != "/v1" || res.Title != "Petstore 3.0" || res.OpenAPI != "3.0.3" {
		t.Errorf("result = %+v", res)
	}
	existing := 0
	for _, r := range res.Routes {
		if r.Status == mockopenapi.StatusExisting {
			existing++
			if r.Operation != "GET /pets/{petId}" || r.ID != "" {
				t.Errorf("existing route %+v", r)
			}
		}
	}
	if existing != 1 || res.Kept != 1 || res.Config.Routes[0].Status != 418 || res.Added != len(res.Routes)-1 {
		t.Errorf("merge: existing %d, kept %d, added %d", existing, res.Kept, res.Added)
	}
	found := false
	for _, d := range res.Diagnostics {
		if d.Code == mockopenapi.CodeExistingRoute && d.Operation == "GET /pets/{petId}" {
			found = true
		}
	}
	if !found {
		t.Errorf("no existing_route diagnostic: %+v", res.Diagnostics)
	}

	// Generating again into the result adds nothing.
	again, err := svc.GenerateMockRoutes(t.Context(), MockOpenAPIInputDTO{Path: path, Config: res.Config})
	if err != nil || again.Added != 0 || len(again.Config.Routes) != len(res.Config.Routes) {
		t.Errorf("again: added %d, err %v", again.Added, err)
	}

	// Replace drops the existing routes; a prefix moves every route.
	prefix := "/mock"
	rep, err := svc.GenerateMockRoutes(t.Context(), MockOpenAPIInputDTO{Path: path, Mode: "replace", Prefix: &prefix, Config: res.Config})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != len(res.Config.Routes) || rep.Kept != 0 || len(rep.Config.Routes) != len(rep.Routes) || rep.Prefix != "/mock" {
		t.Errorf("replace: removed %d kept %d routes %d prefix %q", rep.Removed, rep.Kept, len(rep.Config.Routes), rep.Prefix)
	}
	for _, r := range rep.Config.Routes {
		if !strings.HasPrefix(r.Path, "/mock/") {
			t.Errorf("route %s has no prefix", r.Path)
		}
	}
}

func TestGenerateMockRoutesErrors(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	dir := t.TempDir()

	if _, err := svc.GenerateMockRoutes(t.Context(), MockOpenAPIInputDTO{Path: filepath.Join(openapiFixtures, "petstore-3.0.yaml"), Mode: "append"}); codeOf(err) != CodeInvalidMode {
		t.Errorf("bad mode: %v", err)
	}
	_, err := svc.GenerateMockRoutes(t.Context(), MockOpenAPIInputDTO{Path: filepath.Join(dir, "missing.yaml")})
	if codeOf(err) != CodeFileUnreadable || errs.KindOf(err) != errs.NotFound {
		t.Errorf("missing file: %v (%s)", err, errs.KindOf(err))
	}
	if _, err := svc.GenerateMockRoutes(t.Context(), MockOpenAPIInputDTO{}); codeOf(err) != CodeFileUnreadable {
		t.Errorf("no path: %v", err)
	}
	notOpenAPI := filepath.Join(dir, "x.yaml")
	if err := os.WriteFile(notOpenAPI, []byte("hello: world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GenerateMockRoutes(t.Context(), MockOpenAPIInputDTO{Path: notOpenAPI}); err == nil || !strings.HasPrefix(codeOf(err), "openapi.") {
		t.Errorf("not OpenAPI: %v (%s)", err, codeOf(err))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := svc.GenerateMockRoutes(ctx, MockOpenAPIInputDTO{Path: notOpenAPI}); errs.KindOf(err) != errs.Cancelled {
		t.Errorf("cancelled: %v", err)
	}
}

func TestPickOpenAPIFile(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	if _, err := svc.PickOpenAPIFile(t.Context()); codeOf(err) != CodePickUnavailable {
		t.Errorf("without a picker: %v", err)
	}

	var got FileDialog
	WithFilePicker(func(_ context.Context, d FileDialog) (string, error) {
		got = d
		return "/tmp/api.yaml", nil
	})(svc)
	path, err := svc.PickOpenAPIFile(t.Context())
	if err != nil || path != "/tmp/api.yaml" || got.FilterPattern == "" || got.Title == "" {
		t.Errorf("PickOpenAPIFile = %q, %v (dialog %+v)", path, err, got)
	}

	WithFilePicker(func(context.Context, FileDialog) (string, error) { return "", errors.New("no window") })(svc)
	if _, err := svc.PickOpenAPIFile(t.Context()); codeOf(err) != CodePickUnavailable {
		t.Errorf("dialog failure: %v", err)
	}
}
