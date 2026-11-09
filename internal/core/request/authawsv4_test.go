package request

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/hawk"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func clock(t *testing.T, s string) func() time.Time {
	t.Helper()
	tm, err := time.Parse("20060102T150405Z", s)
	if err != nil {
		t.Fatal(err)
	}
	return func() time.Time { return tm }
}

func checkTypedAuthErr(t *testing.T, err error, typ, code, field string) {
	t.Helper()
	e, ok := err.(*errs.Error)
	if !ok || e.Code != code || e.Details[DetailAuthField] != field || e.Details[DetailAuthType] != typ {
		t.Errorf("err = %#v, want %s field %s", err, code, field)
	}
}

// The worked example of the AWS General Reference's "Signature Version 4
// signing process" (GET ListUsers on IAM, 20150830T123600Z), built from a
// request file: the Authorization header is the published one, byte for
// byte, X-Amz-Date is added, and the secret key and signature are masked.
func TestAWSV4IAMExample(t *testing.T) {
	auth := projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", Region: "us-east-1", Service: "iam"}
	f := projectfile.HTTPRequest{Method: "GET", URL: "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08",
		Headers: []projectfile.KeyValue{{Name: "Content-Type", Value: "application/x-www-form-urlencoded; charset=utf-8"}}}
	b, err := BuildHTTP(f, auth, HTTPOptions{Now: clock(t, "20150830T123600Z")})
	if err != nil {
		t.Fatal(err)
	}
	const want = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got, _ := headerOf(b, "Authorization"); got != want {
		t.Errorf("Authorization\n got %s\nwant %s", got, want)
	}
	if got, _ := headerOf(b, "X-Amz-Date"); got != "20150830T123600Z" {
		t.Errorf("X-Amz-Date = %q", got)
	}
	if _, ok := headerOf(b, "X-Amz-Content-Sha256"); ok {
		t.Error("X-Amz-Content-Sha256 added outside S3")
	}
	if len(b.HeaderSources) != len(b.Request.Headers) || b.HeaderSources[len(b.HeaderSources)-1] != HeaderFromAuth {
		t.Errorf("sources = %v", b.HeaderSources)
	}
	for _, s := range []string{"wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"} {
		if !contains(b.AuthSecrets, s) {
			t.Errorf("secrets %v miss %s", b.AuthSecrets, s)
		}
	}
	if contains(b.AuthSecrets, "AKIDEXAMPLE") {
		t.Error("the access key is not a secret")
	}
}

// The S3 API Reference's GET Object example (bucket examplebucket,
// 20130524T000000Z): X-Amz-Content-Sha256 is added and signed, and the
// signature is the published one.
func TestAWSV4S3GetObject(t *testing.T) {
	auth := projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", Region: "us-east-1", Service: "s3"}
	f := projectfile.HTTPRequest{Method: "GET", URL: "https://examplebucket.s3.amazonaws.com/test.txt",
		Headers: []projectfile.KeyValue{{Name: "Range", Value: "bytes=0-9"}}}
	b, err := BuildHTTP(f, auth, HTTPOptions{Now: clock(t, "20130524T000000Z")})
	if err != nil {
		t.Fatal(err)
	}
	const want = "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got, _ := headerOf(b, "Authorization"); got != want {
		t.Errorf("Authorization\n got %s\nwant %s", got, want)
	}
	if got, _ := headerOf(b, "X-Amz-Content-Sha256"); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("X-Amz-Content-Sha256 = %q", got)
	}
}

// The S3 API Reference's presigned URL example: in the query, the URL is
// the published one, parameter for parameter.
func TestAWSV4S3Presigned(t *testing.T) {
	auth := projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region: "us-east-1", Service: "s3", In: "query", Expires: 86400}
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "https://examplebucket.s3.amazonaws.com/test.txt"}, auth, HTTPOptions{Now: clock(t, "20130524T000000Z")})
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if b.Request.URL != want {
		t.Errorf("URL\n got %s\nwant %s", b.Request.URL, want)
	}
	if _, ok := headerOf(b, "Authorization"); ok {
		t.Error("an Authorization header in query mode")
	}
}

