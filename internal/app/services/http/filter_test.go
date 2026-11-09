package http

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func filterService(t *testing.T) *HTTPService {
	t.Helper()
	return New(nil, nil, nil, nil, nil, nil, WithSaveBody("", nil))
}

// TestFilterBodyJSONPath filters the bytes received, beyond what the view
// shows, and keeps numbers as written (HTTP-36).
func TestFilterBodyJSONPath(t *testing.T) {
	t.Parallel()
	s := filterService(t)
	var b strings.Builder
	b.WriteString(`{"pad":"` + strings.Repeat("x", MaxBodyText) + `","items":[`)
	for i := range 5 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"price":1.50e+%d,"tag":"<b>&"}`, i, i)
	}
	b.WriteString(`]}`)
	id := s.bodies.put([]byte(b.String()), "x.json")

	res, err := s.FilterBody(t.Context(), FilterBodyInputDTO{BodyID: id, Syntax: SyntaxJSONPath, Expression: `$.items[?@.id >= 3]`, Charset: "UTF-8"})
	if err != nil {
		t.Fatal(err)
	}
	want := "[\n  {\n    \"id\": 3,\n    \"price\": 1.50e+3,\n    \"tag\": \"<b>&\"\n  },\n  {\n    \"id\": 4,\n    \"price\": 1.50e+4,\n    \"tag\": \"<b>&\"\n  }\n]"
	if !res.Valid || res.Count != 2 || res.Text != want || res.Language != "json" || res.ResultType != "nodes" {
		t.Fatalf("result = %+v\ntext:\n%s", res, res.Text)
	}
	if strings.Join(res.Paths, " ") != "$['items'][3] $['items'][4]" {
		t.Errorf("paths = %v", res.Paths)
	}

	// Parsed once: the cached document answers the next query.
	res, err = s.FilterBody(t.Context(), FilterBodyInputDTO{BodyID: id, Syntax: SyntaxJSONPath, Expression: `$.nothing`, Charset: "UTF-8"})
	if err != nil || !res.Valid || res.Count != 0 || res.Text != "[]" || len(res.Paths) != 0 {
		t.Fatalf("empty result = %+v, %v", res, err)
	}
}

func TestFilterBodyErrors(t *testing.T) {
	t.Parallel()
	s := filterService(t)
	id := s.bodies.put([]byte(`{"a": [1, 2,]}`), "x.json")

	// A bad expression: its position in UTF-16 code units.
	res, err := s.FilterBody(t.Context(), FilterBodyInputDTO{BodyID: id, Syntax: SyntaxJSONPath, Expression: `$.é[`})
	if err != nil || res.Valid || res.Error == nil || res.Error.Where != "expression" || res.Error.Offset != 4 {
		t.Fatalf("bad jsonpath = %+v %+v, %v", res, res.Error, err)
	}
	if !strings.HasPrefix(res.Error.Message, "Unexpected end") {
		t.Errorf("message = %q", res.Error.Message)
	}
	res, err = s.FilterBody(t.Context(), FilterBodyInputDTO{BodyID: id, Syntax: SyntaxXPath, Expression: `//a[`})
	if err != nil || res.Error == nil || res.Error.Where != "expression" || res.Error.Offset != 4 {
		t.Fatalf("bad xpath = %+v, %v", res.Error, err)
	}

	// A body that is not JSON: where.
	res, err = s.FilterBody(t.Context(), FilterBodyInputDTO{BodyID: id, Syntax: SyntaxJSONPath, Expression: `$.a`})
	if err != nil || res.Error == nil || res.Error.Where != "body" || res.Error.Line != 1 || res.Error.Column == 0 {
		t.Fatalf("bad json = %+v, %v", res.Error, err)
	}
	if !strings.HasPrefix(res.Error.Message, "The body is not valid JSON") {
		t.Errorf("message = %q", res.Error.Message)
	}
	res, err = s.FilterBody(t.Context(), FilterBodyInputDTO{BodyID: id, Syntax: SyntaxXPath, Expression: `/a`})
	if err != nil || res.Error == nil || res.Error.Where != "body" || res.Error.Line != 1 {
		t.Fatalf("bad xml = %+v, %v", res.Error, err)
	}

	// Too much work.
	big := "[" + strings.Repeat(`[[[1,2],[3,4]],[[5,6],[7,8]]],`, 2000) + "0]"
	id2 := s.bodies.put([]byte(big), "big.json")
	res, err = s.FilterBody(t.Context(), FilterBodyInputDTO{BodyID: id2, Syntax: SyntaxJSONPath, Expression: `$[?count($..*) > 0]`})
	if err != nil || res.Error == nil || res.Error.Where != "limit" {
		t.Fatalf("too complex = %+v, %v", res.Error, err)
	}

	// Refusals.
	var e *errs.Error
	_, err = s.FilterBody(t.Context(), FilterBodyInputDTO{BodyID: "body_gone", Syntax: SyntaxJSONPath, Expression: `$`})
	if !errors.As(err, &e) || e.Code != CodeBodyGone {
		t.Errorf("gone = %v", err)
	}
	_, err = s.FilterBody(t.Context(), FilterBodyInputDTO{BodyID: id, Syntax: "css", Expression: `a`})
	if !errors.As(err, &e) || e.Code != CodeInvalidFilter {
		t.Errorf("syntax = %v", err)
	}
	_, err = s.FilterBody(t.Context(), FilterBodyInputDTO{Syntax: SyntaxXPath, Expression: strings.Repeat("a", MaxFilterExpression+1)})
	if !errors.As(err, &e) || e.Code != CodeInvalidFilter {
		t.Errorf("long = %v", err)
	}

	// Cancelled (a newer keystroke).
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, err = s.FilterBody(ctx, FilterBodyInputDTO{BodyID: id2, Syntax: SyntaxJSONPath, Expression: `$..*`})
	if err != nil || res.Error == nil || res.Error.Where != "limit" || res.Error.Message != "The filter was cancelled." {
		t.Fatalf("cancelled = %+v, %v", res.Error, err)
	}
}

