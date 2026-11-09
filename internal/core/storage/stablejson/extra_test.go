package stablejson

import (
	"bytes"
	"encoding/json"
	"maps"
	"testing"

	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

type Base struct {
	ID string `json:"id"`
}

type inner struct {
	Kind string `json:"kind"`
}

type settings struct {
	Base
	inner            // unexported, but its exported fields are promoted
	Name    string   `json:"name"`
	Theme   string   `json:"theme,omitempty"`
	Fonts   []string `json:"fonts,omitempty"`
	Skipped string   `json:"-"`
	Dash    string   `json:"-,"`
	Count   int
	private string
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestSplitUnknown(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantKnown settings
		wantExtra map[string]json.RawMessage
		wantErr   bool
	}{
		{
			name:      "no unknown keys",
			data:      `{"id":"1","name":"n","Count":2}`,
			wantKnown: settings{Base: Base{ID: "1"}, Name: "n", Count: 2},
		},
		{
			name:      "unknown keys kept raw",
			data:      `{"name":"n","future":{"b": 1, "a": [true]},"zzz":null,"aaa":"<x>"}`,
			wantKnown: settings{Name: "n"},
			wantExtra: map[string]json.RawMessage{
				"future": raw(`{"b": 1, "a": [true]}`),
				"zzz":    raw(`null`),
				"aaa":    raw(`"<x>"`),
			},
		},
		{
			name:      "zero-valued omitempty field is known",
			data:      `{"theme":"","fonts":[]}`,
			wantKnown: settings{Fonts: []string{}},
		},
		{
			name:      "keys match case-insensitively",
			data:      `{"NAME":"n","count":3,"Id":"x"}`,
			wantKnown: settings{Base: Base{ID: "x"}, Name: "n", Count: 3},
		},
		{
			name:      "dash-named field is known",
			data:      `{"-":"d"}`,
			wantKnown: settings{Dash: "d"},
		},
		{
			name:      "ignored and unexported fields are unknown",
			data:      `{"Skipped":"s","private":"p","kind":"k"}`,
			wantKnown: settings{inner: inner{Kind: "k"}},
			wantExtra: map[string]json.RawMessage{
				"Skipped": raw(`"s"`),
				"private": raw(`"p"`),
			},
		},
		{name: "not an object", data: `[1,2]`, wantErr: true},
		{name: "invalid json", data: `{"name":`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got settings
			extra, err := SplitUnknown([]byte(tt.data), &got)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SplitUnknown error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.Base != tt.wantKnown.Base || got.inner != tt.wantKnown.inner || got.Name != tt.wantKnown.Name || got.Theme != tt.wantKnown.Theme ||
				got.Count != tt.wantKnown.Count || got.Dash != tt.wantKnown.Dash || len(got.Fonts) != len(tt.wantKnown.Fonts) {
				t.Errorf("decoded %+v, want %+v", got, tt.wantKnown)
			}
			if !maps.EqualFunc(extra, tt.wantExtra, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
				t.Errorf("extra = %q, want %q", extra, tt.wantExtra)
			}
			if len(tt.wantExtra) == 0 && extra != nil {
				t.Errorf("extra = %#v, want nil", extra)
			}
		})
	}
}

func TestSplitUnknownNonStruct(t *testing.T) {
	var m map[string]any
	extra, err := SplitUnknown([]byte(`{"a":1,"b":2}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	if extra != nil || len(m) != 2 {
		t.Errorf("extra = %v, map = %v; want nil extra and both keys in the map", extra, m)
	}
}

type plain struct {
	Name  string `json:"name"`
	Order []int  `json:"order"`
}

func TestMarshalWithExtra(t *testing.T) {
	tests := []struct {
		name    string
		known   any
		extra   map[string]json.RawMessage
		want    string
		wantErr bool
	}{
		{
			name:  "no extra is Marshal",
			known: plain{Name: "a"},
			want:  "{\n  \"name\": \"a\",\n  \"order\": null\n}\n",
		},
		{
			name:  "extra sorted after known fields",
			known: plain{Name: "a", Order: []int{1}},
			extra: map[string]json.RawMessage{"z": raw(`1`), "b": raw(` { "y":2,"x" : 1 } `), "a": raw(`"<&>"`)},
			want: "{\n  \"name\": \"a\",\n  \"order\": [\n    1\n  ],\n" +
				"  \"a\": \"<&>\",\n  \"b\": {\n    \"y\": 2,\n    \"x\": 1\n  },\n  \"z\": 1\n}\n",
		},
		{
			name:  "known field wins over extra",
			known: plain{Name: "a"},
			extra: map[string]json.RawMessage{"name": raw(`"stale"`), "x": raw(`true`)},
			want:  "{\n  \"name\": \"a\",\n  \"order\": null,\n  \"x\": true\n}\n",
		},
		{
			name:  "empty known object",
			known: struct{}{},
			extra: map[string]json.RawMessage{"k": raw(`[]`)},
			want:  "{\n  \"k\": []\n}\n",
		},
		{
			name:  "only duplicates",
			known: map[string]int{"a": 1},
			extra: map[string]json.RawMessage{"a": raw(`2`)},
			want:  "{\n  \"a\": 1\n}\n",
		},
		{
			name:  "nil raw is null",
			known: struct{}{},
			extra: map[string]json.RawMessage{"k": nil},
			want:  "{\n  \"k\": null\n}\n",
		},
		{
			name:  "key with quotes and html",
			known: struct{}{},
			extra: map[string]json.RawMessage{`a"<b>`: raw(`1`)},
			want:  "{\n  \"a\\\"<b>\": 1\n}\n",
		},
		{name: "invalid raw", known: struct{}{}, extra: map[string]json.RawMessage{"k": raw(`{`)}, wantErr: true},
		{name: "known not an object", known: "s", extra: map[string]json.RawMessage{"k": raw(`1`)}, wantErr: true},
		{name: "known nil pointer", known: (*plain)(nil), extra: map[string]json.RawMessage{"k": raw(`1`)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MarshalWithExtra(tt.known, tt.extra)
			if (err != nil) != tt.wantErr {
				t.Fatalf("MarshalWithExtra error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && string(got) != tt.want {
				t.Errorf("MarshalWithExtra =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestUnknownRoundTrip loads a file with unknown keys and saves it again.
// A hand-edited file is normalized once (unknown keys move after the known
// fields, sorted); after that, load/save cycles are byte-identical.
//
//	go test ./internal/core/storage/stablejson -run TestUnknownRoundTrip -update
func TestUnknownRoundTrip(t *testing.T) {
	input := []byte(`{"zNew":{"b":1,"a":2},"name":"Users","aOld":[1, 2],"order":[3,1],"html":"<a&b>"}`)

	save := func(data []byte) []byte {
		t.Helper()
		var p plain
		extra, err := SplitUnknown(data, &p)
		if err != nil {
			t.Fatalf("SplitUnknown: %v", err)
		}
		out, err := MarshalWithExtra(p, extra)
		if err != nil {
			t.Fatalf("MarshalWithExtra: %v", err)
		}
		return out
	}

	first := save(input)
	golden.Assert(t, "unknown_round_trip", first)
	for range 3 {
		if again := save(first); !bytes.Equal(again, first) {
			t.Fatalf("round trip not stable:\nfirst:\n%s\nagain:\n%s", first, again)
		}
	}
}
