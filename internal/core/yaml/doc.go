// Package yaml reads the subset of YAML 1.2 that API description and
// collection files use (Insomnia v5 exports, OpenAPI and Swagger documents)
// into a generic tree with line and column positions, and converts it to
// JSON so the JSON importers can take it from there.
//
//	root, err := yaml.Parse(data)   // err: *yaml.Error (yaml.AsErr for errs)
//	js, err := root.JSON()          // ordered JSON text, aliases expanded
//	err = yaml.Unmarshal(data, &v)  // Parse + JSON + encoding/json
//
// Supported: block and flow mappings and sequences (including compact
// "- key: value" entries and sequences at their key's indentation), plain,
// single- and double-quoted scalars over several lines, literal (|) and
// folded (>) block scalars with chomping and indentation indicators,
// comments, anchors (&a) and aliases (*a), merge keys (<<), the core
// schema tags (!!str, !!int, !!float, !!bool, !!null, !!map, !!seq, plus
// !!binary and !!timestamp as strings), the %YAML directive and several
// documents in one stream (ParseAll).
//
// Plain scalars are typed with the YAML 1.2 core schema: null, ~ and an
// empty value are null; true and false (any case form the schema lists)
// are booleans; decimal, 0o octal and 0x hex integers; decimal floats,
// .inf and .nan. Everything else, including YAML 1.1's yes/no/on/off, is a
// string.
//
// Not supported, each reported as an *Error with code yaml.unsupported and
// the position: explicit keys (? key), collections or empty values as
// keys, custom and local tags (!Ref, !foo), %TAG directives, !!set and
// !!omap, and documents that are not UTF-8.
//
// Input size, nesting depth and the number of nodes (after alias
// expansion, so "billion laughs" documents fail fast) are bounded by
// Limits. See docs/yaml.md.
package yaml
