package collection

import (
	"reflect"
	"strings"
	"testing"
)

// TestResponseExamples saves response examples (HTTP-38) through the
// auto-save pipeline for an http and a graphql request, reads them back
// unchanged, and checks that a request without them is written without
// the member.
func TestResponseExamples(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"http", "graphql"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			svc, _, dir, _, _ := newAutosaveService(t)
			ctx := t.Context()
			res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Get user", Kind: kind, Index: -1})
			if err != nil {
				t.Fatal(err)
			}
			req := getRequest(t, svc, res.NodeID)
			if req.ResponseExamples != nil {
				t.Fatalf("new request examples = %#v", req.ResponseExamples)
			}
			if strings.Contains(readFile(t, dir, "get-user.json"), "responseExamples") {
				t.Error("a request without examples has the member")
			}

			req.ResponseExamples = []ResponseExampleDTO{
				{
					Name: "200 OK", SavedAt: "2026-09-23T10:15:02Z", StatusCode: 200, StatusText: "OK",
					Request: &ExampleRequestDTO{
						Method: "POST", URL: "https://api.test/users",
						Headers: []KeyValueDTO{{Name: "Content-Type", Value: "application/json", Enabled: true}},
						Body:    BodyDTO{Mode: "raw", Type: "json", Text: `{"a":1}`, Fields: []KeyValueDTO{}, Parts: []PartDTO{}},
					},
					Headers: []KeyValueDTO{{Name: "Content-Type", Value: "application/json", Enabled: true}},
					Body:    `{"id":"42"}`,
					Extra:   `{"x-note":"kept"}`,
				},
				{
					Name: "Binary", StatusCode: 200, Headers: []KeyValueDTO{},
					Body: "/w==", BodyEncoding: "base64", BodyClipped: true, BodySize: 90000,
					Request: &ExampleRequestDTO{Method: "GET", URL: "https://api.test/b", Headers: []KeyValueDTO{}, Body: BodyDTO{Mode: "none"}},
				},
			}
			update(t, svc, req, 1)
			if err := svc.Flush(ctx, req.ID); err != nil {
				t.Fatal(err)
			}
			file := readFile(t, dir, "get-user.json")
			for _, want := range []string{
				`"responseExamples": [`,
				`"statusText": "OK",`,
				`"x-note": "kept"`,
				`"bodyEncoding": "base64",`,
				`"bodyClipped": true,`,
				`"bodySize": 90000`,
			} {
				if !strings.Contains(file, want) {
					t.Errorf("file =\n%s\nwant it to contain %s", file, want)
				}
			}
			if strings.Contains(file, `"mode": "none"`) && kind == "graphql" {
				t.Errorf("an example request without a body has one:\n%s", file)
			}

			got := getRequest(t, svc, req.ID)
			if !reflect.DeepEqual(got.ResponseExamples[0], req.ResponseExamples[0]) {
				t.Errorf("example read back =\n%+v\nwant\n%+v", got.ResponseExamples[0], req.ResponseExamples[0])
			}
			if b := got.ResponseExamples[1].Request.Body; b.Mode != "" {
				t.Errorf("body without a mode read back as %+v", b)
			}

			// Saving what was read changes nothing.
			update(t, svc, got, 2)
			if err := svc.Flush(ctx, req.ID); err != nil {
				t.Fatal(err)
			}
			if again := readFile(t, dir, "get-user.json"); again != file {
				t.Errorf("a save of the unchanged request changed the file:\n%s\nwas\n%s", again, file)
			}

			// Deleting every example drops the member.
			got.ResponseExamples = nil
			update(t, svc, got, 3)
			if err := svc.Flush(ctx, req.ID); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(readFile(t, dir, "get-user.json"), "responseExamples") {
				t.Error("the member is still there without examples")
			}
		})
	}
}
