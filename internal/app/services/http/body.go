package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/charset"
	"github.com/sorrelfox/postsonde/internal/core/jsonfmt"
	"github.com/sorrelfox/postsonde/internal/core/xmldom"
)

// Body languages (ResponseDTO.BodyLanguage).
const (
	langJSON = "json"
	langXML  = "xml"
	langHTML = "html"
	langText = "text"
)

// Pretty states (ResponseDTO.PrettyState).
const (
	prettyFormatted  = "formatted"
	prettyTooLarge   = "too_large"
	prettyIncomplete = "incomplete"
	prettyInvalid    = "invalid"
)

// setBody fills the body fields of out from the bytes received (HTTP-31):
// the language, the charset and the decoded text or a hex dump, and the
// pretty-printed text. out.ContentType and out.BodyTruncated are set.
func setBody(out *ResponseDTO, raw []byte) {
	out.BodyLanguage = langText
	if len(raw) == 0 {
		return
	}
	mt, params, _ := mime.ParseMediaType(out.ContentType)
	lang, sniffed := bodyLanguage(mt, raw)
	out.BodyLanguage = lang
	out.PreviewType = previewType(mt, raw)

	enc, source, bom := "", "", 0
	if e, n := charset.BOM(raw); e != "" {
		enc, source, bom = e, "bom", n
	} else if label := params["charset"]; label != "" {
		enc, source = charset.Canonical(label), "header"
		if enc == "" {
			out.BodyNote = fmt.Sprintf("The body's charset, %s, is not supported, so its bytes are shown. PostSonde decodes UTF-8, UTF-16, ISO-8859-1, US-ASCII and windows-1252.", label)
		}
	} else if label := declaredCharset(lang, raw); label != "" {
		enc, source = charset.Canonical(label), "declaration"
		if lang == langHTML {
			source = "meta"
		}
		if enc == "" {
			out.BodyNote = fmt.Sprintf("The body declares the charset %s, which is not supported, so its bytes are shown. PostSonde decodes UTF-8, UTF-16, ISO-8859-1, US-ASCII and windows-1252.", label)
		}
	} else if looksText(mt, raw) {
		enc, source = charset.UTF8, "default"
	} else {
		what := "The body is binary data"
		if mt != "" {
			what += " (" + mt + ")"
		}
		out.BodyNote = what + ", so its bytes are shown."
	}

	if enc == "" {
		shown := raw[:min(len(raw), MaxHexBytes)]
		out.BodyHex = true
		out.BodyLanguage = langText
		out.Body = charset.HexDump(shown)
		out.BodyShown = int64(len(shown))
		out.BodyClipped = len(shown) < len(raw)
		return
	}

	out.Charset, out.CharsetSource = enc, source
	data := raw[bom:]
	if len(data) > MaxBodyText {
		data = clip(data, enc)
		out.BodyClipped = true
	}
	out.BodyShown = int64(bom + len(data))
	out.Body, out.BodyBinary = charset.Decode(data, enc)

	if lang != langJSON && lang != langXML {
		return
	}
	switch {
	case out.BodyClipped || out.BodyTruncated:
		out.PrettyState = prettyIncomplete
	case len(out.Body) > MaxPrettyBytes:
		out.PrettyState = prettyTooLarge
	default:
		pretty, msg := prettify(context.Background(), lang, out.Body)
		switch {
		case msg == "":
			out.PrettyState, out.BodyPretty = prettyFormatted, pretty
		case sniffed:
			// Only looked like JSON or XML: show it as text.
			out.BodyLanguage = langText
		default:
			out.PrettyState, out.PrettyError = prettyInvalid, msg
		}
	}
}

// prettify pretty-prints a JSON (beautified) or XML (indented) body with
// two spaces per level. msg is why it could not, "" when it could.
func prettify(ctx context.Context, lang, text string) (pretty, msg string) {
	switch lang {
	case langJSON:
		out, serr := jsonfmt.Beautify(text, 2)
		if serr != nil {
			return "", fmt.Sprintf("Not valid JSON: %s (line %d, column %d).", serr.Message, serr.Line, serr.Column)
		}
		return out, ""
	case langXML:
		out, err := xmldom.Indent(ctx, []byte(text), "  ")
		var se *xmldom.SyntaxError
		switch {
		case errors.As(err, &se):
			return "", fmt.Sprintf("Not well-formed XML: %s (line %d, column %d).", se.Message, se.Line, se.Column)
		case err != nil:
			return "", "The XML could not be indented: " + err.Error() + "."
		}
		return out, ""
	}
	return "", "There is no pretty-printer for " + lang + "."
}

// bodyLanguage picks the highlighting from the media type. Without one,
// it looks at the first bytes (sniffed is then true).
func bodyLanguage(mt string, raw []byte) (lang string, sniffed bool) {
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		return langJSON, false
	case mt == "text/html":
		return langHTML, false
	case mt == "application/xml" || mt == "text/xml" || strings.HasSuffix(mt, "+xml"):
		return langXML, false
	case mt != "":
		return langText, false
	}
	_, n := charset.BOM(raw)
	head := bytes.TrimLeft(raw[n:min(len(raw), n+512)], " \t\r\n")
	lower := bytes.ToLower(head)
	switch {
	case bytes.HasPrefix(head, []byte("{")), bytes.HasPrefix(head, []byte("[")):
		return langJSON, true
	case bytes.HasPrefix(lower, []byte("<!doctype html")), bytes.HasPrefix(lower, []byte("<html")):
		return langHTML, true
	case bytes.HasPrefix(head, []byte("<?xml")):
		return langXML, true
	}
	return langText, false
}

// declaredCharset returns the charset an XML or HTML body declares in its
// markup, or "".
func declaredCharset(lang string, raw []byte) string {
	switch lang {
	case langXML:
		return charset.XMLDeclared(raw)
	case langHTML:
		return charset.HTMLMeta(raw)
	}
	return ""
}

// looksText reports whether a body without a declared charset is read as
// UTF-8 text: a textual media type, or valid UTF-8 without NUL bytes.
func looksText(mt string, raw []byte) bool {
	switch {
	case strings.HasPrefix(mt, "text/"),
		strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"),
		mt == "application/json", mt == "application/xml",
		mt == "application/javascript", mt == "application/ecmascript",
		mt == "application/x-www-form-urlencoded", mt == "application/graphql",
		mt == "application/yaml", mt == "application/x-yaml", mt == "application/x-ndjson":
		return true
	}
	sample := raw
	if len(sample) > MaxBodyText {
		sample = clip(sample, charset.UTF8)
	}
	if !utf8.Valid(sample) {
		return false
	}
	return bytes.IndexByte(sample, 0) < 0
}

// clip cuts data to MaxBodyText bytes without splitting a character of
// encoding enc.
func clip(data []byte, enc string) []byte {
	data = data[:MaxBodyText]
	switch enc {
	case charset.UTF16LE, charset.UTF16BE:
		// MaxBodyText is even, so no code unit is split; do not split a
		// surrogate pair.
		hi := data[len(data)-2]
		if enc == charset.UTF16LE {
			hi = data[len(data)-1]
		}
		if hi >= 0xD8 && hi < 0xDC {
			data = data[:len(data)-2]
		}
	case charset.UTF8:
		for i := len(data) - 1; i >= 0 && i >= len(data)-utf8.UTFMax; i-- {
			if utf8.RuneStart(data[i]) {
				if !utf8.FullRune(data[i:]) {
					data = data[:i]
				}
				break
			}
		}
	}
	return data
}
