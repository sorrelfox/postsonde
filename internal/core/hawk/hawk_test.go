package hawk

import (
	"strings"
	"testing"
)

// The credentials and requests of the Hawk specification's examples (the
// README of the reference implementation, hueniverse/hawk: "Protocol
// Example" and "Payload Validation").
const (
	specID  = "dh37fgj492je"
	specKey = "werxhqb98rpaxn39848xrunpaw3489ruxnpa98w4rxn"
)

// TestSpecHeaderExample: GET http://example.com:8000/resource/1?b=1&a=2
// with ts 1353832234, nonce j4h3g2 and ext "some-app-ext-data", sha256.
func TestSpecHeaderExample(t *testing.T) {
	t.Parallel()
	a := Artifacts{
		Timestamp: 1353832234, Nonce: "j4h3g2", Method: "GET",
		Resource: "/resource/1?b=1&a=2", Host: "example.com", Port: 8000,
		Ext: "some-app-ext-data",
	}
	wantNorm := "hawk.1.header\n1353832234\nj4h3g2\nGET\n/resource/1?b=1&a=2\nexample.com\n8000\n\nsome-app-ext-data\n"
	if got := NormalizedString(a); got != wantNorm {
		t.Errorf("normalized string:\n%q\nwant\n%q", got, wantNorm)
	}
	mac, err := MAC(SHA256, specKey, a)
	if err != nil {
		t.Fatal(err)
	}
	if want := "6R4rV5iE+NPoym+WwjeHzjAGXUtLNIxmo1vpMofpLAE="; mac != want {
		t.Errorf("mac = %s, want %s", mac, want)
	}
	want := `Hawk id="dh37fgj492je", ts="1353832234", nonce="j4h3g2", ext="some-app-ext-data", mac="6R4rV5iE+NPoym+WwjeHzjAGXUtLNIxmo1vpMofpLAE="`
	if got := Header(specID, a, mac); got != want {
		t.Errorf("header = %s\nwant %s", got, want)
	}
}

// TestSpecPayloadExample: POST of "Thank you for flying Hawk" (text/plain)
// to the same resource, with the payload hash.
func TestSpecPayloadExample(t *testing.T) {
	t.Parallel()
	hash, err := PayloadHash(SHA256, "text/plain", strings.NewReader("Thank you for flying Hawk"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "Yi9LfIIFRtBEPt74PVmbTF/xVAwPn7ub15ePICfgnuY="; hash != want {
		t.Errorf("hash = %s, want %s", hash, want)
	}
	a := Artifacts{
		Timestamp: 1353832234, Nonce: "j4h3g2", Method: "POST",
		Resource: "/resource/1?b=1&a=2", Host: "example.com", Port: 8000,
		Hash: hash, Ext: "some-app-ext-data",
	}
	mac, err := MAC(SHA256, specKey, a)
	if err != nil {
		t.Fatal(err)
	}
	if want := "aSe1DERmZuRl3pI36/9BdZmnErTw3sNzOOAUlfeKjVw="; mac != want {
		t.Errorf("mac = %s, want %s", mac, want)
	}
}

func TestPayloadHashContentType(t *testing.T) {
	t.Parallel()
	a, _ := PayloadHash(SHA256, "Text/Plain; charset=utf-8", strings.NewReader("x"))
	b, _ := PayloadHash(SHA256, "text/plain", strings.NewReader("x"))
	if a != b {
		t.Errorf("parameters and case of the content type count: %s != %s", a, b)
	}
	if _, err := PayloadHash("md5", "", nil); err == nil {
		t.Error("md5 accepted")
	}
}

func TestHeaderAppAndEscapes(t *testing.T) {
	t.Parallel()
	a := Artifacts{Timestamp: 1, Nonce: "n", Method: "get", Resource: "/", Host: "EXAMPLE.com", Port: 443,
		Hash: "h", Ext: `say "hi" \o/`, App: "app1", Dlg: "dlg1"}
	if got, want := Header("id", a, "m"), `Hawk id="id", ts="1", nonce="n", hash="h", ext="say \"hi\" \\o/", mac="m", app="app1", dlg="dlg1"`; got != want {
		t.Errorf("header = %s\nwant %s", got, want)
	}
	if got, want := NormalizedString(a), "hawk.1.header\n1\nn\nGET\n/\nexample.com\n443\nh\nsay \"hi\" \\\\o/\napp1\ndlg1\n"; got != want {
		t.Errorf("normalized = %q\nwant %q", got, want)
	}
	if ValidAttribute(`a"b`) || !ValidAttribute("abc-123") || ValidExt("a\nb") {
		t.Error("ValidAttribute/ValidExt")
	}
}
