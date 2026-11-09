package mockopenapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// readerFixtures are the OpenAPI reader's petstore documents.
const readerFixtures = "../../openapi/testdata"

func dump(res Result) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "title: %s %s (openapi %s", res.Title, res.Version, res.OpenAPI)
	if res.Swagger != "" {
		fmt.Fprintf(&b, ", from swagger %s", res.Swagger)
	}
	fmt.Fprintf(&b, ")\nprefix: %q (default %q)\n", res.Prefix, res.DefaultPrefix)
	for i, r := range res.Routes {
		op := res.Operations[i]
		fmt.Fprintf(&b, "\nroute %d %s %s id=%s enabled=%v\n", i, r.Method, r.Path, r.ID, r.Enabled)
		fmt.Fprintf(&b, "  from %s %s operationId=%q response=%q mediaType=%q\n", op.Method, op.Path, op.OperationID, op.Response, op.MediaType)
		fmt.Fprintf(&b, "  status %d, delay %s\n", r.Status, r.Delay.Mode)
		for _, h := range r.Headers {
			fmt.Fprintf(&b, "  header %s: %s (enabled=%v)\n", h.Name, h.Value, h.Enabled)
		}
		fmt.Fprintf(&b, "  body %s %s\n", r.Body.Mode, r.Body.Type)
		if r.Body.Text != "" {
			for line := range strings.Lines(r.Body.Text) {
				b.WriteString("    | " + strings.TrimRight(line, "\n") + "\n")
			}
		}
	}
	if len(res.Diagnostics) > 0 {
		b.WriteString("\ndiagnostics:\n")
	}
	for _, d := range res.Diagnostics {
		fmt.Fprintf(&b, "  %s %s route=%d op=%q line=%d: %s\n", d.Severity, d.Code, d.RouteIndex, d.Operation, d.Pos.Line, d.Message)
	}
	return []byte(b.String())
}

func parseFile(t *testing.T, path string, opts Options) Result {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Parse(data, opts)
	if err != nil {
		t.Fatalf("Parse %s: %v", path, err)
	}
	return res
}

// Every route Generate returns is valid.
func checkValid(t *testing.T, res Result) {
	t.Helper()
	if len(res.Routes) != len(res.Operations) {
		t.Fatalf("%d routes, %d operations", len(res.Routes), len(res.Operations))
	}
	for _, d := range mockhttp.Check(mockhttp.Config{Routes: res.Routes}) {
		t.Errorf("generated route %d is not valid: %s %s", d.RouteIndex, d.Code, d.Message)
	}
	for _, r := range res.Routes {
		if r.Body.Type == "json" && r.Body.Text != "" && !json.Valid([]byte(r.Body.Text)) {
			t.Errorf("%s %s: body is not valid JSON:\n%s", r.Method, r.Path, r.Body.Text)
		}
	}
}

func TestGoldenPetstores(t *testing.T) {
	for _, name := range []string{
		"petstore-3.0.yaml", "petstore-3.0.json", "petstore-3.1.yaml", "petstore-3.1.json",
		"petstore-2.0.yaml", "petstore-2.0.json",
	} {
		t.Run(name, func(t *testing.T) {
			res := parseFile(t, filepath.Join(readerFixtures, name), Options{})
			checkValid(t, res)
			golden.Assert(t, name, dump(res))
		})
	}
	t.Run("recursive.yaml", func(t *testing.T) {
		res := parseFile(t, filepath.Join("testdata", "recursive.yaml"), Options{})
		checkValid(t, res)
		golden.Assert(t, "recursive.yaml", dump(res))
	})
	t.Run("edge.yaml", func(t *testing.T) {
		res := parseFile(t, filepath.Join("testdata", "edge.yaml"), Options{})
		checkValid(t, res)
		golden.Assert(t, "edge.yaml", dump(res))
	})
}

