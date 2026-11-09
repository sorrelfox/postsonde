package xmldom

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes of the errs errors this package's errors convert to.
const (
	// CodeInvalidXML: the document is not well-formed. Details: offset,
	// line, column.
	CodeInvalidXML = "xmldom.invalid_xml"
	// CodeTooLarge: the document is over a Limits bound. Details: limit,
	// max.
	CodeTooLarge = "xmldom.too_large"
	// CodeUnsupportedEncoding: the document's character encoding is not
	// supported. Details: encoding.
	CodeUnsupportedEncoding = "xmldom.unsupported_encoding"
	// CodeInvalidEncoding: the bytes are not valid in the document's
	// encoding. Details: encoding, offset.
	CodeInvalidEncoding = "xmldom.invalid_encoding"
)

// SyntaxError is a document that is not well-formed (or, in HTML mode,
// that encoding/xml still cannot tokenize).
type SyntaxError struct {
	// Offset is the byte offset in Document.Source (the input converted
	// to UTF-8, without a byte order mark) at or just after the problem.
	Offset int
	// Line and Column are 1-based; Column counts bytes.
	Line, Column int
	// Message says what is wrong, e.g. "element <a> closed by </b>".
	Message string
}

// Error returns "xml: <message> at line L, column C".
func (e *SyntaxError) Error() string {
	return "xml: " + e.Message + " at line " + strconv.Itoa(e.Line) + ", column " + strconv.Itoa(e.Column)
}

// Err returns the error as errs.Parse with code xmldom.invalid_xml.
func (e *SyntaxError) Err() error {
	return errs.Wrap(e, errs.Parse, "The XML is not well-formed: "+e.Message+" (line "+strconv.Itoa(e.Line)+", column "+strconv.Itoa(e.Column)+").").
		WithCode(CodeInvalidXML).
		WithDetail("offset", strconv.Itoa(e.Offset)).
		WithDetail("line", strconv.Itoa(e.Line)).
		WithDetail("column", strconv.Itoa(e.Column))
}

// Limit names used in LimitError.Limit.
const (
	LimitBytes = "bytes"
	LimitDepth = "depth"
	LimitNodes = "nodes"
)

// LimitError is a document over one of the Limits.
type LimitError struct {
	// Limit is LimitBytes, LimitDepth or LimitNodes.
	Limit string
	// Max is the bound that was exceeded.
	Max int
	// Offset is where parsing stopped (0 for LimitBytes).
	Offset int
}

// Error returns "xml: document exceeds the <limit> limit (<max>)".
func (e *LimitError) Error() string {
	return "xml: document exceeds the " + e.Limit + " limit (" + strconv.Itoa(e.Max) + ")"
}

// Err returns the error as errs.Unsupported with code xmldom.too_large.
func (e *LimitError) Err() error {
	var msg string
	switch e.Limit {
	case LimitBytes:
		msg = "The XML document is too large to query (over " + strconv.Itoa(e.Max) + " bytes)."
	case LimitDepth:
		msg = "The XML document is nested too deeply (over " + strconv.Itoa(e.Max) + " levels)."
	default:
		msg = "The XML document has too many nodes (over " + strconv.Itoa(e.Max) + ")."
	}
	return errs.Wrap(e, errs.Unsupported, msg).
		WithCode(CodeTooLarge).
		WithDetail("limit", e.Limit).
		WithDetail("max", strconv.Itoa(e.Max))
}

// EncodingError is a document in an unsupported encoding, or with bytes
// that are invalid in its encoding.
type EncodingError struct {
	// Encoding is the encoding name as declared or detected.
	Encoding string
	// Unsupported is true when the encoding itself is not supported.
	Unsupported bool
	// Offset is the byte offset of the invalid bytes in the input.
	Offset int
	// Message says what is wrong.
	Message string
}

// Error returns "xml: <message>".
func (e *EncodingError) Error() string { return "xml: " + e.Message }

// Err returns the error as errs.Unsupported with code
// xmldom.unsupported_encoding, or errs.Parse with code
// xmldom.invalid_encoding.
func (e *EncodingError) Err() error {
	if e.Unsupported {
		return errs.Wrap(e, errs.Unsupported, "The XML document's encoding "+strconv.Quote(e.Encoding)+" is not supported.").
			WithCode(CodeUnsupportedEncoding).
			WithHint("Supported encodings are UTF-8, UTF-16, ISO-8859-1, Windows-1252 and US-ASCII.").
			WithDetail("encoding", e.Encoding)
	}
	return errs.Wrap(e, errs.Parse, "The XML document is not valid "+e.Encoding+": "+e.Message+".").
		WithCode(CodeInvalidEncoding).
		WithDetail("encoding", e.Encoding).
		WithDetail("offset", strconv.Itoa(e.Offset))
}

// AsErr converts an error from Parse to its errs form (the Err method of
// *SyntaxError, *LimitError and *EncodingError); other errors are
// returned unchanged.
func AsErr(err error) error {
	if e, ok := err.(interface{ Err() error }); ok {
		return e.Err()
	}
	return err
}
