package request

import (
	"errors"
	"io"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body projectfile.Body
		want *string // nil: no body
	}{
		{name: "none", body: projectfile.Body{Mode: "none"}},
		{name: "empty mode", body: projectfile.Body{}},
		{name: "none ignores stale raw members", body: projectfile.Body{Mode: "none", Type: "json", Text: `{"a":1}`}},
		{name: "raw json as typed", body: projectfile.Body{Mode: "raw", Type: "json", Text: "{\n  \"n\": 1.50e+3\n}"}, want: ptr("{\n  \"n\": 1.50e+3\n}")},
		{name: "raw invalid json is still sent", body: projectfile.Body{Mode: "raw", Type: "json", Text: `{"a":`}, want: ptr(`{"a":`)},
		{name: "raw xml", body: projectfile.Body{Mode: "raw", Type: "xml", Text: "<a>é</a>"}, want: ptr("<a>é</a>")},
		{name: "raw html", body: projectfile.Body{Mode: "raw", Type: "html", Text: "<p>x</p>"}, want: ptr("<p>x</p>")},
		{name: "raw yaml keeps CRLF", body: projectfile.Body{Mode: "raw", Type: "yaml", Text: "a: 1\r\nb: 2\r\n"}, want: ptr("a: 1\r\nb: 2\r\n")},
		{name: "raw text", body: projectfile.Body{Mode: "raw", Type: "text", Text: "hi 😀"}, want: ptr("hi 😀")},
		{name: "raw without type", body: projectfile.Body{Mode: "raw", Text: "x"}, want: ptr("x")},
		{name: "raw of unknown type", body: projectfile.Body{Mode: "raw", Type: "csv", Text: "a,b"}, want: ptr("a,b")},
		{name: "raw empty text is a zero-length body", body: projectfile.Body{Mode: "raw", Type: "json"}, want: ptr("")},
		{name: "urlencoded fields", body: projectfile.Body{Mode: "urlencoded", Fields: []projectfile.KeyValue{kv("a b", "c&d"), off("x", "y")}}, want: ptr("a+b=c%26d")},
		{name: "urlencoded without fields is a zero-length body", body: projectfile.Body{Mode: "urlencoded"}, want: ptr("")},
		{name: "urlencoded ignores stale raw members", body: projectfile.Body{Mode: "urlencoded", Type: "json", Text: "{}", Fields: []projectfile.KeyValue{kv("k", "v")}}, want: ptr("k=v")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Body(tt.body, nil, "")
			if err != nil {
				t.Fatalf("Body: %v", err)
			}
			if tt.want == nil {
				if got != nil {
					t.Fatalf("Body = %#v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("Body = nil, want a body")
			}
			if ct, want := got.ContentType(), ContentType(tt.body); ct != want {
				t.Errorf("ContentType = %q, want %q (the auto Content-Type)", ct, want)
			}
			// Open twice: a redirect sends the body again.
			for range 2 {
				r, n, err := got.Open()
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				data, _ := io.ReadAll(r)
				_ = r.Close()
				if string(data) != *tt.want || n != int64(len(*tt.want)) {
					t.Errorf("Open = %q (%d bytes), want %q (%d bytes)", data, n, *tt.want, len(*tt.want))
				}
			}
			if _, ok := got.(httpclient.RawBody); !ok {
				t.Errorf("Body = %T, want httpclient.RawBody", got)
			}
		})
	}
}

func TestBodyUnsupported(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"graphql-future"} {
		_, err := Body(projectfile.Body{Mode: mode, Text: "x"}, nil, "")
		if !errors.Is(err, &errs.Error{Kind: errs.Unsupported, Code: CodeBodyModeUnsupported}) {
			t.Errorf("Body(mode %q) error = %v, want unsupported %s", mode, err, CodeBodyModeUnsupported)
		}
	}
}

func TestRawMediaType(t *testing.T) {
	t.Parallel()

	for typ, want := range map[string]string{
		"json": "application/json",
		"xml":  "application/xml",
		"html": "text/html",
		"text": "text/plain",
		"":     "text/plain",
		"yaml": "application/yaml",
		"csv":  "",
	} {
		if got := RawMediaType(typ); got != want {
			t.Errorf("RawMediaType(%q) = %q, want %q", typ, got, want)
		}
	}
}

func ptr(s string) *string { return &s }
