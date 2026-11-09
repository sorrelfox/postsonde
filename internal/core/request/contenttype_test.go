package request

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// contentTypeCase is a case of testdata/content_type.json, which
// frontend/src/lib/contentType.test.ts runs too.
type contentTypeCase struct {
	Name string `json:"name"`
	Body struct {
		Mode        string `json:"mode"`
		Type        string `json:"type"`
		Path        string `json:"path"`
		ContentType string `json:"contentType"`
	} `json:"body"`
	Headers []struct {
		Name    string `json:"name"`
		Value   string `json:"value"`
		Enabled bool   `json:"enabled"`
	} `json:"headers"`
	Want string `json:"want"`
}

type contentTypeFixture struct {
	Extensions map[string]string `json:"extensions"`
	Cases      []contentTypeCase `json:"cases"`
}

func loadContentTypeFixture(t *testing.T) contentTypeFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/content_type.json")
	if err != nil {
		t.Fatal(err)
	}
	var file contentTypeFixture
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) == 0 || len(file.Extensions) == 0 {
		t.Fatal("no cases or extensions")
	}
	return file
}

func loadContentTypeCases(t *testing.T) []contentTypeCase {
	t.Helper()
	return loadContentTypeFixture(t).Cases
}

// TestExtensionTableFixture keeps the extension table equal to the one the
// frontend checks too.
func TestExtensionTableFixture(t *testing.T) {
	t.Parallel()

	want := loadContentTypeFixture(t).Extensions
	if !reflect.DeepEqual(extensionMediaTypes, want) {
		t.Errorf("extension table = %v\nfixture = %v", extensionMediaTypes, want)
	}
	for ext, typ := range want {
		if got := MediaTypeByExtension("dir/NAME" + strings.ToUpper(ext)); got != typ {
			t.Errorf("MediaTypeByExtension(%q) = %q, want %q", ext, got, typ)
		}
	}
}

func (c contentTypeCase) body() projectfile.Body {
	return projectfile.Body{Mode: c.Body.Mode, Type: c.Body.Type, Path: c.Body.Path, ContentType: c.Body.ContentType}
}

func (c contentTypeCase) rows() []projectfile.KeyValue {
	rows := make([]projectfile.KeyValue, 0, len(c.Headers))
	for _, h := range c.Headers {
		rows = append(rows, projectfile.KeyValue{Name: h.Name, Value: h.Value, Disabled: !h.Enabled})
	}
	return rows
}

func TestImplicitContentTypeFixture(t *testing.T) {
	t.Parallel()

	for _, c := range loadContentTypeCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			headers, err := Headers(c.rows(), nil)
			if err != nil {
				t.Fatalf("Headers: %v", err)
			}
			if got := ImplicitContentType(c.body(), headers); got != c.Want {
				t.Errorf("ImplicitContentType = %q, want %q", got, c.Want)
			}
			if len(c.Headers) == 0 {
				if got := ContentType(c.body()); got != c.Want {
					t.Errorf("ContentType = %q, want %q", got, c.Want)
				}
			}
		})
	}
}

func TestContentTypeEveryMode(t *testing.T) {
	t.Parallel()

	for mode, want := range map[string]string{
		"":               "",
		"none":           "",
		"urlencoded":     "application/x-www-form-urlencoded",
		"multipart":      "multipart/form-data",
		"file":           "application/octet-stream",
		"graphql-future": "",
	} {
		// Members of other modes change nothing.
		if got := ContentType(projectfile.Body{Mode: mode, Type: "json"}); got != want {
			t.Errorf("ContentType(%q) = %q, want %q", mode, got, want)
		}
	}
	for _, typ := range []string{"json", "xml", "html", "text", "", "yaml", "csv"} {
		if got, want := ContentType(projectfile.Body{Mode: "raw", Type: typ}), RawMediaType(typ); got != want {
			t.Errorf("ContentType(raw %q) = %q, want %q", typ, got, want)
		}
	}
}

