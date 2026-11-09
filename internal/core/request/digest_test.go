package request

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestDigestAuth(t *testing.T) {
	a := projectfile.Auth{Type: projectfile.AuthDigest, Username: "Mufasa", Password: "{{pw}}"}
	expand := func(s string) (string, error) { return strings.ReplaceAll(s, "{{pw}}", "Circle of Life"), nil }
	out, err := Auth(a, nil, expand)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Headers) != 0 || len(out.Query) != 0 || out.Digest == nil || *out.Digest != (httpclient.DigestCredentials{Username: "Mufasa", Password: "Circle of Life"}) {
		t.Errorf("digest = %+v", out)
	}
	for _, tc := range []struct {
		name string
		auth projectfile.Auth
	}{
		{"empty", projectfile.Auth{Type: projectfile.AuthDigest}},
		{"disabled", projectfile.Auth{Type: projectfile.AuthDigest, Username: "u", Password: "p", Disabled: true}},
	} {
		if out, err := Auth(tc.auth, nil, nil); err != nil || out.Digest != nil {
			t.Errorf("%s: %+v, %v; want nothing", tc.name, out, err)
		}
	}
	_, err = Auth(projectfile.Auth{Type: projectfile.AuthDigest, Username: "u\n", Password: "p"}, nil, nil)
	if e, ok := err.(*errs.Error); !ok || e.Code != CodeAuthInvalid || e.Details[DetailAuthField] != "username" {
		t.Errorf("control character: %v", err)
	}
	_, err = Auth(projectfile.Auth{Type: projectfile.AuthDigest, Username: "u", LocalSecrets: []string{"password"}}, nil, nil)
	if e, ok := err.(*errs.Error); !ok || e.Code != CodeAuthSecretMissing || e.Details[DetailAuthField] != "password" {
		t.Errorf("missing secret: %v", err)
	}

	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "http://example.com/"}, a, HTTPOptions{Expand: expand})
	if err != nil {
		t.Fatal(err)
	}
	if b.Request.Digest == nil || b.Request.Digest.Password != "Circle of Life" {
		t.Errorf("Request.Digest = %+v", b.Request.Digest)
	}
	b, err = BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: "http://example.com/", Headers: []projectfile.KeyValue{kv("authorization", "Bearer x")}}, a, HTTPOptions{Expand: expand})
	if err != nil {
		t.Fatal(err)
	}
	if b.Request.Digest != nil {
		t.Errorf("an Authorization row should win over Digest")
	}
}

// TestDigestOnTheWire builds a Digest request and sends it to a server
// that checks the answer (RFC 7616, MD5, qop auth).
func TestDigestOnTheWire(t *testing.T) {
	const realm, user, pass = "r", "zoe", "p:ä"
	h := func(s string) string {
		sum := md5.Sum([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get("Authorization")
		p := map[string]string{}
		for part := range strings.SplitSeq(strings.TrimPrefix(v, "Digest "), ", ") {
			k, val, _ := strings.Cut(part, "=")
			p[k] = strings.Trim(val, `"`)
		}
		ha1 := h(user + ":" + realm + ":" + pass)
		ha2 := h(r.Method + ":" + p["uri"])
		if p["username"] == user && p["uri"] == "/a?b=1" && p["response"] == h(ha1+":n1:"+p["nc"]+":"+p["cnonce"]+":auth:"+ha2) {
			io.WriteString(w, "ok")
			return
		}
		w.Header().Set("WWW-Authenticate", `Digest realm="r", nonce="n1", qop="auth"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	client := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	defer client.CloseIdleConnections()
	b, err := BuildHTTP(projectfile.HTTPRequest{Method: "GET", URL: srv.URL + "/a?b=1"},
		projectfile.Auth{Type: projectfile.AuthDigest, Username: user, Password: pass}, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Send(context.Background(), b.Request)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(resp.Redirects) != 1 {
		t.Errorf("status %d, hops %d", resp.StatusCode, len(resp.Redirects))
	}
}
