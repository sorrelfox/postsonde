// Package request turns saved requests (package projectfile) into what the
// protocol clients send. body.go maps an HTTP request's body
// (HTTP-10, HTTP-11, HTTP-13, HTTP-14, HTTP-15); see docs/json-tools.md#sending-bodies and
// docs/http-client.md.
package request

import (
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// CodeBodyModeUnsupported is the errs code of the error Body returns for a
// body mode this version does not know (a mode of a newer PostSonde).
const CodeBodyModeUnsupported = "request.body_mode_unsupported"

// Body returns the body to send for b, or nil when nothing is sent. root
// is the project root, against which relative file paths are resolved.
//
//   - "none" (and "") sends no body.
//   - "raw" sends Text as UTF-8, whatever its Type, including an empty text
//     (a zero-length body) and invalid JSON: the JSON tools only mark
//     errors, they never block a send. expand, when not nil, renders the
//     text's {{variables}} first; its error is returned as it is.
//   - "urlencoded" sends FormURLEncoded(Fields, expand), which may be
//     empty (a zero-length body).
//   - "multipart" sends Multipart(Parts, expand, root), with files
//     streamed from disk.
//   - "file" streams the file at Path (FileBody); a body without a file is
//     an error with CodeBodyFileUnset, a missing file one with
//     CodeBodyFileMissing.
//
// The returned body's ContentType is ContentType(b), the auto Content-Type
// (HTTP-16), with the generated boundary for multipart: httpclient sends it
// unless the request has an enabled Content-Type header.
func Body(b projectfile.Body, expand Expand, root string) (httpclient.Body, error) {
	switch b.Mode {
	case projectfile.BodyNone, "":
		return nil, nil
	case projectfile.BodyRaw:
		text := b.Text
		if expand != nil {
			var err error
			if text, err = expand(b.Text); err != nil {
				return nil, err
			}
		}
		return httpclient.RawBody{Data: []byte(text), Type: ContentType(b)}, nil
	case projectfile.BodyURLEncoded:
		form, err := FormURLEncoded(b.Fields, expand)
		if err != nil {
			return nil, err
		}
		return httpclient.RawBody{Data: []byte(form), Type: ContentType(b)}, nil
	case projectfile.BodyMultipart:
		m, err := Multipart(b.Parts, expand, root)
		if err != nil {
			return nil, err
		}
		return m, nil
	case projectfile.BodyFile:
		if hasHeaderBreak(b.ContentType) {
			return nil, errs.New(errs.InvalidArgument, "The Content-Type of the binary body contains a line break or control character.").
				WithCode(CodeInvalidPartContentType).
				WithHint("Remove line breaks and control characters from the Content-Type.")
		}
		return fileBody(b, root)
	}
	return nil, errs.New(errs.Unsupported, "This body type cannot be sent by this version of PostSonde.").
		WithCode(CodeBodyModeUnsupported).
		WithHint("Choose another body type, or send the request from a newer PostSonde.").
		WithDetail("mode", b.Mode)
}

// RawMediaType returns the media type a raw body of type typ implies,
// for the auto Content-Type header (ContentType): "application/json" for
// "json", and so on. It returns "" for a type this version does not know.
func RawMediaType(typ string) string {
	switch typ {
	case projectfile.RawJSON:
		return "application/json"
	case projectfile.RawXML:
		return "application/xml"
	case projectfile.RawHTML:
		return "text/html"
	case projectfile.RawText, "":
		return "text/plain"
	case projectfile.RawYAML:
		// RFC 9512.
		return "application/yaml"
	}
	return ""
}