// The body is hashed as it is sent: post-x-www-form-urlencoded of the
// aws-sig-v4-test-suite (service "service", 20150830T123600Z).
func TestAWSV4SuiteFormBody(t *testing.T) {
	auth := projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", Region: "us-east-1", Service: "service"}
	f := projectfile.HTTPRequest{Method: "POST", URL: "https://example.amazonaws.com/",
		Headers: []projectfile.KeyValue{{Name: "Content-Type", Value: "application/x-www-form-urlencoded"}},
		Body:    projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawText, Text: "Param1=value1"}}
	b, err := BuildHTTP(f, auth, HTTPOptions{Now: clock(t, "20150830T123600Z")})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := headerOf(b, "Authorization"); !strings.HasSuffix(got, "SignedHeaders=content-type;host;x-amz-date, Signature=ff11897932ad3f4e8b18135d722051e5ac45fc38421b1da7b9d196a0fe09473a") {
		t.Errorf("Authorization = %s\n%s", got, b.AWSV4.CanonicalRequest)
	}
}

// The path is signed as sent, its escapes encoded again (not for S3);
// the body's implied Content-Type, a session token and an unsigned
// payload are signed; an Authorization row wins and nothing is added;
// fields are checked.
func TestAWSV4Details(t *testing.T) {
	auth := projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AK", SecretKey: "sk", SessionToken: "to/ken+=", Region: "eu-west-1", Service: "execute-api", UnsignedPayload: true}
	f := projectfile.HTTPRequest{Method: "PUT", URL: "https://api.example.com/a b/c?x=1 2",
		Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{}`}}
	b, err := BuildHTTP(f, auth, HTTPOptions{Now: clock(t, "20240102T030405Z")})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(b.AWSV4.CanonicalRequest, "\n")
	if lines[1] != "/a%2520b/c" || lines[2] != "x=1%202" || lines[len(lines)-1] != "UNSIGNED-PAYLOAD" ||
		b.AWSV4.SignedHeaders != "content-type;host;x-amz-content-sha256;x-amz-date;x-amz-security-token" {
		t.Errorf("canonical request:\n%s", b.AWSV4.CanonicalRequest)
	}
	if got, _ := headerOf(b, "X-Amz-Security-Token"); got != "to/ken+=" {
		t.Errorf("token = %q", got)
	}
	if !contains(b.AuthSecrets, "to/ken+=") || !contains(b.AuthSecrets, "to%2Fken%2B%3D") {
		t.Errorf("secrets = %v", b.AuthSecrets)
	}

	auth.Service = "s3"
	if b, err = BuildHTTP(f, auth, HTTPOptions{}); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(b.AWSV4.CanonicalRequest, "\n"); lines[1] != "/a%20b/c" {
		t.Errorf("s3 path = %s", lines[1])
	}

	f.Headers = []projectfile.KeyValue{{Name: "Authorization", Value: "typed"}}
	if b, err = BuildHTTP(f, auth, HTTPOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := headerOf(b, "X-Amz-Date"); ok || len(b.Suppressed) != 1 || b.Suppressed[0].Reason != SuppressedOverridden {
		t.Errorf("override: %+v %+v", b.Request.Headers, b.Suppressed)
	}

	f.Headers = nil
	for _, c := range []struct {
		change func(*projectfile.Auth)
		code   string
		field  string
	}{
		{func(a *projectfile.Auth) { a.Region = "" }, CodeAuthInvalid, "region"},
		{func(a *projectfile.Auth) { a.Service = "a b" }, CodeAuthInvalid, "service"},
		{func(a *projectfile.Auth) { a.SecretKey = "a\nb" }, CodeAuthInvalid, "secretKey"},
		{func(a *projectfile.Auth) { a.In = "query"; a.Expires = 604801 }, CodeAuthInvalid, "expires"},
		{func(a *projectfile.Auth) { a.In = "body" }, CodeAuthUnsupported, "in"},
	} {
		a := auth
		c.change(&a)
		_, err := BuildHTTP(f, a, HTTPOptions{})
		checkTypedAuthErr(t, err, projectfile.AuthAWSV4, c.code, c.field)
	}
	a := auth
	a.AccessKey = ""
	if b, err = BuildHTTP(f, a, HTTPOptions{}); err != nil || len(b.Request.Headers) != 0 || b.AWSV4 != nil {
		t.Errorf("no access key: %+v %v", b.Request.Headers, err)
	}
}

// GraphQL: the signature is made last and covers the Accept header the
// builder adds and the JSON body's Content-Type.
func TestAWSV4GraphQL(t *testing.T) {
	auth := projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AK", SecretKey: "sk", Region: "us-east-1", Service: "appsync"}
	b, err := BuildGraphQL(projectfile.GraphQLRequest{Method: "POST", URL: "https://x.appsync-api.us-east-1.amazonaws.com/graphql", Query: "{ me { id } }"}, auth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b.AWSV4 == nil || b.AWSV4.SignedHeaders != "accept;content-type;host;x-amz-date" {
		t.Errorf("signed = %+v", b.AWSV4)
	}
}

// The Hawk specification's header example (GET
// http://example.com:8000/resource/1?b=1&a=2, ts 1353832234, nonce
// j4h3g2, ext "some-app-ext-data", sha256), built from a request file
// with that clock and randomness that makes that nonce: the header is the
// specification's, byte for byte.
func TestHawkSpecExample(t *testing.T) {
	auth := projectfile.Auth{Type: projectfile.AuthHawk, HawkID: "dh37fgj492je", HawkKey: "werxhqb98rpaxn39848xrunpaw3489ruxnpa98w4rxn", Ext: "some-app-ext-data"}
	now := func() time.Time { return time.Unix(1353832234, 0) }
	// 9, 56, 7, 55, 6, 54 are "j4h3g2" in the nonce alphabet.
	rnd := bytes.NewReader([]byte{9, 56, 7, 55, 6, 54})
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "http://example.com:8000/resource/1?b=1&a=2"}, auth, HTTPOptions{Now: now, Rand: rnd})
	if err != nil {
		t.Fatal(err)
	}
	const want = `Hawk id="dh37fgj492je", ts="1353832234", nonce="j4h3g2", ext="some-app-ext-data", mac="6R4rV5iE+NPoym+WwjeHzjAGXUtLNIxmo1vpMofpLAE="`
	if got, _ := headerOf(b, "Authorization"); got != want {
		t.Errorf("Authorization\n got %s\nwant %s", got, want)
	}
	for _, s := range []string{"werxhqb98rpaxn39848xrunpaw3489ruxnpa98w4rxn", "6R4rV5iE+NPoym+WwjeHzjAGXUtLNIxmo1vpMofpLAE="} {
		if !contains(b.AuthSecrets, s) {
			t.Errorf("secrets %v miss %s", b.AuthSecrets, s)
		}
	}
}

// Hawk details: the payload hash covers the body with its content type,
// the default port and a new nonce on each build, sha1, app and dlg, the
// Authorization row winning, and the field checks.
func TestHawkDetails(t *testing.T) {
	auth := projectfile.Auth{Type: projectfile.AuthHawk, HawkID: "id", HawkKey: "key", HawkAlgorithm: "sha1", PayloadHash: true, App: "app", Dlg: "dlg"}
	f := projectfile.HTTPRequest{Method: "POST", URL: "https://Api.Example.com/p a?q",
		Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{"a":1}`}}
	b, err := BuildHTTP(f, auth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a := b.Hawk
	want, _ := hawk.PayloadHash(hawk.SHA1, "application/json", strings.NewReader(`{"a":1}`))
	if a.Host != "api.example.com" || a.Port != 443 || a.Resource != "/p%20a?q" || a.Hash != want || len(a.Nonce) != 6 {
		t.Errorf("artifacts = %+v", a)
	}
	h, _ := headerOf(b, "Authorization")
	if !strings.Contains(h, `hash="`+want+`"`) || !strings.HasSuffix(h, `, app="app", dlg="dlg"`) {
		t.Errorf("header = %s", h)
	}
	b2, _ := BuildHTTP(f, auth, HTTPOptions{})
	if b2.Hawk.Nonce == a.Nonce {
		t.Error("the nonce is not new on every build")
	}

	f.Headers = []projectfile.KeyValue{{Name: "Authorization", Value: "typed"}}
	if b, err = BuildHTTP(f, auth, HTTPOptions{}); err != nil {
		t.Fatal(err)
	}
	if h, _ := headerOf(b, "Authorization"); h != "typed" || len(b.Suppressed) != 1 {
		t.Errorf("override: %s %+v", h, b.Suppressed)
	}
	f.Headers = nil
	for _, c := range []struct {
		change func(*projectfile.Auth)
		code   string
		field  string
	}{
		{func(a *projectfile.Auth) { a.HawkAlgorithm = "md5" }, CodeAuthUnsupported, "hawkAlgorithm"},
		{func(a *projectfile.Auth) { a.HawkID = `a"b` }, CodeAuthInvalid, "hawkId"},
		{func(a *projectfile.Auth) { a.Ext = "é" }, CodeAuthInvalid, "ext"},
	} {
		x := auth
		c.change(&x)
		_, err := BuildHTTP(f, x, HTTPOptions{})
		checkTypedAuthErr(t, err, projectfile.AuthHawk, c.code, c.field)
	}
	x := auth
	x.HawkID = ""
	if b, err = BuildHTTP(f, x, HTTPOptions{}); err != nil || b.Hawk != nil {
		t.Errorf("no id: %+v %v", b.Hawk, err)
	}
}
