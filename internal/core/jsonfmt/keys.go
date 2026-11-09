package jsonfmt

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Key is a member name of a JSON object.
type Key struct {
	// Name is the decoded name.
	Name string
	// Start and End are the byte range of the name in the source, quotes
	// included.
	Start, End int
	// Variable is true when the name holds a {{variable}}: what it names
	// is only known once it is rendered.
	Variable bool
}

// TopKind is the kind of a JSON text's top-level value.
type TopKind int

// Top-level value kinds of TopLevelKeys.
const (
	// TopBlank: the text is blank.
	TopBlank TopKind = iota
	// TopNull: the text is null.
	TopNull
	// TopObject: the text is an object.
	TopObject
	// TopOther: an array, string, number, boolean or {{variable}}.
	TopOther
)

// TopLevelKeys returns the member names of the object src holds, in source
// order, allowing {{variables}} as for Validate (GQL-03: the GraphQL
// variables editor compares them with the operation's variables). It also
// says what the top-level value is. Blank text is TopBlank, not an error;
// invalid JSON returns the error and no keys.
func TopLevelKeys(src string) ([]Key, TopKind, *SyntaxError) {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return nil, TopBlank, nil
	}
	if err := Validate(src); err != nil {
		return nil, TopOther, err
	}
	if trimmed == "null" {
		return nil, TopNull, nil
	}
	// A variable masks to digits: "{{vars}}" is not an object.
	masked, _ := mask(src)
	if first := bytes.TrimSpace(masked); first[0] != '{' {
		return nil, TopOther, nil
	}
	dec := json.NewDecoder(bytes.NewReader(masked))
	if _, err := dec.Token(); err != nil { // "{"
		return nil, TopObject, nil
	}
	var keys []Key
	for dec.More() {
		before := int(dec.InputOffset())
		tok, err := dec.Token()
		if err != nil {
			break // cannot happen: the text is valid
		}
		name, _ := tok.(string)
		end := int(dec.InputOffset())
		start := before + bytes.IndexByte(masked[before:end], '"')
		keys = append(keys, Key{
			Name:     name,
			Start:    start,
			End:      end,
			Variable: strings.Contains(src[start:end], "{{"),
		})
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			break
		}
	}
	return keys, TopObject, nil
}