func TestPrefixOption(t *testing.T) {
	path := filepath.Join(readerFixtures, "petstore-3.0.yaml")
	for _, tt := range []struct {
		prefix string
		want   string
	}{
		{"", "/pets/:petId"},
		{"/", "/pets/:petId"},
		{"api/", "/api/pets/:petId"},
		{"https://mock.example/base/v2/?x=1", "/base/v2/pets/:petId"},
	} {
		p := tt.prefix
		res := parseFile(t, path, Options{Prefix: &p})
		checkValid(t, res)
		found := false
		for _, r := range res.Routes {
			if r.Method == "GET" && r.Path == tt.want {
				found = true
			}
		}
		if !found {
			t.Errorf("prefix %q: no GET %s in %v", tt.prefix, tt.want, paths(res.Routes))
		}
		if res.DefaultPrefix != "/v1" {
			t.Errorf("DefaultPrefix = %q, want /v1", res.DefaultPrefix)
		}
	}
}

func paths(rs []mockhttp.Route) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Method + " " + r.Path
	}
	return out
}

func TestConvertPath(t *testing.T) {
	for _, tt := range []struct {
		in, want string
		notes    int
	}{
		{"/pets", "/pets", 0},
		{"/", "/", 0},
		{"", "/", 0},
		{"/pets/{petId}", "/pets/:petId", 0},
		{"/pets/{petId}/", "/pets/:petId/", 0},
		{"/users/{user-id}/repos/{repo.name}", "/users/:user_id/repos/:repo_name", 2},
		{"/a/{id}/b/{id}", "/a/:id/b/:id_2", 1},
		{"/{1st}", "/:p1st", 1},
		{"/{}", "/:param", 1},
		{"/files/{name}.{ext}", `/files/{name_ext:.+\..+}`, 1},
		{"/v1/items/{id}:activate", `/v1/items/{id:.+:activate}`, 1},
		{"/report.{format}", `/{format:report\..+}`, 1},
		{"/search?type=x", "/search", 1},
		{"pets/{id}", "/pets/:id", 1},
		{"/a//b", "/a/b", 1},
		{"/v1/items:batchGet", "/v1/items:batchGet", 0},
		{"/:literal", "/%3Aliteral", 0},
		{"/glob*", "/glob%2A", 0},
		{"/100%/x%41", "/100%25/x%41", 0},
		{"/a/{unclosed", "/a/%7Bunclosed", 0},
		{"/caf%C3%A9/{id}", "/caf%C3%A9/:id", 0},
	} {
		got, notes := ConvertPath(tt.in)
		if got != tt.want || len(notes) != tt.notes {
			t.Errorf("ConvertPath(%q) = %q, %d notes %q; want %q, %d notes", tt.in, got, len(notes), notes, tt.want, tt.notes)
		}
		// Every conversion is a valid pattern.
		cfg := mockhttp.Config{Routes: []mockhttp.Route{{ID: "r", Enabled: true, Method: "GET", Path: got, Status: 200}}}
		for _, d := range mockhttp.Check(cfg) {
			t.Errorf("ConvertPath(%q) = %q is not valid: %s", tt.in, got, d.Message)
		}
	}
}

