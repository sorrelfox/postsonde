package stablejson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// SplitUnknown decodes the JSON object in data into known, which must be a
// non-nil pointer, and returns the top-level members of the object that
// known has no field for. It returns a nil map when there are none.
//
// A member is known when its key names a field of known's struct type the
// way encoding/json matches keys: by the field's json tag name (or Go name
// when untagged), exactly or case-insensitively, including fields promoted
// from embedded structs. Fields tagged "-" and unexported fields are not
// known, so members with those names are returned. Only top-level members
// are split; unknown keys inside nested objects are dropped as usual.
//
// If known does not point to a struct (a map, for instance), every member
// was decoded into it and SplitUnknown returns nil.
//
// Write the value back with [MarshalWithExtra] to keep the unknown members.
func SplitUnknown(data []byte, known any) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, known); err != nil {
		return nil, fmt.Errorf("stablejson: %w", err)
	}
	names, ok := fieldNames(reflect.TypeOf(known))
	if !ok {
		return nil, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, fmt.Errorf("stablejson: %w", err)
	}
	var extra map[string]json.RawMessage
	for key, raw := range members {
		if isKnown(names, key) {
			continue
		}
		if extra == nil {
			extra = make(map[string]json.RawMessage)
		}
		extra[key] = raw
	}
	return extra, nil
}

// MarshalWithExtra is [Marshal] for a value that encodes as a JSON object,
// with the members of extra appended after known's own members, sorted by
// key. An extra member whose key known already encodes is dropped: the
// known value wins. A nil or empty json.RawMessage is written as null.
//
// With an empty extra it is the same as Marshal(known).
func MarshalWithExtra(known any, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return Marshal(known)
	}
	obj, err := encode(known, false)
	if err != nil {
		return nil, err
	}
	if len(obj) < 2 || obj[0] != '{' {
		return nil, fmt.Errorf("stablejson: %T does not encode as a JSON object", known)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(obj, &fields); err != nil {
		return nil, fmt.Errorf("stablejson: %w", err)
	}

	var buf bytes.Buffer
	buf.Write(obj[:len(obj)-1]) // all but the closing brace
	n := len(fields)
	for _, key := range slices.Sorted(maps.Keys(extra)) {
		if _, dup := fields[key]; dup {
			continue
		}
		if n > 0 {
			buf.WriteByte(',')
		}
		n++
		k, err := encode(key, false)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		raw := extra[key]
		if len(raw) == 0 {
			raw = json.RawMessage("null")
		}
		buf.Write(raw)
	}
	buf.WriteByte('}')

	// Encoding the assembled object as a RawMessage validates it and
	// re-indents every member, extra ones included, in the standard format.
	return Marshal(json.RawMessage(buf.Bytes()))
}

// fieldNames returns the JSON member names that encoding/json maps to
// fields of t (after dereferencing pointers), and false if t is not a
// struct type.
func fieldNames(t reflect.Type) (map[string]struct{}, bool) {
	if t == nil {
		return nil, false
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, false
	}
	names := make(map[string]struct{})
	collectFields(t, names, make(map[reflect.Type]bool))
	return names, true
}

func collectFields(t reflect.Type, names map[string]struct{}, seen map[reflect.Type]bool) {
	if seen[t] {
		return
	}
	seen[t] = true
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				// Promoted fields, also from unexported embedded structs.
				collectFields(ft, names, seen)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		names[name] = struct{}{}
	}
}

// isKnown reports whether encoding/json would decode key into one of the
// named fields: an exact match, or else a case-insensitive one.
func isKnown(names map[string]struct{}, key string) bool {
	if _, ok := names[key]; ok {
		return true
	}
	for name := range names {
		if strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}
