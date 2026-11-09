package tlsconf

import (
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/pkcs12"
)

// Per-host client certificates (AUTH-30, NET-05): the settings keep a list
// of host patterns, each with a client certificate as a PEM certificate
// and key or a PKCS#12 (.p12, .pfx) file. Every TLS client (HTTP, GraphQL,
// SSE, WebSocket, gRPC) asks ClientCerts for the certificate of the host
// and port it connects to. Only paths are stored; a passphrase comes from
// the app's local secret storage through the passphrase function. See
// docs/network.md#client-certificates.

// Error codes of the per-host client certificates, besides those of Build
// and pkcs12.Decode.
const (
	CodeHostPatternInvalid = "tlsconf.host_pattern_invalid"   // not a host pattern
	CodeClientCertSource   = "tlsconf.client_cert_source"     // neither or both PEM and PKCS#12, a key with PKCS#12
	CodeClientCertID       = "tlsconf.client_cert_id_invalid" // an entry ID that is empty or not [A-Za-z0-9_-]
	CodePassphraseFailed   = "tlsconf.passphrase_unavailable" // the passphrase could not be read from the secret storage
)

// Hints of the per-host client certificates.
const (
	HintHostPattern    = "Enter a host name, optionally with * wildcards and a port: api.example.com, *.example.com, localhost:8443 or [::1]:8443. Leave out https:// and any path."
	HintClientCertFile = "Choose a PEM certificate and its key, or one PKCS#12 (.p12, .pfx) file."
	HintClientCertFix  = "Fix or remove the client certificate for this host in Settings, Network, Client certificates."
)

// ClientCertEntry is one per-host client certificate as the settings keep
// it (network.clientCerts). Paths are absolute. A PEM entry has CertFile
// and, unless the certificate file holds the key too, KeyFile; a PKCS#12
// entry has PFXFile only. A passphrase (of the PKCS#12 file or of an
// encrypted PKCS#8 key) is never part of the entry: it is kept in the
// local secret storage under ID.
type ClientCertEntry struct {
	// ID identifies the entry, and its passphrase in the secret storage.
	ID string `json:"id"`
	// Host is the host pattern (ParseHostPattern).
	Host     string `json:"host"`
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
	PFXFile  string `json:"pfxFile,omitempty"`
}

// IsPKCS12 reports whether the entry is a PKCS#12 file.
func (e ClientCertEntry) IsPKCS12() bool { return e.PFXFile != "" }

// MaxClientCertIDLen bounds ClientCertEntry.ID.
const MaxClientCertIDLen = 64

// ValidateClientCertEntry checks the shape of e without reading files:
// the ID, the host pattern, and a complete certificate source with
// absolute paths. Errors are invalid_argument with a detail "field".
func ValidateClientCertEntry(e ClientCertEntry) error {
	if !validID(e.ID) {
		return errs.New(errs.InvalidArgument, "The client certificate has an invalid ID.").
			WithCode(CodeClientCertID).WithDetail("field", "id")
	}
	if _, err := ParseHostPattern(e.Host); err != nil {
		return err
	}
	source := func(msg string) error {
		return errs.New(errs.InvalidArgument, msg).WithCode(CodeClientCertSource).WithHint(HintClientCertFile).WithDetail("field", "certFile")
	}
	switch {
	case e.PFXFile != "" && (e.CertFile != "" || e.KeyFile != ""):
		return source("A client certificate is either a PKCS#12 file or a PEM certificate and key, not both.")
	case e.PFXFile == "" && e.CertFile == "":
		if e.KeyFile != "" {
			return source("A private key is given without its certificate.")
		}
		return source("No client certificate file is given.")
	}
	for _, f := range entryFiles(e) {
		if f.path != "" && (strings.TrimSpace(f.path) != f.path || !filepath.IsAbs(f.path)) {
			return errs.New(errs.InvalidArgument, "The path "+strconv.Quote(f.path)+" is not absolute.").
				WithCode(CodeInvalidPath).WithHint(HintRelative).WithDetail("field", f.field).WithDetail("path", f.path)
		}
	}
	return nil
}

type entryFile struct{ field, path string }

// entryFiles lists the file fields of e, in a fixed order.
func entryFiles(e ClientCertEntry) []entryFile {
	return []entryFile{{"certFile", e.CertFile}, {"keyFile", e.KeyFile}, {"pfxFile", e.PFXFile}}
}

