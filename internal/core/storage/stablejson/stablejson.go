package stablejson

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Indent is the indentation used for each nesting level.
const Indent = "  "

// Marshal returns the stable JSON encoding of v: two-space indentation,
// sorted map keys, no HTML escaping and exactly one trailing newline. See
// the package documentation for the full format.
func Marshal(v any) ([]byte, error) {
	return encode(v, true)
}

// encode encodes v without HTML escaping, indented or compact. The result
// always ends in a single newline when indented and never when compact.
func encode(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", Indent)
	}
	// Encode writes exactly one '\n' after the value.
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("stablejson: %w", err)
	}
	out := buf.Bytes()
	if !indent {
		out = bytes.TrimSuffix(out, []byte("\n"))
	}
	return out, nil
}
