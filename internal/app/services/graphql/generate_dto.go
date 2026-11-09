package graphql

// The DTOs of the editor's schema actions (generate.go): jump to the docs
// (GQL-15), operations from the docs explorer (GQL-16) and fill fields
// (GQL-17). Offsets are UTF-16 code units (CodeMirror positions). See
// docs/graphql.md.

// DocsTargetInputDTO asks what the name at a cursor refers to.
type DocsTargetInputDTO struct {
	// Key is SchemaInfoDTO.Key.
	Key    string `json:"key"`
	Text   string `json:"text"`
	Offset int    `json:"offset"`
}

// DocsTargetDTO is the docs explorer's page for a name in the query.
type DocsTargetDTO struct {
	Found bool `json:"found"`
	// Kind is "type", "field", "argument", "input-field", "enum-value" or
	// "directive".
	Kind string `json:"kind"`
	// Type is the type page to open ("" for a directive and its
	// arguments), Member the field, input field or enum value to show on
	// it (an argument's field).
	Type   string `json:"type"`
	Member string `json:"member"`
	// Directive names the directive of a directive or its argument.
	Directive string `json:"directive"`
	// Name is the name clicked, [From, To) its range.
	Name string `json:"name"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

// GenerateInputDTO asks for an operation or fragment.
type GenerateInputDTO struct {
	Key string `json:"key"`
	// Kind is "operation" (Type is a root type, Field one of its fields)
	// or "fragment" (on Type; Field "" for all its fields).
	Kind  string `json:"kind"`
	Type  string `json:"type"`
	Field string `json:"field"`
	// Depth is the number of selection sets (1 to 5; 0 means 2).
	Depth int `json:"depth"`
	// Text is the query the result goes into: its names are not reused.
	Text string `json:"text"`
}

// GeneratedDTO is a written operation or fragment and where it goes.
type GeneratedDTO struct {
	// Schema is false without a cached schema.
	Schema bool `json:"schema"`
	// Text is the definition, Name its name.
	Text string `json:"text"`
	Name string `json:"name"`
	// Insert replaces [From, To) of the query: the definition after a
	// blank line at its end, or the whole query when it is blank. Cursor
	// is where the definition starts after the edit.
	Insert string `json:"insert"`
	From   int    `json:"from"`
	To     int    `json:"to"`
	Cursor int    `json:"cursor"`
	// Truncated: the selection was cut at the size limit.
	Truncated bool `json:"truncated"`
	// Reason ("unknown-type", "unknown-field", "required-arguments") and
	// Message say why nothing was written; Message also notes a cut.
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// FillFieldsInputDTO asks for the fields to fill in at a cursor.
type FillFieldsInputDTO struct {
	Key    string `json:"key"`
	Text   string `json:"text"`
	Offset int    `json:"offset"`
	// Depth is the number of selection sets (1: leaf fields only; 0 means 2).
	Depth int `json:"depth"`
}

// FillFieldsDTO is the edit that fills in the fields.
type FillFieldsDTO struct {
	Schema bool `json:"schema"`
	// Text replaces [From, To); Cursor is its end after the edit.
	From   int    `json:"from"`
	To     int    `json:"to"`
	Text   string `json:"text"`
	Cursor int    `json:"cursor"`
	// Type is the selection set's type, Count the selections added to it.
	Type      string `json:"type"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated"`
	// Reason ("not-in-selection", "unknown-type", "nothing-to-add") and
	// Message say why nothing can be filled in.
	Reason  string `json:"reason"`
	Message string `json:"message"`
}