func validID(id string) bool {
	if id == "" || len(id) > MaxClientCertIDLen {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// HostPattern matches the host and port of a connection. Host is lower
// case, without brackets or a trailing dot, and may hold * wildcards, each
// matching any run of characters (dots included): *.example.com matches
// api.example.com and a.b.example.com but not example.com. Port is ""
// for any port, or a port number.
type HostPattern struct {
	Host string
	Port string
}

// ParseHostPattern parses "host", "host:port", "[ipv6]:port" or a bare
// IPv6 address. The host may use * wildcards; a port of * or none means
// any port. A scheme, path, query, user info or spaces are errors (code
// CodeHostPatternInvalid, detail field "host").
func ParseHostPattern(s string) (HostPattern, error) {
	bad := func(msg string) (HostPattern, error) {
		return HostPattern{}, errs.New(errs.InvalidArgument, msg).WithCode(CodeHostPatternInvalid).
			WithHint(HintHostPattern).WithDetail("field", "host").WithDetail("value", s)
	}
	t := strings.TrimSpace(s)
	switch {
	case t == "":
		return bad("The host is empty.")
	case len(t) > 260:
		return bad("The host is too long.")
	case strings.Contains(t, "://"):
		return bad("The host " + strconv.Quote(t) + " has a scheme.")
	case strings.ContainsAny(t, " \t\r\n/?#@\\,;"):
		return bad("The host " + strconv.Quote(t) + " is not a host name.")
	}
	host, port := t, ""
	switch {
	case strings.HasPrefix(t, "["):
		end := strings.Index(t, "]")
		if end < 0 {
			return bad("The host " + strconv.Quote(t) + " has an unclosed bracket.")
		}
		host, port = t[1:end], ""
		rest := t[end+1:]
		if rest != "" {
			p, ok := strings.CutPrefix(rest, ":")
			if !ok {
				return bad("The host " + strconv.Quote(t) + " is not a host name.")
			}
			port = p
			if port == "" {
				return bad("The port is empty.")
			}
		}
		if !strings.Contains(host, ":") {
			return bad("Only IPv6 addresses go in brackets.")
		}
	case strings.Count(t, ":") == 1:
		host, port, _ = strings.Cut(t, ":")
		if port == "" {
			return bad("The port is empty.")
		}
	case strings.Count(t, ":") > 1:
		// A bare IPv6 address, any port.
	}
	if port == "*" {
		port = ""
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return bad("The port " + strconv.Quote(port) + " is not a number from 1 to 65535.")
		}
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return bad("The host name is empty.")
	}
	v6 := strings.Contains(host, ":")
	for _, r := range host {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' || r == '*' || (v6 && r == ':')
		if !ok {
			return bad("The host " + strconv.Quote(t) + " has a character that cannot be in a host name: " + strconv.QuoteRune(r) + ".")
		}
	}
	return HostPattern{Host: host, Port: port}, nil
}

// String returns the pattern as ParseHostPattern reads it.
func (p HostPattern) String() string {
	h := p.Host
	if strings.Contains(h, ":") && p.Port != "" {
		h = "[" + h + "]"
	}
	if p.Port != "" {
		return h + ":" + p.Port
	}
	return h
}

// Matches reports whether the pattern matches host (a name or an IP
// address, IPv6 without brackets) and port. Names are compared without
// case and a trailing dot.
func (p HostPattern) Matches(host, port string) bool {
	if p.Port != "" && p.Port != port {
		return false
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	return glob(p.Host, host)
}

// specificity orders matching patterns: more literal host characters
// first, then a pattern with a port before one for any port.
func (p HostPattern) specificity() int {
	n := 2 * (len(p.Host) - strings.Count(p.Host, "*"))
	if p.Port != "" {
		n++
	}
	return n
}

// glob matches s against pattern, where * matches any run of characters.
func glob(pattern, s string) bool {
	star, from := -1, 0
	i, j := 0, 0
	for j < len(s) {
		switch {
		case i < len(pattern) && pattern[i] == '*':
			star, from = i, j
			i++
		case i < len(pattern) && pattern[i] == s[j]:
			i++
			j++
		case star >= 0:
			i = star + 1
			from++
			j = from
		default:
			return false
		}
	}
	for i < len(pattern) && pattern[i] == '*' {
		i++
	}
	return i == len(pattern)
}

// MatchClientCert returns the index of the entry for host and port: the
// most specific matching pattern (see HostPattern), the first in the list
// on a tie; -1 when none matches. Entries with an invalid pattern are
// skipped.
func MatchClientCert(entries []ClientCertEntry, host, port string) int {
	best, bestScore := -1, -1
	for i, e := range entries {
		p, err := ParseHostPattern(e.Host)
		if err != nil || !p.Matches(host, port) {
			continue
		}
		if s := p.specificity(); s > bestScore {
			best, bestScore = i, s
		}
	}
	return best
}

// ClientCert is a loaded per-host client certificate.
type ClientCert struct {
	// Entry is the settings entry it was loaded from.
	Entry ClientCertEntry
	// Certificate is ready for tls.Config: the leaf, the chain, the key.
	Certificate *tls.Certificate
	// Leaf is the client certificate.
	Leaf *x509.Certificate
	// Chain holds the other certificates sent with it, in order.
	Chain []*x509.Certificate
	// Warnings: expired or not yet valid, not issued for client
	// authentication.
	Warnings []Warning
}

// Describe names the certificate for the timeline: its subject and the
// pattern that chose it, never key material.
func (c *ClientCert) Describe() string {
	name := c.Leaf.Subject.CommonName
	if name == "" {
		name = c.Leaf.Subject.String()
	}
	return strconv.Quote(name) + " (" + c.Entry.Host + ")"
}

// LoadClientCert reads and checks the certificate of e: the files parse,
// the private key belongs to the certificate, a PKCS#12 file or an
// encrypted PKCS#8 key opens with passphrase. An expired certificate is
// only a warning. Errors are *errs.Error with the codes of Build
// (tlsconf.*) and of pkcs12.Decode (pkcs12.*), with the details field and
// path.
func LoadClientCert(e ClientCertEntry, passphrase string) (*ClientCert, error) {
	return loadEntry(e, passphrase, time.Now())
}

func loadEntry(e ClientCertEntry, passphrase string, now time.Time) (*ClientCert, error) {
	if err := ValidateClientCertEntry(e); err != nil {
		return nil, err
	}
	var cert tls.Certificate
	if e.IsPKCS12() {
		c, err := loadPKCS12(e.PFXFile, passphrase)
		if err != nil {
			return nil, err
		}
		cert = c
	} else {
		c, err := loadClientCert(Options{ClientCertFile: e.CertFile, ClientKeyFile: e.KeyFile}, "", &passphrase)
		if err != nil {
			return nil, entryField(err)
		}
		cert = c
	}
	out := &ClientCert{Entry: e, Certificate: &cert, Leaf: cert.Leaf, Warnings: clientCertWarnings(cert.Leaf, now)}
	for _, der := range cert.Certificate[1:] {
		if c, err := x509.ParseCertificate(der); err == nil {
			out.Chain = append(out.Chain, c)
		}
	}
	return out, nil
}

// entryField renames the detail field of a Build error to the entry's
// field (clientCertFile to certFile, clientKeyFile to keyFile).
func entryField(err error) error {
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		return err
	}
	switch e.Details["field"] {
	case "clientCertFile":
		return e.WithDetail("field", "certFile")
	case "clientKeyFile":
		return e.WithDetail("field", "keyFile")
	}
	return err
}

func loadPKCS12(path, passphrase string) (tls.Certificate, error) {
	const field = "pfxFile"
	data, p, err := readFile(field, path, "")
	if err != nil {
		return tls.Certificate{}, err
	}
	b, err := pkcs12.Decode(data, passphrase)
	if err != nil {
		e, ok := errors.AsType[*errs.Error](err)
		if !ok {
			e = errs.Wrap(err, errs.Parse, "The PKCS#12 file "+p+" could not be read.").WithCode(pkcs12.CodeInvalid)
		}
		return tls.Certificate{}, e.WithDetail("field", field).WithDetail("path", p)
	}
	if _, ok := b.PrivateKey.(crypto.Signer); !ok {
		return tls.Certificate{}, errs.New(errs.Unsupported, "The private key in "+p+" is of an unsupported type.").
			WithCode(CodeClientKeyInvalid).WithDetail("field", field).WithDetail("path", p)
	}
	return b.TLSCertificate(), nil
}

// PassphraseFunc returns the passphrase stored for an entry ID, "" for
// none.
type PassphraseFunc func(id string) (string, error)

// ClientCerts selects the per-host client certificate of a connection
// from the entries a function returns (read on every call, so a settings
// change applies to the next connection). Loaded certificates are cached
// per entry until the entry, its passphrase, or a file's size or
// modification time changes, so the same *tls.Certificate comes back for
// an unchanged entry and callers may key connection pools on it. It is
// safe for concurrent use; a nil *ClientCerts has no certificates.
type ClientCerts struct {
	entries    func() []ClientCertEntry
	passphrase PassphraseFunc
	now        func() time.Time

	mu    sync.Mutex
	cache map[string]cachedClientCert // by entry ID
}

type cachedClientCert struct {
	key  string
	cert *ClientCert
}

// NewClientCerts returns a ClientCerts over the entries entries returns;
// passphrase gives an entry's passphrase (nil: none).
func NewClientCerts(entries func() []ClientCertEntry, passphrase PassphraseFunc) *ClientCerts {
	return &ClientCerts{entries: entries, passphrase: passphrase, now: time.Now, cache: map[string]cachedClientCert{}}
}

// ForAddress returns the client certificate for a connection to addr
// ("host:port"), or nil when no entry matches. An entry that matches but
// cannot be loaded (a moved file, a wrong passphrase) is an error, so the
// connection fails with a clear message instead of going out without the
// certificate: it has the code of LoadClientCert, HintClientCertFix and
// the details host (the entry's pattern) and clientCert (its ID).
func (c *ClientCerts) ForAddress(addr string) (*ClientCert, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = addr, ""
	}
	return c.For(host, port)
}

