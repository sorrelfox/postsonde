package httpclient

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// RFC 7616 section 3.9.1: the same credentials answered with MD5 and
// SHA-256.
func TestDigestRFC7616Example(t *testing.T) {
	cred := DigestCredentials{Username: "Mufasa", Password: "Circle of Life"}
	in := digestInput{method: "GET", uri: "/dir/index.html", nc: 1, cnonce: "f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ"}
	for _, tc := range []struct{ algorithm, want string }{
		{"MD5", "8ca523f5e9506fed4657c9700eebdbec"},
		{"SHA-256", "753927fa0e85d155564e2e272a28d1802ca10daf4496794697cf8db5856cb6c1"},
	} {
		h := http.Header{}
		h.Add("WWW-Authenticate", `Digest realm="http-auth@example.org", qop="auth, auth-int", algorithm=`+tc.algorithm+`, nonce="7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v", opaque="FQhe/qaU925kfnzjCev0ciny7QMkPqMAFRtzCUYo5tdS"`)
		c, reason := pickDigest(h)
		if c == nil {
			t.Fatalf("%s: no challenge: %s", tc.algorithm, reason)
		}
		if c.qop != "auth" {
			t.Errorf("%s: qop = %q, want auth", tc.algorithm, c.qop)
		}
		if got := c.response(cred, in); got != tc.want {
			t.Errorf("%s: response = %s, want %s", tc.algorithm, got, tc.want)
		}
		got := c.authorization(cred, in)
		want := `Digest username="Mufasa", realm="http-auth@example.org", uri="/dir/index.html", algorithm=` + tc.algorithm +
			`, nonce="7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v", nc=00000001, cnonce="f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ", qop=auth, response="` +
			tc.want + `", opaque="FQhe/qaU925kfnzjCev0ciny7QMkPqMAFRtzCUYo5tdS"`
		if got != want {
			t.Errorf("%s: authorization =\n%s\nwant\n%s", tc.algorithm, got, want)
		}
	}
}

// RFC 2617 section 3.5: a challenge with qop, MD5 by default.
func TestDigestRFC2617Example(t *testing.T) {
	h := http.Header{"Www-Authenticate": {`Digest realm="testrealm@host.com", qop="auth,auth-int", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093", opaque="5ccc069c403ebaf9f0171e9517f40e41"`}}
	c, _ := pickDigest(h)
	if c == nil {
		t.Fatal("no challenge")
	}
	got := c.response(DigestCredentials{Username: "Mufasa", Password: "Circle Of Life"},
		digestInput{method: "GET", uri: "/dir/index.html", nc: 1, cnonce: "0a4f113b"})
	if want := "6629fae49393a05397450978507c4ef1"; got != want {
		t.Errorf("response = %s, want %s", got, want)
	}
	if a := c.authorization(DigestCredentials{Username: "Mufasa"}, digestInput{method: "GET", uri: "/", nc: 1, cnonce: "x"}); strings.Contains(a, "algorithm=") {
		t.Errorf("algorithm sent although the challenge had none: %s", a)
	}
}

func TestParseChallenges(t *testing.T) {
	got := parseChallenges([]string{
		`Basic realm="x", Digest realm="a, \"b\"", nonce="n1", qop="auth,auth-int",algorithm=SHA-256`,
		`Negotiate abc==, Digest nonce=n2`,
	})
	if len(got) != 4 {
		t.Fatalf("got %d challenges, want 4: %+v", len(got), got)
	}
	if got[0].scheme != "Basic" || got[0].params["realm"] != "x" {
		t.Errorf("first = %+v", got[0])
	}
	d := got[1]
	if d.scheme != "Digest" || d.params["realm"] != `a, "b"` || d.params["nonce"] != "n1" || d.params["qop"] != "auth,auth-int" || d.params["algorithm"] != "SHA-256" {
		t.Errorf("digest = %+v", d)
	}
	if got[2].scheme != "Negotiate" || got[3].scheme != "Digest" || got[3].params["nonce"] != "n2" {
		t.Errorf("rest = %+v", got[2:])
	}
}

