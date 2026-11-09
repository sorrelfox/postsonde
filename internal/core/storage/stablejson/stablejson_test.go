package stablejson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// Request- and folder-like shapes, close to what the project files hold.

type header struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type body struct {
	Mode string `json:"mode"`
	Text string `json:"text"`
}

type request struct {
	Version        int               `json:"version"`
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Headers        []header          `json:"headers"`
	Query          map[string]string `json:"query,omitempty"`
	Body           *body             `json:"body,omitempty"`
	TimeoutSeconds float64           `json:"timeoutSeconds"`
	Tags           []string          `json:"tags"`
}

type folder struct {
	Version   int               `json:"version"`
	Name      string            `json:"name"`
	Order     []string          `json:"order"`
	Variables map[string]string `json:"variables"`
	Auth      map[string]any    `json:"auth,omitempty"`
}

type emptiness struct {
	NilSlice        []string          `json:"nilSlice"`
	EmptySlice      []string          `json:"emptySlice"`
	NilMap          map[string]string `json:"nilMap"`
	EmptyMap        map[string]string `json:"emptyMap"`
	OmitNilSlice    []string          `json:"omitNilSlice,omitempty"`
	OmitEmptySlice  []string          `json:"omitEmptySlice,omitempty"`
	NilPointer      *body             `json:"nilPointer"`
	EmptyString     string            `json:"emptyString"`
	OmitEmptyString string            `json:"omitEmptyString,omitempty"`
}

type withRaw struct {
	Name    string                     `json:"name"`
	Payload json.RawMessage            `json:"payload"`
	Missing json.RawMessage            `json:"missing"`
	ByKey   map[string]json.RawMessage `json:"byKey"`
}

type text struct {
	HTML        string `json:"html"`
	Unicode     string `json:"unicode"`
	Escapes     string `json:"escapes"`
	LineSep     string `json:"lineSep"`
	InvalidUTF8 string `json:"invalidUtf8"`
}

func sampleRequest() request {
	return request{
		Version: 1,
		ID:      "01J8Z6Q0XK",
		Name:    "Search users",
		Method:  "GET",
		URL:     "https://api.example.com/users?q=<name>&limit=10",
		Headers: []header{
			{Name: "Accept", Value: "application/json", Enabled: true},
			{Name: "X-Trace", Value: "a&b", Enabled: false},
		},
		Query: map[string]string{"limit": "10", "q": "<name>", "after": "", "Zeta": "z"},
		Body: &body{
			Mode: "json",
			Text: "{\n  \"name\": \"Zoë <admin>\"\n}",
		},
		TimeoutSeconds: 2.5,
	}
}

// TestMarshalGolden checks the output format against testdata/marshal/*.
// Regenerate with:
//
//	go test ./internal/core/storage/stablejson -run TestMarshalGolden -update
func TestMarshalGolden(t *testing.T) {
	tests := []struct {
		name string
		v    any
	}{
		{name: "request", v: sampleRequest()},
		{name: "folder", v: folder{
			Version: 1,
			Name:    "Users & roles",
			Order:   []string{"list.json", "create.json", "admin"},
			Variables: map[string]string{
				"baseUrl": "https://api.example.com",
				"apiKey":  "{{secret}}",
				"Accept":  "*/*",
			},
			Auth: map[string]any{"type": "bearer", "token": "{{token}}", "inherit": false},
		}},
		{name: "nested_any", v: map[string]any{
			"zebra": []any{3, "two", 1.5, true, nil, map[string]any{"b": 2, "a": 1}},
			"alpha": map[string]any{
				"nested": map[string]any{"z": "last", "m": "middle", "a": "first"},
				"empty":  map[string]any{},
				"list":   []any{},
			},
			"Mixed": "capitals sort before lowercase",
			"10":    "numeric strings sort as strings",
			"9":     "so 10 comes before 9",
		}},
		{name: "empty_vs_nil", v: emptiness{
			EmptySlice:     []string{},
			EmptyMap:       map[string]string{},
			OmitEmptySlice: []string{},
		}},
		{name: "raw_message", v: withRaw{
			Name:    "raw",
			Payload: json.RawMessage(" {\"z\" :1,\"a\":[ 1,2 ,{ }],\n\t\"html\":\"<b>&amp;</b>\", \"esc\":\"\\u003c\"}  "),
			ByKey: map[string]json.RawMessage{
				"second": json.RawMessage(`[]`),
				"first":  json.RawMessage(`{"k":"v"}`),
			},
		}},
		{name: "floats", v: []float64{
			0, 1, -1.5, 0.1, 1.0 / 3, 1e-6, 1e-7, 1e20, 1e21, 123456789.125,
			math.MaxFloat64, math.SmallestNonzeroFloat64, math.Copysign(0, -1),
		}},
		{name: "text", v: text{
			HTML:        `<script>if (a && b > c) {}</script>`,
			Unicode:     "héllo — 世界 🚀",
			Escapes:     "tab\tnewline\nquote\"backslash\\ctrl\x01",
			LineSep:     "a\u2028b\u2029c",
			InvalidUTF8: "bad\xffbyte",
		}},
		{name: "scalar", v: "just a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Marshal(tt.v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			golden.Assert(t, "marshal/"+tt.name, got)

			// Load/save round trip: decoding the output into a fresh value
			// of the same type and encoding it again is byte-identical.
			ptr := reflect.New(reflect.TypeOf(tt.v))
			if err := json.Unmarshal(got, ptr.Interface()); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			again, err := Marshal(ptr.Elem().Interface())
			if err != nil {
				t.Fatalf("Marshal after round trip: %v", err)
			}
			if !bytes.Equal(again, got) {
				t.Errorf("round trip changed the output:\nfirst:\n%s\nagain:\n%s", got, again)
			}
		})
	}
}

