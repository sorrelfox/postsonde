package awssigv4

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// Credentials and scope of the AWS SigV4 test suite
// (aws-sig-v4-test-suite, the 2015-08-30 edition published with the
// "Signature Version 4 test suite" page of the AWS General Reference).
const (
	suiteAccessKey = "AKIDEXAMPLE"
	suiteSecretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	suiteHost      = "example.amazonaws.com"
	suiteDate      = "20150830T123600Z"
)

func suiteTime(t *testing.T) time.Time {
	t.Helper()
	tm, err := time.Parse(TimeFormat, suiteDate)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// TestSuiteVectors reproduces cases of the aws-sig-v4-test-suite (service
// "service", region us-east-1, 20150830T123600Z, host
// example.amazonaws.com): the request line and headers of each case's .req
// file and the signature of its .authz file. The paths are passed as they
// appear in the .req request line, which is what goes on the wire.
func TestSuiteVectors(t *testing.T) {
	t.Parallel()
	scope := Scope{Time: suiteTime(t), Region: "us-east-1", Service: "service"}
	base := func(extra ...Header) []Header {
		h := []Header{{"Host", suiteHost}}
		h = append(h, extra...)
		return append(h, Header{"X-Amz-Date", suiteDate})
	}
	cases := []struct {
		name    string
		method  string
		target  string
		headers []Header
		body    string
		sig     string
	}{
		{"get-vanilla", "GET", "/", base(), "", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"get-vanilla-query-order-key-case", "GET", "/?Param2=value2&Param1=value1", base(), "", "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
		{"get-vanilla-query-order-key", "GET", "/?Param1=value2&Param1=Value1", base(), "", "eedbc4e291e521cf13422ffca22be7d2eb8146eecf653089df300a15b2382bd1"},
		{"get-vanilla-query-order-value", "GET", "/?Param1=value2&Param1=value1", base(), "", "5772eed61e12b33fae39ee5e7012498b51d56abc0abb7c60486157bd471c4694"},
		{"get-vanilla-query-unreserved", "GET", "/?-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz=-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", base(), "", "9c3e54bfcdf0b19771a7f523ee5669cdf59bc7cc0884027167c21bb143a40197"},
		{"get-vanilla-utf8-query", "GET", "/?ሴ=bar", base(), "", "2cdec8eed098649ff3a119c94853b13c643bcf08f8b0a1d91e12c9027818dd04"},
		{"get-vanilla-empty-query-key", "GET", "/?Param1=value1", base(), "", "a67d582fa61cc504c4bae71f336f98b97f1ea3c7a6bfe1b6e45aec72011b9aeb"},
		{"get-unreserved", "GET", "/-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", base(), "", "07ef7494c76fa4850883e2b006601f940f8a34d404d0cfa977f52a65bbf5f24f"},
		{"get-utf8", "GET", "/ሴ", base(), "", "8318018e0b0f223aa2bbf98705b62bb787dc9c0e678f255a891fd03141be5d85"},
		{"normalize-path/get-space", "GET", "/example space/", base(), "", "652487583200325589f1fba4c7e578f72c47cb61beeca81406b39ddec1366741"},
		{"normalize-path/get-slash", "GET", "//", base(), "", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"normalize-path/get-slash-dotslash", "GET", "/./", base(), "", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"normalize-path/get-relative", "GET", "/example/..", base(), "", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"normalize-path/get-relative-relative", "GET", "/example1/example2/../..", base(), "", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"normalize-path/get-slash-pointless-dot", "GET", "/./example", base(), "", "ef75d96142cf21edca26f06005da7988e4f8dc83a165a80865db7089db637ec5"},
		{"normalize-path/get-slashes", "GET", "//example//", base(), "", "9a624bd73a37c9a373b5312afbebe7a714a789de108f0bdfe846570885f57e84"},
		{"get-header-key-duplicate", "GET", "/", base(Header{"My-Header1", "value2"}, Header{"My-Header1", "value2"}, Header{"My-Header1", "value1"}), "", "c9d5ea9f3f72853aea855b47ea873832890dbdd183b4468f858259531a5138ea"},
		{"get-header-value-order", "GET", "/", base(Header{"My-Header1", "value4"}, Header{"My-Header1", "value1"}, Header{"My-Header1", "value3"}, Header{"My-Header1", "value2"}), "", "08c7e5a9acfcfeb3ab6b2185e75ce8b1deb5e634ec47601a50643f830c755c01"},
		{"get-header-value-trim", "GET", "/", base(Header{"My-Header1", " value1"}, Header{"My-Header2", ` "a   b   c"`}), "", "acc3ed3afb60bb290fc8d2dd0098b9911fcaa05412b367055dee359757a9c736"},
		{"post-vanilla", "POST", "/", base(), "", "5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
		{"post-vanilla-query", "POST", "/?Param1=value1", base(), "", "28038455d6de14eafc1f9222cf5aa6f1a96197d7deb8263271d420d138af7f11"},
		{"post-header-key-case", "POST", "/", []Header{{"host", suiteHost}, {"x-amz-date", suiteDate}}, "", "5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
		{"post-header-key-sort", "POST", "/", base(Header{"My-Header1", "value1"}), "", "c5410059b04c1ee005303aed430f6e6645f61f4dc9e1461ec8f8916fdf18852c"},
		{"post-header-value-case", "POST", "/", base(Header{"My-Header1", "VALUE1"}), "", "cdbc9802e29d2942e5e10b5bccfdd67c5f22c7c4e8ae67b53629efa58b974b7d"},
		{"post-x-www-form-urlencoded", "POST", "/", []Header{{"Content-Type", "application/x-www-form-urlencoded"}, {"Host", suiteHost}, {"X-Amz-Date", suiteDate}}, "Param1=value1", "ff11897932ad3f4e8b18135d722051e5ac45fc38421b1da7b9d196a0fe09473a"},
		{"post-x-www-form-urlencoded-parameters", "POST", "/", []Header{{"Content-Type", "application/x-www-form-urlencoded; charset=utf8"}, {"Host", suiteHost}, {"X-Amz-Date", suiteDate}}, "Param1=value1", "1a72ec8f64bd914b0e42e42607c7fbce7fb2c7465f63e3092b3b0d39fa77a6fe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, query, _ := strings.Cut(c.target, "?")
			payload, err := PayloadHash(strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			res := Sign(Request{Method: c.method, Path: path, Query: query, Headers: c.headers, PayloadHash: payload}, suiteSecretKey, scope)
			if res.Signature != c.sig {
				t.Errorf("signature = %s, want %s\ncanonical request:\n%s", res.Signature, c.sig, res.CanonicalRequest)
			}
		})
	}
}

// TestSuiteSessionToken reproduces post-sts-token/post-sts-header-before
// of the aws-sig-v4-test-suite: the session token is a signed header.
func TestSuiteSessionToken(t *testing.T) {
	t.Parallel()
	const token = "AQoDYXdzEPT//////////wEXAMPLEtc764bNrC9SAPBSM22wDOk4x4HIZ8j4FZTwdQWLWsKWHGBuFqwAeMicRXmxfpSPfIeoIYRqTflfKD8YUuwthAx7mSEI/qkPpKPi/kMcGdQrmGdeehM4IC1NtBmUpp2wUE8phUZampKsburEDy0KPkyQDYwT7WZ0wq5VSXDvp75YU9HFvlRd8Tx6q6fE8YQcHNVXAkiY9q6d+xo0rKwT38xVqr7ZD0u0iPPkUL64lIZbqBAz+scqKmlzm8FDrypNC9Yjc8fPOLn9FX9KSYvKTr4rvx3iSIlTJabIQwj2ICCR/oLxBA=="
	scope := Scope{Time: suiteTime(t), Region: "us-east-1", Service: "service"}
	res := Sign(Request{Method: "POST", Path: "/", Headers: []Header{
		{"Host", suiteHost}, {"X-Amz-Date", suiteDate}, {"X-Amz-Security-Token", token},
	}, PayloadHash: EmptyPayloadHash}, suiteSecretKey, scope)
	if want := "85d96828115b5dc0cfc3bd16ad9e210dd772bbebba041836c64533a82be05ead"; res.Signature != want {
		t.Errorf("signature = %s, want %s", res.Signature, want)
	}
	if res.SignedHeaders != "host;x-amz-date;x-amz-security-token" {
		t.Errorf("signed headers = %s", res.SignedHeaders)
	}
}

// TestIAMExample reproduces the worked example of the "Signature Version 4
// signing process" pages of the AWS General Reference (tasks 1 to 4):
// GET https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08 on
// 20150830T123600Z, us-east-1/iam: the canonical request, the string to
// sign (with the canonical request's hash), the derived signing key, and
// the signature in the Authorization header.
func TestIAMExample(t *testing.T) {
	t.Parallel()
	scope := Scope{Time: suiteTime(t), Region: "us-east-1", Service: "iam"}
	r := Request{
		Method: "GET", Path: "/", Query: "Action=ListUsers&Version=2010-05-08",
		Headers: []Header{
			{"Host", "iam.amazonaws.com"},
			{"Content-Type", "application/x-www-form-urlencoded; charset=utf-8"},
			{"X-Amz-Date", suiteDate},
		},
		PayloadHash: EmptyPayloadHash,
	}
	res := Sign(r, suiteSecretKey, scope)
	wantCreq := "GET\n/\nAction=ListUsers&Version=2010-05-08\n" +
		"content-type:application/x-www-form-urlencoded; charset=utf-8\nhost:iam.amazonaws.com\nx-amz-date:20150830T123600Z\n\n" +
		"content-type;host;x-amz-date\n" + EmptyPayloadHash
	if res.CanonicalRequest != wantCreq {
		t.Errorf("canonical request:\n%s\nwant:\n%s", res.CanonicalRequest, wantCreq)
	}
	wantSTS := "AWS4-HMAC-SHA256\n20150830T123600Z\n20150830/us-east-1/iam/aws4_request\nf536975d06c0309214f805bb90ccff089219ecd68b2577efef23edd43b7e1a59"
	if res.StringToSign != wantSTS {
		t.Errorf("string to sign:\n%s\nwant:\n%s", res.StringToSign, wantSTS)
	}
	if got := hex.EncodeToString(SigningKey(suiteSecretKey, scope)); got != "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9" {
		t.Errorf("signing key = %s", got)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := res.AuthorizationHeader(suiteAccessKey); got != want {
		t.Errorf("Authorization = %s\nwant %s", got, want)
	}
}

// S3 examples of the Amazon S3 API Reference, "Signature Calculations for
// the Authorization Header: Transferring Payload in a Single Chunk" and
// "Authenticating Requests: Using Query Parameters": bucket examplebucket,
// 20130524T000000Z, us-east-1.
const (
	s3AccessKey = "AKIAIOSFODNN7EXAMPLE"
	s3SecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	s3Host      = "examplebucket.s3.amazonaws.com"
	s3Date      = "20130524T000000Z"
)

func TestS3Examples(t *testing.T) {
	t.Parallel()
	tm, err := time.Parse(TimeFormat, s3Date)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Time: tm, Region: "us-east-1", Service: ServiceS3}
	bodyHash, _ := PayloadHash(strings.NewReader("Welcome to Amazon S3."))
	cases := []struct {
		name, method, target string
		headers              []Header
		payload              string
		signed, sig          string
	}{
		{"GET Object", "GET", "/test.txt", []Header{
			{"Host", s3Host}, {"Range", "bytes=0-9"}, {"x-amz-content-sha256", EmptyPayloadHash}, {"x-amz-date", s3Date},
		}, EmptyPayloadHash, "host;range;x-amz-content-sha256;x-amz-date", "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"PUT Object", "PUT", "/test$file.text", []Header{
			{"Host", s3Host}, {"Date", "Fri, 24 May 2013 00:00:00 GMT"}, {"x-amz-date", s3Date},
			{"x-amz-storage-class", "REDUCED_REDUNDANCY"}, {"x-amz-content-sha256", bodyHash},
		}, bodyHash, "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class", "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"},
		{"GET Bucket Lifecycle", "GET", "/?lifecycle", []Header{
			{"Host", s3Host}, {"x-amz-date", s3Date}, {"x-amz-content-sha256", EmptyPayloadHash},
		}, EmptyPayloadHash, "host;x-amz-content-sha256;x-amz-date", "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"GET Bucket (List Objects)", "GET", "/?max-keys=2&prefix=J", []Header{
			{"Host", s3Host}, {"x-amz-date", s3Date}, {"x-amz-content-sha256", EmptyPayloadHash},
		}, EmptyPayloadHash, "host;x-amz-content-sha256;x-amz-date", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, query, _ := strings.Cut(c.target, "?")
			res := Sign(Request{Method: c.method, Path: path, Query: query, Headers: c.headers, PayloadHash: c.payload}, s3SecretKey, scope)
			if res.SignedHeaders != c.signed || res.Signature != c.sig {
				t.Errorf("signed %s, signature %s; want %s, %s\ncanonical request:\n%s", res.SignedHeaders, res.Signature, c.signed, c.sig, res.CanonicalRequest)
			}
		})
	}

	t.Run("presigned GET Object", func(t *testing.T) {
		var q []string
		for _, p := range PresignParams(s3AccessKey, scope, 86400, "host", "") {
			q = append(q, Encode(p.Name)+"="+Encode(p.Value))
		}
		res := Sign(Request{Method: "GET", Path: "/test.txt", Query: strings.Join(q, "&"), Headers: []Header{{"Host", s3Host}}, PayloadHash: UnsignedPayload}, s3SecretKey, scope)
		wantQuery := "X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host"
		if !strings.Contains(res.CanonicalRequest, "\n"+wantQuery+"\n") {
			t.Errorf("canonical request:\n%s", res.CanonicalRequest)
		}
		if want := "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"; res.Signature != want {
			t.Errorf("signature = %s, want %s", res.Signature, want)
		}
	})
}

// TestCanonicalURI: the path as sent is encoded again for most services
// (so an escape is double-encoded) and normalized; S3 decodes and encodes
// each segment once, without normalizing.
func TestCanonicalURI(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		path string
		s3   bool
		want string
	}{
		{"", false, "/"},
		{"/a%20b/c", false, "/a%2520b/c"},
		{"/a%20b/c", true, "/a%20b/c"},
		{"/a b/c!", true, "/a%20b/c%21"},
		{"/a//b/./c/../d/", false, "/a/b/d/"},
		{"/a//b/./c/../d/", true, "/a//b/./c/../d/"},
		{"/documents and settings/", false, "/documents%20and%20settings/"},
		{"/%7Euser/%2F", true, "/~user/%2F"},
	} {
		if got := CanonicalURI(c.path, c.s3); got != c.want {
			t.Errorf("CanonicalURI(%q, %v) = %q, want %q", c.path, c.s3, got, c.want)
		}
	}
}

func TestCanonicalQuery(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"b=2&a=1&a=0", "a=0&a=1&b=2"},
		{"flag&x=", "flag=&x="},
		{"q=a+b&r=%7e&&s=%2f", "q=a%20b&r=~&s=%2F"},
	} {
		if got := CanonicalQuery(c.in); got != c.want {
			t.Errorf("CanonicalQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
