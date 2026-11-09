package snippet

// fetch.go: the request as a JavaScript fetch call (GEN-05).
// docs/snippets.md#javascript-fetch.

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Fetch returns r as JavaScript that sends it with fetch (GEN-05): an ES
// module with top-level await, for Node 20 or later, or a browser when no
// file is sent (a file body or file part is read with Node's openAsBlob).
//
// Header rows go into a headers object (fields of the same name joined,
// an empty User-Agent row left out, a value that is not ASCII as its
// UTF-8 bytes, since fetch takes header values as bytes); Basic
// credentials are encoded with btoa, from UTF-8; a text body is a
// string, a template literal when it has several lines; a multipart body
// is a FormData (a text part's Content-Type cannot be set there and is
// left out). A timeout is an
// AbortSignal.timeout, redirects not followed are redirect: "manual". A
// comment says what fetch cannot do: Digest auth, turning TLS
// verification off. It fails with CodeUnsupportedChar for text that is
// not UTF-8.
func Fetch(r Request) (string, error) {
	if err := textFields(r, func(s, field string) error { return validText(s, field, "JavaScript") }); err != nil {
		return "", err
	}
	var top, pre, opts []string
	usesFile := r.Body.Kind == BodyFile
	for _, p := range r.Body.Parts {
		usesFile = usesFile || p.File != ""
	}
	if usesFile {
		top = append(top, `import { openAsBlob } from "node:fs";`)
	}

	hasBody := r.Body.Kind != BodyNone
	if r.Method != "GET" || hasBody {
		opts = append(opts, "method: "+jsString(r.Method)+",")
	}
	var headers []string
	for _, h := range combined(sentHeaders(r)) {
		if !noUserAgent(h) {
			headers = append(headers, jsString(h.Name)+": "+jsBytes(h.Value)+",")
		}
	}
	if c := r.Basic; c != nil {
		cred := c.Username + ":" + c.Password
		headers = append(headers, `"Authorization": "Basic " + btoa(`+jsBytes(cred)+"),")
	}
	if len(headers) > 0 {
		opts = append(opts, "headers: {")
		for _, h := range headers {
			opts = append(opts, "  "+h)
		}
		opts = append(opts, "},")
	}
	switch r.Body.Kind {
	case BodyData:
		opts = append(opts, "body: "+jsText(string(r.Body.Data))+",")
	case BodyFile:
		opts = append(opts, "body: await openAsBlob("+jsString(r.Body.File)+"),")
	case BodyMultipart:
		pre = append(pre, "const form = new FormData();")
		for _, p := range r.Body.Parts {
			if p.File == "" {
				pre = append(pre, "form.append("+jsString(p.Name)+", "+jsString(p.Value)+");")
				continue
			}
			blob := "await openAsBlob(" + jsString(p.File) + ", { type: " + jsString(p.ContentType) + " })"
			pre = append(pre, "form.append("+jsString(p.Name)+", "+blob+", "+jsString(partFilename(p))+");")
		}
		pre = append(pre, "")
		opts = append(opts, "body: form,")
	}
	if !r.FollowRedirects {
		opts = append(opts, `redirect: "manual",`)
	}
	if r.Timeout > 0 {
		opts = append(opts, "signal: AbortSignal.timeout("+strconv.FormatInt(r.Timeout.Milliseconds(), 10)+"),")
	}
	if r.Digest != nil {
		pre = append(pre, "// fetch has no Digest auth: answer the server's challenge for user "+jsString(r.Digest.Username)+".")
	}
	if r.Insecure {
		pre = append(pre, "// TLS verification is off for this request; fetch cannot turn it off (in Node, NODE_TLS_REJECT_UNAUTHORIZED=0 does).")
	}

	var b strings.Builder
	for _, l := range top {
		b.WriteString(l + "\n")
	}
	if len(top) > 0 {
		b.WriteString("\n")
	}
	for _, l := range pre {
		b.WriteString(l + "\n")
	}
	b.WriteString("const response = await fetch(" + jsString(r.URL))
	if len(opts) > 0 {
		b.WriteString(", {\n")
		for _, o := range opts {
			b.WriteString("  " + o + "\n")
		}
		b.WriteString("}")
	}
	b.WriteString(");\n\nconsole.log(response.status);\nconsole.log(await response.text());")
	return b.String(), nil
}

// jsBytes is s as a JavaScript byte string (one character per byte) of
// its UTF-8 encoding, as header values and btoa take them: the string
// literal when s is ASCII, else the literal encoded with TextEncoder.
func jsBytes(s string) string {
	if ascii(s) {
		return jsString(s)
	}
	return "String.fromCharCode(...new TextEncoder().encode(" + jsString(s) + "))"
}

// jsText is s as a template literal when it has several lines, else as a
// string literal.
func jsText(s string) string {
	if strings.Contains(strings.TrimSuffix(s, "\n"), "\n") {
		return jsTemplate(s)
	}
	return jsString(s)
}

// jsString is s as a JavaScript string literal in double quotes: \ and "
// escaped, control characters and characters that are not printable
// (U+2028, U+2029, which end a line in a comment, included) as escapes.
// s must be valid UTF-8.
func jsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '\\', '"':
			b.WriteRune('\\')
			b.WriteRune(c)
		default:
			jsChar(&b, c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// jsTemplate is s as a template literal without substitutions: \ ` and ${
// escaped, line feeds and tabs kept, other control characters and
// characters that are not printable as escapes (a template literal would
// turn a CR into a line feed).
func jsTemplate(s string) string {
	var b strings.Builder
	b.WriteByte('`')
	for i, c := range s {
		switch {
		case c == '\\' || c == '`' || c == '$' && strings.HasPrefix(s[i+1:], "{"):
			b.WriteRune('\\')
			b.WriteRune(c)
		case c == '\n' || c == '\t':
			b.WriteRune(c)
		default:
			jsChar(&b, c)
		}
	}
	b.WriteByte('`')
	return b.String()
}

// jsChar writes c, or its escape when it is a control character or not
// printable.
func jsChar(b *strings.Builder, c rune) {
	switch {
	case c == '\n':
		b.WriteString(`\n`)
	case c == '\r':
		b.WriteString(`\r`)
	case c == '\t':
		b.WriteString(`\t`)
	case c < ' ' || c == 0x7f:
		fmt.Fprintf(b, `\x%02x`, c)
	case c > 0x7f && !unicode.IsPrint(c):
		if c > 0xffff {
			fmt.Fprintf(b, `\u{%x}`, c)
		} else {
			fmt.Fprintf(b, `\u%04x`, c)
		}
	default:
		b.WriteRune(c)
	}
}