// TestMarshalDeterministic encodes values whose maps are rebuilt each time
// (so their iteration order differs) and expects identical bytes.
func TestMarshalDeterministic(t *testing.T) {
	build := []struct {
		name string
		v    func() any
	}{
		{name: "map[string]string", v: func() any {
			m := make(map[string]string)
			for i := range 200 {
				m[fmt.Sprintf("key-%03d", (i*37)%200)] = fmt.Sprint(i)
			}
			return m
		}},
		{name: "map[string]any nested", v: func() any {
			m := make(map[string]any)
			for i := range 50 {
				m[fmt.Sprint(i)] = map[string]any{"b": i, "a": []any{i, map[string]any{"y": 1, "x": 2}}}
			}
			return m
		}},
		{name: "any holding struct with maps", v: func() any {
			var v any = sampleRequest()
			return v
		}},
	}
	for _, tt := range build {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, err := Marshal(tt.v())
			if err != nil {
				t.Fatal(err)
			}
			for range 50 {
				got, err := Marshal(tt.v())
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, first) {
					t.Fatalf("output differs between runs:\n%s\nvs\n%s", first, got)
				}
			}
		})
	}
}

func TestMarshalFormat(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{name: "string keeps html", v: "<a href=\"x?a=1&b=2\">", want: `"<a href=\"x?a=1&b=2\">"` + "\n"},
		{name: "null", v: nil, want: "null\n"},
		{name: "empty object", v: struct{}{}, want: "{}\n"},
		{name: "empty array", v: []int{}, want: "[]\n"},
		{name: "two-space indent", v: map[string][]int{"a": {1}}, want: "{\n  \"a\": [\n    1\n  ]\n}\n"},
		{name: "raw with trailing newlines", v: json.RawMessage("[1]\n\n"), want: "[\n  1\n]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Marshal(tt.v)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal = %q, want %q", got, tt.want)
			}
			if !strings.HasSuffix(string(got), "\n") || strings.HasSuffix(string(got), "\n\n") {
				t.Errorf("output must end in exactly one newline: %q", got)
			}
		})
	}
}

func TestMarshalErrors(t *testing.T) {
	tests := []struct {
		name string
		v    any
	}{
		{name: "NaN", v: math.NaN()},
		{name: "Inf", v: math.Inf(1)},
		{name: "channel", v: make(chan int)},
		{name: "func in map", v: map[string]any{"f": func() {}}},
		{name: "invalid raw", v: json.RawMessage(`{"a":`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Marshal(tt.v)
			if err == nil {
				t.Fatalf("Marshal = %q, want an error", got)
			}
			if !strings.HasPrefix(err.Error(), "stablejson: ") {
				t.Errorf("error %q lacks the package prefix", err)
			}
		})
	}
}
