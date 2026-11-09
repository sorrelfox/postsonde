// Package stablejson encodes values as JSON in one fixed, diff-friendly
// format, so that project files written by PostSonde are byte-identical
// whenever their content is the same.
//
// # Format
//
// [Marshal] produces:
//
//   - two-space indentation, no prefix, and exactly one trailing newline;
//   - no HTML escaping: '<', '>' and '&' in URLs and bodies are written as
//     is, not as <, > and &;
//   - "[]" for an empty slice and "null" for a nil one (the two are kept
//     apart on purpose; normalize before encoding if they should look the
//     same), and likewise "{}" and "null" for maps.
//
// # Key order
//
// The order of object members never depends on map iteration order:
//
//   - struct fields keep their declaration order (embedded struct fields
//     are inlined where the embedding field is declared);
//   - map keys are sorted, for every map, including map[string]any and
//     maps held in interface values at any depth. This is what
//     encoding/json always does; the tests pin it down.
//   - the members of a [encoding/json.RawMessage] keep the order they have
//     in the raw bytes.
//
// # Raw messages
//
// A json.RawMessage is compacted and then re-indented like the rest of the
// document, so hand-formatted or compact raw JSON comes out in the
// standard format, and a value decoded from a file and written again is
// byte-identical. Only whitespace changes: member order and string
// escapes inside the raw bytes are kept. Invalid raw JSON is an error.
//
// # Numbers and strings
//
// Floating-point output is deterministic: encoding/json prints the
// shortest representation that round-trips (strconv 'f' format, or 'e'
// format for exponents below -6 or from 21 up, as in ECMAScript), so the
// same float64 always produces the same text. NaN and ±Inf cannot be
// encoded and return an error. Strings are valid UTF-8 on output: invalid
// bytes are replaced with U+FFFD, control characters and U+2028/U+2029
// are escaped.
//
// # Unknown keys
//
// [SplitUnknown] decodes a JSON object into a struct and returns the
// top-level members that the struct has no field for; [MarshalWithExtra]
// writes the struct back with those members appended after the known
// fields, in sorted key order. Together they let a file written by a newer
// version of PostSonde (or edited by hand) keep keys the current version
// does not understand across a load/save cycle.
//
// The format follows encoding/json (v1) behaviour. The golden files in
// testdata/ catch any change to it, for example after a Go upgrade.
package stablejson