func TestPickDigest(t *testing.T) {
	h := http.Header{"Www-Authenticate": {
		`Digest realm="r", nonce="a", algorithm=MD5, qop="auth"`,
		`Digest realm="r", nonce="b", algorithm=SHA-256, qop="auth"`,
		`Digest realm="r", nonce="c", algorithm=UNKNOWN-9, qop="auth"`,
	}}
	c, _ := pickDigest(h)
	if c == nil || c.nonce != "b" {
		t.Fatalf("picked %+v, want the SHA-256 challenge", c)
	}
	for _, tc := range []struct{ header, reason string }{
		{`Basic realm="r"`, "no Digest challenge"},
		{`Digest realm="r", nonce="a", algorithm=SHA-1`, `Digest algorithm "SHA-1" not supported`},
		{`Digest realm="r", nonce="a", qop="auth-conf"`, `Digest qop "auth-conf" not supported`},
		{`Digest realm="r"`, "Digest challenge without nonce"},
	} {
		c, reason := pickDigest(http.Header{"Www-Authenticate": {tc.header}})
		if c != nil || reason != tc.reason {
			t.Errorf("%s: got %+v, %q; want nil, %q", tc.header, c, reason, tc.reason)
		}
	}
	c, _ = pickDigest(http.Header{"Www-Authenticate": {`Digest realm="r", nonce="a", qop="auth-int"`}})
	if c == nil || c.qop != "auth-int" {
		t.Errorf("auth-int only: %+v", c)
	}
}

func TestDigestUsernameEncoding(t *testing.T) {
	c := &digestChallenge{realm: "api@example.org", nonce: "n", qop: "auth", newHash: sha256.New, algorithm: "SHA-256"}
	a := c.authorization(DigestCredentials{Username: "Jäsøn Doe", Password: "p"}, digestInput{method: "GET", uri: "/", nc: 1, cnonce: "c"})
	if !strings.Contains(a, `username*=UTF-8''J%C3%A4s%C3%B8n%20Doe,`) {
		t.Errorf("non-ASCII username not sent as username*: %s", a)
	}
	a = c.authorization(DigestCredentials{Username: `a"b\c`, Password: "p"}, digestInput{method: "GET", uri: "/", nc: 1, cnonce: "c"})
	if !strings.Contains(a, `username="a\"b\\c",`) {
		t.Errorf("username not escaped: %s", a)
	}
	c.userhash = true
	a = c.authorization(DigestCredentials{Username: "Jäsøn Doe", Password: "p"}, digestInput{method: "GET", uri: "/", nc: 1, cnonce: "c"})
	sum := sha256.Sum256([]byte("Jäsøn Doe:api@example.org"))
	if !strings.Contains(a, `username="`+hex.EncodeToString(sum[:])+`"`) || !strings.HasSuffix(a, ", userhash=true") {
		t.Errorf("userhash: %s", a)
	}
}

func TestMaskDigestAuthorization(t *testing.T) {
	m := NewSecretMasker([]string{"s3cret"})
	v := `Digest username="bob", realm="r", uri="/", nonce="n", nc=00000001, cnonce="c", qop=auth, response="0123abcd", opaque="o"`
	want := `Digest username="bob", realm="r", uri="/", nonce="n", nc=00000001, cnonce="c", qop=auth, response="` + Mask + `", opaque="o"`
	if got := m.MaskHeader("authorization", v); got != want {
		t.Errorf("masked =\n%s\nwant\n%s", got, want)
	}
	if got := m.MaskHeader("Authorization", `Digest username="s3cret", response=abc`); got != `Digest username="`+Mask+`", response="`+Mask+`"` {
		t.Errorf("masked = %s", got)
	}
}

// digestServer is an in-process Digest server: it challenges every
// request without valid credentials and checks the answer with its own
// computation (RFC 7616 section 3.4.1).
type digestServer struct {
	username, password, realm string
	// algorithm is sent in the challenge; "" sends none (MD5).
	algorithm string
	// qop is the challenge's qop list; "" sends none (RFC 2617).
	qop      string
	userhash bool
	// staleFirst answers the first valid credentials with a stale=true
	// challenge and a new nonce.
	staleFirst bool

	mu      sync.Mutex
	nonces  int
	counts  map[string]int // last nc per nonce
	stale   bool
	auths   []string // Authorization of each request, "" for none
	bodies  []string
	answers int
}

func (s *digestServer) hash() func() hash.Hash {
	switch strings.TrimSuffix(strings.ToUpper(s.algorithm), "-SESS") {
	case "SHA-256":
		return sha256.New
	case "SHA-512-256":
		return sha512.New512_256
	}
	return md5.New
}

