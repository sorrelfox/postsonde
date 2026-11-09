package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// awsEncode is SigV4's URI encoding, written again for the test servers.
func awsEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSum(h func() hash.Hash, key []byte, data string) []byte {
	m := hmac.New(h, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// awsServer verifies AWS Signature Version 4 (not S3) on its own, from
// the request as received: the Authorization header or the X-Amz-* query
// parameters, the raw request target, the signed headers as received and
// the body. It answers "ok|<where>|<token>" or "bad|<reason>".
func awsServer(t *testing.T, secretKey string) *httptest.Server {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		body, _ := io.ReadAll(r.Body)
		rawPath, rawQuery, _ := strings.Cut(r.RequestURI, "?")
		q, _ := url.ParseQuery(rawQuery)
		var credential, signedHeaders, signature, amzDate, where, payload string
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "AWS4-HMAC-SHA256 ") {
			where = "header"
			for part := range strings.SplitSeq(strings.TrimPrefix(h, "AWS4-HMAC-SHA256 "), ", ") {
				k, v, _ := strings.Cut(part, "=")
				switch k {
				case "Credential":
					credential = v
				case "SignedHeaders":
					signedHeaders = v
				case "Signature":
					signature = v
				}
			}
			amzDate = r.Header.Get("X-Amz-Date")
			sum := sha256.Sum256(body)
			payload = hex.EncodeToString(sum[:])
		} else {
			where = "query"
			credential, signedHeaders, signature, amzDate = q.Get("X-Amz-Credential"), q.Get("X-Amz-SignedHeaders"), q.Get("X-Amz-Signature"), q.Get("X-Amz-Date")
			sum := sha256.Sum256(body)
			payload = hex.EncodeToString(sum[:])
		}
		if v := r.Header.Get("X-Amz-Content-Sha256"); v != "" {
			payload = v
		}
		var pairs []string
		for k, vs := range q {
			if k == "X-Amz-Signature" {
				continue
			}
			for _, v := range vs {
				pairs = append(pairs, awsEncode(k)+"="+awsEncode(v))
			}
		}
		sort.Strings(pairs)
		var segs []string
		for s := range strings.SplitSeq(rawPath, "/") {
			segs = append(segs, awsEncode(s))
		}
		var canonHeaders strings.Builder
		for name := range strings.SplitSeq(signedHeaders, ";") {
			v := strings.Join(r.Header.Values(name), ",")
			if name == "host" {
				v = r.Host
			}
			canonHeaders.WriteString(name + ":" + strings.Join(strings.Fields(v), " ") + "\n")
		}
		creq := strings.Join([]string{r.Method, strings.Join(segs, "/"), strings.Join(pairs, "&"), canonHeaders.String(), signedHeaders, payload}, "\n")
		parts := strings.Split(credential, "/")
		if len(parts) != 5 {
			fmt.Fprintf(w, "bad|credential %q", credential)
			return
		}
		scope := strings.Join(parts[1:], "/")
		creqSum := sha256.Sum256([]byte(creq))
		sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(creqSum[:])
		k := hmacSum(sha256.New, []byte("AWS4"+secretKey), parts[1])
		for _, p := range parts[2:] {
			k = hmacSum(sha256.New, k, p)
		}
		if want := hex.EncodeToString(hmacSum(sha256.New, k, sts)); want != signature {
			fmt.Fprintf(w, "bad|signature %s, want %s\n%s", signature, want, creq)
			return
		}
		token := r.Header.Get("X-Amz-Security-Token")
		if where == "query" {
			token = q.Get("X-Amz-Security-Token")
		}
		fmt.Fprintf(w, "ok|%s|%s", where, token)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// hawkServer verifies Hawk (sha256) on its own, from the request as
// received, with the payload hash when the header has one. It answers
// "ok|<id>|<hashed>" or "bad|<reason>".
func hawkServer(t *testing.T, key string) *httptest.Server {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Hawk ") {
			fmt.Fprintf(w, "bad|no Hawk header %q", h)
			return
		}
		attrs := map[string]string{}
		for part := range strings.SplitSeq(strings.TrimPrefix(h, "Hawk "), ", ") {
			k, v, _ := strings.Cut(part, "=")
			attrs[k] = strings.Trim(v, `"`)
		}
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil {
			fmt.Fprintf(w, "bad|host %q", r.Host)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if attrs["hash"] != "" {
			ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
			sum := sha256.Sum256([]byte("hawk.1.payload\n" + strings.ToLower(strings.TrimSpace(ct)) + "\n" + string(body) + "\n"))
			if got := base64.StdEncoding.EncodeToString(sum[:]); got != attrs["hash"] {
				fmt.Fprintf(w, "bad|hash %s, want %s", attrs["hash"], got)
				return
			}
		}
		norm := "hawk.1.header\n" + attrs["ts"] + "\n" + attrs["nonce"] + "\n" + r.Method + "\n" + r.RequestURI + "\n" +
			strings.ToLower(host) + "\n" + port + "\n" + attrs["hash"] + "\n" + attrs["ext"] + "\n"
		if want := base64.StdEncoding.EncodeToString(hmacSum(sha256.New, []byte(key), norm)); want != attrs["mac"] {
			fmt.Fprintf(w, "bad|mac %s, want %s\n%s", attrs["mac"], want, norm)
			return
		}
		fmt.Fprintf(w, "ok|%s|%v", attrs["id"], attrs["hash"] != "")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSendAWSV4AndHawk (AUTH-17, AUTH-18): requests with an awsv4 or hawk
// auth inherited from a folder, with a typed secret (kept on this
// computer) and a {{variable}} of a secret environment variable, are
// signed at send time; servers that verify the signature on their own
// accept them, in the header and as a presigned URL, with a query, a path
// with a space and a body. The secrets and signatures are masked in the
// timeline and the copied curl command.
func TestSendAWSV4AndHawk(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	aws := awsServer(t, "aws/s3cret+key")
	hawkSrv := hawkServer(t, "hawk-s3cret-key")
	env, err := f.envs.Create("dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.envs.SaveVariables(env.ID, []environments.Variable{
		{Name: "sessionToken", Value: "sess/t0ken+=", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.envs.SetActive(env.ID); err != nil {
		t.Fatal(err)
	}
	f.write(t, "aws/_folder.json", `{"kind": "folder", "id": "fld_aws", "name": "AWS", "auth": {"type": "awsv4", "accessKey": "AKID", "secretKey": "aws/s3cret+key", "sessionToken": "{{sessionToken}}", "region": "us-east-1", "service": "execute-api"}}`)
	f.write(t, "aws/get.json", `{"kind": "http", "id": "req_aws_get", "name": "Get", "method": "GET", "url": "`+aws.URL+`/items/a b?b=2&a=1&a=0&e=", "auth": {"type": "inherit"}}`)
	f.write(t, "aws/post.json", `{"kind": "http", "id": "req_aws_post", "name": "Post", "method": "POST", "url": "`+aws.URL+`/items", "headers": [{"name": "X-Custom", "value": "  a   b "}], "body": {"mode": "raw", "type": "json", "text": "{\"n\": 1}"}, "auth": {"type": "inherit"}}`)
	f.write(t, "hawk/_folder.json", `{"kind": "folder", "id": "fld_hawk", "name": "Hawk", "auth": {"type": "hawk", "hawkId": "dh37fgj492je", "hawkKey": "hawk-s3cret-key", "ext": "app-data", "payloadHash": true}}`)
	f.write(t, "hawk/post.json", `{"kind": "http", "id": "req_hawk", "name": "Hawk", "method": "POST", "url": "`+hawkSrv.URL+`/resource/1?b=1&a=2", "body": {"mode": "raw", "type": "text", "text": "Thank you for flying Hawk"}, "auth": {"type": "inherit"}}`)
	f.reload(t)

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
	for _, id := range []string{"req_aws_get", "req_aws_post"} {
		if got := send(id); got != "ok|header|sess/t0ken+=" {
			t.Errorf("%s: server said %s", id, got)
		}
	}
	if got := send("req_hawk"); got != "ok|dh37fgj492je|true" {
		t.Errorf("hawk: server said %s", got)
	}

	// Presigned: the X-Amz-* parameters in the query.
	f.write(t, "aws/_folder.json", `{"kind": "folder", "id": "fld_aws", "name": "AWS", "auth": {"type": "awsv4", "accessKey": "AKID", "secretKey": "aws/s3cret+key", "sessionToken": "{{sessionToken}}", "region": "us-east-1", "service": "execute-api", "in": "query", "expires": 60}}`)
	f.reload(t)
	if got := send("req_aws_get"); got != "ok|query|sess/t0ken+=" {
		t.Errorf("presigned: server said %s", got)
	}

	// Masked: the timeline and curl show neither the secrets nor the
	// signature.
	started, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_aws_get"})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.wait(t, started.OpID)
	resp := f.rec.response(t, started.OpID)
	hops := resp.Timeline.Hops
	sent := hops[len(hops)-1].Request
	if !strings.Contains(sent.URL, "X-Amz-Security-Token="+httpclient.Mask) || !strings.HasSuffix(sent.URL, "&X-Amz-Signature="+httpclient.Mask) || !resp.Timeline.Masked {
		t.Errorf("timeline URL = %s", sent.URL)
	}
	for _, id := range []string{"req_aws_post", "req_hawk"} {
		c, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: id})
		if err != nil {
			t.Fatal(err)
		}
		wantMasked := "X-Amz-Signature=" + httpclient.Mask
		if id == "req_hawk" {
			wantMasked = "Authorization: Hawk " + httpclient.Mask
		}
		if !c.Masked || strings.Contains(c.Command, "s3cret") || strings.Contains(c.Command, "t0ken") || strings.Contains(c.Command, "mac=") || !strings.Contains(c.Command, wantMasked) {
			t.Errorf("curl = %s", c.Command)
		}
	}
}

// ExportHAR masks the AWS secret key, session token and signature, and
// the Hawk MAC, like every other secret.
func TestExportHARMasksAWSAndHawk(t *testing.T) {
	t.Parallel()
	for _, auth := range []projectfile.Auth{
		{Type: projectfile.AuthAWSV4, AccessKey: "AKID", SecretKey: "aws-s3cret", SessionToken: "t0ken/x", Region: "us-east-1", Service: "s3", In: "query"},
		{Type: projectfile.AuthAWSV4, AccessKey: "AKID", SecretKey: "aws-s3cret", SessionToken: "t0ken/x", Region: "us-east-1", Service: "s3"},
		{Type: projectfile.AuthHawk, HawkID: "id", HawkKey: "hawk-s3cret", Ext: "t0ken"},
	} {
		src := httpSource("https://api.test/items")
		src.Auth = auth
		path := filepath.Join(t.TempDir(), "out.har")
		pick := func(context.Context, SaveDialog) (string, error) { return path, nil }
		f := newFixture(t, src, nil, WithSaveBody("", pick))
		res, err := f.svc.ExportHAR(t.Context(), ExportHARInputDTO{Name: "x", RequestIDs: []string{"req_1"}})
		if err != nil {
			t.Fatal(err)
		}
		data := readFile(t, path)
		built, err := f.svc.CurlCommand(t.Context(), CurlInputDTO{RequestID: "req_1", IncludeSecrets: true})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Masked || strings.Contains(data, "s3cret") || strings.Contains(data, "t0ken/x") || strings.Contains(data, "t0ken%2Fx") {
			t.Errorf("%s: HAR = %s", auth.Type, data)
		}
		if auth.Type == projectfile.AuthHawk && !strings.Contains(data, `"Hawk `+httpclient.Mask+`"`) {
			t.Errorf("hawk header not masked: %s", data)
		}
		if auth.In == "query" && (!strings.Contains(data, "X-Amz-Signature="+httpclient.Mask) || !strings.Contains(built.Command, "X-Amz-Signature=")) {
			t.Errorf("presigned: HAR %s\ncurl %s", data, built.Command)
		}
	}
}
