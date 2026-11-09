package http

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// oauthServer verifies OAuth 1.0 signatures (RFC 5849) on its own, with
// net/url's parsing and the credentials it knows, and answers
// "ok|<where>|<method>" or "bad|<reason>".
func oauthServer(t *testing.T, consumerSecret, tokenSecret string) *httptest.Server {
	enc := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		type kv struct{ k, v string }
		var params []kv
		oauth := map[string]string{}
		where := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "OAuth ") {
			where = "header"
			for part := range strings.SplitSeq(strings.TrimPrefix(h, "OAuth "), ",") {
				k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
				v, _ = url.PathUnescape(strings.Trim(v, `"`))
				if k == "realm" {
					continue
				}
				oauth[k] = v
			}
		}
		for k, vs := range r.URL.Query() {
			for _, v := range vs {
				params = append(params, kv{k, v})
				if strings.HasPrefix(k, "oauth_") {
					oauth[k], where = v, "query"
				}
			}
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			if err := r.ParseForm(); err != nil {
				fmt.Fprintf(w, "bad|form %v", err)
				return
			}
			for k, vs := range r.PostForm {
				for _, v := range vs {
					params = append(params, kv{k, v})
					if strings.HasPrefix(k, "oauth_") {
						oauth[k], where = v, "body"
					}
				}
			}
		}
		if where == "header" {
			for k, v := range oauth {
				params = append(params, kv{k, v})
			}
		}
		sig := oauth["oauth_signature"]
		var norm []string
		for _, p := range params {
			if p.k != "oauth_signature" {
				norm = append(norm, enc(p.k)+"="+enc(p.v))
			}
		}
		sort.Strings(norm)
		base := r.Method + "&" + enc("http://"+r.Host+r.URL.EscapedPath()) + "&" + enc(strings.Join(norm, "&"))
		var h func() hash.Hash
		switch oauth["oauth_signature_method"] {
		case "HMAC-SHA1":
			h = sha1.New
		case "HMAC-SHA256":
			h = sha256.New
		default:
			fmt.Fprintf(w, "bad|method %q", oauth["oauth_signature_method"])
			return
		}
		m := hmac.New(h, []byte(enc(consumerSecret)+"&"+enc(tokenSecret)))
		m.Write([]byte(base))
		if want := base64.StdEncoding.EncodeToString(m.Sum(nil)); want != sig {
			fmt.Fprintf(w, "bad|signature %q, want %q, base %s", sig, want, base)
			return
		}
		fmt.Fprintf(w, "ok|%s|%s|%s", where, oauth["oauth_signature_method"], oauth["oauth_token"])
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSendOAuth1 (AUTH-15): requests with an oauth1 auth, inherited from a
// folder, with its secrets typed (kept on this computer) and a
// {{variable}} of a secret environment variable, are signed at send time;
// a server that checks the signature on its own accepts them in the
// header, the query string and a form body, with query and form
// parameters signed. The secrets and the signature are masked in the
// timeline and the copied curl command.
func TestSendOAuth1(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	srv := oauthServer(t, "c0nsumer secret", "t&ken+secret")
	env, err := f.envs.Create("dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.envs.SaveVariables(env.ID, []environments.Variable{
		{Name: "tokenSecret", Value: "t&ken+secret", Secret: true},
		{Name: "token", Value: "access-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.envs.SetActive(env.ID); err != nil {
		t.Fatal(err)
	}
	folder := func(in, method string) string {
		return `{"kind": "folder", "id": "fld_api", "name": "API", "auth": {"type": "oauth1", "consumerKey": "ck", "consumerSecret": "c0nsumer secret", "accessToken": "{{token}}", "tokenSecret": "{{tokenSecret}}", "signatureMethod": "` + method + `", "realm": "Photos", "in": "` + in + `"}}`
	}
	f.write(t, "api/get.json", `{"kind": "http", "id": "req_get", "name": "Get", "method": "GET", "url": "`+srv.URL+`/photos/a b?size=original&tag=x&tag=%C3%A9&empty=", "auth": {"type": "inherit"}}`)
	f.write(t, "api/post.json", `{"kind": "http", "id": "req_post", "name": "Post", "method": "POST", "url": "`+srv.URL+`/photos?q=1", "body": {"mode": "urlencoded", "fields": [{"name": "title", "value": "a+b & c"}, {"name": "tag", "value": "x"}, {"name": "tag", "value": "y"}]}, "auth": {"type": "inherit"}}`)

	send := func(id string) string {
		t.Helper()
		started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: id})
		if err != nil {
			t.Fatal(err)
		}
		if done := f.rec.wait(t, started.OpID); done.Status != operation.StatusOK {
			t.Fatalf("done = %+v", done)
		}
		return f.rec.response(t, started.OpID).Body
	}
	for _, c := range []struct{ in, method string }{
		{"header", "HMAC-SHA1"}, {"query", "HMAC-SHA256"}, {"body", "HMAC-SHA1"},
	} {
		f.write(t, "api/_folder.json", folder(c.in, c.method))
		f.reload(t)
		for _, id := range []string{"req_get", "req_post"} {
			if c.in == "body" && id == "req_get" {
				continue
			}
			if got, want := send(id), "ok|"+c.in+"|"+c.method+"|access-1"; got != want {
				t.Errorf("%s in %s: server said %s", id, c.in, got)
			}
		}
	}

	// Masked: the timeline and curl show neither the secrets nor the
	// signature.
	f.write(t, "api/_folder.json", folder("query", "HMAC-SHA1"))
	f.reload(t)
	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_get"})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.wait(t, started.OpID)
	resp := f.rec.response(t, started.OpID)
	hops := resp.Timeline.Hops
	sent := hops[len(hops)-1].Request
	if !strings.Contains(sent.URL, "&oauth_consumer_key=ck&") || !strings.HasSuffix(sent.URL, "&oauth_signature="+httpclient.Mask) || !resp.Timeline.Masked {
		t.Errorf("timeline URL = %s", sent.URL)
	}
	c, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_get"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Masked || strings.Contains(c.Command, "c0nsumer") || !strings.Contains(c.Command, "oauth_signature="+httpclient.Mask) {
		t.Errorf("curl = %s", c.Command)
	}
	c, err = f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_get", IncludeSecrets: true})
	if err != nil || !strings.Contains(c.Command, "oauth_signature=") {
		t.Errorf("curl with secrets = %s, %v", c.Command, err)
	}
}
