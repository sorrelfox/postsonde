package cookies

import (
	"net/netip"
	"net/url"
	"strings"
)

// canonicalHost returns the host of u in the form cookies are matched
// against (RFC 6265 §5.1.2): without port and IPv6 brackets, lower case.
// Internationalized names are not converted to punycode (the standard
// library has no IDNA); a jar only sees them as typed.
func canonicalHost(u *url.URL) string {
	return strings.ToLower(u.Hostname())
}

// isIP reports whether host is an IPv4 or IPv6 address.
func isIP(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

// domainMatch implements RFC 6265 §5.1.3: s domain-matches d if they are
// identical, or d is a suffix of s preceded by a dot in s and s is a host
// name, not an IP address.
func domainMatch(s, d string) bool {
	if s == d {
		return true
	}
	return len(s) > len(d) && strings.HasSuffix(s, d) && s[len(s)-len(d)-1] == '.' && !isIP(s)
}

// requestPath returns the path of u as sent. httpclient keeps the path as
// typed in URL.Opaque; other URLs use their escaped path.
func requestPath(u *url.URL) string {
	if strings.HasPrefix(u.Opaque, "/") {
		return u.Opaque
	}
	return u.EscapedPath()
}

// defaultPath implements RFC 6265 §5.1.4: the directory of the request
// path, "/" when there is none.
func defaultPath(p string) string {
	if p == "" || p[0] != '/' {
		return "/"
	}
	i := strings.LastIndexByte(p, '/')
	if i == 0 {
		return "/"
	}
	return p[:i]
}

// pathMatch implements RFC 6265 §5.1.4: the request path reqPath
// path-matches the cookie path if they are identical, or the cookie path
// is a prefix of it that ends in "/" or is followed by "/".
func pathMatch(reqPath, cookiePath string) bool {
	if reqPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(reqPath, cookiePath) {
		return false
	}
	return cookiePath[len(cookiePath)-1] == '/' || reqPath[len(cookiePath)] == '/'
}

// isSecureContext reports whether a request to u counts as a secure
// channel: https and wss, and, as in browsers ("potentially trustworthy"
// origins), localhost names and loopback addresses over plain http.
func isSecureContext(u *url.URL) bool {
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		return true
	}
	h := canonicalHost(u)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.IsLoopback()
	}
	return false
}

// isHTTPScheme reports whether u uses an HTTP-based scheme, for which the
// jar stores and sends cookies.
func isHTTPScheme(u *url.URL) bool {
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss":
		return true
	}
	return false
}

// isPublicSuffix reports whether domain is treated as a public suffix, a
// domain under which anyone can register names, so a Domain attribute
// naming it would set a cookie for unrelated sites.
//
// PostSonde has no public suffix list (golang.org/x/net/publicsuffix is not
// in the standard library). The heuristic, documented in docs/cookies.md:
//
//   - every single-label domain ("com", "dev", "internal") except
//     "localhost";
//   - "<generic>.<cc>" for a generic second-level label such as "co",
//     "com", "org" or "gov" under a two-letter country code ("co.uk",
//     "com.au");
//   - a short list of shared-hosting suffixes ("github.io", "vercel.app").
func isPublicSuffix(domain string) bool {
	d := strings.TrimSuffix(domain, ".")
	if d == "" {
		return true
	}
	dot := strings.IndexByte(d, '.')
	if dot < 0 {
		return d != "localhost"
	}
	if privateSuffixes[d] {
		return true
	}
	if second, cc := d[:dot], d[dot+1:]; len(cc) == 2 && !strings.Contains(second, ".") && genericSecondLevel[second] {
		return true
	}
	return false
}

// siteKey groups cookies for the per-site limit: the shortest suffix of
// domain that is not a public suffix (an approximation of the registrable
// domain, "eTLD+1"). IP addresses and single labels are their own key.
func siteKey(domain string) string {
	if isIP(domain) {
		return domain
	}
	labels := strings.Split(domain, ".")
	for n := 2; n <= len(labels); n++ {
		cand := strings.Join(labels[len(labels)-n:], ".")
		if !isPublicSuffix(cand) {
			return cand
		}
	}
	return domain
}

// genericSecondLevel are second-level labels that country-code registries
// commonly open for public registration.
var genericSecondLevel = map[string]bool{
	"ac": true, "co": true, "com": true, "edu": true, "gob": true, "go": true,
	"gov": true, "govt": true, "ltd": true, "me": true, "mil": true, "ne": true,
	"net": true, "nic": true, "nom": true, "or": true, "org": true, "plc": true,
	"sch": true,
}

// privateSuffixes are widely used hosting domains that give every customer
// a subdomain, from the private section of the public suffix list.
var privateSuffixes = map[string]bool{
	"appspot.com":           true,
	"azurewebsites.net":     true,
	"blogspot.com":          true,
	"cloudfront.net":        true,
	"elasticbeanstalk.com":  true,
	"firebaseapp.com":       true,
	"fly.dev":               true,
	"github.io":             true,
	"githubusercontent.com": true,
	"glitch.me":             true,
	"herokuapp.com":         true,
	"netlify.app":           true,
	"ngrok-free.app":        true,
	"ngrok.app":             true,
	"ngrok.io":              true,
	"onrender.com":          true,
	"pages.dev":             true,
	"s3.amazonaws.com":      true,
	"trycloudflare.com":     true,
	"vercel.app":            true,
	"web.app":               true,
	"workers.dev":           true,
	"azurestaticapps.net":   true,
	"cloudfunctions.net":    true,
	"run.app":               true,
	"railway.app":           true,
	"deno.dev":              true,
	"readthedocs.io":        true,
	"surge.sh":              true,
}
