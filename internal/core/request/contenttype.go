package request

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Media types of the auto Content-Type (HTTP-16) for the form and file
// bodies; raw bodies use RawMediaType.
const (
	MediaTypeFormURLEncoded = "application/x-www-form-urlencoded"
	// MediaTypeMultipart is sent with "; boundary=…", the boundary
	// generated when the body is built.
	MediaTypeMultipart   = "multipart/form-data"
	MediaTypeOctetStream = "application/octet-stream"
)

// ContentType returns the Content-Type a body of b implies (HTTP-16), or
// "" when it implies none:
//
//   - "none", "" and modes this version does not know: "";
//   - "raw": RawMediaType(Type), "" for an unknown type; no charset is
//     added (JSON is UTF-8 by definition, and the text is sent as UTF-8);
//   - "urlencoded": "application/x-www-form-urlencoded", without charset;
//   - "multipart": "multipart/form-data", without the boundary, which is
//     generated when the body is built (MultipartBody.ContentType sends
//     "multipart/form-data; boundary=…");
//   - "file": FileMediaType(Path, ContentType): the body's own ContentType
//     when set, else the type of the file name's extension
//     (MediaTypeByExtension), else "application/octet-stream".
//
// The frontend mirrors this rule (frontend/src/lib/contentType.ts); both
// are tested against testdata/content_type.json.
func ContentType(b projectfile.Body) string {
	switch b.Mode {
	case projectfile.BodyRaw:
		return RawMediaType(b.Type)
	case projectfile.BodyURLEncoded:
		return MediaTypeFormURLEncoded
	case projectfile.BodyMultipart:
		return MediaTypeMultipart
	case projectfile.BodyFile:
		return FileMediaType(b.Path, b.ContentType)
	}
	return ""
}

// HasContentType reports whether headers has an enabled Content-Type
// header, in any letter case. Pass the result of Headers, whose names are
// already expanded: a row named {{ct}} that renders to content-type counts.
// This is the check httpclient makes before adding a body's ContentType.
func HasContentType(headers []httpclient.Header) bool {
	for _, h := range headers {
		if h.Enabled && strings.EqualFold(h.Name, "Content-Type") {
			return true
		}
	}
	return false
}

// ImplicitContentType returns the Content-Type added to a request with
// body b and the mapped headers: ContentType(b), or "" when an explicit
// enabled Content-Type header overrides it. A disabled row does not.
func ImplicitContentType(b projectfile.Body, headers []httpclient.Header) string {
	if HasContentType(headers) {
		return ""
	}
	return ContentType(b)
}
