package http

import (
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/request"
)

func bodyDTO(ct string, body []byte, truncated bool) ResponseDTO {
	var headers []httpclient.Header
	if ct != "" {
		headers = []httpclient.Header{{Name: "Content-Type", Value: ct}}
	}
	return toResponseDTO(request.HTTPBuild{}, &httpclient.Response{
		Headers:       headers,
		Body:          body,
		BodyTruncated: truncated,
		Size:          int64(len(body)),
	}, nil, time.Time{}, nil)
}

func TestResponseBodyPretty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ct      string
		body    string
		lang    string
		state   string
		pretty  string
		errPart string
	}{
		{"json", "application/json", `{"a":[1]}`, "json", "formatted", "{\n  \"a\": [\n    1\n  ]\n}", ""},
		{"json already pretty", "application/problem+json", "{\n  \"a\": 1\n}", "json", "formatted", "{\n  \"a\": 1\n}", ""},
		{"json invalid", "application/json", `{"a":`, "json", "invalid", "", "Not valid JSON: "},
		{"xml", "application/xml", `<a><b>1</b></a>`, "xml", "formatted", "<a>\n  <b>1</b>\n</a>", ""},
		{"xml suffix", "application/soap+xml; charset=utf-8", `<e><f/></e>`, "xml", "formatted", "<e>\n  <f/>\n</e>", ""},
		{"xml invalid", "text/xml", `<a><b></a>`, "xml", "invalid", "", "Not well-formed XML: "},
		{"html highlighted only", "text/html", `<html><body>x</body></html>`, "html", "", "", ""},
		{"text", "text/plain", `{"a":1}`, "text", "", "", ""},
		{"sniffed json", "", `  [1,2]`, "json", "formatted", "[\n  1,\n  2\n]", ""},
		{"sniffed json that is not", "", `[INFO] started`, "text", "", "", ""},
		{"sniffed xml", "", `<?xml version="1.0"?><a/>`, "xml", "formatted", "<?xml version=\"1.0\"?>\n<a/>", ""},
		{"sniffed html", "", `<!DOCTYPE html><p>x`, "html", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := bodyDTO(tt.ct, []byte(tt.body), false)
			if d.Body != tt.body || d.BodyLanguage != tt.lang || d.PrettyState != tt.state || d.BodyPretty != tt.pretty {
				t.Errorf("body %q, language %q, state %q, pretty %q", d.Body, d.BodyLanguage, d.PrettyState, d.BodyPretty)
			}
			if !strings.HasPrefix(d.PrettyError, tt.errPart) || (tt.errPart == "") != (d.PrettyError == "") {
				t.Errorf("PrettyError = %q, want it to start with %q", d.PrettyError, tt.errPart)
			}
			if d.Charset != "UTF-8" || d.BodyShown != int64(len(tt.body)) || d.BodyHex || d.BodyClipped {
				t.Errorf("charset %q, shown %d, hex %v, clipped %v", d.Charset, d.BodyShown, d.BodyHex, d.BodyClipped)
			}
		})
	}

	if d := bodyDTO("application/json", []byte(`{"a":1}`), true); d.PrettyState != "incomplete" || d.BodyPretty != "" {
		t.Errorf("truncated: state %q, pretty %q", d.PrettyState, d.BodyPretty)
	}
	big := []byte(`["` + strings.Repeat("x", MaxPrettyBytes) + `"]`)
	if d := bodyDTO("application/json", big, false); d.PrettyState != "too_large" || d.BodyPretty != "" || d.Body != string(big) {
		t.Errorf("large: state %q, pretty %d bytes", d.PrettyState, len(d.BodyPretty))
	}
	if d := bodyDTO("", nil, false); d.Body != "" || d.BodyLanguage != "text" || d.Charset != "" || d.PrettyState != "" {
		t.Errorf("empty = %+v", d)
	}
}

