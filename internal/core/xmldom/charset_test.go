package xmldom

import (
	"context"
	"errors"
	"testing"
	"unicode/utf16"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func utf16Bytes(s string, bigEndian, bom bool) []byte {
	var out []byte
	units := utf16.Encode([]rune(s))
	if bom {
		units = append([]uint16{0xFEFF}, units...)
	}
	for _, u := range units {
		if bigEndian {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	return out
}

func TestEncodings(t *testing.T) {
	const text = "Grüße € \U0001F600"
	doc16 := `<?xml version="1.0" encoding="UTF-16"?><a>` + text + `</a>`
	for _, c := range []struct {
		name     string
		data     []byte
		opts     Options
		encoding string
		want     string
	}{
		{"utf-8", []byte(`<a>` + text + `</a>`), Options{}, EncodingUTF8, text},
		{"utf-8 declared lowercase", []byte(`<?xml version="1.0" encoding="utf-8"?><a>` + text + `</a>`), Options{}, EncodingUTF8, text},
		{"utf-8 bom", append([]byte{0xEF, 0xBB, 0xBF}, `<?xml version="1.0"?><a>`+text+`</a>`...), Options{}, EncodingUTF8, text},
		{"utf-16le bom", utf16Bytes(doc16, false, true), Options{}, EncodingUTF16LE, text},
		{"utf-16be bom", utf16Bytes(doc16, true, true), Options{}, EncodingUTF16BE, text},
		{"utf-16le no bom", utf16Bytes(doc16, false, false), Options{}, EncodingUTF16LE, text},
		{"utf-16be no bom", utf16Bytes(doc16, true, false), Options{}, EncodingUTF16BE, text},
		{"utf-16 declared on utf-8 bytes", []byte(doc16), Options{}, EncodingUTF8, text},
		{"iso-8859-1", []byte("<?xml version='1.0' encoding='ISO-8859-1'?><a>Gr\xfc\xdfe \xa4</a>"), Options{}, EncodingISO88591, "Grüße ¤"},
		{"latin1 alias", []byte("<?xml version=\"1.0\" encoding = \"latin1\" ?><a v=\"\xe9\">\xe9</a>"), Options{}, EncodingISO88591, "é"},
		{"windows-1252", []byte("<?xml version=\"1.0\" encoding=\"windows-1252\"?><a>\x80 \x93q\x94 \xfc</a>"), Options{}, EncodingWindows1252, "€ “q” ü"},
		{"us-ascii", []byte(`<?xml version="1.0" encoding="US-ASCII"?><a>&#252;</a>`), Options{}, EncodingASCII, "ü"},
		{"fallback from Content-Type", []byte("<a>\xfc</a>"), Options{Encoding: "iso-8859-1"}, EncodingISO88591, "ü"},
		{"declaration wins over fallback", []byte(`<?xml version="1.0" encoding="UTF-8"?><a>ü</a>`), Options{Encoding: "iso-8859-1"}, EncodingUTF8, "ü"},
	} {
		doc, err := ParseOptions(context.Background(), c.data, c.opts)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if doc.Encoding != c.encoding || doc.Element().StringValue() != c.want {
			t.Errorf("%s: %s %q, want %s %q", c.name, doc.Encoding, doc.Element().StringValue(), c.encoding, c.want)
		}
		// Offsets refer to Source, which is UTF-8.
		el := doc.Element()
		if src := string(doc.Source[el.Offset:el.End]); src[:2] != "<a" || src[len(src)-4:] != "</a>" {
			t.Errorf("%s: element source %q", c.name, src)
		}
	}
}

func TestEncodingErrors(t *testing.T) {
	for _, c := range []struct {
		name        string
		data        []byte
		unsupported bool
		code        string
	}{
		{"shift_jis", []byte(`<?xml version="1.0" encoding="Shift_JIS"?><a/>`), true, CodeUnsupportedEncoding},
		{"ebcdic declared", []byte(`<?xml version="1.0" encoding="IBM037"?><a/>`), true, CodeUnsupportedEncoding},
		{"ebcdic bytes", []byte{0x4C, 0x6F, 0xA7, 0x94, 0x40}, true, CodeUnsupportedEncoding},
		{"utf-32 bom", []byte{0xFF, 0xFE, 0, 0, '<', 0, 0, 0}, true, CodeUnsupportedEncoding},
		{"unknown fallback", []byte(`<a/>`), true, CodeUnsupportedEncoding},
		{"ascii above 0x7f", []byte("<?xml version=\"1.0\" encoding=\"ascii\"?><a>\xe9</a>"), false, CodeInvalidEncoding},
		{"odd utf-16", append(utf16Bytes(`<a/>`, false, true), 'x'), false, CodeInvalidEncoding},
		{"unpaired surrogate", append(utf16Bytes(`<a>`, false, true), 0x00, 0xD8, '<', 0), false, CodeInvalidEncoding},
		{"lone low surrogate", append(utf16Bytes(`<a>`, true, true), 0xDC, 0x00), false, CodeInvalidEncoding},
	} {
		opts := Options{}
		if c.name == "unknown fallback" {
			opts.Encoding = "koi8-r"
		}
		_, err := ParseOptions(context.Background(), c.data, opts)
		var ee *EncodingError
		if !errors.As(err, &ee) || ee.Unsupported != c.unsupported {
			t.Errorf("%s: err = %v, want an EncodingError (unsupported %v)", c.name, err, c.unsupported)
			continue
		}
		var e *errs.Error
		if !errors.As(AsErr(err), &e) || e.Code != c.code {
			t.Errorf("%s: AsErr = %v", c.name, AsErr(err))
		}
	}
}
