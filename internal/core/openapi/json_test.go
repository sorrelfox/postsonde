package openapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

func TestParseJSONPositions(t *testing.T) {
	t.Parallel()
	src := "\ufeff{\n  \"é\": [1, -2.5e3, true, null],\n\t\"b\": {\"c\": \"x\\u00e9\\ud83d\\ude00\\n\"}\n}"
	root, jerr := parseJSON([]byte(src), 100, 1000)
	if jerr != nil {
		t.Fatalf("%+v", jerr)
	}
	if root.Line != 1 || root.Column != 1 {
		t.Errorf("root at %d:%d", root.Line, root.Column)
	}
	arr := root.Get("é")
	if arr.Line != 2 || arr.Column != 8 || len(arr.Items) != 4 {
		t.Errorf("array at %d:%d", arr.Line, arr.Column)
	}
	tags := []string{yaml.TagInt, yaml.TagFloat, yaml.TagBool, yaml.TagNull}
	cols := []int{9, 12, 20, 26}
	for i, it := range arr.Items {
		if it.Tag != tags[i] || it.Column != cols[i] || it.Line != 2 {
			t.Errorf("item %d = %s at %d:%d", i, it.Tag, it.Line, it.Column)
		}
	}
	c := root.Get("b").Get("c")
	if c.Value != "xé😀\n" || c.Line != 3 || c.Column != 13 {
		t.Errorf("c = %q at %d:%d", c.Value, c.Line, c.Column)
	}
	js, err := root.JSON()
	if err != nil || !json.Valid(js) {
		t.Errorf("JSON() = %s, %v", js, err)
	}
}

func TestParseJSONLongLine(t *testing.T) {
	t.Parallel()
	// Columns on one long line are counted incrementally: a large minified
	// document parses in linear time.
	var b strings.Builder
	b.WriteString(`{"a":[`)
	for i := range 200_000 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"é"`)
	}
	b.WriteString(`]}`)
	root, jerr := parseJSON([]byte(b.String()), 100, 1_000_000)
	if jerr != nil {
		t.Fatal(jerr.msg)
	}
	items := root.Get("a").Items
	if last := items[len(items)-1]; last.Column != 7+4*(len(items)-1) {
		t.Errorf("last column = %d", last.Column)
	}
}

func TestParseJSONErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src       string
		msg       string
		line, col int
	}{
		{`{"a": 01}`, "expected ',' or '}'", 1, 8},
		{`{"a": 1.}`, "decimal point", 1, 9},
		{`{"a": 1e}`, "exponent", 1, 9},
		{`{"a": tru}`, "expected true", 1, 7},
		{`{"a": "x`, "unterminated string", 1, 7},
		{"{\"a\": \"x\ny\"}", "control character", 1, 9},
		{`{"a": "\q"}`, `invalid escape sequence \q`, 1, 8},
		{`{"a": "\u12"}`, `invalid \u escape`, 1, 8},
		{`{a: 1}`, "expected a string key", 1, 2},
		{`{"a" 1}`, "expected ':'", 1, 6},
		{`[1 2]`, "expected ',' or ']'", 1, 4},
		{`[1,]`, "expected a value", 1, 4},
		{`{"a": 1} x`, "unexpected text", 1, 10},
		{"{\"a\":\n  \"\xff\"}", "not valid UTF-8", 2, 4},
		{`{"a": 1, "a": 2}`, `duplicate key "a"`, 1, 10},
		{"[", "unexpected end", 1, 2},
	}
	for _, tt := range tests {
		_, jerr := parseJSON([]byte(tt.src), 100, 1000)
		if jerr == nil {
			t.Errorf("%q: no error", tt.src)
			continue
		}
		if !strings.Contains(jerr.msg, tt.msg) || jerr.line != tt.line || jerr.column != tt.col {
			t.Errorf("%q: %q at %d:%d, want %q at %d:%d", tt.src, jerr.msg, jerr.line, jerr.column, tt.msg, tt.line, tt.col)
		}
	}
	if _, jerr := parseJSON([]byte(`[1,2,3,4]`), 100, 3); jerr == nil || !jerr.tooLarge {
		t.Errorf("node limit: %+v", jerr)
	}
	if _, jerr := parseJSON([]byte(`"\ud800x"`), 100, 3); jerr != nil {
		t.Errorf("lone surrogate: %+v", jerr)
	}
}