// ForURL returns the client certificate for u, with the default port of
// its scheme (443 for https and wss, 80 otherwise) when it has none.
func (c *ClientCerts) ForURL(u *url.URL) (*ClientCert, error) {
	port := u.Port()
	if port == "" {
		port = "80"
		switch strings.ToLower(u.Scheme) {
		case "https", "wss":
			port = "443"
		}
	}
	return c.For(u.Hostname(), port)
}

// For returns the client certificate for host and port, as ForAddress.
func (c *ClientCerts) For(host, port string) (*ClientCert, error) {
	if c == nil || c.entries == nil {
		return nil, nil
	}
	entries := c.entries()
	i := MatchClientCert(entries, host, port)
	if i < 0 {
		return nil, nil
	}
	e := entries[i]
	fix := func(err error) error {
		ee, ok := errors.AsType[*errs.Error](err)
		if !ok {
			ee = errs.Wrap(err, errs.KindOf(err), "The client certificate for "+e.Host+" cannot be used.")
		}
		return ee.WithHint(HintClientCertFix).WithDetail("host", e.Host).WithDetail("clientCert", e.ID)
	}
	pass := ""
	if c.passphrase != nil {
		p, err := c.passphrase(e.ID)
		if err != nil {
			return nil, fix(errs.Wrap(err, errs.KindOf(err), "The passphrase of the client certificate for "+e.Host+" could not be read.").WithCode(CodePassphraseFailed))
		}
		pass = p
	}
	key, err := cacheKey(e, pass)
	if err != nil {
		return nil, fix(err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if hit, ok := c.cache[e.ID]; ok && hit.key == key {
		return hit.cert, nil
	}
	cert, err := loadEntry(e, pass, c.now())
	if err != nil {
		return nil, fix(err)
	}
	// Drop entries that are gone, so the cache stays as long as the list.
	for id := range c.cache {
		if !containsID(entries, id) {
			delete(c.cache, id)
		}
	}
	c.cache[e.ID] = cachedClientCert{key: key, cert: cert}
	return cert, nil
}

func containsID(entries []ClientCertEntry, id string) bool {
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

// cacheKey identifies what an entry loads to: its fields, the passphrase
// (hashed) and each file's size and modification time.
func cacheKey(e ClientCertEntry, passphrase string) (string, error) {
	sum := sha256.Sum256([]byte(passphrase))
	var b strings.Builder
	b.WriteString(e.Host + "\x00" + hex.EncodeToString(sum[:8]) + "\x00")
	for _, f := range entryFiles(e) {
		b.WriteString(f.path + "\x00")
		if f.path == "" {
			continue
		}
		info, err := os.Stat(f.path)
		if err != nil {
			return "", fileErr(err, f.field, f.path)
		}
		b.WriteString(strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + "\x00")
	}
	return b.String(), nil
}

// GetClientCertificate returns a tls.Config.GetClientCertificate that
// always sends cert (the leaf and its chain) when the server asks, even
// if its issuer is not among the CAs the server lists: the server's
// verdict is a clearer error than sending none. A nil cert sends none.
func GetClientCertificate(cert *tls.Certificate) func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		if cert == nil {
			return &tls.Certificate{}, nil
		}
		return cert, nil
	}
}
