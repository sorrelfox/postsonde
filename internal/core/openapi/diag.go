package openapi

import "strconv"

// Severity is how bad a diagnostic is. The values match the importer's.
type Severity string

// Severities.
const (
	// SeverityError: part of the document could not be read (a broken
	// reference, a value of the wrong type); it is left out or replaced
	// by a placeholder.
	SeverityError Severity = "error"
	// SeverityWarning: the document breaks a rule of the specification,
	// or uses something PostSonde does not support; it was read as well as
	// possible.
	SeverityWarning Severity = "warning"
	// SeverityInfo: a note, e.g. a 3.0 construct read the 3.1 way.
	SeverityInfo Severity = "info"
)

// Diagnostic codes.
const (
	// CodeBadRef: a $ref whose target does not exist, or is not a valid
	// JSON pointer or anchor.
	CodeBadRef = "openapi.bad_ref"
	// CodeExternalRef: a $ref to another file or a URL; only references
	// inside the document are resolved.
	CodeExternalRef = "openapi.external_ref"
	// CodeRefCycle: references that only point at each other, with no
	// object at the end.
	CodeRefCycle = "openapi.ref_cycle"
	// CodeInvalidValue: a value of the wrong type or form.
	CodeInvalidValue = "openapi.invalid_value"
	// CodeMissingField: a required field is missing.
	CodeMissingField = "openapi.missing_field"
	// CodeUnsupported: a construct PostSonde reads only partly or not at
	// all ($dynamicRef, a later 3.x version).
	CodeUnsupported = "openapi.unsupported"
	// CodeIgnored: something the specification says to ignore, e.g. the
	// siblings of a $ref in 3.0.
	CodeIgnored = "openapi.ignored"
	// CodeDuplicate: a duplicate operationId or parameter.
	CodeDuplicate = "openapi.duplicate"
	// CodePathParameter: a {name} in a path without a path parameter, or
	// a path parameter that is not in the path.
	CodePathParameter = "openapi.path_parameter"
	// CodeLimit: a limit was reached while building the model (reference
	// resolution steps, total size of values); the rest of that part is
	// left out.
	CodeLimit = "openapi.limit"
)

// Diagnostic is a problem or a note about part of the document.
type Diagnostic struct {
	Severity Severity
	Code     string
	// Message is a sentence for the user.
	Message string
	// Pos is where the problem is: line, column and JSON pointer.
	Pos Pos
}

// String returns "line:column: message (pointer)".
func (d Diagnostic) String() string {
	s := ""
	if d.Pos.Line > 0 {
		s = strconv.Itoa(d.Pos.Line) + ":" + strconv.Itoa(d.Pos.Column) + ": "
	}
	s += d.Message
	if d.Pos.Pointer != "" {
		s += " (" + d.Pos.Pointer + ")"
	}
	return s
}

// HasErrors reports whether any diagnostic has SeverityError.
func HasErrors(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}
