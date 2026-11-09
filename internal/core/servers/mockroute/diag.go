package mockroute

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Diagnostic codes. Every code makes the route invalid: Compile leaves it
// out of the Matcher.
const (
	CodeEmptyPattern        = "mockroute.empty_pattern"
	CodeMissingLeadingSlash = "mockroute.missing_leading_slash"
	CodeQueryInPattern      = "mockroute.query_in_pattern"
	CodeEmptySegment        = "mockroute.empty_segment"
	CodeInvalidSegment      = "mockroute.invalid_segment"
	CodeInvalidEscape       = "mockroute.invalid_escape"
	CodeInvalidParamName    = "mockroute.invalid_param_name"
	CodeDuplicateParam      = "mockroute.duplicate_param"
	CodeCatchAllNotLast     = "mockroute.catch_all_not_last"
	CodeInvalidRegex        = "mockroute.invalid_regex"
	CodeInvalidMethod       = "mockroute.invalid_method"
	CodeDuplicateRoute      = "mockroute.duplicate_route"
	CodeDuplicateID         = "mockroute.duplicate_id"
)

// CodeInvalidRoute is the errs code of Diagnostic.Err.
const CodeInvalidRoute = "mockroute.invalid_route"

// Fields a Diagnostic points into.
const (
	FieldPattern = "pattern"
	FieldMethod  = "method"
	FieldID      = "id"
)

// Diagnostic is one problem with a route.
type Diagnostic struct {
	// Code is one of the Code constants.
	Code string `json:"code"`
	// RouteIndex is the index of the route in the slice given to Compile;
	// RouteID is its ID.
	RouteIndex int    `json:"routeIndex"`
	RouteID    string `json:"routeId"`
	// Field is FieldPattern, FieldMethod or FieldID; Start and End are the
	// byte range [Start, End) in that field.
	Field string `json:"field"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	// Message is a user-facing sentence.
	Message string `json:"message"`
}

// Error returns "route N field S-E: message".
func (d Diagnostic) Error() string {
	return "route " + strconv.Itoa(d.RouteIndex) + " " + d.Field + " " +
		strconv.Itoa(d.Start) + "-" + strconv.Itoa(d.End) + ": " + d.Message
}

// Err returns the diagnostic as an errs.InvalidArgument error with code
// mockroute.invalid_route and the diagnostic code, route and range in the
// details.
func (d Diagnostic) Err() error {
	return errs.New(errs.InvalidArgument, d.Message).
		WithCode(CodeInvalidRoute).
		WithDetail("diagnostic", d.Code).
		WithDetail("routeIndex", strconv.Itoa(d.RouteIndex)).
		WithDetail("routeId", d.RouteID).
		WithDetail("field", d.Field).
		WithDetail("start", strconv.Itoa(d.Start)).
		WithDetail("end", strconv.Itoa(d.End))
}
