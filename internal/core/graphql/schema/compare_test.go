package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The semantic comparison of introspection JSON used by the round-trip
// tests. Both sides are decoded into generic values and normalized:
//
//   - A key whose value is null, false or [] is the same as a missing key:
//     older servers omit newer fields (isRepeatable, isOneOf, input value
//     deprecation), graphql-js 14 sends interfaces: null for interfaces,
//     and wrappers carry name: null.
//   - "" is the same as null for description, deprecationReason and
//     specifiedByURL (some servers send "" for "no description").
//   - specifiedByUrl (graphql-js 15) is specifiedByURL.
//   - types, directives and possibleTypes are sets (ordered by name before
//     comparing) and so are directive locations. Every other list keeps its
//     order: fields, arguments, input fields, enum values and interfaces
//     must come back in declaration order.

var emptyIsNull = map[string]bool{"description": true, "deprecationReason": true, "specifiedByURL": true}

var unorderedByName = map[string]bool{"types": true, "directives": true, "possibleTypes": true}

func normalize(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if k == "specifiedByUrl" {
				k = "specifiedByURL"
			}
			n := normalize(val, k)
			if isDefault(n, k) {
				continue
			}
			out[k] = n
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e, key)
		}
		switch {
		case unorderedByName[key]:
			slices.SortStableFunc(out, func(a, b any) int { return strings.Compare(nameOf(a), nameOf(b)) })
		case key == "locations":
			slices.SortFunc(out, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
		}
		return out
	}
	return v
}

func isDefault(v any, key string) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case []any:
		return len(x) == 0
	case string:
		return x == "" && emptyIsNull[key]
	}
	return false
}

func nameOf(v any) string {
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["name"].(string); ok {
			return s
		}
	}
	return ""
}

// schemaValue extracts the normalized __schema value from introspection
// JSON in any of the accepted envelopes.
func schemaValue(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d, ok := top["data"].(map[string]any); ok {
		top = d
	}
	if s, ok := top["__schema"].(map[string]any); ok {
		top = s
	}
	return normalize(top, "").(map[string]any)
}

// diff lists the paths at which a and b differ, at most limit of them.
func diff(a, b any, path string, out *[]string, limit int) {
	if len(*out) >= limit {
		return
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: %T vs %T", path, a, b))
			return
		}
		keys := make([]string, 0, len(x)+len(y))
		for k := range x {
			keys = append(keys, k)
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			xv, xok := x[k]
			yv, yok := y[k]
			switch {
			case !xok:
				*out = append(*out, fmt.Sprintf("%s.%s: missing on the left, right has %v", path, k, short(yv)))
			case !yok:
				*out = append(*out, fmt.Sprintf("%s.%s: left has %v, missing on the right", path, k, short(xv)))
			default:
				diff(xv, yv, path+"."+k, out, limit)
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: %T vs %T", path, a, b))
			return
		}
		if len(x) != len(y) {
			*out = append(*out, fmt.Sprintf("%s: %d vs %d items", path, len(x), len(y)))
		}
		for i := range min(len(x), len(y)) {
			p := fmt.Sprintf("%s[%d]", path, i)
			if n := nameOf(x[i]); n != "" {
				p = fmt.Sprintf("%s[%s]", path, n)
			}
			diff(x[i], y[i], p, out, limit)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, fmt.Sprintf("%s: %v vs %v", path, short(a), short(b)))
		}
	}
}

func short(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

func assertSameIntrospection(t *testing.T, want, got map[string]any) {
	t.Helper()
	var diffs []string
	diff(want, got, "__schema", &diffs, 20)
	if len(diffs) > 0 {
		t.Errorf("introspection differs (fixture vs model):\n  %s", strings.Join(diffs, "\n  "))
	}
}

func TestNormalizeRules(t *testing.T) {
	a := normalize(map[string]any{
		"description":    "",
		"specifiedByUrl": "https://x",
		"isRepeatable":   false,
		"interfaces":     nil,
		"fields":         []any{},
		"defaultValue":   "",
		"types":          []any{map[string]any{"name": "B"}, map[string]any{"name": "A"}},
		"enumValues":     []any{map[string]any{"name": "B"}, map[string]any{"name": "A"}},
	}, "")
	want := map[string]any{
		"specifiedByURL": "https://x",
		"defaultValue":   "",
		"types":          []any{map[string]any{"name": "A"}, map[string]any{"name": "B"}},
		"enumValues":     []any{map[string]any{"name": "B"}, map[string]any{"name": "A"}},
	}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("normalize = %v, want %v", a, want)
	}
	var diffs []string
	diff(map[string]any{"a": []any{1.0, 2.0}}, map[string]any{"a": []any{1.0, 3.0}, "b": true}, "$", &diffs, 10)
	if len(diffs) != 2 {
		t.Errorf("diff found %v, want 2 differences", diffs)
	}
}
