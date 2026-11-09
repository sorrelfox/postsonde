package httpclient

// digest.go implements HTTP Digest access authentication (AUTH-06): RFC
// 7616, with RFC 2617 servers (no qop) still answered. The challenge
// round trip itself is in authTransport (authtransport.go). See
// docs/http-client.md, "Digest authentication".

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// DigestCredentials answer a Digest challenge (Request.Digest). They are
// never sent as they are: only the hashed response goes over the wire.
type DigestCredentials struct {
	Username string
	Password string
}

// Digest algorithms this client answers, strongest first. The "-sess"
// variants hash the password once more with the nonces.
var digestAlgorithms = []struct {
	name string
	new  func() hash.Hash
	rank int
}{
	{"SHA-512-256", sha512.New512_256, 3},
	{"SHA-512-256-SESS", sha512.New512_256, 3},
	{"SHA-256", sha256.New, 2},
	{"SHA-256-SESS", sha256.New, 2},
	{"MD5", md5.New, 1},
	{"MD5-SESS", md5.New, 1},
}

// digestHash returns the hash of an algorithm name (any case) and its rank,
// or nil for one this client does not know.
func digestHash(algorithm string) (func() hash.Hash, int) {
	up := strings.ToUpper(algorithm)
	for _, a := range digestAlgorithms {
		if a.name == up {
			return a.new, a.rank
		}
	}
	return nil, 0
}

// authChallenge is one challenge of a WWW-Authenticate field: the scheme
// and its parameters, names lower-cased, values unquoted.
type authChallenge struct {
	scheme string
	params map[string]string
}

// parseChallenges parses WWW-Authenticate field values (RFC 9110 section
// 11.6.1), which may hold several challenges each, separated by commas.
// A token68 challenge ("Negotiate abc==") is kept without parameters.
// Malformed parts are skipped.
func parseChallenges(values []string) []authChallenge {
	var out []authChallenge
	for _, v := range values {
		p := &authParser{s: v}
		for {
			p.skip(" \t,")
			if p.done() {
				break
			}
			scheme := p.token()
			if scheme == "" {
				p.i++ // not a token: skip the byte
				continue
			}
			c := authChallenge{scheme: scheme, params: map[string]string{}}
			p.params(c.params)
			out = append(out, c)
		}
	}
	return out
}

type authParser struct {
	s string
	i int
}

func (p *authParser) done() bool { return p.i >= len(p.s) }