func TestHasContentTypeAfterExpansion(t *testing.T) {
	t.Parallel()

	resolver := template.Resolver{Scopes: template.Chain{template.Vars("env", map[string]string{"ct": "content-type"})}}
	expand := func(s string) (string, error) {
		r := resolver.Render(s)
		return r.Text, r.Err()
	}
	headers, err := Headers([]projectfile.KeyValue{kv("{{ct}}", "text/csv")}, expand)
	if err != nil {
		t.Fatal(err)
	}
	if !HasContentType(headers) {
		t.Error("a row whose name renders to content-type does not override")
	}
	if HasContentType([]httpclient.Header{{Name: "Content-Type", Value: "x", Enabled: false}}) {
		t.Error("a disabled header overrides")
	}
}

// TestContentTypeOnTheWire sends mapped bodies and headers with httpclient
// and checks what the server receives.
func TestContentTypeOnTheWire(t *testing.T) {
	type received struct {
		contentType []string
		body        string
		form        map[string][]string
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		rec := received{contentType: r.Header.Values("Content-Type"), body: string(data)}
		if r.Header.Get("Content-Type") == MediaTypeFormURLEncoded {
			r.Body = io.NopCloser(strings.NewReader(rec.body))
			if err := r.ParseForm(); err == nil {
				rec.form = r.PostForm
			}
		}
		got <- rec
	}))
	defer srv.Close()
	c := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	defer c.CloseIdleConnections()

	tests := []struct {
		name     string
		body     projectfile.Body
		headers  []projectfile.KeyValue
		wantCT   []string
		wantBody string
		wantForm map[string][]string
	}{
		{
			name:     "form body with the implicit type",
			body:     projectfile.Body{Mode: "urlencoded", Fields: []projectfile.KeyValue{kv("name", "Zoë & co"), off("skip", "1"), kv("sum", "1+1=2"), kv("name", "100%")}},
			wantCT:   []string{"application/x-www-form-urlencoded"},
			wantBody: "name=Zo%C3%AB+%26+co&sum=1%2B1%3D2&name=100%25",
			wantForm: map[string][]string{"name": {"Zoë & co", "100%"}, "sum": {"1+1=2"}},
		},
		{
			name:     "raw json with the implicit type",
			body:     projectfile.Body{Mode: "raw", Type: "json", Text: `{"a":1}`},
			wantCT:   []string{"application/json"},
			wantBody: `{"a":1}`,
		},
		{
			name:     "an explicit header overrides",
			body:     projectfile.Body{Mode: "raw", Type: "json", Text: `{"a":1}`},
			headers:  []projectfile.KeyValue{kv("content-type", "application/vnd.api+json")},
			wantCT:   []string{"application/vnd.api+json"},
			wantBody: `{"a":1}`,
		},
		{
			name:     "a disabled explicit header does not",
			body:     projectfile.Body{Mode: "urlencoded", Fields: []projectfile.KeyValue{kv("a", "b")}},
			headers:  []projectfile.KeyValue{off("Content-Type", "text/plain")},
			wantCT:   []string{"application/x-www-form-urlencoded"},
			wantBody: "a=b",
			wantForm: map[string][]string{"a": {"b"}},
		},
		{
			name:    "no body, no type",
			body:    projectfile.Body{Mode: "none"},
			wantCT:  nil,
			headers: []projectfile.KeyValue{kv("Accept", "*/*")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers, err := Headers(tt.headers, nil)
			if err != nil {
				t.Fatal(err)
			}
			body, err := Body(tt.body, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.Send(context.Background(), httpclient.Request{Method: "POST", URL: srv.URL + "/", Headers: headers, Body: body, Settings: httpclient.DefaultSettings()})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			rec := <-got
			if !reflect.DeepEqual(rec.contentType, tt.wantCT) {
				t.Errorf("Content-Type = %q, want %q", rec.contentType, tt.wantCT)
			}
			if rec.body != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.body, tt.wantBody)
			}
			if tt.wantForm != nil && !reflect.DeepEqual(rec.form, tt.wantForm) {
				t.Errorf("parsed form = %#v, want %#v", rec.form, tt.wantForm)
			}
		})
	}
}
