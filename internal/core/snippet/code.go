package snippet

// code.go: what the generators other than Curl share: the headers a
// request sends, multipart dispositions and the errors for text a
// language cannot hold. See docs/snippets.md#languages.

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// CodeUnsupportedChar: a value holds a character the language cannot
// write (kind invalid_argument): a carriage return or another control
// character in a cmd command, text that is not valid UTF-8 where the
// language only has Unicode strings. Details: field, language.
const CodeUnsupportedChar = "snippet.unsupported_char"

// unsupportedChar is the CodeUnsupportedChar error of field in language.
func unsupportedChar(field, language string) error {
	return errs.New(errs.InvalidArgument, "The "+field+" holds a character that "+language+" code cannot carry.").
		WithCode(CodeUnsupportedChar).
		WithHint("Send such a body from a file (the Binary body type), or pick another language.").
		WithDetail("field", field).
		WithDetail("language", language)
}

// unsupportedBody is the CodeBodyUnsupported error of a body language
// cannot send.
func unsupportedBody(language, what string) error {
	return errs.New(errs.Unsupported, language+" cannot send "+what+".").
		WithCode(CodeBodyUnsupported).
		WithDetail("language", language)
}

// validText fails with CodeUnsupportedChar when s is not valid UTF-8,
// for the languages whose strings are Unicode text.
func validText(s, field, language string) error {
	if !utf8.ValidString(s) {
		return unsupportedChar(field, language)
	}
	return nil
}

// textFields checks every text value of r with check: the URL, header
// names and values, the cookies, credentials, the body and the multipart
// names, values and paths.
func textFields(r Request, check func(s, field string) error) error {
	fields := []struct{ s, name string }{{r.Method, "method"}, {r.URL, "URL"}, {r.Cookies, "cookies"}}
	for _, h := range r.Headers {
		fields = append(fields, struct{ s, name string }{h.Name + h.Value, "header " + h.Name})
	}
	for _, c := range []*Credentials{r.Basic, r.Digest} {
		if c != nil {
			fields = append(fields, struct{ s, name string }{c.Username + c.Password, "credentials"})
		}
	}
	switch r.Body.Kind {
	case BodyData:
		fields = append(fields, struct{ s, name string }{string(r.Body.Data), "body"})
	case BodyFile:
		fields = append(fields, struct{ s, name string }{r.Body.File, "body file path"})
	case BodyMultipart:
		for _, p := range r.Body.Parts {
			fields = append(fields, struct{ s, name string }{p.Name + p.Value + p.File + p.Filename + p.ContentType, "multipart part " + p.Name})
		}
	}
	for _, f := range fields {
		if err := check(f.s, f.name); err != nil {
			return err
		}
	}
	return nil
}

// sentHeaders returns the header fields r sends besides its credentials,
// in order: the header rows, the jar's cookies as a Cookie field (when no
// row took them), and the Content-Type the body implies when no row sets
// one. An empty User-Agent row stays: each generator writes it as "no
// User-Agent" where its client can.
func sentHeaders(r Request) []Header {
	out := append([]Header(nil), r.Headers...)
	if r.Cookies != "" {
		out = append(out, Header{Name: "Cookie", Value: r.Cookies})
	}
	if ct := impliedContentType(r); ct != "" {
		out = append(out, Header{Name: "Content-Type", Value: ct})
	}
	return out
}

// impliedContentType is the Content-Type r's data or file body implies
// when no header row sets one; "" otherwise.
func impliedContentType(r Request) string {
	if r.Body.Kind != BodyData && r.Body.Kind != BodyFile {
		return ""
	}
	if hasHeader(r.Headers, "Content-Type") {
		return ""
	}
	return r.Body.ContentType
}

// hasHeader reports whether hs has a field named name, in any case.
func hasHeader(hs []Header, name string) bool {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

// noUserAgent reports whether h is an empty User-Agent row, which sends
// no User-Agent at all.
func noUserAgent(h Header) bool {
	return h.Value == "" && strings.EqualFold(h.Name, "User-Agent")
}

// combined returns hs with the fields of the same name (in any case)
// joined into the first, values separated by ", " ("; " for Cookie), as a
// recipient may combine them (RFC 9110, 5.3): for the clients that take
// headers as a map.
func combined(hs []Header) []Header {
	var out []Header
	at := map[string]int{}
	for _, h := range hs {
		k := strings.ToLower(h.Name)
		i, ok := at[k]
		if !ok {
			at[k] = len(out)
			out = append(out, h)
			continue
		}
		sep := ", "
		if k == "cookie" {
			sep = "; "
		}
		out[i].Value += sep + h.Value
	}
	return out
}

// partFilename is the file name a file part sends.
func partFilename(p Part) string {
	if p.Filename != "" {
		return p.Filename
	}
	return path.Base(strings.ReplaceAll(p.File, `\`, "/"))
}

// partDisposition is the Content-Disposition of p as PostSonde sends it:
// form-data with the name and, for a file, the file name, escaped as the
// HTML standard escapes them (" CR LF percent-encoded).
func partDisposition(p Part) string {
	esc := strings.NewReplacer(`"`, "%22", "\r", "%0D", "\n", "%0A")
	d := `form-data; name="` + esc.Replace(p.Name) + `"`
	if p.File != "" {
		d += `; filename="` + esc.Replace(partFilename(p)) + `"`
	}
	return d
}

// splitLines splits s after each "\n", keeping it; a last line without
// one is kept too. "" gives no line.
func splitLines(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// ascii reports whether s is ASCII.
func ascii(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// urlScheme returns the scheme of u in lower case ("https"), or "" when u
// has none (a templated {{baseUrl}}/path).
func urlScheme(u string) string {
	i := strings.Index(u, "://")
	if i <= 0 {
		return ""
	}
	s := strings.ToLower(u[:i])
	for _, c := range s {
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '+' || c == '-' || c == '.') {
			return ""
		}
	}
	return s
}
