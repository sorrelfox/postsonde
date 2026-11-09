package template

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Dot notation (ENV-13): {{auth.token}} reads the field "token" of the
// JSON value of the variable auth when no scope defines the name
// "auth.token" itself. See docs/templates.md.

// SplitPath returns the candidate splits of a dotted name into a base
// variable and a path inside its JSON value, longest base first:
// "a.b.c" gives ("a.b", ["c"]) then ("a", ["b", "c"]). A name with
// whitespace (dynamic values with arguments), with an empty part
// ("a..b", ".a", "a.") or without a dot has no splits.
func SplitPath(name string) []PathSplit {
	if !strings.Contains(name, ".") || strings.ContainsAny(name, spaces) {
		return nil
	}
	parts := strings.Split(name, ".")
	for _, p := range parts {
		if p == "" {
			return nil
		}
	}
	out := make([]PathSplit, 0, len(parts)-1)
	for n := len(parts) - 1; n >= 1; n-- {
		out = append(out, PathSplit{Base: strings.Join(parts[:n], "."), Path: parts[n:]})
	}
	return out
}

// PathSplit is a dotted name split into the variable it starts with and the
// keys (or array indexes) that follow.
type PathSplit struct {
	Base string
	Path []string
}

// pathBase returns the first split of name whose base the chain defines.
func (c Chain) pathBase(name string) (PathSplit, bool) {
	for _, s := range SplitPath(name) {
		if _, ok := c.Lookup(s.Base); ok {
			return s, true
		}
	}
	return PathSplit{}, false
}

// Select walks path through the JSON document value and returns what it
// finds: a string as its text, any other value as compact JSON (numbers as
// written, objects with their keys in order). Object keys match exactly; an
// array takes a decimal index from 0. The error says, without the value,
// which step failed ("the value of {{auth}} is not JSON", "{{auth}} has no
// field \"token\"").
func Select(base, value string, path []string) (string, error) {
	cur := json.RawMessage(strings.TrimSpace(value))
	if !json.Valid(cur) {
		return "", pathError("the value of " + braces(base) + " is not JSON")
	}
	at := base
	for _, key := range path {
		switch firstByte(cur) {
		case '{':
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(cur, &obj); err != nil {
				return "", pathError("the value of " + braces(at) + " is not JSON")
			}
			next, ok := obj[key]
			if !ok {
				return "", pathError(braces(at) + " has no field " + strconv.Quote(key))
			}
			cur = next
		case '[':
			var arr []json.RawMessage
			if err := json.Unmarshal(cur, &arr); err != nil {
				return "", pathError("the value of " + braces(at) + " is not JSON")
			}
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || key != strconv.Itoa(i) {
				return "", pathError(braces(at) + " is a list: " + strconv.Quote(key) + " is not an index")
			}
			if i >= len(arr) {
				return "", pathError(braces(at) + " has no element " + key)
			}
			cur = arr[i]
		default:
			return "", pathError(braces(at) + " is not an object or a list")
		}
		at += "." + key
	}
	if firstByte(cur) == '"' {
		var s string
		if err := json.Unmarshal(cur, &s); err == nil {
			return s, nil
		}
	}
	var b bytes.Buffer
	if err := json.Compact(&b, cur); err != nil {
		return "", pathError("the value of " + braces(base) + " is not JSON")
	}
	return b.String(), nil
}

type pathError string

func (e pathError) Error() string { return string(e) }

func firstByte(raw json.RawMessage) byte {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	if len(raw) == 0 {
		return 0
	}
	return raw[0]
}
