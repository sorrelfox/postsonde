package jsonfmt

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want *SyntaxError // nil: valid
	}{
		{name: "object", src: `{"a": [1, 2.50, -3e+10], "b": null}`},
		{name: "scalar", src: ` 12345678901234567890 `},
		{name: "variable as value", src: `{"id": {{userId}}}`},
		{name: "variable with spaces", src: `{"id": {{ user id }} }`},
		{name: "variable in string", src: `{"name": "Hi {{name}}!"}`},
		{name: "variable in array and number", src: `[{{a}}, -{{b}}, {{c}}.5]`},
		{name: "escaped braces in string", src: `{"t": "\\{{x}}"}`},
		{
			name: "missing value",
			src:  `{"a":}`,
			want: &SyntaxError{Offset: 5, Offset16: 5, Line: 1, Column: 6, Message: "Invalid character '}' looking for beginning of value"},
		},
		{
			name: "trailing comma",
			src:  "[1,\n  2,\n]",
			want: &SyntaxError{Offset: 9, Offset16: 9, Line: 3, Column: 1, Message: "Invalid character ']' looking for beginning of value"},
		},
		{
			name: "ends early",
			src:  "{\"a\": 1",
			want: &SyntaxError{Offset: 7, Offset16: 7, Line: 1, Column: 8, Message: "Unexpected end of JSON input"},
		},
		{
			name: "empty",
			src:  "",
			want: &SyntaxError{Offset: 0, Offset16: 0, Line: 1, Column: 1, Message: "Unexpected end of JSON input"},
		},
		{
			name: "second value",
			src:  `{} x`,
			want: &SyntaxError{Offset: 3, Offset16: 3, Line: 1, Column: 4, Message: "Invalid character 'x' after top-level value"},
		},
		{
			name: "CRLF lines",
			src:  "{\r\n\"a\": 1,\r\n\"b\" 2}",
			want: &SyntaxError{Offset: 16, Offset16: 16, Line: 3, Column: 5, Message: "Invalid character '2' after object key"},
		},
		{
			// "é" is 2 bytes and 1 UTF-16 unit, "😀" 4 bytes and 2 units.
			name: "non-ASCII before the error",
			src:  "{\"é😀\": \"x\",\n \"ü\": tru }",
			want: &SyntaxError{Offset: 26, Offset16: 22, Line: 2, Column: 10, Message: "Invalid character ' ' in literal true (expecting 'e')"},
		},
		{
			name: "non-ASCII offending character",
			src:  `{"a": é}`,
			want: &SyntaxError{Offset: 6, Offset16: 6, Line: 1, Column: 7, Message: "Invalid character 'é' looking for beginning of value"},
		},
		{
			name: "variable where a key belongs",
			src:  `{{{key}}: 1}`,
			want: &SyntaxError{Offset: 1, Offset16: 1, Line: 1, Column: 2, Message: "Unexpected variable {{key}} looking for beginning of object key string"},
		},
		{
			name: "two variables in a row",
			src:  `{"a": {{x}} {{y}}}`,
			want: &SyntaxError{Offset: 12, Offset16: 12, Line: 1, Column: 13, Message: "Unexpected variable {{y}} after object key:value pair"},
		},
		{
			name: "error after a variable keeps its offset",
			src:  `{"a": {{x}}, }`,
			want: &SyntaxError{Offset: 13, Offset16: 13, Line: 1, Column: 14, Message: "Invalid character '}' looking for beginning of object key string"},
		},
		{
			name: "unterminated variable is text",
			src:  `{"a": {{x}`,
			want: &SyntaxError{Offset: 7, Offset16: 7, Line: 1, Column: 8, Message: "Invalid character '{' looking for beginning of object key string"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Validate(tt.src)
			if !sameError(got, tt.want) {
				t.Fatalf("Validate(%q) = %#v, want %#v", tt.src, got, tt.want)
			}
			if got != nil {
				checkOffsets(t, tt.src, got)
			}
		})
	}
}

// checkOffsets checks Offset16 against a reference conversion.
func checkOffsets(t *testing.T, src string, e *SyntaxError) {
	t.Helper()
	if want := len(utf16.Encode([]rune(src[:e.Offset]))); e.Offset16 != want {
		t.Errorf("Offset16 = %d, want %d", e.Offset16, want)
	}
}

