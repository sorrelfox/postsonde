package oauth1

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// The three requests of the RFC 5849 section 1.2 walkthrough (printer and
// photos.example.net), with their consumer and token credentials. Each
// is checked byte for byte: base string, signature and header.
func TestRFC5849Walkthrough(t *testing.T) {
	const consumerKey, consumerSecret = "dpf43f3p2l4k3l03", "kd94hf93k423kf44"
	cases := []struct {
		name       string
		in         Input
		baseString string
		signature  string
		header     string
	}{
		{
			name: "temporary credentials",
			in: Input{
				Method: "POST", URL: "https://photos.example.net/initiate",
				ConsumerKey: consumerKey, Callback: "http://printer.example.com/ready",
				Nonce: "wIjqoS", Timestamp: "137131200",
				Keys: Keys{ConsumerSecret: consumerSecret},
			},
			baseString: "POST&https%3A%2F%2Fphotos.example.net%2Finitiate&oauth_callback%3Dhttp%253A%252F%252Fprinter.example.com%252Fready%26oauth_consumer_key%3Ddpf43f3p2l4k3l03%26oauth_nonce%3DwIjqoS%26oauth_signature_method%3DHMAC-SHA1%26oauth_timestamp%3D137131200",
			signature:  "74KNZJeDHnMBp0EMJ9ZHt/XKycU=",
			header:     `OAuth realm="Photos", oauth_consumer_key="dpf43f3p2l4k3l03", oauth_signature_method="HMAC-SHA1", oauth_timestamp="137131200", oauth_nonce="wIjqoS", oauth_callback="http%3A%2F%2Fprinter.example.com%2Fready", oauth_signature="74KNZJeDHnMBp0EMJ9ZHt%2FXKycU%3D"`,
		},
		{
			name: "token credentials",
			in: Input{
				Method: "POST", URL: "https://photos.example.net/token",
				ConsumerKey: consumerKey, Token: "hh5s93j4hdidpola", Verifier: "hfdp7dh39dks9884",
				Nonce: "walatlh", Timestamp: "137131201",
				Keys: Keys{ConsumerSecret: consumerSecret, TokenSecret: "hdhd0244k9j7ao03"},
			},
			baseString: "POST&https%3A%2F%2Fphotos.example.net%2Ftoken&oauth_consumer_key%3Ddpf43f3p2l4k3l03%26oauth_nonce%3Dwalatlh%26oauth_signature_method%3DHMAC-SHA1%26oauth_timestamp%3D137131201%26oauth_token%3Dhh5s93j4hdidpola%26oauth_verifier%3Dhfdp7dh39dks9884",
			signature:  "gKgrFCywp7rO0OXSjdot/IHF7IU=",
			header:     `OAuth realm="Photos", oauth_consumer_key="dpf43f3p2l4k3l03", oauth_token="hh5s93j4hdidpola", oauth_signature_method="HMAC-SHA1", oauth_timestamp="137131201", oauth_nonce="walatlh", oauth_verifier="hfdp7dh39dks9884", oauth_signature="gKgrFCywp7rO0OXSjdot%2FIHF7IU%3D"`,
		},
		{
			name: "protected resource",
			in: Input{
				Method: "GET", URL: "http://photos.example.net/photos?file=vacation.jpg&size=original",
				ConsumerKey: consumerKey, Token: "nnch734d00sl2jdk",
				Nonce: "chapoH", Timestamp: "137131202",
				Keys: Keys{ConsumerSecret: consumerSecret, TokenSecret: "pfkkdhi9sl3r4s00"},
			},
			baseString: "GET&http%3A%2F%2Fphotos.example.net%2Fphotos&file%3Dvacation.jpg%26oauth_consumer_key%3Ddpf43f3p2l4k3l03%26oauth_nonce%3DchapoH%26oauth_signature_method%3DHMAC-SHA1%26oauth_timestamp%3D137131202%26oauth_token%3Dnnch734d00sl2jdk%26size%3Doriginal",
			signature:  "MdpQcU8iPSUjWoN/UDMsK2sui9I=",
			header:     `OAuth realm="Photos", oauth_consumer_key="dpf43f3p2l4k3l03", oauth_token="nnch734d00sl2jdk", oauth_signature_method="HMAC-SHA1", oauth_timestamp="137131202", oauth_nonce="chapoH", oauth_signature="MdpQcU8iPSUjWoN%2FUDMsK2sui9I%3D"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := SignRequest(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if r.BaseString != c.baseString {
				t.Errorf("base string\n got %s\nwant %s", r.BaseString, c.baseString)
			}
			if r.Signature != c.signature {
				t.Errorf("signature = %s, want %s", r.Signature, c.signature)
			}
			if h := Header("Photos", r.Params); h != c.header {
				t.Errorf("header\n got %s\nwant %s", h, c.header)
			}
		})
	}
}

// Section 3.4.1: the example request with query parameters, a form body
// with a duplicate name and a pair without "=", and encoded characters,
// gives the base string of section 3.4.1.1 exactly.
func TestRFC5849BaseStringExample(t *testing.T) {
	r, err := SignRequest(Input{
		Method:      "POST",
		URL:         "http://example.com/request?b5=%3D%253D&a3=a&c%40=&a2=r%20b",
		Form:        FormParams("c2&a3=2+q"),
		ConsumerKey: "9djdj82h48djs9d2", Token: "kkk9d7dh3k39sjv7",
		Nonce: "7d8f3e4a", Timestamp: "137131201",
		Keys: Keys{ConsumerSecret: "j49sk3j29djd", TokenSecret: "dh893hdasih9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "POST&http%3A%2F%2Fexample.com%2Frequest&a2%3Dr%2520b%26a3%3D2%2520q%26a3%3Da%26b5%3D%253D%25253D%26c%2540%3D%26c2%3D%26oauth_consumer_key%3D9djdj82h48djs9d2%26oauth_nonce%3D7d8f3e4a%26oauth_signature_method%3DHMAC-SHA1%26oauth_timestamp%3D137131201%26oauth_token%3Dkkk9d7dh3k39sjv7"
	if r.BaseString != want {
		t.Errorf("base string\n got %s\nwant %s", r.BaseString, want)
	}
	// The normalized parameters of section 3.4.1.3.2.
	const norm = "a2=r%20b&a3=2%20q&a3=a&b5=%3D%253D&c%40=&c2=&oauth_consumer_key=9djdj82h48djs9d2&oauth_nonce=7d8f3e4a&oauth_signature_method=HMAC-SHA1&oauth_timestamp=137131201&oauth_token=kkk9d7dh3k39sjv7"
	params := append(FormParams(QueryOf("http://example.com/request?b5=%3D%253D&a3=a&c%40=&a2=r%20b")), FormParams("c2&a3=2+q")...)
	params = append(params, r.Params[:len(r.Params)-1]...)
	if got := NormalizeParams(params); got != norm {
		t.Errorf("normalized\n got %s\nwant %s", got, norm)
	}
}

func TestEncode(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"abcXYZ019-._~":      "abcXYZ019-._~",
		"a b":                "a%20b",
		"a+b":                "a%2Bb",
		"*":                  "%2A",
		"!'()":               "%21%27%28%29",
		"%":                  "%25",
		"%20":                "%2520",
		"=&/?#:@":            "%3D%26%2F%3F%23%3A%40",
		"é":                  "%C3%A9",
		"日本":                 "%E6%97%A5%E6%9C%AC",
		"\x00\n\x7f":         "%00%0A%7F",
		"http://a.b/c?d=e&f": "http%3A%2F%2Fa.b%2Fc%3Fd%3De%26f",
	}
	for in, want := range cases {
		if got := Encode(in); got != want {
			t.Errorf("Encode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormParams(t *testing.T) {
	got := FormParams("a=1&&b&c=x+y&d=%41%zz&=v&e==")
	want := []Param{{"a", "1"}, {"b", ""}, {"c", "x y"}, {"d", "%41%zz"}, {"", "v"}, {"e", "="}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("param %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// Duplicate names sort by value, compared encoded (byte order), and names
// compare encoded too: "a%20" sorts before "a1" ("%" < "1").
func TestNormalizeParamsSorting(t *testing.T) {
	params := []Param{
		{"b", "2"}, {"a", "z"}, {"a", "Z"}, {"a", "10"}, {"a", "9"}, {"a", ""},
		{"a1", "x"}, {"a ", "y"}, {"B", "1"}, {"a", "a b"}, {"a", "a+b"},
	}
	const want = "B=1&a=&a=10&a=9&a=Z&a=a%20b&a=a%2Bb&a=z&a%20=y&a1=x&b=2"
	if got := NormalizeParams(params); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestBaseURI(t *testing.T) {
	cases := map[string]string{
		"HTTP://EXAMPLE.COM:80/r%20v/X?id=123": "http://example.com/r%20v/X",
		"https://www.example.net:8080/?q=1":    "https://www.example.net:8080/",
		"https://example.com:443":              "https://example.com/",
		"http://example.com:443/a":             "http://example.com:443/a",
		"http://user:pw@example.com/a#frag":    "http://example.com/a",
		"http://[::1]:8080/p":                  "http://[::1]:8080/p",
		"http://[::1]/p":                       "http://[::1]/p",
	}
	for in, want := range cases {
		got, err := BaseURI(in)
		if err != nil || got != want {
			t.Errorf("BaseURI(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "example.com/a", "http:///a"} {
		if _, err := BaseURI(bad); err == nil {
			t.Errorf("BaseURI(%q): no error", bad)
		}
	}
}

func TestPlaintext(t *testing.T) {
	r, err := SignRequest(Input{
		Method: "GET", URL: "https://example.com/", SignatureMethod: PLAINTEXT,
		ConsumerKey: "key", Nonce: "n", Timestamp: "1",
		Keys: Keys{ConsumerSecret: "c s&", TokenSecret: "t+s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Signature != "c%20s%26&t%2Bs" {
		t.Errorf("signature = %q", r.Signature)
	}
	if h := Header("", r.Params); !strings.HasSuffix(h, `oauth_signature="c%2520s%2526%26t%252Bs"`) || strings.Contains(h, "realm") {
		t.Errorf("header = %s", h)
	}
	// Without a token secret the signature still ends with "&" (3.4.4).
	r, _ = SignRequest(Input{Method: "GET", URL: "https://example.com/", SignatureMethod: PLAINTEXT, ConsumerKey: "k", Nonce: "n", Timestamp: "1", Keys: Keys{ConsumerSecret: "kd94hf93k423kf44"}})
	if r.Signature != "kd94hf93k423kf44&" {
		t.Errorf("signature = %q", r.Signature)
	}
}

// HMAC-SHA256 signs the same base string with SHA-256; the value is
// checked against the base string and key computed independently.
func TestHMACSHA256(t *testing.T) {
	in := Input{
		Method: "GET", URL: "http://photos.example.net/photos?file=vacation.jpg&size=original",
		SignatureMethod: HMACSHA256, ConsumerKey: "dpf43f3p2l4k3l03", Token: "nnch734d00sl2jdk",
		Nonce: "chapoH", Timestamp: "137131202", Version: Version,
		Keys: Keys{ConsumerSecret: "kd94hf93k423kf44", TokenSecret: "pfkkdhi9sl3r4s00"},
	}
	r, err := SignRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	const base = "GET&http%3A%2F%2Fphotos.example.net%2Fphotos&file%3Dvacation.jpg%26oauth_consumer_key%3Ddpf43f3p2l4k3l03%26oauth_nonce%3DchapoH%26oauth_signature_method%3DHMAC-SHA256%26oauth_timestamp%3D137131202%26oauth_token%3Dnnch734d00sl2jdk%26oauth_version%3D1.0%26size%3Doriginal"
	if r.BaseString != base {
		t.Errorf("base string\n got %s\nwant %s", r.BaseString, base)
	}
	// HMAC-SHA256("kd94hf93k423kf44&pfkkdhi9sl3r4s00", base), from openssl.
	const want = "rAAvYu1BQL0v7E7CJl81nKGKZdQr4XFo7E7vbGJxPz4="
	if r.Signature != want {
		t.Errorf("signature = %s, want %s", r.Signature, want)
	}
}

func TestRSASHA1(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	r, err := SignRequest(Input{
		Method: "GET", URL: "http://example.com/a?b=c", SignatureMethod: RSASHA1,
		ConsumerKey: "k", Nonce: "n", Timestamp: "1", Keys: Keys{RSA: key},
	})
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := base64.StdEncoding.DecodeString(r.Signature)
	sum := sha1.Sum([]byte(r.BaseString))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA1, sum[:], sig); err != nil {
		t.Errorf("signature does not verify: %v", err)
	}
	if _, err := SignRequest(Input{Method: "GET", URL: "http://example.com/", SignatureMethod: RSASHA1}); !hasCode(err, CodeRSAKeyMissing) {
		t.Errorf("no key: %v", err)
	}
}

func TestUnsupportedMethod(t *testing.T) {
	_, err := SignRequest(Input{Method: "GET", URL: "http://example.com/", SignatureMethod: "HMAC-MD5"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeUnsupportedMethod || e.Kind != errs.Unsupported {
		t.Errorf("err = %v", err)
	}
}

func TestGeneratedNonceAndTimestamp(t *testing.T) {
	now := func() time.Time { return time.Unix(1700000000, 0) }
	a, err := SignRequest(Input{Method: "GET", URL: "http://example.com/", ConsumerKey: "k", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SignRequest(Input{Method: "GET", URL: "http://example.com/", ConsumerKey: "k", Now: now})
	get := func(r Result, name string) string {
		for _, p := range r.Params {
			if p.Name == name {
				return p.Value
			}
		}
		return ""
	}
	if get(a, "oauth_timestamp") != "1700000000" {
		t.Errorf("timestamp = %q", get(a, "oauth_timestamp"))
	}
	if n := get(a, "oauth_nonce"); len(n) != 32 || n == get(b, "oauth_nonce") || Encode(n) != n {
		t.Errorf("nonces %q, %q", n, get(b, "oauth_nonce"))
	}
}

func TestBodyHash(t *testing.T) {
	// SHA-1 of the empty string, and SHA-256 of "Hello World!".
	if h, _ := BodyHash(HMACSHA1, nil); h != "2jmj7l5rSw0yVb/vlWAYkK/YBwk=" {
		t.Errorf("empty = %s", h)
	}
	if h, _ := BodyHash(HMACSHA256, strings.NewReader("Hello World!")); h != "f4OxZX/x/FO5LcGBSKHWXfwtSx+j1ncoSt3SABJtkGk=" {
		t.Errorf("sha256 = %s", h)
	}
}

func TestPairs(t *testing.T) {
	got := Pairs([]Param{{"oauth_signature", "a+b/c="}, {"oauth_callback", "oob"}})
	if got != "oauth_signature=a%2Bb%2Fc%3D&oauth_callback=oob" {
		t.Errorf("got %s", got)
	}
}

func hasCode(err error, code string) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Code == code
}