func TestResponseBodyCharset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ct      string
		body    string
		text    string
		charset string
		source  string
		binary  bool
	}{
		{"utf-8 default", "application/json", "{\"\u00e9\":1}", "{\"\u00e9\":1}", "UTF-8", "default", false},
		{"utf-8 header", "text/plain; charset=UTF-8", "héllo", "héllo", "UTF-8", "header", false},
		{"latin-1 header", "text/plain; charset=iso-8859-1", "caf\xe9", "café", "ISO-8859-1", "header", false},
		{"windows-1252 header", "text/html; charset=windows-1252", "\x93hi\x94", "\u201chi\u201d", "windows-1252", "header", false},
		{"ascii header, high byte", "text/plain; charset=us-ascii", "a\xe9", "a\uFFFD", "US-ASCII", "header", true},
		{"utf-16le bom", "application/json", "\xFF\xFE{\x00}\x00", "{}", "UTF-16LE", "bom", false},
		{"utf-16be header", "text/plain; charset=utf-16be", "\x00h\x00i", "hi", "UTF-16BE", "header", false},
		{"utf-8 bom stripped", "text/plain", "\xEF\xBB\xBFhi", "hi", "UTF-8", "bom", false},
		{"xml declaration", "application/xml", `<?xml version="1.0" encoding="ISO-8859-1"?><a>` + "\xe9</a>", `<?xml version="1.0" encoding="ISO-8859-1"?><a>` + "\u00e9</a>", "ISO-8859-1", "declaration", false},
		{"html meta", "text/html", `<meta charset="windows-1252"><p>` + "\x80", `<meta charset="windows-1252"><p>` + "\u20ac", "windows-1252", "meta", false},
		{"invalid utf-8 text", "text/plain", "a\xffb", "a\uFFFDb", "UTF-8", "default", true},
		{"no type, valid utf-8", "", "plain words", "plain words", "UTF-8", "default", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := bodyDTO(tt.ct, []byte(tt.body), false)
			if d.Body != tt.text || d.Charset != tt.charset || d.CharsetSource != tt.source || d.BodyBinary != tt.binary || d.BodyHex {
				t.Errorf("body %q, charset %q from %q, binary %v, hex %v", d.Body, d.Charset, d.CharsetSource, d.BodyBinary, d.BodyHex)
			}
		})
	}

	// The XML declaration's charset is used for pretty-printing too.
	d := bodyDTO("text/xml", []byte(`<?xml version="1.0" encoding="latin1"?><a><b>`+"\xe9</b></a>"), false)
	if d.BodyPretty != `<?xml version="1.0" encoding="latin1"?>`+"\n<a>\n  <b>é</b>\n</a>" {
		t.Errorf("pretty = %q", d.BodyPretty)
	}
}

func TestResponseBodyHex(t *testing.T) {
	t.Parallel()
	d := bodyDTO("text/plain; charset=shift_jis", []byte("\x82\xa0"), false)
	if !d.BodyHex || d.Charset != "" || !strings.Contains(d.BodyNote, "shift_jis") || d.Body != "00000000  82 a0"+strings.Repeat(" ", 45)+"|..|" {
		t.Errorf("unsupported charset = %+v", d)
	}
	d = bodyDTO("application/xml", []byte(`<?xml version="1.0" encoding="EUC-JP"?><a/>`), false)
	if !d.BodyHex || !strings.Contains(d.BodyNote, "EUC-JP") || d.BodyLanguage != "text" || d.PrettyState != "" {
		t.Errorf("unsupported declaration = %+v", d)
	}
	d = bodyDTO("application/octet-stream", []byte{0xff, 'a'}, false)
	if !d.BodyHex || d.BodyNote != "The body is binary data (application/octet-stream), so its bytes are shown." || d.BodyShown != 2 {
		t.Errorf("binary = %+v", d)
	}
	d = bodyDTO("", []byte("a\x00b"), false)
	if !d.BodyHex || d.BodyNote != "The body is binary data, so its bytes are shown." {
		t.Errorf("NUL without a type = %+v", d)
	}
	if d := bodyDTO("image/png", []byte("PNG plain"), false); d.BodyHex {
		t.Errorf("valid UTF-8 without NUL is text: %+v", d)
	}

	large := make([]byte, MaxHexBytes+10)
	d = bodyDTO("application/octet-stream", large, false)
	if !d.BodyClipped || d.BodyShown != MaxHexBytes || strings.Count(d.Body, "\n") != MaxHexBytes/16-1 {
		t.Errorf("large binary: clipped %v, shown %d, lines %d", d.BodyClipped, d.BodyShown, strings.Count(d.Body, "\n")+1)
	}
}

func TestResponseBodyClipped(t *testing.T) {
	t.Parallel()
	// U+00E9 is 2 bytes and straddles the limit.
	long := []byte(strings.Repeat("a", MaxBodyText-1) + "\u00e9" + "tail")
	d := bodyDTO("text/plain", long, false)
	if !d.BodyClipped || d.BodyBinary || len(d.Body) != MaxBodyText-1 || d.BodyShown != MaxBodyText-1 || d.Size != int64(len(long)) {
		t.Errorf("clipped: len %d, clipped %v, binary %v, shown %d", len(d.Body), d.BodyClipped, d.BodyBinary, d.BodyShown)
	}
	if d := bodyDTO("application/json", append([]byte(`["`), long...), false); d.PrettyState != "incomplete" {
		t.Errorf("clipped JSON state = %q", d.PrettyState)
	}

	// A UTF-16 surrogate pair straddling the limit is not split.
	pair := []byte{0x3d, 0xd8, 0x00, 0xde}
	u16 := append([]byte(strings.Repeat("a\x00", MaxBodyText/2-1)), pair...)
	d = bodyDTO("text/plain; charset=utf-16le", u16, false)
	if !d.BodyClipped || d.BodyBinary || len(d.Body) != MaxBodyText/2-1 {
		t.Errorf("utf-16 clipped: len %d, binary %v", len(d.Body), d.BodyBinary)
	}
}
