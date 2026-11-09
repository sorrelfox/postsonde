package request

// authhawk.go maps a hawk auth (AUTH-18): the fields are rendered with the
// rest of the auth, and the request is signed by package hawk once it is
// complete (HTTPBuild.signHawk), since the MAC covers the method, the URL
// as sent and, with the payload hash, the body. See
// docs/auth.md#hawk-auth-18.

import (
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/hawk"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// hawkNonceLength is the length of a generated Hawk nonce, as the
// reference client's.
const hawkNonceLength = 6

// hawkPending is a hawk auth rendered and checked, waiting for the
// finished request to sign.
type hawkPending struct {
	id, key, algorithm, ext, app, dlg string
	payloadHash                       bool
	now                               func() time.Time
	rand                              io.Reader
	// omit are the request's settings.omitHeaders.
	omit []string
}

func (m authMapper) hawk() (AuthResult, error) {
	a := m.auth
	v, err := m.fields(
		"hawkId", a.HawkID, "hawkKey", a.HawkKey, "hawkAlgorithm", a.HawkAlgorithm,
		"ext", a.Ext, "app", a.App, "dlg", a.Dlg,
	)
	if err != nil {
		return AuthResult{}, err
	}
	id, key, algorithm, ext, app, dlg := v[0], v[1], v[2], v[3], v[4], v[5]
	if id == "" {
		return AuthResult{}, nil
	}
	if algorithm == "" {
		algorithm = hawk.DefaultAlgorithm
	}
	if !hawk.Supported(algorithm) {
		return AuthResult{}, m.detail(errs.New(errs.Unsupported, "PostSonde cannot sign Hawk requests with this algorithm.").
			WithCode(CodeAuthUnsupported).
			WithHint("Choose sha256 or sha1."), "hawkAlgorithm")
	}
	for _, f := range []struct{ name, label, value string }{{"hawkId", "ID", id}, {"app", "app", app}, {"dlg", "dlg", dlg}} {
		if !hawk.ValidAttribute(f.value) {
			return AuthResult{}, m.invalid(f.name, "The Hawk "+f.label+" contains a quote, a backslash, a control character or a character outside ASCII.",
				"Hawk sends it in the Authorization header as it is: use printable ASCII without \" and \\.")
		}
	}
	if !hawk.ValidExt(ext) {
		return AuthResult{}, m.invalid("ext", "The Hawk ext contains a control character or a character outside ASCII.",
			"Hawk sends ext in the Authorization header: use printable ASCII.")
	}
	if hasControl(key) {
		return AuthResult{}, m.invalid("hawkKey", "The Hawk key contains a line break or another control character.",
			"Remove control characters from the key, or from the variables it uses.")
	}
	p := &hawkPending{
		id: id, key: key, algorithm: algorithm, ext: ext, app: app, dlg: dlg,
		payloadHash: a.PayloadHash, now: m.opts.Now, rand: m.opts.Rand,
	}
	return AuthResult{hawk: p, Secrets: nonEmpty([]string{key})}, nil
}

// signHawk signs b's request as it is now with the pending hawk auth, if
// there is one, and adds Authorization: Hawk …. A new timestamp and nonce
// are made on every build. It runs once, as the last step of a build.
func (b *HTTPBuild) signHawk() error {
	p := b.hawk
	if p == nil {
		return nil
	}
	b.hawk = nil
	r := &b.Request
	target, err := sentURL(r)
	if err != nil {
		return err
	}
	authority, typed := headerRow(r.Headers, "Host")
	if !typed {
		authority = target.Authority
	}
	host, port := splitHostPort(authority, target.Scheme)
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	nonce, err := hawkNonce(p.rand)
	if err != nil {
		return errs.Wrap(err, errs.Internal, "A Hawk nonce could not be generated.").
			WithDetail(DetailAuthType, projectfile.AuthHawk)
	}
	a := hawk.Artifacts{
		Timestamp: now().Unix(), Nonce: nonce, Method: r.Method,
		Resource: target.Target(), Host: host, Port: port,
		Ext: p.ext, App: p.app, Dlg: p.dlg,
	}
	if a.App == "" {
		a.Dlg = ""
	}
	if p.payloadHash {
		var body io.Reader
		if r.Body != nil {
			rc, _, err := r.Body.Open()
			if err != nil {
				return hawkBodyFailed(err)
			}
			defer rc.Close()
			body = rc
		}
		if a.Hash, err = hawk.PayloadHash(p.algorithm, requestContentType(r.Headers, r.Body), body); err != nil {
			return hawkBodyFailed(err)
		}
	}
	mac, err := hawk.MAC(p.algorithm, p.key, a)
	if err != nil {
		e, ok := err.(*errs.Error)
		if !ok {
			e = errs.Wrap(err, errs.Internal, "The request could not be signed.")
		}
		return e.WithDetail(DetailAuthType, projectfile.AuthHawk)
	}
	b.Hawk = &a
	b.AuthSecrets = append(b.AuthSecrets, mac)
	value := hawk.Header(p.id, a, mac)
	if !b.authorizationWins(value, p.omit) {
		b.addAuthHeader(AuthorizationHeader, value)
	}
	return nil
}

func hawkBodyFailed(err error) error {
	return errs.Wrap(err, errs.KindOf(err), "The body could not be read to hash it.").
		WithHint("Check the body, or turn the payload hash off.").
		WithDetail(DetailAuthType, projectfile.AuthHawk)
}

// splitHostPort splits an authority as sent into the host name in lower
// case (an IPv6 address keeps its brackets) and the port, the scheme's
// default when there is none.
func splitHostPort(authority, scheme string) (string, int) {
	port := 80
	if scheme == "https" {
		port = 443
	}
	host := authority
	if h, p, err := net.SplitHostPort(authority); err == nil {
		host = h
		if strings.Contains(h, ":") {
			host = "[" + h + "]"
		}
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	return strings.ToLower(host), port
}

// hawkNonce is hawkNonceLength random letters and digits.
func hawkNonce(rnd io.Reader) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	if rnd == nil {
		rnd = rand.Reader
	}
	buf := make([]byte, hawkNonceLength)
	if _, err := io.ReadFull(rnd, buf); err != nil {
		return "", err
	}
	for i, c := range buf {
		buf[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(buf), nil
}