// TestFilterBodyXPath decodes the bytes with the response's charset and
// shows node-sets indented and scalars as strings.
func TestFilterBodyXPath(t *testing.T) {
	t.Parallel()
	s := filterService(t)
	raw := []byte("<s:Envelope xmlns:s=\"urn:s\"><s:Body><item id=\"1\"><name>caf\xe9</name></item><item id=\"2\"><name>tea</name><!--x--></item></s:Body></s:Envelope>")
	id := s.bodies.put(raw, "x.xml")
	in := func(expr string) FilterBodyInputDTO {
		return FilterBodyInputDTO{BodyID: id, Syntax: SyntaxXPath, Expression: expr, Charset: "ISO-8859-1"}
	}

	res, err := s.FilterBody(t.Context(), in(`//item`))
	if err != nil {
		t.Fatal(err)
	}
	want := "<item id=\"1\">\n  <name>café</name>\n</item>\n<item id=\"2\">\n  <name>tea</name>\n  <!--x-->\n</item>"
	if !res.Valid || res.Count != 2 || res.Text != want || res.Language != "xml" {
		t.Fatalf("nodes = %+v\n%s", res, res.Text)
	}
	if strings.Join(res.Paths, " ") != "/s:Envelope[1]/s:Body[1]/item[1] /s:Envelope[1]/s:Body[1]/item[2]" {
		t.Errorf("paths = %v", res.Paths)
	}

	for expr, want := range map[string][3]string{
		`//item/@id`:         {"nodes", "id=\"1\"\nid=\"2\"", "xml"},
		`//name/text()`:      {"nodes", "café\ntea", "xml"},
		`count(//item)`:      {"number", "2", "text"},
		`string(//name)`:     {"string", "café", "text"},
		`boolean(//nothing)`: {"boolean", "false", "text"},
		`//item[@id=3]`:      {"nodes", "", "xml"},
	} {
		res, err := s.FilterBody(t.Context(), in(expr))
		if err != nil || !res.Valid || res.ResultType != want[0] || res.Text != want[1] || res.Language != want[2] {
			t.Errorf("%s = %+v, %v", expr, res, err)
		}
	}

	// HTML mode, and the shown text when there is no body ID.
	res, err = s.FilterBody(t.Context(), FilterBodyInputDTO{Text: "<html><body><p>a<br>b</p></body></html>", Syntax: SyntaxXPath, Expression: `count(//br)`, HTML: true})
	if err != nil || res.Text != "1" {
		t.Errorf("html = %+v, %v", res, err)
	}
}

// TestFilterBodyClipped cuts a result over MaxFilterText.
func TestFilterBodyClipped(t *testing.T) {
	t.Parallel()
	s := filterService(t)
	body := `["` + strings.Repeat("é", MaxFilterText) + `"]`
	res, err := s.FilterBody(t.Context(), FilterBodyInputDTO{Text: body, Syntax: SyntaxJSONPath, Expression: `$[0]`})
	if err != nil || !res.Valid || !res.Clipped || len(res.Text) > MaxFilterText || !strings.HasPrefix(res.Text, "[\n  \"éé") {
		t.Fatalf("clipped = %v %d, %v", res.Clipped, len(res.Text), err)
	}
}
