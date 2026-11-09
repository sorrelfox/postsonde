package request

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/oauth1"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// photosAuth is the protected-resource request of RFC 5849 section 1.2.
func photosAuth() projectfile.Auth {
	return projectfile.Auth{
		Type: projectfile.AuthOAuth1, ConsumerKey: "dpf43f3p2l4k3l03", ConsumerSecret: "kd94hf93k423kf44",
		AccessToken: "nnch734d00sl2jdk", TokenSecret: "pfkkdhi9sl3r4s00",
		Realm: "Photos", OmitVersion: true, Nonce: "chapoH", Timestamp: "137131202",
	}
}

func headerOf(b HTTPBuild, name string) (string, bool) {
	for _, h := range b.Request.Headers {
		if h.Enabled && strings.EqualFold(h.Name, name) {
			return h.Value, true
		}
	}
	return "", false
}

func paramOf(r *oauth1.Result, name string) string {
	for _, p := range r.Params {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// The RFC 5849 section 1.2 request, built from a request file: the
// Authorization header is the RFC's, byte for byte, and the secrets and
// signature are masked.
func TestOAuth1HeaderRFCExample(t *testing.T) {
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "http://photos.example.net/photos?file=vacation.jpg&size=original"}, photosAuth(), HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const want = `OAuth realm="Photos", oauth_consumer_key="dpf43f3p2l4k3l03", oauth_token="nnch734d00sl2jdk", oauth_signature_method="HMAC-SHA1", oauth_timestamp="137131202", oauth_nonce="chapoH", oauth_signature="MdpQcU8iPSUjWoN%2FUDMsK2sui9I%3D"`
	if got, _ := headerOf(b, "Authorization"); got != want {
		t.Errorf("Authorization\n got %s\nwant %s", got, want)
	}
	if b.HeaderSources[len(b.HeaderSources)-1] != HeaderFromAuth || len(b.HeaderSources) != len(b.Request.Headers) {
		t.Errorf("sources = %v", b.HeaderSources)
	}
	if b.Request.URL != "http://photos.example.net/photos?file=vacation.jpg&size=original" || b.Request.Body != nil {
		t.Errorf("request changed: %s %v", b.Request.URL, b.Request.Body)
	}
	for _, s := range []string{"kd94hf93k423kf44", "pfkkdhi9sl3r4s00", "MdpQcU8iPSUjWoN/UDMsK2sui9I=", "MdpQcU8iPSUjWoN%2FUDMsK2sui9I%3D"} {
		if !contains(b.AuthSecrets, s) {
			t.Errorf("secrets %v miss %s", b.AuthSecrets, s)
		}
	}
	if contains(b.AuthSecrets, "dpf43f3p2l4k3l03") {
		t.Errorf("the consumer key is not a secret")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Section 3.4.1: query parameters and the parameters of a URL-encoded
// form body are signed, duplicates included, and the URL is signed as it
// goes on the wire (WHATWG encoding: the typed space becomes %20).
func TestOAuth1SignsFormBodyAndQuery(t *testing.T) {
	auth := projectfile.Auth{
		Type: projectfile.AuthOAuth1, ConsumerKey: "9djdj82h48djs9d2", ConsumerSecret: "j49sk3j29djd",
		AccessToken: "kkk9d7dh3k39sjv7", TokenSecret: "dh893hdasih9",
		OmitVersion: true, Nonce: "7d8f3e4a", Timestamp: "137131201",
	}
	f := projectfile.HTTPRequest{
		Method: "POST", URL: "http://example.com/request?b5=%3D%253D&a3=a&c%40=&a2=r b",
		Body: projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: []projectfile.KeyValue{{Name: "c2"}, {Name: "a3", Value: "2 q"}}},
	}
	b, err := BuildHTTP(f, auth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "POST&http%3A%2F%2Fexample.com%2Frequest&a2%3Dr%2520b%26a3%3D2%2520q%26a3%3Da%26b5%3D%253D%25253D%26c%2540%3D%26c2%3D%26oauth_consumer_key%3D9djdj82h48djs9d2%26oauth_nonce%3D7d8f3e4a%26oauth_signature_method%3DHMAC-SHA1%26oauth_timestamp%3D137131201%26oauth_token%3Dkkk9d7dh3k39sjv7"
	if b.OAuth1 == nil || b.OAuth1.BaseString != want {
		t.Fatalf("base string\n got %+v\nwant %s", b.OAuth1, want)
	}
	if got := bodyText(t, b.Request.Body); got != "c2=&a3=2+q" {
		t.Errorf("body = %q", got)
	}
	// A Content-Type row naming the form type counts too; another type
	// leaves the body out of the signature.
	f.Body = projectfile.Body{Mode: projectfile.BodyRaw, Text: "c2&a3=2+q"}
	f.Headers = []projectfile.KeyValue{{Name: "Content-Type", Value: "application/x-www-form-urlencoded; charset=utf-8"}}
	if b, err = BuildHTTP(f, auth, HTTPOptions{}); err != nil || b.OAuth1.BaseString != want {
		t.Errorf("raw form body: %v %+v", err, b.OAuth1)
	}
	f.Headers = []projectfile.KeyValue{{Name: "Content-Type", Value: "text/plain"}}
	if b, err = BuildHTTP(f, auth, HTTPOptions{}); err != nil || strings.Contains(b.OAuth1.BaseString, "c2") {
		t.Errorf("text body: %v %+v", err, b.OAuth1)
	}
}

func TestOAuth1InQueryAndBody(t *testing.T) {
	auth := photosAuth()
	auth.In = projectfile.APIKeyInQuery
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "http://photos.example.net/photos?file=vacation.jpg&size=original#top"}, auth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := headerOf(b, "Authorization"); ok {
		t.Error("an Authorization header with in query")
	}
	const want = "http://photos.example.net/photos?file=vacation.jpg&size=original&oauth_consumer_key=dpf43f3p2l4k3l03&oauth_token=nnch734d00sl2jdk&oauth_signature_method=HMAC-SHA1&oauth_timestamp=137131202&oauth_nonce=chapoH&oauth_signature=MdpQcU8iPSUjWoN%2FUDMsK2sui9I%3D#top"
	if b.Request.URL != want {
		t.Errorf("URL\n got %s\nwant %s", b.Request.URL, want)
	}

	// In the body: appended to a form body, or a new form body.
	auth.In = projectfile.AuthInBody
	f := projectfile.HTTPRequest{Method: "POST", URL: "http://photos.example.net/photos",
		Body: projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: []projectfile.KeyValue{{Name: "file", Value: "vacation.jpg"}, {Name: "size", Value: "original"}}}}
	if b, err = BuildHTTP(f, auth, HTTPOptions{}); err != nil {
		t.Fatal(err)
	}
	body := bodyText(t, b.Request.Body)
	if !strings.HasPrefix(body, "file=vacation.jpg&size=original&oauth_consumer_key=dpf43f3p2l4k3l03&") || !strings.HasSuffix(body, "&oauth_signature="+oauth1.Encode(b.OAuth1.Signature)) {
		t.Errorf("body = %s", body)
	}
	if b.Request.Body.ContentType() != FormContentType || strings.Contains(b.OAuth1.BaseString, "realm") {
		t.Errorf("type = %q", b.Request.Body.ContentType())
	}
	f.Body = projectfile.Body{Mode: projectfile.BodyNone}
	if b, err = BuildHTTP(f, auth, HTTPOptions{}); err != nil {
		t.Fatal(err)
	}
	if body := bodyText(t, b.Request.Body); !strings.HasPrefix(body, "oauth_consumer_key=") || b.Request.Body.ContentType() != FormContentType {
		t.Errorf("new body = %q", body)
	}
	f.Body = projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: "{}"}
	_, err = BuildHTTP(f, auth, HTTPOptions{})
	checkAuthErr(t, err, CodeAuthInvalid, "in")
}

