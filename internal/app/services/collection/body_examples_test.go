package collection

import (
	"bytes"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// TestBodyExamples saves named bodies (HTTP-17) through the auto-save
// pipeline, switches the active one the way the editor does and checks
// that the body sent (LoadHTTPSource) follows it.
func TestBodyExamples(t *testing.T) {
	t.Parallel()
	svc, _, dir, _, _ := newAutosaveService(t)
	ctx := t.Context()

	res, err := svc.CreateRequest(ctx, CreateRequestInputDTO{Name: "Quote", Kind: "http", Index: -1})
	if err != nil {
		t.Fatal(err)
	}
	req := getRequest(t, svc, res.NodeID)
	if req.BodyExamples == nil || len(req.BodyExamples) != 0 {
		t.Fatalf("new request examples = %#v", req.BodyExamples)
	}

	minimal := BodyDTO{Mode: "raw", Type: "xml", Text: "<a/>"}
	full := BodyDTO{Mode: "urlencoded", Fields: []KeyValueDTO{{Name: "b", Value: "2", Enabled: true}}}
	req.Method = "POST"
	req.Body = minimal
	req.BodyExamples = []BodyExampleDTO{
		{Name: "Minimal", Active: true},
		{Name: "Full", Body: full, Extra: `{"x-note":"kept"}`},
	}
	update(t, svc, req, 1)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, dir, "quote.json")
	want := `  "body": {
    "mode": "raw",
    "type": "xml",
    "text": "<a/>"
  },
  "bodyExamples": [
    {
      "name": "Minimal",
      "active": true
    },
    {
      "name": "Full",
      "body": {
        "mode": "urlencoded",
        "fields": [
          {
            "name": "b",
            "value": "2"
          }
        ]
      },
      "x-note": "kept"
    }
  ],
`
	if !bytes.Contains([]byte(file), []byte(want)) {
		t.Errorf("file =\n%s\nwant it to contain\n%s", file, want)
	}
	src, err := LoadHTTPSource(svc, ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if src.Request.Body.Mode != "raw" || src.Request.Body.Text != "<a/>" {
		t.Errorf("sent body = %+v", src.Request.Body)
	}

	// The DTO read back has the same shape.
	got := getRequest(t, svc, req.ID)
	if len(got.BodyExamples) != 2 || !got.BodyExamples[0].Active || got.BodyExamples[0].Body.Mode != "" ||
		got.BodyExamples[1].Name != "Full" || got.BodyExamples[1].Body.Mode != "urlencoded" || got.BodyExamples[1].Extra != `{"x-note":"kept"}` {
		t.Fatalf("examples = %+v", got.BodyExamples)
	}

	// Switch to "Full": its body becomes the request's body.
	got.Body = got.BodyExamples[1].Body
	got.BodyExamples = []BodyExampleDTO{
		{Name: "Minimal", Body: minimal},
		{Name: "Full", Active: true, Extra: `{"x-note":"kept"}`},
	}
	update(t, svc, got, 2)
	if err := svc.Flush(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	src, err = LoadHTTPSource(svc, ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if src.Request.Body.Mode != "urlencoded" || len(src.Request.Body.Fields) != 1 || src.Request.Body.Fields[0].Name != "b" {
		t.Errorf("sent body after the switch = %+v", src.Request.Body)
	}
	if len(src.Request.BodyExamples) != 2 || src.Request.BodyExamples[0].Body.Text != "<a/>" || !src.Request.BodyExamples[1].Active {
		t.Errorf("examples after the switch = %+v", src.Request.BodyExamples)
	}
}

// TestBodyExamplesRead checks how the examples of a hand-written file
// read: exactly one active example whenever there are any.
func TestBodyExamplesRead(t *testing.T) {
	t.Parallel()
	raw := projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawText, Text: "sent"}
	tests := []struct {
		name     string
		examples []projectfile.BodyExample
		// want lists the names, "*" marking the active one.
		want []string
	}{
		{"none", nil, []string{}},
		{"one active", []projectfile.BodyExample{{Name: "A", Active: true}, {Name: "B"}}, []string{"*A", "B"}},
		{"no active", []projectfile.BodyExample{{Name: "A", Body: raw}, {Name: "B"}}, []string{"*", "A", "B"}},
		{"two active", []projectfile.BodyExample{{Name: "A"}, {Name: "B", Active: true}, {Name: "C", Active: true}}, []string{"A", "*B", "C"}},
	}
	for _, tt := range tests {
		f := &projectfile.HTTPRequest{ID: "req_1", Method: "POST", Body: raw, BodyExamples: tt.examples}
		dto := toRequestDTO(f)
		names := []string{}
		for _, e := range dto.BodyExamples {
			n := e.Name
			if e.Active {
				n = "*" + n
				if e.Body.Mode != "" {
					t.Errorf("%s: active example has a body %+v", tt.name, e.Body)
				}
			} else if e.Body.Mode == "" {
				t.Errorf("%s: example %q has no mode", tt.name, e.Name)
			}
			names = append(names, n)
		}
		if len(names) != len(tt.want) {
			t.Errorf("%s: examples = %q, want %q", tt.name, names, tt.want)
			continue
		}
		for i := range names {
			if names[i] != tt.want[i] {
				t.Errorf("%s: examples = %q, want %q", tt.name, names, tt.want)
				break
			}
		}
		if dto.Body.Text != "sent" {
			t.Errorf("%s: body = %+v", tt.name, dto.Body)
		}
	}
}