func sameError(a, b *SyntaxError) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestBeautify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		src    string
		indent int
		want   string
	}{
		{
			name:   "nested",
			src:    ` {"a":1,"b":[true,null,{}],"c":[] ,"d":{"e":"x"}} ` + "\n",
			indent: 2,
			want:   "{\n  \"a\": 1,\n  \"b\": [\n    true,\n    null,\n    {}\n  ],\n  \"c\": [],\n  \"d\": {\n    \"e\": \"x\"\n  }\n}",
		},
		{
			name:   "number literals and escapes kept",
			src:    "[1.50e+3,12345678901234567890,-0.0,1E-7,\"\\u00e9\\n\\\"\",0.1000]",
			indent: 4,
			want:   "[\n    1.50e+3,\n    12345678901234567890,\n    -0.0,\n    1E-7,\n    \"\\u00e9\\n\\\"\",\n    0.1000\n]",
		},
		{
			name:   "tab indent",
			src:    `{"a":1}`,
			indent: 0,
			want:   "{\n\t\"a\": 1\n}",
		},
		{
			name:   "indent clamped",
			src:    `[1]`,
			indent: 20,
			want:   "[\n        1\n]",
		},
		{
			name:   "strings keep their whitespace",
			src:    `{"a b" : "  c\td  "}`,
			indent: 2,
			want:   "{\n  \"a b\": \"  c\\td  \"\n}",
		},
		{
			name:   "variables kept as typed",
			src:    `{"id":{{ userId }},"tags":[{{a}},"x{{ b }}y"],"n":-{{n}}}`,
			indent: 2,
			want:   "{\n  \"id\": {{ userId }},\n  \"tags\": [\n    {{a}},\n    \"x{{ b }}y\"\n  ],\n  \"n\": -{{n}}\n}",
		},
		{
			name:   "digits next to a variable",
			src:    `[1111, {{x}}, "1111 {{y}}"]`,
			indent: 1,
			want:   "[\n 1111,\n {{x}},\n \"1111 {{y}}\"\n]",
		},
		{
			name:   "non-ASCII",
			src:    `{"zoë":"日本","e":"😀"}`,
			indent: 2,
			want:   "{\n  \"zoë\": \"日本\",\n  \"e\": \"😀\"\n}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Beautify(tt.src, tt.indent)
			if err != nil {
				t.Fatalf("Beautify(%q): %v", tt.src, err)
			}
			if got != tt.want {
				t.Errorf("Beautify(%q) =\n%s\nwant\n%s", tt.src, got, tt.want)
			}
			// Minify undoes it.
			min1, err := Minify(got)
			if err != nil {
				t.Fatalf("Minify(beautified): %v", err)
			}
			min2, _ := Minify(tt.src)
			if min1 != min2 {
				t.Errorf("Minify(Beautify(src)) = %q, want Minify(src) = %q", min1, min2)
			}
		})
	}
}

func TestMinify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, src, want string
	}{
		{"whitespace", "{\n  \"a\" : [ 1 , 2 ],\r\n\t\"b\": \"x y\"\n}\n", `{"a":[1,2],"b":"x y"}`},
		{"numbers kept", "[ 1.50e+3 , 12345678901234567890 ]", `[1.50e+3,12345678901234567890]`},
		{"no HTML escaping", `{"h": "<b>&</b>"}`, `{"h":"<b>&</b>"}`},
		{"variables kept", "{ \"id\" : {{ id }} ,\n \"s\": \"{{ a b }}\" }", `{"id":{{ id }},"s":"{{ a b }}"}`},
		{"escaped quote before variable", `[ "\"", {{x}} ]`, `["\"",{{x}}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Minify(tt.src)
			if err != nil {
				t.Fatalf("Minify(%q): %v", tt.src, err)
			}
			if got != tt.want {
				t.Errorf("Minify(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestFormatRefusesInvalid(t *testing.T) {
	t.Parallel()

	src := "{\n  \"a\": 1,\n  \"b\": 2,\n}"
	want := &SyntaxError{Offset: 22, Offset16: 22, Line: 4, Column: 1, Message: "Invalid character '}' looking for beginning of object key string"}
	for name, format := range map[string]func(string) (string, *SyntaxError){
		"Beautify": func(s string) (string, *SyntaxError) { return Beautify(s, 2) },
		"Minify":   Minify,
	} {
		got, err := format(src)
		if got != "" || !sameError(err, want) {
			t.Errorf("%s(invalid) = %q, %+v; want \"\", %+v", name, got, err, want)
		}
	}
}

func TestSyntaxErrorError(t *testing.T) {
	t.Parallel()
	if got := Validate("[").Error(); got != "Unexpected end of JSON input" {
		t.Errorf("Error() = %q", got)
	}
}

// FuzzFormat checks that formatting valid JSON keeps its meaning and its
// variables, and that error positions are inside the source.
func FuzzFormat(f *testing.F) {
	for _, s := range []string{
		`{"a": [1, 2.5e3, "x"]}`, `{"id": {{ id }}, "s": "a{{b}}c"}`, `[`, `{"é": tru}`, "\"\\\"{{x}}\"",
		`{{a}}{{b}}`, "[\r\n1]", `{"a":"é"}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		verr := Validate(src)
		if verr != nil {
			if verr.Offset < 0 || verr.Offset > len(src) || verr.Line < 1 || verr.Column < 1 {
				t.Fatalf("Validate(%q): bad position %+v", src, verr)
			}
		}
		pretty, berr := Beautify(src, 2)
		small, merr := Minify(src)
		if (verr == nil) != (berr == nil) || (verr == nil) != (merr == nil) {
			t.Fatalf("Validate, Beautify and Minify disagree on %q: %v, %v, %v", src, verr, berr, merr)
		}
		if verr != nil {
			return
		}
		if again, err := Minify(pretty); err != nil || again != small {
			t.Fatalf("Minify(Beautify(%q)) = %q, %v; want %q", src, again, err, small)
		}
		if strings.Count(pretty, "{{") != strings.Count(src, "{{") {
			t.Fatalf("Beautify(%q) = %q changed the variables", src, pretty)
		}
		if !strings.Contains(src, "{{") {
			if !json.Valid([]byte(src)) || !json.Valid([]byte(small)) {
				t.Fatalf("Minify(%q) = %q: valid JSON expected on both sides", src, small)
			}
		}
	})
}
