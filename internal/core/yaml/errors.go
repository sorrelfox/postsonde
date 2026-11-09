package yaml

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes, in Error.Code and in the errs errors AsErr returns.
const (
	// CodeInvalidYAML: the document is not valid YAML (or has duplicate
	// keys). errs kind Parse.
	CodeInvalidYAML = "yaml.invalid_yaml"
	// CodeUnsupported: valid YAML that this reader does not support, or
	// that has no JSON equivalent. errs kind Unsupported.
	CodeUnsupported = "yaml.unsupported"
	// CodeTooLarge: the input is over one of the Limits. errs kind
	// Unsupported.
	CodeTooLarge = "yaml.too_large"
)

// Error is a YAML document that could not be read or converted.
type Error struct {
	// Code is CodeInvalidYAML, CodeUnsupported or CodeTooLarge.
	Code string
	// Message says what is wrong, e.g. "mapping values are not allowed
	// here".
	Message string
	// Line and Column are 1-based; Column counts characters (runes).
	// Both are 0 for errors about the whole input, such as its size.
	Line, Column int
}

// Error returns "yaml: line L, column C: <message>".
func (e *Error) Error() string {
	if e.Line == 0 {
		return "yaml: " + e.Message
	}
	return "yaml: line " + strconv.Itoa(e.Line) + ", column " + strconv.Itoa(e.Column) + ": " + e.Message
}

// Err returns the error in its errs form: kind Parse for
// CodeInvalidYAML, Unsupported otherwise, with details line and column.
func (e *Error) Err() error {
	where := ""
	if e.Line > 0 {
		where = " (line " + strconv.Itoa(e.Line) + ", column " + strconv.Itoa(e.Column) + ")"
	}
	var out *errs.Error
	switch e.Code {
	case CodeInvalidYAML:
		out = errs.Wrap(e, errs.Parse, "The YAML is not valid: "+e.Message+where+".")
	case CodeTooLarge:
		out = errs.Wrap(e, errs.Unsupported, "The YAML document is too large: "+e.Message+where+".")
	default:
		out = errs.Wrap(e, errs.Unsupported, "The YAML uses a feature PostSonde does not support: "+e.Message+where+".").
			WithHint("Convert the file to JSON, or remove the construct, and import it again.")
	}
	out = out.WithCode(e.Code)
	if e.Line > 0 {
		out = out.WithDetail("line", strconv.Itoa(e.Line)).WithDetail("column", strconv.Itoa(e.Column))
	}
	return out
}

// AsErr converts an *Error to its errs form; other errors are returned
// unchanged.
func AsErr(err error) error {
	if e, ok := err.(*Error); ok {
		return e.Err()
	}
	return err
}
