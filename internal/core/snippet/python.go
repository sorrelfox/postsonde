package snippet

// python.go: the request as Python requests code (GEN-06).
// docs/snippets.md#python-requests.

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Python returns r as Python 3 code that sends it with the requests
// package (GEN-06): one requests.request call.
//
// Header rows go into a headers dict (fields of the same name joined, a
// value that is not ASCII as UTF-8 bytes; an empty User-Agent row is
// None, which leaves requests' own out); Basic credentials are
// auth=(user, password), Digest ones HTTPDigestAuth; a
// text body is a str (encoded to UTF-8 when it is not ASCII), several
// lines as adjacent literals; a file body is an open file; a multipart
// body is files=[...], text parts as (None, value[, type]). timeout=,
// verify=False and allow_redirects=False follow the settings (requests
// follows up to 30 redirects). It fails with CodeUnsupportedChar for text
// that is not UTF-8.
func Python(r Request) (string, error) {
	if err := textFields(r, func(s, field string) error { return validText(s, field, "Python") }); err != nil {
		return "", err
	}
	imports := []string{"import requests"}
	args := []string{pyString(r.Method) + ",", pyString(r.URL) + ","}

	var headers []string
	for _, h := range combined(sentHeaders(r)) {
		// http.client encodes a str value as Latin-1; PostSonde sends
		// UTF-8.
		v := pyBytes(h.Value)
		if noUserAgent(h) {
			v = "None"
		}
		headers = append(headers, "    "+pyString(h.Name)+": "+v+",")
	}
	if len(headers) > 0 {
		args = append(args, "headers={")
		args = append(args, headers...)
		args = append(args, "},")
	}
	switch {
	case r.Basic != nil:
		args = append(args, "auth=("+pyBytes(r.Basic.Username)+", "+pyBytes(r.Basic.Password)+"),")
	case r.Digest != nil:
		imports = append(imports, "from requests.auth import HTTPDigestAuth")
		args = append(args, "auth=HTTPDigestAuth("+pyString(r.Digest.Username)+", "+pyString(r.Digest.Password)+"),")
	}
	switch r.Body.Kind {
	case BodyData:
		args = append(args, pyText("data=", string(r.Body.Data))...)
	case BodyFile:
		args = append(args, "data=open("+pyString(r.Body.File)+", \"rb\"),")
	case BodyMultipart:
		args = append(args, "files=[")
		for _, p := range r.Body.Parts {
			var v string
			switch {
			case p.File != "":
				v = pyString(partFilename(p)) + ", open(" + pyString(p.File) + ", \"rb\"), " + pyString(p.ContentType)
			case p.ContentType != "":
				v = "None, " + pyString(p.Value) + ", " + pyString(p.ContentType)
			default:
				v = "None, " + pyString(p.Value)
			}
			args = append(args, "    ("+pyString(p.Name)+", ("+v+")),")
		}
		args = append(args, "],")
	}
	if r.Timeout > 0 {
		args = append(args, "timeout="+strconv.FormatFloat(r.Timeout.Seconds(), 'f', -1, 64)+",")
	}
	if r.Insecure {
		args = append(args, "verify=False,")
	}
	if !r.FollowRedirects {
		args = append(args, "allow_redirects=False,")
	}

	var b strings.Builder
	for _, l := range imports {
		b.WriteString(l + "\n")
	}
	b.WriteString("\nresponse = requests.request(\n")
	for _, a := range args {
		b.WriteString("    " + a + "\n")
	}
	b.WriteString(")\n\nprint(response.status_code)\nprint(response.text)")
	return b.String(), nil
}

// pyText returns the lines of the argument name=s: one literal, or
// adjacent literals in parentheses, one per line of s; .encode() when s is
// not ASCII (requests would encode a str as Latin-1).
func pyText(name, s string) []string {
	enc := ""
	if !ascii(s) {
		enc = ".encode()"
	}
	lines := splitLines(s)
	if len(lines) <= 1 {
		return []string{name + pyString(s) + enc + ","}
	}
	out := []string{name + "("}
	for _, l := range lines {
		out = append(out, "    "+pyString(l))
	}
	return append(out, ")"+enc+",")
}

// pyBytes is pyString(s), encoded to UTF-8 when it is not ASCII
// (requests encodes str credentials as Latin-1).
func pyBytes(s string) string {
	if ascii(s) {
		return pyString(s)
	}
	return pyString(s) + ".encode()"
}

// pyString is s as a Python str literal, in double quotes unless s has
// " and no ': \ and the quote escaped, control characters and characters
// that are not printable as \n \r \t \xHH \uHHHH \UHHHHHHHH. s must be
// valid UTF-8.
func pyString(s string) string {
	q := '"'
	if strings.ContainsRune(s, '"') && !strings.ContainsRune(s, '\'') {
		q = '\''
	}
	var b strings.Builder
	b.WriteRune(q)
	for _, c := range s {
		switch {
		case c == '\\' || c == q:
			b.WriteRune('\\')
			b.WriteRune(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < ' ' || c == 0x7f || c > 0x7f && c < 0x100 && !unicode.IsPrint(c):
			fmt.Fprintf(&b, `\x%02x`, c)
		case c > 0xffff && !unicode.IsPrint(c):
			fmt.Fprintf(&b, `\U%08x`, c)
		case c > 0x7f && !unicode.IsPrint(c):
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteRune(q)
	return b.String()
}
