package requestsecrets

import (
	"net/url"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// ProxyPasswordKey is the key of a request's own proxy password in its
// side file (settings.proxy.password, docs/network.md#a-requests-own-proxy).
const ProxyPasswordKey = "proxy." + projectfile.ProxyPasswordMember

// SplitProxy separates the password of a request's own proxy from what
// may be written to a project file, as Split does for an auth secret. A
// password in the URL's user info (scheme://user:password@host) is taken
// out of the URL first and becomes the password. Then:
//
//   - a typed password goes to the values, keyed "proxy.password"; in the
//     result Password is "" and LocalSecrets names it;
//   - a reference (IsReference) stays in the result, not marked local;
//   - an empty password stays marked local when p marks it.
//
// It returns the proxy to write and the values for the side file (nil
// when there are none).
func SplitProxy(p projectfile.RequestProxy) (projectfile.RequestProxy, map[string]string) {
	if u, pw, ok := cutURLPassword(p.URL); ok {
		p.URL, p.Password = u, pw
	}
	local := p.PasswordIsLocal()
	p.LocalSecrets = nil
	switch v := p.Password; {
	case v == "":
		if local {
			p.LocalSecrets = []string{projectfile.ProxyPasswordMember}
		}
		return p, nil
	case IsReference(v):
		return p, nil
	default:
		p.Password = ""
		p.LocalSecrets = []string{projectfile.ProxyPasswordMember}
		return p, map[string]string{ProxyPasswordKey: v}
	}
}

// MergeProxy puts the local password back: a password that is "" and
// marked local gets values["proxy.password"] if there is one. The mark
// stays, so SplitProxy stores it locally again.
func MergeProxy(p projectfile.RequestProxy, values map[string]string) projectfile.RequestProxy {
	if p.Password != "" || !p.PasswordIsLocal() {
		return p
	}
	if v, ok := values[ProxyPasswordKey]; ok {
		p.Password = v
	}
	return p
}

// ProxyPasswordMissing reports whether p (after MergeProxy) marks its
// password as stored locally but has none: it was typed on another
// machine.
func ProxyPasswordMissing(p projectfile.RequestProxy) bool {
	return p.PasswordIsLocal() && p.Password == ""
}

// cutURLPassword returns raw without the password of its user info and
// that password, percent-decoded; ok is false when raw has none. It works
// on the text as typed, {{variables}} included, without parsing the URL.
func cutURLPassword(raw string) (rest, password string, ok bool) {
	scheme, after, found := strings.Cut(raw, "://")
	if !found {
		return raw, "", false
	}
	authority := after
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return raw, "", false
	}
	user, pw, found := strings.Cut(authority[:at], ":")
	if !found {
		return raw, "", false
	}
	if dec, err := url.PathUnescape(pw); err == nil {
		pw = dec
	}
	return scheme + "://" + user + after[at:], pw, true
}
