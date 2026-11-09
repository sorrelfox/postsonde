package postman_test

import (
	"encoding/json"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// A relative URL is sent to {{baseUrl}} (COL-08); Postman has no such
// rule, so the export writes the variable out and the request works there.
func TestExportRelativeURL(t *testing.T) {
	res := export(t, importer.Folder{
		Name: "Relative",
		Items: []importer.Item{
			httpReq("Users", projectfile.HTTPRequest{Method: "GET", URL: "/users?page=2"}),
			httpReq("Absolute", projectfile.HTTPRequest{Method: "GET", URL: "{{host}}/users"}),
			httpReq("Network path", projectfile.HTTPRequest{Method: "GET", URL: "//cdn.example/x"}),
		},
	})
	var c struct {
		Item []struct {
			Request struct {
				URL struct {
					Raw string `json:"raw"`
				} `json:"url"`
			} `json:"request"`
		} `json:"item"`
	}
	if err := json.Unmarshal(res.Data, &c); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range c.Item {
		got = append(got, it.Request.URL.Raw)
	}
	want := []string{"{{baseUrl}}/users?page=2", "{{host}}/users", "//cdn.example/x"}
	if len(got) != len(want) {
		t.Fatalf("urls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("url %d = %q, want %q", i, got[i], want[i])
		}
	}
}
