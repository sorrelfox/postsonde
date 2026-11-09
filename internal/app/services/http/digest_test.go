package http

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// AUTH-06: a Digest request answers the challenge; the timeline shows
// both hops, the challenged one first, with the response hash masked, and
// the challenge does not count as a redirect.
func TestSendDigest(t *testing.T) {
	t.Parallel()
	h := func(s string) string {
		sum := md5.Sum([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	var response string
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		p := map[string]string{}
		for part := range strings.SplitSeq(strings.TrimPrefix(r.Header.Get("Authorization"), "Digest "), ", ") {
			k, v, _ := strings.Cut(part, "=")
			p[k] = strings.Trim(v, `"`)
		}
		ha1 := h("zoe:r:dev-secret")
		ha2 := h(r.Method + ":" + p["uri"])
		if want := h(ha1 + ":n1:" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2); p["response"] == want {
			response = want
			io.WriteString(w, "ok")
			return
		}
		w.Header().Set("WWW-Authenticate", `Digest realm="r", nonce="n1", qop="auth"`)
		w.WriteHeader(nethttp.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	ws, store, dev, _ := envWorkspace(t, srv.URL)
	if err := store.SetActive(dev); err != nil {
		t.Fatal(err)
	}
	src := httpSource("{{base}}/p")
	src.Auth = projectfile.Auth{Type: projectfile.AuthDigest, Username: "zoe", Password: "{{token}}"}
	src.Workspace = ws
	f := newFixture(t, src, nil)

	id := f.send(t)
	if done := f.rec.wait(t, id); done.Status != operation.StatusOK {
		t.Fatalf("done = %+v", done)
	}
	resp := f.rec.response(t, id)
	if resp.StatusCode != 200 || resp.Redirects != 0 || response == "" {
		t.Fatalf("status %d, redirects %d", resp.StatusCode, resp.Redirects)
	}
	tl := resp.Timeline
	if len(tl.Hops) != 2 || tl.Hops[0].Kind != HopAuth || tl.Hops[0].StatusCode != 401 || tl.Hops[1].Kind != HopFinal || !tl.Masked {
		t.Fatalf("hops = %+v", tl.Hops)
	}
	head := tl.Hops[1].Request.Head
	if strings.Contains(head, response) || !strings.Contains(head, `, response="`+httpclient.Mask+`"`) || !strings.Contains(head, `Authorization: Digest username="zoe", realm="r", uri="/p"`) {
		t.Errorf("final head:\n%s", head)
	}
	if strings.Contains(tl.Hops[0].Request.Head, "Authorization") {
		t.Errorf("challenged head:\n%s", tl.Hops[0].Request.Head)
	}
	found := false
	for _, e := range tl.Events {
		found = found || e.Name == httpclient.EventAuthChallenge
	}
	if !found {
		t.Errorf("no auth_challenge event")
	}
}