func (s *digestServer) h(parts ...string) string {
	h := s.hash()()
	io.WriteString(h, strings.Join(parts, ":"))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *digestServer) challenge(w http.ResponseWriter, stale bool) {
	s.nonces++
	nonce := "nonce-" + strconv.Itoa(s.nonces)
	v := `Digest realm="` + s.realm + `", nonce="` + nonce + `", opaque="opq"`
	if s.algorithm != "" {
		v += ", algorithm=" + s.algorithm
	}
	if s.qop != "" {
		v += `, qop="` + s.qop + `"`
	}
	if s.userhash {
		v += ", userhash=true"
	}
	if stale {
		v += ", stale=true"
	}
	w.Header().Add("WWW-Authenticate", `Basic realm="`+s.realm+`"`)
	w.Header().Add("WWW-Authenticate", v)
	w.WriteHeader(http.StatusUnauthorized)
	io.WriteString(w, "unauthorized")
}

func (s *digestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	auth := r.Header.Get("Authorization")
	s.auths = append(s.auths, auth)
	s.bodies = append(s.bodies, string(body))
	if s.counts == nil {
		s.counts = map[string]int{}
	}
	if !s.valid(r, auth, body) {
		s.challenge(w, false)
		return
	}
	if s.staleFirst && !s.stale {
		s.stale = true
		s.challenge(w, true)
		return
	}
	s.answers++
	io.WriteString(w, "welcome")
}

func (s *digestServer) valid(r *http.Request, auth string, body []byte) bool {
	scheme, _, _ := strings.Cut(auth, " ")
	if !strings.EqualFold(scheme, "Digest") {
		return false
	}
	cs := parseChallenges([]string{auth})
	if len(cs) != 1 {
		return false
	}
	p := cs[0].params
	nonce := p["nonce"]
	if n, err := strconv.Atoi(strings.TrimPrefix(nonce, "nonce-")); err != nil || n != s.nonces {
		return false // only the latest nonce is valid
	}
	user := s.username
	if s.userhash {
		user = s.h(s.username, s.realm)
		if p["userhash"] != "true" {
			return false
		}
	}
	if p["username"] != user || p["realm"] != s.realm || p["uri"] != r.URL.RequestURI() || p["opaque"] != "opq" {
		return false
	}
	if p["algorithm"] != s.algorithm {
		return false
	}
	ha1 := s.h(s.username, s.realm, s.password)
	if strings.HasSuffix(strings.ToUpper(s.algorithm), "-SESS") {
		ha1 = s.h(ha1, nonce, p["cnonce"])
	}
	qop := p["qop"]
	ha2 := s.h(r.Method, p["uri"])
	if qop == "auth-int" {
		ha2 = s.h(r.Method, p["uri"], s.h(string(body)))
	}
	var want string
	if s.qop == "" {
		if qop != "" || p["nc"] != "" {
			return false
		}
		want = s.h(ha1, nonce, ha2)
	} else {
		if !strings.Contains(s.qop, qop) || qop == "" {
			return false
		}
		nc, err := strconv.ParseInt(p["nc"], 16, 32)
		if err != nil || len(p["nc"]) != 8 || int(nc) <= s.counts[nonce] || p["cnonce"] == "" {
			return false // a replayed or malformed nonce count
		}
		s.counts[nonce] = int(nc)
		want = s.h(ha1, nonce, p["nc"], p["cnonce"], qop, ha2)
	}
	return p["response"] == want
}

func (s *digestServer) requests() (auths, bodies []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths...), append([]string(nil), s.bodies...)
}

