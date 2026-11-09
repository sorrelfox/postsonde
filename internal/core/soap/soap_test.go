package soap

import (
	"encoding/xml"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version, action string
		want            []Header
	}{
		{V11, "urn:example:GetQuote", []Header{
			{"Content-Type", "text/xml; charset=utf-8"},
			{"SOAPAction", `"urn:example:GetQuote"`},
		}},
		{V11, "", []Header{
			{"Content-Type", "text/xml; charset=utf-8"},
			{"SOAPAction", `""`},
		}},
		{V11, `say "hi" \ {{x}}`, []Header{
			{"Content-Type", "text/xml; charset=utf-8"},
			{"SOAPAction", `"say \"hi\" \\ {{x}}"`},
		}},
		{V12, "urn:example:GetQuote", []Header{
			{"Content-Type", `application/soap+xml; charset=utf-8; action="urn:example:GetQuote"`},
		}},
		{V12, "", []Header{
			{"Content-Type", "application/soap+xml; charset=utf-8"},
		}},
	}
	for _, tt := range tests {
		got, err := Headers(tt.version, tt.action)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Headers(%q, %q) = %q, %v; want %q", tt.version, tt.action, got, err, tt.want)
		}
	}
	if _, err := Headers("1.3", ""); !errors.Is(err, ErrVersion) {
		t.Errorf("Headers(1.3) err = %v", err)
	}
}

func TestEnvelopeBlank(t *testing.T) {
	t.Parallel()
	got, err := Envelope(V11, " \n")
	want := `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Header/>
  <soap:Body>
    <!-- The operation's element goes here. -->
  </soap:Body>
</soap:Envelope>`
	if err != nil || got != want {
		t.Errorf("Envelope = %q, %v\nwant %q", got, err, want)
	}
}

func TestEnvelopeWraps(t *testing.T) {
	t.Parallel()
	in := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<m:GetQuote xmlns:m=\"urn:example\">\r\n  <m:Symbol>{{symbol}}</m:Symbol>\n\n</m:GetQuote>\n"
	got, err := Envelope(V12, in)
	want := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
  <soap:Header/>
  <soap:Body>
    <m:GetQuote xmlns:m="urn:example">
      <m:Symbol>{{symbol}}</m:Symbol>

    </m:GetQuote>
  </soap:Body>
</soap:Envelope>`
	if err != nil || got != want {
		t.Fatalf("Envelope = %q, %v\nwant %q", got, err, want)
	}
	// The result is well-formed, with the operation inside Body.
	var env struct {
		XMLName xml.Name
		Body    struct {
			Op struct {
				XMLName xml.Name
			} `xml:",any"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal([]byte(got), &env); err != nil {
		t.Fatal(err)
	}
	if env.XMLName.Space != NS12 || env.Body.Op.XMLName.Local != "GetQuote" {
		t.Errorf("parsed %+v", env)
	}
}

func TestEnvelopeSwitchesVersion(t *testing.T) {
	t.Parallel()
	v11, err := Envelope(V11, "<ping/>")
	if err != nil {
		t.Fatal(err)
	}
	// Already an envelope of that version: unchanged.
	if again, _ := Envelope(V11, v11); again != v11 {
		t.Errorf("same version changed the text: %q", again)
	}
	v12, err := Envelope(V12, v11)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.ReplaceAll(v11, NS11, NS12); v12 != want {
		t.Errorf("1.2 = %q, want %q", v12, want)
	}
	back, _ := Envelope(V11, v12)
	if back != v11 {
		t.Errorf("back to 1.1 = %q", back)
	}
	// A default-namespace envelope and a leading comment are recognised too.
	def := "<!-- c -->\n<Envelope xmlns=\"" + NS11 + "\"><Body/></Envelope>"
	if got, _ := Envelope(V12, def); got != strings.ReplaceAll(def, NS11, NS12) {
		t.Errorf("default namespace: %q", got)
	}
}

func TestEnvelopeNotAnEnvelope(t *testing.T) {
	t.Parallel()
	// An Envelope of another namespace, and text that is not XML, are wrapped.
	for _, in := range []string{`<Envelope xmlns="urn:other"/>`, `{"json": true}`} {
		got, err := Envelope(V11, in)
		if err != nil || !strings.Contains(got, "<soap:Body>\n    "+in+"\n  </soap:Body>") {
			t.Errorf("Envelope(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := Envelope("2", ""); !errors.Is(err, ErrVersion) {
		t.Errorf("err = %v", err)
	}
}