func checkAuthErr(t *testing.T, err error, code, field string) {
	t.Helper()
	e, ok := err.(*errs.Error)
	if !ok || e.Code != code || e.Details[DetailAuthField] != field || e.Details[DetailAuthType] != projectfile.AuthOAuth1 {
		t.Errorf("err = %#v, want %s field %s", err, code, field)
	}
}

// The body hash covers a body that is not a form: SHA-1 of its bytes, or
// of nothing without a body; a form body gets none.
func TestOAuth1BodyHash(t *testing.T) {
	auth := photosAuth()
	auth.BodyHash = true
	f := projectfile.HTTPRequest{Method: "PUT", URL: "http://photos.example.net/photos",
		Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{"a": 1}`}}
	b, err := BuildHTTP(f, auth, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(`{"a": 1}`))
	want := base64.StdEncoding.EncodeToString(sum[:])
	if got := paramOf(b.OAuth1, "oauth_body_hash"); got != want || !strings.Contains(b.OAuth1.BaseString, "oauth_body_hash%3D"+oauth1.Encode(oauth1.Encode(want))) {
		t.Errorf("body hash = %q, want %q; base %s", got, want, b.OAuth1.BaseString)
	}
	if h, _ := headerOf(b, "Authorization"); !strings.Contains(h, `oauth_body_hash="`+oauth1.Encode(want)+`"`) {
		t.Errorf("header = %s", h)
	}
	f.Body = projectfile.Body{Mode: projectfile.BodyNone}
	f.Method = "GET"
	if b, _ = BuildHTTP(f, auth, HTTPOptions{}); paramOf(b.OAuth1, "oauth_body_hash") != "2jmj7l5rSw0yVb/vlWAYkK/YBwk=" {
		t.Errorf("empty body hash = %q", paramOf(b.OAuth1, "oauth_body_hash"))
	}
	f.Body = projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: []projectfile.KeyValue{{Name: "a", Value: "1"}}}
	if b, _ = BuildHTTP(f, auth, HTTPOptions{}); paramOf(b.OAuth1, "oauth_body_hash") != "" {
		t.Error("a form body got a body hash")
	}
}

// Fields are rendered first; nonce and timestamp are generated when
// empty; the version is sent unless left out; an Authorization row wins
// and settings.omitHeaders leaves the header out.
func TestOAuth1FieldsAndOverrides(t *testing.T) {
	r := template.Resolver{Scopes: template.Chain{template.MapScope{Label: "env", Variables: map[string]template.Variable{
		"ck": {Value: "key-{{n}}"}, "cs": {Value: "sec", Secret: true}, "n": {Value: "7"}, "m": {Value: "HMAC-SHA256"},
	}}}}
	expand := func(s string) (string, error) { res := r.Render(s); return res.Text, res.Err() }
	auth := projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "{{ck}}", ConsumerSecret: "{{cs}}", SignatureMethod: "{{m}}", Callback: "oob"}
	f := projectfile.HTTPRequest{Method: "GET", URL: "https://api.example.com:443/x"}
	b, err := BuildHTTP(f, auth, HTTPOptions{Expand: expand, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	res := b.OAuth1
	if paramOf(res, "oauth_consumer_key") != "key-7" || paramOf(res, "oauth_signature_method") != "HMAC-SHA256" ||
		paramOf(res, "oauth_version") != "1.0" || paramOf(res, "oauth_callback") != "oob" || paramOf(res, "oauth_token") != "" ||
		paramOf(res, "oauth_timestamp") != "1789819200" || len(paramOf(res, "oauth_nonce")) != 32 {
		t.Errorf("params = %+v", res.Params)
	}
	if !strings.HasPrefix(res.BaseString, "GET&https%3A%2F%2Fapi.example.com%2Fx&") {
		t.Errorf("base = %s", res.BaseString)
	}
	b2, _ := BuildHTTP(f, auth, HTTPOptions{Expand: expand, Now: fixedNow})
	if paramOf(b2.OAuth1, "oauth_nonce") == paramOf(res, "oauth_nonce") {
		t.Error("the nonce is not new on every build")
	}

	f.Headers = []projectfile.KeyValue{{Name: "authorization", Value: "OAuth typed"}}
	if b, err = BuildHTTP(f, auth, HTTPOptions{Expand: expand}); err != nil {
		t.Fatal(err)
	}
	if h, _ := headerOf(b, "Authorization"); h != "OAuth typed" || len(b.Suppressed) != 1 || b.Suppressed[0].Reason != SuppressedOverridden {
		t.Errorf("override: %s %+v", h, b.Suppressed)
	}
	f.Headers = nil
	f.Settings.OmitHeaders = []string{"Authorization"}
	if b, err = BuildHTTP(f, auth, HTTPOptions{Expand: expand}); err != nil {
		t.Fatal(err)
	}
	if _, ok := headerOf(b, "Authorization"); ok || len(b.Suppressed) != 1 || b.Suppressed[0].Reason != SuppressedOmitted {
		t.Errorf("omitted: %+v", b.Suppressed)
	}
}

func TestOAuth1Errors(t *testing.T) {
	f := projectfile.HTTPRequest{Method: "GET", URL: "http://example.com/"}
	// No consumer key: nothing is sent.
	b, err := BuildHTTP(f, projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerSecret: "x"}, HTTPOptions{})
	if err != nil || b.OAuth1 != nil || len(b.Request.Headers) != 0 {
		t.Errorf("empty key: %v %+v", err, b.Request.Headers)
	}
	// Disabled: nothing either.
	a := photosAuth()
	a.Disabled = true
	if b, err = BuildHTTP(f, a, HTTPOptions{}); err != nil || b.OAuth1 != nil {
		t.Errorf("disabled: %v", err)
	}

	a = photosAuth()
	a.SignatureMethod = "HMAC-MD5"
	_, err = BuildHTTP(f, a, HTTPOptions{})
	checkAuthErr(t, err, CodeAuthUnsupported, "signatureMethod")

	a = photosAuth()
	a.In = "cookie"
	_, err = BuildHTTP(f, a, HTTPOptions{})
	checkAuthErr(t, err, CodeAuthUnsupported, "in")

	// The token secret typed on another computer.
	a = photosAuth()
	a.TokenSecret, a.LocalSecrets = "", []string{"tokenSecret"}
	_, err = BuildHTTP(f, a, HTTPOptions{})
	checkAuthErr(t, err, CodeAuthSecretMissing, "tokenSecret")
	if !strings.Contains(err.Error(), "token secret") {
		t.Errorf("message = %v", err)
	}

	a = photosAuth()
	a.SignatureMethod = oauth1.RSASHA1
	_, err = BuildHTTP(f, a, HTTPOptions{})
	checkAuthErr(t, err, CodeAuthInvalid, "consumerSecret")
}

// RSA-SHA1 signs with the PEM key typed as the consumer secret, which is
// masked; the token secret is not used.
func TestOAuth1RSASHA1(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	text := pemKey(t, key)
	a := photosAuth()
	a.SignatureMethod, a.ConsumerSecret = oauth1.RSASHA1, text
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "http://photos.example.net/photos"}, a, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if paramOf(b.OAuth1, "oauth_signature_method") != "RSA-SHA1" || !contains(b.AuthSecrets, text) || contains(b.AuthSecrets, "pfkkdhi9sl3r4s00") {
		t.Errorf("result = %+v", b.OAuth1)
	}
	sig, _ := base64.StdEncoding.DecodeString(b.OAuth1.Signature)
	sum := sha1.Sum([]byte(b.OAuth1.BaseString))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA1, sum[:], sig); err != nil {
		t.Errorf("the signature does not verify: %v", err)
	}
	a.ConsumerSecret = "-----BEGIN PRIVATE KEY-----\nnope\n-----END PRIVATE KEY-----\n"
	_, err = BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "http://photos.example.net/photos"}, a, HTTPOptions{})
	checkAuthErr(t, err, CodeAuthInvalid, "consumerSecret")
}

// GraphQL over GET: the signature covers the query parameters the builder
// adds after the HTTP part is built.
func TestOAuth1GraphQLGet(t *testing.T) {
	b, err := BuildGraphQL(projectfile.GraphQLRequest{Method: "GET", URL: "http://photos.example.net/graphql", Query: "{ me { id } }"}, photosAuth(), HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.OAuth1.BaseString, "query%3D%257B%2520me%2520%257B%2520id%2520%257D%2520%257D") {
		t.Errorf("base = %s", b.OAuth1.BaseString)
	}
	if _, ok := headerOf(b, "Authorization"); !ok {
		t.Error("no Authorization header")
	}
}