func TestPrefixOf(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "",
		"/":                           "",
		"https://api.example.com":     "",
		"https://api.example.com/":    "",
		"https://api.example.com/v1/": "/v1",
		"//api.example.com/v2":        "/v2",
		"/api/v3?x#y":                 "/api/v3",
		"v1":                          "/v1",
	} {
		if got := PrefixOf(in); got != want {
			t.Errorf("PrefixOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickResponse(t *testing.T) {
	doc := `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /a:
    get:
      responses:
        "404": {description: x}
        default: {description: x}
        "201": {description: x}
        "200": {description: x}
  /b:
    get:
      responses:
        4XX: {description: x}
        2XX: {description: x}
  /c:
    get:
      responses:
        "404": {description: x}
        "302": {description: x}
  /d:
    get:
      responses:
        5XX: {description: x}
  /e:
    get:
      responses: {}
`
	res, err := Parse([]byte(doc), Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"/a": 201, "/b": 200, "/c": 404, "/d": 500, "/e": 200}
	for _, r := range res.Routes {
		if r.Status != want[r.Path] {
			t.Errorf("%s: status %d, want %d", r.Path, r.Status, want[r.Path])
		}
	}
	if len(res.Routes) != len(want) {
		t.Errorf("routes %v", paths(res.Routes))
	}
}

func TestMerge(t *testing.T) {
	existing := mockhttp.Config{CaseInsensitive: true, Routes: []mockhttp.Route{
		{ID: "rt_listpets", Enabled: true, Method: "GET", Path: "/PETS", Status: 418},
		{ID: "rt_mine", Enabled: false, Method: "POST", Path: "/other", Status: 200},
	}}
	generated := []mockhttp.Route{
		{ID: "rt_listpets", Enabled: true, Method: "GET", Path: "/pets", Status: 200},
		{ID: "rt_mine", Enabled: true, Method: "GET", Path: "/pets/:id", Status: 200},
		{ID: "rt_other", Enabled: true, Method: "POST", Path: "/other", Status: 201},
		{ID: "rt_new", Enabled: true, Method: "DELETE", Path: "/pets/:petId", Status: 204},
	}

	m := Merge(existing, generated, ModeMerge)
	if got, want := strings.Join(m.Statuses, ","), "existing,added,existing,added"; got != want {
		t.Errorf("statuses %s, want %s", got, want)
	}
	if m.Added != 2 || m.Kept != 2 || m.Removed != 0 || len(m.Config.Routes) != 4 || !m.Config.CaseInsensitive {
		t.Errorf("merge: %+v", m)
	}
	if m.IDs[1] != "rt_mine_2" || m.Config.Routes[2].ID != "rt_mine_2" {
		t.Errorf("clashing ID not renamed: %v", m.IDs)
	}
	if m.Config.Routes[0].Status != 418 {
		t.Error("the existing route was changed")
	}
	if len(m.Diagnostics) != 2 || m.Diagnostics[0].Code != CodeExistingRoute || m.Diagnostics[0].RouteIndex != 0 {
		t.Errorf("diagnostics %+v", m.Diagnostics)
	}
	if len(mockhttp.Check(m.Config)) != 0 {
		t.Errorf("merged config has problems: %+v", mockhttp.Check(m.Config))
	}

	r := Merge(existing, generated, ModeReplace)
	if r.Added != 4 || r.Removed != 2 || r.Kept != 0 || len(r.Config.Routes) != 4 || r.Config.Routes[0].ID != "rt_listpets" {
		t.Errorf("replace: %+v", r)
	}

	// The route limit.
	full := mockhttp.Config{}
	for i := range mockhttp.MaxRoutes - 1 {
		full.Routes = append(full.Routes, mockhttp.Route{ID: fmt.Sprintf("r%d", i), Enabled: true, Method: "GET", Path: fmt.Sprintf("/x%d", i), Status: 200})
	}
	l := Merge(full, generated, ModeMerge)
	if got := strings.Join(l.Statuses, ","); got != "added,over_limit,over_limit,over_limit" || len(l.Config.Routes) != mockhttp.MaxRoutes {
		t.Errorf("limit: %s, %d routes", got, len(l.Config.Routes))
	}
}

// Merging does not share header slices with its inputs.
func TestMergeCopies(t *testing.T) {
	gen := []mockhttp.Route{{ID: "a", Enabled: true, Method: "GET", Path: "/a", Status: 200, Headers: []mockhttp.Header{{Name: "X", Value: "1"}}}}
	m := Merge(mockhttp.Config{}, gen, ModeMerge)
	m.Config.Routes[0].Headers[0].Value = "2"
	if gen[0].Headers[0].Value != "1" {
		t.Error("Merge shares headers")
	}
}

// Whatever the path, the conversion is a valid pattern.
func FuzzConvertPath(f *testing.F) {
	for _, s := range []string{"/pets/{id}", "/a/{x}.{y}/b", "/{a-b}/{a_b}", "/%zz/*/{", "x?y", "//{}/**", "/{:}/:", "/{1}{2}"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		got, _ := ConvertPath(p)
		cfg := mockhttp.Config{Routes: []mockhttp.Route{{ID: "r", Enabled: true, Method: "GET", Path: got, Status: 200}}}
		for _, d := range mockhttp.Check(cfg) {
			t.Fatalf("ConvertPath(%q) = %q is not valid: %s", p, got, d.Message)
		}
	})
}

func TestNotADocument(t *testing.T) {
	if _, err := Parse([]byte("hello: world\n"), Options{}); err == nil {
		t.Error("Parse of a non-OpenAPI file did not fail")
	}
}
