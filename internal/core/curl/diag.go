package curl

import (
	"cmp"
	"slices"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Severity is how serious a diagnostic is.
type Severity string

// Severities.
const (
	// SeverityError: the command could not be read as intended; Command.Err
	// is not nil.
	SeverityError Severity = "error"
	// SeverityWarning: the command was read, but something in it is not
	// imported or may not mean what it meant in the shell.
	SeverityWarning Severity = "warning"
	// SeverityInfo: something was skipped that does not change the request
	// (an output option, an unquoted glob character).
	SeverityInfo Severity = "info"
)

// Diagnostic codes. Ranges are byte offsets [Start, End) into the source
// text given to Tokenize or Parse.
const (
	// Errors.

	// CodeEmpty: the text has no command. Range: the whole text.
	CodeEmpty = "curl.empty"
	// CodeNotCurl: the first word is not curl or curl.exe. Range: that word.
	CodeNotCurl = "curl.not_curl"
	// CodeUnterminatedQuote: a quote is never closed. Range: from the
	// opening quote to the end of the text.
	CodeUnterminatedQuote = "curl.unterminated_quote"
	// CodeMissingValue: an option that takes a value is the last word.
	// Range: the option.
	CodeMissingValue = "curl.missing_value"
	// CodeMissingURL: no URL was given. Range: the whole command.
	CodeMissingURL = "curl.missing_url"

	// Warnings.

	// CodeUnknownOption: an option curl does not have. Range: the option,
	// and the word taken as its value when the heuristic took one.
	CodeUnknownOption = "curl.unknown_option"
	// CodeUnsupportedOption: a curl option PostSonde does not import (client
	// certificates, --resolve, --next, ...). Range: the option and its value.
	CodeUnsupportedOption = "curl.unsupported_option"
	// CodeUnexpectedValue: --flag=value for an option that takes no value.
	// The value is ignored. Range: the word.
	CodeUnexpectedValue = "curl.unexpected_value"
	// CodeInvalidValue: a value curl would reject (a header without a colon,
	// a timeout that is not a number, a malformed -F). Range: the option and
	// its value.
	CodeInvalidValue = "curl.invalid_value"
	// CodeFileReference: a value curl reads from a file or stdin
	// (-d @file, -H @file, -b file). The file is not read. Range: the option
	// and its value. Info instead of warning where the path itself can be
	// imported (-F name=@file, --data-binary @file, -T file).
	CodeFileReference = "curl.file_reference"
	// CodePasswordMissing: -u or -U without ":password"; curl would prompt
	// for it. Range: the option and its value.
	CodePasswordMissing = "curl.password_missing"
	// CodeMultipleURLs: more than one URL; only the first is used. Range:
	// each extra URL.
	CodeMultipleURLs = "curl.multiple_urls"
	// CodeConflictingBody: options curl refuses to combine (-d with -F,
	// -T or -I). Range: the whole command.
	CodeConflictingBody = "curl.conflicting_body"
	// CodeShellExpansion: $VAR, ${VAR}, $(...), `...` or %VAR% that the
	// shell would expand. The text is kept as written. Range: the expansion.
	CodeShellExpansion = "curl.shell_expansion"
	// CodeTrailingInput: text after the command (a pipe, ;, &&, a second
	// command) is ignored. Range: from the operator to the end of the text.
	CodeTrailingInput = "curl.trailing_input"
	// CodeRedirectIgnored: a shell redirection (> file, 2>&1) is ignored.
	// Range: the operator and its target.
	CodeRedirectIgnored = "curl.redirect_ignored"
	// CodeMissingContinuation: a line break without a continuation character
	// before a line that starts with "-"; it is read as one command. Range:
	// the line break.
	CodeMissingContinuation = "curl.missing_continuation"
	// CodeUnquotedAmpersand: an "&" inside an unquoted word (typically a
	// URL). A shell would run the command in the background there; it is
	// kept as part of the word. Range: the "&".
	CodeUnquotedAmpersand = "curl.unquoted_ampersand"

	// Info.

	// CodeIgnoredOption: an option that does not change the request
	// (-s, -v, -o, -i, -w, ...). Range: the option and its value.
	CodeIgnoredOption = "curl.ignored_option"
	// CodeShellGlob: an unquoted *, ? or [ that a shell may expand. The
	// text is kept as written. Range: the word.
	CodeShellGlob = "curl.shell_glob"
)

// CodeInvalidCommand is the errs code of Command.Err.
const CodeInvalidCommand = "curl.invalid_command"

// Diagnostic is one problem found while reading a command.
type Diagnostic struct {
	// Code is one of the Code constants: stable, for tests and quick-fixes.
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	// Start and End are the byte range [Start, End) in the source text.
	Start int `json:"start"`
	End   int `json:"end"`
	// Message is a user-facing sentence. It never contains header, data or
	// credential values; it may name options, header names and files.
	Message string `json:"message"`
}

func sortDiagnostics(d []Diagnostic) {
	slices.SortStableFunc(d, func(a, b Diagnostic) int { return cmp.Compare(a.Start, b.Start) })
}

// HasErrors reports whether ds contains a SeverityError diagnostic.
func HasErrors(ds []Diagnostic) bool {
	return slices.ContainsFunc(ds, func(d Diagnostic) bool { return d.Severity == SeverityError })
}

// Err returns nil when the command has no error diagnostics. Otherwise it
// returns an errs.Parse error with code curl.invalid_command whose message
// is the first error's message, with its range in the details.
func (c *Command) Err() error {
	for _, d := range c.Diagnostics {
		if d.Severity != SeverityError {
			continue
		}
		return errs.New(errs.Parse, d.Message).
			WithCode(CodeInvalidCommand).
			WithHint("Check the cURL command and paste it again.").
			WithDetail("diagnostic", d.Code).
			WithDetail("start", strconv.Itoa(d.Start)).
			WithDetail("end", strconv.Itoa(d.End))
	}
	return nil
}