func (p *authParser) skip(set string) {
	for p.i < len(p.s) && strings.IndexByte(set, p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *authParser) peek() byte {
	if p.done() {
		return 0
	}
	return p.s[p.i]
}

func (p *authParser) token() string {
	start := p.i
	for p.i < len(p.s) && isTokenChar(p.s[p.i]) {
		p.i++
	}
	return p.s[start:p.i]
}

// params reads the auth-params of a challenge into m, up to the start of
// the next challenge (a token not followed by "=") or the end.
func (p *authParser) params(m map[string]string) {
	for {
		save := p.i
		p.skip(" \t,")
		if p.done() {
			return
		}
		name := p.token()
		if name == "" {
			// A token68 ("abc==") or junk: skip to the next comma.
			for !p.done() && p.peek() != ',' {
				p.i++
			}
			continue
		}
		p.skip(" \t")
		if p.peek() != '=' {
			// The scheme of the next challenge.
			p.i = save
			return
		}
		p.i++
		p.skip(" \t")
		var value string
		if p.peek() == '"' {
			value = p.quoted()
		} else {
			value = p.token()
			// The rest of a token68 ("=="), or junk up to the next comma.
			for !p.done() && p.peek() != ',' {
				p.i++
			}
		}
		m[strings.ToLower(name)] = value
	}
}

// quoted reads a quoted-string at p.i, undoing backslash escapes. An
// unterminated one runs to the end.
func (p *authParser) quoted() string {
	p.i++ // opening quote
	var b strings.Builder
	for !p.done() {
		c := p.s[p.i]
		p.i++
		switch c {
		case '"':
			return b.String()
		case '\\':
			if !p.done() {
				b.WriteByte(p.s[p.i])
				p.i++
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isTokenChar(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
		strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// digestChallenge is a Digest challenge this client can answer.
type digestChallenge struct {
	realm, nonce, opaque string
	// algorithm is as the server wrote it; "" for none (MD5, RFC 2617).
	algorithm string
	// qop is "auth", "auth-int", or "" for an RFC 2617 challenge without
	// qop.
	qop      string
	stale    bool
	userhash bool
	newHash  func() hash.Hash
	sess     bool
}

// describe is the challenge for the timeline: "SHA-256, qop auth".
func (c *digestChallenge) describe() string {
	alg := c.algorithm
	if alg == "" {
		alg = "MD5"
	}
	s := alg
	if c.qop != "" {
		s += ", qop " + c.qop
	}
	if c.stale {
		s += ", stale nonce"
	}
	return s
}

// pickDigest returns the Digest challenge of h to answer: the one with the
// strongest algorithm this client knows (SHA-512-256, SHA-256, MD5, first
// wins among equals). reason says why there is none, for the timeline.
func pickDigest(h http.Header) (best *digestChallenge, reason string) {
	rank := 0
	reason = "no Digest challenge"
	for _, c := range parseChallenges(h.Values("Www-Authenticate")) {
		if !strings.EqualFold(c.scheme, "Digest") {
			continue
		}
		alg := c.params["algorithm"]
		name := alg
		if name == "" {
			name = "MD5"
		}
		newHash, r := digestHash(name)
		if newHash == nil {
			reason = "Digest algorithm " + strconv.Quote(alg) + " not supported"
			continue
		}
		if c.params["nonce"] == "" {
			reason = "Digest challenge without nonce"
			continue
		}
		qop, ok := pickQop(c.params)
		if !ok {
			reason = "Digest qop " + strconv.Quote(c.params["qop"]) + " not supported"
			continue
		}
		if r <= rank {
			continue
		}
		rank = r
		best = &digestChallenge{
			realm:     c.params["realm"],
			nonce:     c.params["nonce"],
			opaque:    c.params["opaque"],
			algorithm: alg,
			qop:       qop,
			stale:     strings.EqualFold(c.params["stale"], "true"),
			userhash:  strings.EqualFold(c.params["userhash"], "true"),
			newHash:   newHash,
			sess:      strings.HasSuffix(strings.ToUpper(name), "-SESS"),
		}
	}
	if best != nil {
		reason = ""
	}
	return best, reason
}

// pickQop chooses the quality of protection: "auth" when offered (it does
// not need the body), else "auth-int"; "" for a challenge without qop (RFC
// 2617). ok is false when the challenge offers only unknown values.
func pickQop(params map[string]string) (qop string, ok bool) {
	offer, has := params["qop"]
	if !has {
		return "", true
	}
	var authInt bool
	for v := range strings.SplitSeq(offer, ",") {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "auth":
			return "auth", true
		case "auth-int":
			authInt = true
		}
	}
	if authInt {
		return "auth-int", true
	}
	return "", false
}

// digestInput is what a Digest response is computed from besides the
// challenge.
type digestInput struct {
	method, uri string
	// bodyHash is H(entity-body) in hex, for qop auth-int.
	bodyHash string
	nc       uint32
	cnonce   string
}

// hexHash returns the hex digest of the parts joined by ":".
func hexHash(newHash func() hash.Hash, parts ...string) string {
	h := newHash()
	for i, p := range parts {
		if i > 0 {
			io.WriteString(h, ":")
		}
		io.WriteString(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// response computes the request-digest (RFC 7616 section 3.4.1).
func (c *digestChallenge) response(cred DigestCredentials, in digestInput) string {
	ha1 := hexHash(c.newHash, cred.Username, c.realm, cred.Password)
	if c.sess {
		ha1 = hexHash(c.newHash, ha1, c.nonce, in.cnonce)
	}
	ha2 := hexHash(c.newHash, in.method, in.uri)
	if c.qop == "auth-int" {
		ha2 = hexHash(c.newHash, in.method, in.uri, in.bodyHash)
	}
	if c.qop == "" {
		return hexHash(c.newHash, ha1, c.nonce, ha2)
	}
	return hexHash(c.newHash, ha1, c.nonce, ncString(in.nc), in.cnonce, c.qop, ha2)
}

func ncString(nc uint32) string {
	s := strconv.FormatUint(uint64(nc), 16)
	return strings.Repeat("0", 8-len(s)) + s
}

// authorization returns the Authorization field value answering c.
func (c *digestChallenge) authorization(cred DigestCredentials, in digestInput) string {
	var b strings.Builder
	b.WriteString("Digest ")
	switch {
	case c.userhash:
		b.WriteString("username=" + quote(hexHash(c.newHash, cred.Username, c.realm)))
	case needsExtValue(cred.Username):
		// RFC 7616 section 3.4.4: a name that cannot go in a quoted-string.
		b.WriteString("username*=UTF-8''" + extEncode(cred.Username))
	default:
		b.WriteString("username=" + quote(cred.Username))
	}
	b.WriteString(", realm=" + quote(c.realm))
	b.WriteString(", uri=" + quote(in.uri))
	if c.algorithm != "" {
		b.WriteString(", algorithm=" + c.algorithm)
	}
	b.WriteString(", nonce=" + quote(c.nonce))
	if c.qop != "" {
		b.WriteString(", nc=" + ncString(in.nc))
		b.WriteString(", cnonce=" + quote(in.cnonce))
		b.WriteString(", qop=" + c.qop)
	} else if c.sess {
		b.WriteString(", cnonce=" + quote(in.cnonce))
	}
	b.WriteString(", response=" + quote(c.response(cred, in)))
	if c.opaque != "" {
		b.WriteString(", opaque=" + quote(c.opaque))
	}
	if c.userhash {
		b.WriteString(", userhash=true")
	}
	return b.String()
}

// quote writes s as a quoted-string.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// needsExtValue reports bytes a quoted-string cannot carry as text:
// non-ASCII (UTF-8) and control characters.
func needsExtValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' || c >= 0x7f {
			return true
		}
	}
	return false
}

// extEncode percent-encodes s for an RFC 8187 ext-value: all but
// attr-char.
func extEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

// newCnonce returns a fresh client nonce: 128 random bits in hex.
func newCnonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
