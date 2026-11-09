package httpclient

import (
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Timeline events of challenge-response auth (Request.Digest).
const (
	// EventAuthChallenge: a hop was answered with a 401 Digest challenge,
	// and the request is sent again with credentials. Detail: the status,
	// algorithm and qop ("401 Digest SHA-256, qop auth").
	EventAuthChallenge = "auth_challenge"
	// EventAuthUnanswered: a 401 was returned as it is although the
	// request has Digest credentials. Detail: why (no Digest challenge,
	// an unsupported algorithm, credentials rejected, another origin, a
	// body that cannot be sent again).
	EventAuthUnanswered = "auth_unanswered"
)

// Kinds of Hop.
const (
	// HopRedirect: the hop was answered with a redirect that Send followed.
	HopRedirect = "redirect"
	// HopAuth: the hop was answered with a 401 challenge that Send
	// answered by sending the request again with credentials.
	HopAuth = "auth"
)

// drainLimit is how much of a challenge response's body is read before
// the connection is given back; a longer one closes the connection.
const drainLimit = 256 << 10

// digestSession is what one Send remembers of the Digest challenges it
// answered: the last challenge of each origin, to answer the next hop to
// that origin (a same-origin redirect) without waiting for a new 401, and
// the nonce count of each nonce.
type digestSession struct {
	mu     sync.Mutex
	last   map[string]*digestChallenge
	counts map[string]uint32
}

func (s *digestSession) remember(origin string, c *digestChallenge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[string]*digestChallenge{}
	}
	s.last[origin] = c
}

func (s *digestSession) lastFor(origin string) *digestChallenge {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last[origin]
}

// next returns the nonce count of the next request with nonce: 1 for its
// first use.
func (s *digestSession) next(nonce string) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = map[string]uint32{}
	}
	s.counts[nonce]++
	return s.counts[nonce]
}

// authTransport is the outermost RoundTripper of the http.Client: it sees
// every hop, before cookieTransport. For a request with Digest credentials
// (sendState.digest) it answers a 401 Digest challenge by sending the hop
// again with an Authorization field, within the same RoundTrip, so
// net/http's redirect handling sees only the answered response. Both
// requests are recorded as hops (Hop.Kind HopAuth for the challenged one)
// and go through the same context: the dial guard, cancellation, timeouts
// and traces apply to each.
//
//   - A request that has an Authorization field (a typed header, or URL
//     userinfo) is sent as it is: explicit credentials win.
//   - Only the request's origin is answered, as for Authorization on
//     redirects (PRV-11), unless Settings.ForwardAuthorization is set.
//   - A challenge is answered once. A second 401 is returned as it is,
//     unless its challenge says stale=true (the nonce expired, not the
//     credentials): then it is answered once more with the new nonce.
//   - After a challenge, later hops to the same origin in the same Send
//     are sent with credentials straight away, the nonce count increased.
type authTransport struct {
	base http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	st, _ := req.Context().Value(stateKey{}).(*sendState)
	if st == nil || st.digest == nil || hasAuthorization(req.Header) {
		return t.base.RoundTrip(req)
	}
	allowed := st.settings.ForwardAuthorization || st.origin == nil || sameOrigin(st.origin, req.URL)
	origin := originKey(req)
	send := req
	if c := st.digestSession.lastFor(origin); c != nil && allowed {
		// Credentials an earlier hop was challenged for; a 401 to them is
		// answered like a first challenge.
		if r, err := t.authorize(req, c, st); err == nil && r != nil {
			send = r
		}
	}
	answered := 0
	for {
		resp, err := t.base.RoundTrip(send)
		if err != nil || resp.StatusCode != http.StatusUnauthorized {
			return resp, err
		}
		unanswered := func(reason string) (*http.Response, error) {
			if st.rec != nil {
				st.rec.record(EventAuthUnanswered, reason, nil)
			}
			return resp, nil
		}
		if !allowed {
			return unanswered("the request was redirected to another origin")
		}
		c, reason := pickDigest(resp.Header)
		if c == nil {
			return unanswered(reason)
		}
		if answered >= 2 || (answered == 1 && !c.stale) {
			return unanswered("credentials rejected")
		}
		next, err := t.authorize(req, c, st)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		if next == nil {
			return unanswered("the request body cannot be sent again")
		}
		st.digestSession.remember(origin, c)
		if st.rec != nil {
			st.rec.authChallenge(resp, "401 Digest "+c.describe())
		}
		drain(resp.Body)
		answered++
		send = next
	}
}

// authorize returns a copy of req, with a fresh body, carrying the
// answer to c. It returns nil and no error when the body cannot be read
// again.
func (t *authTransport) authorize(req *http.Request, c *digestChallenge, st *sendState) (*http.Request, error) {
	r := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, nil
		}
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		r.Body = body
	}
	in := digestInput{method: req.Method, uri: req.URL.RequestURI(), nc: st.digestSession.next(c.nonce), cnonce: st.cnonce()}
	if c.qop == "auth-int" {
		sum, err := bodyHash(req, c)
		if err != nil {
			if r.Body != nil {
				r.Body.Close()
			}
			return nil, err
		}
		in.bodyHash = sum
	}
	r.Header["Authorization"] = []string{c.authorization(*st.digest, in)}
	return r, nil
}

// bodyHash returns H(entity-body) of req for qop auth-int, reading the
// body once more; authorize has checked that it can.
func bodyHash(req *http.Request, c *digestChallenge) (string, error) {
	h := c.newHash()
	if req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()
		if err != nil {
			return "", err
		}
		defer body.Close()
		if _, err := io.Copy(h, body); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// drain reads what is left of a challenge response's body, up to
// drainLimit, so the connection can be reused, and closes it.
func drain(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, drainLimit))
	body.Close()
}

func hasAuthorization(h http.Header) bool {
	for name := range h {
		if strings.EqualFold(name, "Authorization") {
			return true
		}
	}
	return false
}

// originKey is req's scheme, host and port, for digestSession.
func originKey(req *http.Request) string {
	return strings.ToLower(req.URL.Scheme) + "://" + strings.ToLower(req.URL.Hostname()) + ":" + portOf(req.URL)
}
