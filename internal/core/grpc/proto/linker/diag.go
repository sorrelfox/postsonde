package linker

import (
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
)

// Diagnostic codes. Syntax errors and parser.Check diagnostics are included
// too, as CodeSyntax and "proto.check.<name>" (for example
// "proto.check.duplicate_name").
const (
	// CodeSyntax is a syntax error from the parser.
	CodeSyntax = "proto.syntax"
	// CodeFileNotFound is an input file that does not exist.
	CodeFileNotFound = "proto.file_not_found"
	// CodeReadFailed is a file that exists but could not be read.
	CodeReadFailed = "proto.read_failed"
	// An input file outside the directories Options.Access allows is
	// reported with fileaccess.CodeNotAllowed ("fileaccess.not_allowed").
	// CodeImplicitImportPath is a warning: an input file is not under any
	// import path, so its directory was added as one.
	CodeImplicitImportPath = "proto.implicit_import_path"
	// CodeInputShadowed is an input file whose import path resolves to a
	// different file in an earlier import path.
	CodeInputShadowed = "proto.input_shadowed"
	// CodeImportShadowed is a warning: an imported path exists under more
	// than one import path; the first one is used.
	CodeImportShadowed = "proto.import_shadowed"
	// CodeInvalidImportPath is an import that is not a clean relative path
	// with forward slashes.
	CodeInvalidImportPath = "proto.invalid_import_path"
	// CodeImportNotFound is an import found under no import path.
	CodeImportNotFound = "proto.import_not_found"
	// CodeWeakImportNotFound is a warning: a missing weak import.
	CodeWeakImportNotFound = "proto.weak_import_not_found"
	// CodeImportCycle is an import that closes a cycle.
	CodeImportCycle = "proto.import_cycle"
	// CodeDuplicateSymbol is a full name defined in two files, or a
	// package name that is also a message, enum or service name.
	CodeDuplicateSymbol = "proto.duplicate_symbol"
	// CodeUnknownType is a type name that does not resolve.
	CodeUnknownType = "proto.unknown_type"
	// CodeNotImported is a name defined in a file that is not visible:
	// neither imported directly nor through import public.
	CodeNotImported = "proto.not_imported"
	// CodeWrongKind is a name that resolves to the wrong kind of element:
	// a field type that is not a message or enum, an extendee or rpc type
	// that is not a message.
	CodeWrongKind = "proto.wrong_kind"
	// CodeExtensionNumber is an extension number outside the extendee's
	// extension ranges, or above the field number limit of a message that
	// is not a message set.
	CodeExtensionNumber = "proto.extension_number"
	// CodeDuplicateExtension is an extension number already used for the
	// same extendee.
	CodeDuplicateExtension = "proto.duplicate_extension"
	// CodeInvalidExtension is an extension not allowed for its extendee or
	// file: proto3 extensions of non-option messages, message set
	// extensions that are not optional messages.
	CodeInvalidExtension = "proto.invalid_extension"
	// CodeJSONNameConflict is two fields of a message with the same JSON
	// name, or json_name on an extension.
	CodeJSONNameConflict = "proto.json_name_conflict"
	// CodeClosedEnum is a proto2 (closed) enum used by a proto3 message.
	CodeClosedEnum = "proto.closed_enum"
	// CodeUnknownOption is an option name that does not resolve.
	CodeUnknownOption = "proto.unknown_option"
	// CodeOptionValue is an option or default value of the wrong type or
	// out of range, or an aggregate naming unknown fields.
	CodeOptionValue = "proto.option_value"
	// CodeDuplicateOption is a non-repeated option set twice.
	CodeDuplicateOption = "proto.duplicate_option"
)

// Diagnostic is a problem found while loading or linking.
type Diagnostic struct {
	Code     string
	Severity parser.Severity
	// File is the import path of the file the diagnostic is in, or the
	// name given in Options.Files for an input that could not be loaded.
	File string
	// DiskPath is the file's DiskPath ("" for built-in files and inputs
	// that were not found).
	DiskPath string
	// Start and End are the byte range in the file's source; Line and
	// Column (1-based) are 0 when the diagnostic has no position.
	Start, End   int
	Line, Column int
	Message      string
}

// Error returns "file:line:column: message" (or "file: message" without a
// position), with "warning: " before the message for warnings.
func (d Diagnostic) Error() string {
	var b strings.Builder
	b.WriteString(d.File)
	if d.Line > 0 {
		b.WriteString(":" + strconv.Itoa(d.Line) + ":" + strconv.Itoa(d.Column))
	}
	b.WriteString(": ")
	if d.Severity == parser.SeverityWarning {
		b.WriteString("warning: ")
	}
	b.WriteString(d.Message)
	return b.String()
}

// checkCode maps a parser.Check code to a diagnostic code.
func checkCode(c parser.DiagCode) string {
	return "proto.check." + strings.ReplaceAll(c.String(), " ", "_")
}
