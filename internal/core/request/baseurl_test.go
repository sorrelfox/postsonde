package request

import (
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// mapExpand renders {{name}} from vars; an undefined name stays as written.
func mapExpand(vars map[string]string) Expand {
	return func(text string) (string, error) {
		for name, value := range vars {
			text = strings.ReplaceAll(text, "{{"+name+"}}", value)
		}
		return text, nil
	}
}

func TestExpandURL(t *testing.T) {
	t.Parallel()
	vars := map[string]string{"baseUrl": "https://api.example/v1/", "id": "7", "path": "/users", "host": "https://other.example"}
	tests := []struct {
		raw, want string
		expand    Expand
	}{
		// Relative: joined to the base, one slash between them.
		{"/users", "https://api.example/v1/users", mapExpand(vars)},
		{"  /users/{{id}}?q=1#top ", "https://api.example/v1/users/7?q=1#top", mapExpand(vars)},
		{"/", "https://api.example/v1/", mapExpand(vars)},
		// Relative after rendering.
		{"{{path}}/{{id}}", "https://api.example/v1/users/7", mapExpand(vars)},
		// Not relative: absolute, a variable host, a network path, no slash.
		{"https://h.example/x", "https://h.example/x", mapExpand(vars)},
		{"{{host}}/x", "https://other.example/x", mapExpand(vars)},
		{"//cdn.example/x", "//cdn.example/x", mapExpand(vars)},
		{"users", "users", mapExpand(vars)},
		{"", "", mapExpand(vars)},
		// Undefined baseUrl: left as written for the caller to report.
		{"/users", "{{baseUrl}}/users", mapExpand(map[string]string{})},
		// No expand (text as typed): no base either.
		{"/users", "/users", nil},
		// A base without a path or trailing slash.
		{"/users", "http://localhost:8080/users", mapExpand(map[string]string{"baseUrl": " http://localhost:8080 "})},
	}
	for _, tt := range tests {
		got, err := ExpandURL(tt.raw, tt.expand)
		if err != nil || got != tt.want {
			t.Errorf("ExpandURL(%q) = %q, %v; want %q", tt.raw, got, err, tt.want)
		}
	}

	for _, base := range []string{"api.example.com", "ftp://h.example", "https://", "https://h.example?x=1", "https://h.example/#f", "ws://h.example"} {
		_, err := ExpandURL("/users", mapExpand(map[string]string{"baseUrl": base}))
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != CodeInvalidBaseURL || e.Details[DetailField] != "url" {
			t.Errorf("base %q: err = %v, want %s", base, err, CodeInvalidBaseURL)
		}
		if err != nil && strings.Contains(err.Error(), base) {
			t.Errorf("base %q: the error repeats the value: %v", base, err)
		}
	}

	// An error of the expansion is returned as it is.
	boom := errors.New("boom")
	if _, err := ExpandURL("/users", func(text string) (string, error) {
		if strings.Contains(text, "baseUrl") {
			return "", boom
		}
		return text, nil
	}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestBuildHTTPRelativeURL(t *testing.T) {
	t.Parallel()
	f := projectfile.HTTPRequest{Method: "GET", URL: "/users/:id", PathParams: []projectfile.KeyValue{{Name: "id", Value: "7"}}}
	b, err := BuildHTTP(f, projectfile.Auth{Type: projectfile.AuthNone}, HTTPOptions{Expand: mapExpand(map[string]string{"baseUrl": "https://api.example"})})
	if err != nil {
		t.Fatal(err)
	}
	if b.Request.URL != "https://api.example/users/7" {
		t.Errorf("URL = %q", b.Request.URL)
	}
}
