package jsonfmt

import (
	"reflect"
	"testing"
)

func TestTopLevelKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		keys []Key
		kind TopKind
		err  bool
	}{
		{name: "blank", src: " \n", kind: TopBlank},
		{name: "null", src: " null ", kind: TopNull},
		{name: "array", src: "[1]", kind: TopOther},
		{name: "variable", src: "{{vars}}", kind: TopOther},
		{name: "empty object", src: "{}", kind: TopObject},
		{
			name: "members in order, nested ones skipped",
			src:  `{"id": 1, "input": {"deep": [1, {"x": 2}]}, "é": "{{v}}"}`,
			keys: []Key{
				{Name: "id", Start: 1, End: 5},
				{Name: "input", Start: 10, End: 17},
				{Name: "é", Start: 44, End: 48},
			},
			kind: TopObject,
		},
		{
			name: "variables as values and names",
			src:  "{\n  \"n\": {{count}},\n  \"{{key}}\": 2\n}",
			keys: []Key{
				{Name: "n", Start: 4, End: 7},
				{Name: "1111111", Start: 22, End: 31, Variable: true},
			},
			kind: TopObject,
		},
		{name: "escaped name", src: `{"a\"b": 1}`, keys: []Key{{Name: `a"b`, Start: 1, End: 7}}, kind: TopObject},
		{name: "invalid", src: `{"a": }`, kind: TopOther, err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			keys, kind, err := TopLevelKeys(tt.src)
			if (err != nil) != tt.err {
				t.Fatalf("err = %v, want error %v", err, tt.err)
			}
			if kind != tt.kind {
				t.Errorf("kind = %v, want %v", kind, tt.kind)
			}
			if !reflect.DeepEqual(keys, tt.keys) {
				t.Errorf("keys = %+v\nwant   %+v", keys, tt.keys)
			}
			for _, k := range keys {
				if tt.src[k.Start] != '"' || tt.src[k.End-1] != '"' {
					t.Errorf("key %q range [%d,%d) is %q", k.Name, k.Start, k.End, tt.src[k.Start:k.End])
				}
			}
		})
	}
}