func newDigestServer(t *testing.T, s *digestServer) *httptest.Server {
	t.Helper()
	if s.username == "" {
		s.username, s.password, s.realm = "Mufasa", "Circle of Life", "http-auth@example.org"
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv
}

func digestPost(url, body string, cred DigestCredentials) Request {
	return Request{Method: http.MethodPost, URL: url, Body: RawBody{Data: []byte(body), Type: "text/plain"}, Settings: DefaultSettings(), Digest: &cred}
}

// Every algorithm and qop variant authenticates (AUTH-06).
func TestDigestRoundTrip(t *testing.T) {
	c := newTestClient(t, TransportOptions{})
	cred := DigestCredentials{Username: "Mufasa", Password: "Circle of Life"}
	for _, alg := range []string{"", "MD5", "MD5-sess", "SHA-256", "SHA-256-sess", "SHA-512-256", "SHA-512-256-sess"} {
		for _, qop := range []string{"auth", "auth-int", "auth,auth-int", ""} {
			for _, userhash := range []bool{false, true} {
				name := "alg=" + alg + " qop=" + qop + " userhash=" + strconv.FormatBool(userhash)
				t.Run(name, func(t *testing.T) {
					s := &digestServer{algorithm: alg, qop: qop, userhash: userhash}
					srv := newDigestServer(t, s)
					resp, err := c.Send(context.Background(), digestPost(srv.URL+"/dir/index.html?x=1", `{"a":1}`, cred))
					if err != nil {
						t.Fatal(err)
					}
					if resp.StatusCode != 200 || string(resp.Body) != "welcome" {
						t.Fatalf("status %d, body %q", resp.StatusCode, resp.Body)
					}
					auths, bodies := s.requests()
					if len(auths) != 2 || auths[0] != "" || auths[1] == "" {
						t.Fatalf("authorization per request = %q", auths)
					}
					if bodies[0] != `{"a":1}` || bodies[1] != `{"a":1}` {
						t.Errorf("bodies = %q, want the body sent both times", bodies)
					}
					if len(resp.Redirects) != 1 || resp.Redirects[0].Kind != HopAuth || resp.Redirects[0].StatusCode != 401 {
						t.Fatalf("hops = %+v", resp.Redirects)
					}
					if !strings.Contains(auths[1], "qop=auth,") && !strings.Contains(auths[1], "qop=auth-int,") && qop != "" {
						t.Errorf("no qop: %s", auths[1])
					}
					if qop == "auth,auth-int" && !strings.Contains(auths[1], "qop=auth,") {
						t.Errorf("auth not preferred: %s", auths[1])
					}
				})
			}
		}
	}
}

func TestDigestTimeline(t *testing.T) {
	s := &digestServer{algorithm: "SHA-256", qop: "auth"}
	srv := newDigestServer(t, s)
	c := newTestClient(t, TransportOptions{})
	c.cnonce = func() string { return "fixedcnonce" }
	resp, err := c.Send(context.Background(), Request{Method: "GET", URL: srv.URL + "/p", Settings: DefaultSettings(),
		Digest: &DigestCredentials{Username: "Mufasa", Password: "Circle of Life"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	hop := resp.Redirects[0]
	if hop.Kind != HopAuth || hop.Request == nil || hop.Status != "401 Unauthorized" {
		t.Fatalf("hop = %+v", hop)
	}
	for _, h := range hop.Request.Headers {
		if strings.EqualFold(h.Name, "Authorization") {
			t.Errorf("challenged request sent Authorization %q", h.Value)
		}
	}
	var sent string
	for _, h := range resp.Request.Headers {
		if strings.EqualFold(h.Name, "Authorization") {
			sent = h.Value
		}
	}
	if !strings.HasPrefix(sent, `Digest username="Mufasa", realm="http-auth@example.org", uri="/p", algorithm=SHA-256, nonce="nonce-1", nc=00000001, cnonce="fixedcnonce", qop=auth, response="`) {
		t.Errorf("final Authorization = %q", sent)
	}
	var challenged bool
	for _, e := range resp.Events {
		if e.Name == EventAuthChallenge {
			challenged = true
			if e.Detail != "401 Digest SHA-256, qop auth" {
				t.Errorf("challenge detail = %q", e.Detail)
			}
		}
	}
	if !challenged {
		t.Errorf("no %s event: %+v", EventAuthChallenge, resp.Events)
	}
	masked := resp.Request.Masked(NewSecretMasker([]string{"Circle of Life"}))
	for _, h := range masked.Headers {
		if strings.EqualFold(h.Name, "Authorization") && !strings.HasSuffix(h.Value, `response="`+Mask+`", opaque="opq"`) {
			t.Errorf("masked Authorization = %q", h.Value)
		}
	}
}

func TestDigestStaleNonce(t *testing.T) {
	s := &digestServer{algorithm: "MD5", qop: "auth", staleFirst: true}
	srv := newDigestServer(t, s)
	c := newTestClient(t, TransportOptions{})
	resp, err := c.Send(context.Background(), digestPost(srv.URL+"/", "x", DigestCredentials{Username: "Mufasa", Password: "Circle of Life"}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	auths, _ := s.requests()
	if len(auths) != 3 || !strings.Contains(auths[1], `nonce="nonce-1"`) || !strings.Contains(auths[2], `nonce="nonce-2"`) {
		t.Errorf("authorizations = %q", auths)
	}
	if len(resp.Redirects) != 2 || resp.Redirects[1].Kind != HopAuth {
		t.Errorf("hops = %+v", resp.Redirects)
	}
}

func TestDigestWrongPassword(t *testing.T) {
	s := &digestServer{algorithm: "SHA-256", qop: "auth"}
	srv := newDigestServer(t, s)
	c := newTestClient(t, TransportOptions{})
	resp, err := c.Send(context.Background(), digestPost(srv.URL+"/", "x", DigestCredentials{Username: "Mufasa", Password: "wrong"}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 || string(resp.Body) != "unauthorized" {
		t.Fatalf("status %d, body %q", resp.StatusCode, resp.Body)
	}
	if auths, _ := s.requests(); len(auths) != 2 {
		t.Errorf("%d requests, want 2", len(auths))
	}
	found := false
	for _, e := range resp.Events {
		if e.Name == EventAuthUnanswered && e.Detail == "credentials rejected" {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s event: %+v", EventAuthUnanswered, resp.Events)
	}
}

// A typed Authorization header wins over the Digest credentials.
func TestDigestExplicitAuthorizationWins(t *testing.T) {
	s := &digestServer{qop: "auth"}
	srv := newDigestServer(t, s)
	c := newTestClient(t, TransportOptions{})
	r := get(srv.URL+"/", on("Authorization", "Bearer x"))
	r.Digest = &DigestCredentials{Username: "Mufasa", Password: "Circle of Life"}
	resp, err := c.Send(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 || len(resp.Redirects) != 0 {
		t.Fatalf("status %d, hops %d", resp.StatusCode, len(resp.Redirects))
	}
	if auths, _ := s.requests(); len(auths) != 1 || auths[0] != "Bearer x" {
		t.Errorf("authorizations = %q", auths)
	}
}

// A same-origin redirect is answered straight away with the challenge
// already known, the nonce count increased; another origin is not
// answered.
func TestDigestRedirects(t *testing.T) {
	other := &digestServer{qop: "auth"}
	otherSrv := newDigestServer(t, other)
	s := &digestServer{qop: "auth"}
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		// Challenged like the rest, then redirected.
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, r)
		if rec.Code != 200 {
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			return
		}
		http.Redirect(w, r, "/next", http.StatusFound)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherSrv.URL+"/x", http.StatusFound)
	})
	mux.Handle("/next", s)
	s.username, s.password, s.realm = "Mufasa", "Circle of Life", "http-auth@example.org"
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newTestClient(t, TransportOptions{})
	cred := DigestCredentials{Username: "Mufasa", Password: "Circle of Life"}

	resp, err := c.Send(context.Background(), Request{Method: "GET", URL: srv.URL + "/start", Settings: DefaultSettings(), Digest: &cred})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(resp.Body) != "welcome" {
		t.Fatalf("status %d, body %q", resp.StatusCode, resp.Body)
	}
	auths, _ := s.requests()
	if len(auths) != 3 || !strings.Contains(auths[1], "nc=00000001") || !strings.Contains(auths[2], "nc=00000002") || !strings.Contains(auths[2], `uri="/next"`) {
		t.Errorf("authorizations = %q", auths)
	}
	if len(resp.Redirects) != 2 || resp.Redirects[0].Kind != HopAuth || resp.Redirects[1].Kind != HopRedirect {
		t.Errorf("hops = %+v", resp.Redirects)
	}

	resp, err = c.Send(context.Background(), Request{Method: "GET", URL: srv.URL + "/away", Settings: DefaultSettings(), Digest: &cred})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("other origin: status %d, want the 401 unanswered", resp.StatusCode)
	}
	if auths, _ := other.requests(); len(auths) != 1 || auths[0] != "" {
		t.Errorf("other origin authorizations = %q", auths)
	}

	// ForwardAuthorization answers the other origin too.
	r := Request{Method: "GET", URL: srv.URL + "/away", Settings: DefaultSettings(), Digest: &cred}
	r.Settings.ForwardAuthorization = true
	if resp, err = c.Send(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("forwarded: status %d", resp.StatusCode)
	}
}

// A 401 without a Digest challenge is returned as it is.
func TestDigestNoChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="x"`)
		w.WriteHeader(401)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, TransportOptions{})
	r := get(srv.URL)
	r.Digest = &DigestCredentials{Username: "u", Password: "p"}
	resp, err := c.Send(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 || len(resp.Redirects) != 0 {
		t.Fatalf("status %d, hops %d", resp.StatusCode, len(resp.Redirects))
	}
}
