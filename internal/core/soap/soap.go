// Package soap is the SOAP-over-HTTP helper (HTTP-18): the envelope
// template of SOAP 1.1 and 1.2 and the headers each version sends the
// action in. There is no WSDL tooling: a SOAP call is an ordinary HTTP
// request with a raw XML body, and this package only writes that body and
// its headers. See docs/http-client.md#soap-helper.
package soap

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// Versions.
const (
	V11 = "1.1"
	V12 = "1.2"
)

// Envelope namespaces.
const (
	NS11 = "http://schemas.xmlsoap.org/soap/envelope/"
	NS12 = "http://www.w3.org/2003/05/soap-envelope"
)

// Content types of the envelope, without parameters.
const (
	MediaType11 = "text/xml"
	MediaType12 = "application/soap+xml"
)

// ActionHeader is the SOAP 1.1 header naming the action.
const ActionHeader = "SOAPAction"

// Placeholder is the Body's content of an envelope made from blank text.
const Placeholder = "<!-- The operation's element goes here. -->"

// ErrVersion is returned for a version other than V11 and V12.
var ErrVersion = errors.New(`soap: the version must be "1.1" or "1.2"`)

// Namespace returns the envelope namespace of version.
func Namespace(version string) (string, error) {
	switch version {
	case V11:
		return NS11, nil
	case V12:
		return NS12, nil
	}
	return "", ErrVersion
}

// Header is a header the request sends.
type Header struct {
	Name  string
	Value string
}

// Headers returns the headers a call of version with action sends:
//
//   - 1.1: Content-Type text/xml; charset=utf-8, and SOAPAction with the
//     action as a quoted string (`""` for none, which the 1.1 spec reads as
//     "the request URI").
//   - 1.2: Content-Type application/soap+xml; charset=utf-8, with the action
//     as its action parameter when there is one; there is no SOAPAction.
//
// The action is used as typed: {{variables}} in it are rendered when the
// request is sent.
func Headers(version, action string) ([]Header, error) {
	switch version {
	case V11:
		return []Header{
			{Name: "Content-Type", Value: MediaType11 + "; charset=utf-8"},
			{Name: ActionHeader, Value: quote(action)},
		}, nil
	case V12:
		ct := MediaType12 + "; charset=utf-8"
		if action != "" {
			ct += "; action=" + quote(action)
		}
		return []Header{{Name: "Content-Type", Value: ct}}, nil
	}
	return nil, ErrVersion
}

// quote writes s as an HTTP quoted-string.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// Envelope returns the text of a raw XML body for a call of version:
//
//   - blank text: a new envelope whose Body holds Placeholder;
//   - an envelope of either version (the root element is Envelope in NS11
//     or NS12): the same text, with the other version's namespace replaced
//     by version's, so switching versions keeps what was typed;
//   - anything else (the operation's element, say): wrapped in the Body of
//     a new envelope, indented by two levels. An XML declaration at its
//     start moves to the start of the envelope.
//
// The envelope uses the prefix "soap" and has an empty Header.
func Envelope(version, text string) (string, error) {
	ns, err := Namespace(version)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return build(ns, "", "    "+Placeholder), nil
	}
	if root, ok := rootNamespace(text); ok && (root == NS11 || root == NS12) {
		if root == ns {
			return text, nil
		}
		return strings.ReplaceAll(text, root, ns), nil
	}
	decl, rest := splitDeclaration(strings.TrimSpace(text))
	return build(ns, decl, indent(rest, "    ")), nil
}

// build lays out an envelope around body, which is already indented.
func build(ns, decl, body string) string {
	var b strings.Builder
	if decl != "" {
		b.WriteString(decl)
		b.WriteByte('\n')
	}
	b.WriteString(`<soap:Envelope xmlns:soap="` + ns + `">` + "\n")
	b.WriteString("  <soap:Header/>\n")
	b.WriteString("  <soap:Body>\n")
	b.WriteString(body)
	b.WriteString("\n  </soap:Body>\n")
	b.WriteString("</soap:Envelope>")
	return b.String()
}

// indent prefixes every non-blank line of s with prefix.
func indent(s, prefix string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// splitDeclaration splits an XML declaration off the start of s.
func splitDeclaration(s string) (decl, rest string) {
	if !strings.HasPrefix(s, "<?xml") {
		return "", s
	}
	end := strings.Index(s, "?>")
	if end < 0 {
		return "", s
	}
	return s[:end+2], strings.TrimSpace(s[end+2:])
}

// rootNamespace returns the namespace of text's root element when it is
// named Envelope. Text that is not XML up to the root's start tag, or
// whose root has another name, gives ok false.
func rootNamespace(text string) (ns string, ok bool) {
	d := xml.NewDecoder(strings.NewReader(text))
	d.Strict = false
	// No DTD processing and no entity expansion: only the first start tag
	// is read.
	d.Entity = map[string]string{}
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	for {
		tok, err := d.RawToken()
		if err != nil {
			return "", false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "Envelope" {
				return "", false
			}
			for _, a := range t.Attr {
				if (a.Name.Space == "xmlns" && a.Name.Local == t.Name.Space) || (a.Name.Space == "" && a.Name.Local == "xmlns" && t.Name.Space == "") {
					return a.Value, true
				}
			}
			return "", false
		case xml.CharData:
			if len(bytes.TrimSpace(t)) != 0 {
				return "", false
			}
		}
	}
}
