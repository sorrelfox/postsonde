package netx

import (
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// The proxy bypass list (SOCKS-09): targets that connect directly while a
// proxy is in effect, shared by every kind of proxy (SOCKS and HTTP). An
// entry is, in the spirit of curl's and Go's NO_PROXY:
//
//	*                  every target
//	example.com        that host and every name under it (api.example.com)
//	.example.com       only the names under it, not example.com itself
//	*.example.com      the same as .example.com
//	<local>            plain host names without a dot (intranet, as Windows)
//	10.1.2.3, ::1      that IP address ([::1] too)
//	10.0.0.0/8         the addresses in that CIDR range (fd00::/8 too)
//
// and any of them but a CIDR range with a port, so it only matches
// connections to that port: example.com:8443, *.corp:443, 10.1.2.3:80,
// [::1]:8080, *:8080. Names are compared without case and without a
// trailing dot. Nothing is resolved to match: an IP entry matches only a
// target given as an IP address, a name entry only a target given as a
// name. localhost and loopback addresses are always connected to directly
// (IsLoopbackTarget), whatever the list says. See
// docs/network.md#bypass-list.

// CodeProxyInvalidBypass: an entry of a bypass list that cannot be used
// (kind invalid_argument). Details: entry.
const CodeProxyInvalidBypass = "netx.proxy_invalid_bypass"

// HintProxyInvalidBypass is the hint of CodeProxyInvalidBypass errors.
const HintProxyInvalidBypass = "Write one host per entry: example.com, .example.com or *.example.com, <local>, an IP address such as 10.1.2.3 or ::1, or a CIDR range such as 10.0.0.0/8, optionally followed by :port."

// MaxBypassEntries bounds a bypass list.
const MaxBypassEntries = 500

// maxBypassEntryLen bounds one entry: a 255-byte host and ":65535".
const maxBypassEntryLen = 262

type bypassKind uint8

const (
	bypassAll bypassKind = iota + 1
	// bypassDomain: the name and every name under it.
	bypassDomain
	// bypassSuffix: only the names under it.
	bypassSuffix
	// bypassLocal: names without a dot.
	bypassLocal
	// bypassPrefix: an IP address (a full-length prefix) or a CIDR range.
	bypassPrefix
)

type bypassEntry struct {
	kind   bypassKind
	name   string
	prefix netip.Prefix
	// port restricts the entry to that target port; 0 for any.
	port int
}

// Bypass is a parsed bypass list. The zero Bypass bypasses nothing. It is
// immutable and safe for concurrent use.
type Bypass struct {
	entries []bypassEntry
	// text is the canonical form of the entries, joined by commas: two
	// lists with the same text match the same targets.
	text string
}

// IsZero reports whether b has no entries.
func (b Bypass) IsZero() bool { return len(b.entries) == 0 }

// String returns the canonical entries joined by commas ("" for none):
// names lower-case without a trailing dot, *.x written .x, IPs and ranges
// in Go's form.
func (b Bypass) String() string { return b.text }

// Entries returns the canonical entries.
func (b Bypass) Entries() []string {
	if b.text == "" {
		return nil
	}
	return strings.Split(b.text, ",")
}

// ParseBypass parses a bypass list entry by entry. An entry that cannot be
// used is an invalid_argument error with CodeProxyInvalidBypass and the
// entry in its details; so is a list longer than MaxBypassEntries.
// Spaces around entries are ignored; empty entries are an error.
func ParseBypass(entries []string) (Bypass, error) {
	if len(entries) > MaxBypassEntries {
		return Bypass{}, errs.New(errs.InvalidArgument, "The bypass list has more than "+strconv.Itoa(MaxBypassEntries)+" entries.").
			WithCode(CodeProxyInvalidBypass).WithHint(HintProxyInvalidBypass)
	}
	var b bypassBuilder
	for _, raw := range entries {
		e, text, msg := parseBypassEntry(strings.TrimSpace(raw))
		if msg != "" {
			return Bypass{}, errs.New(errs.InvalidArgument, "The bypass list entry "+strconv.Quote(raw)+" cannot be used: "+msg).
				WithCode(CodeProxyInvalidBypass).WithHint(HintProxyInvalidBypass).WithDetail("entry", raw)
		}
		b.add(e, text)
	}
	return b.build(), nil
}

// ParseBypassList parses a list as NO_PROXY and the OS proxy settings
// write it: entries separated by commas, semicolons or white space.
// Entries that cannot be used are left out and returned in skipped, so a
// list typed for another program still works for the rest.
func ParseBypassList(list string) (b Bypass, skipped []string) {
	var bb bypassBuilder
	for _, raw := range SplitBypassList(list) {
		e, text, msg := parseBypassEntry(raw)
		if msg != "" {
			skipped = append(skipped, raw)
			continue
		}
		if len(bb.entries) < MaxBypassEntries {
			bb.add(e, text)
		}
	}
	return bb.build(), skipped
}

// SplitBypassList splits list at commas, semicolons and white space,
// dropping empty entries.
func SplitBypassList(list string) []string {
	return strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

// JoinBypass returns one list with the entries of every list.
func JoinBypass(lists ...Bypass) Bypass {
	var b bypassBuilder
	for _, l := range lists {
		texts := l.Entries()
		for i, e := range l.entries {
			b.add(e, texts[i])
		}
	}
	return b.build()
}

type bypassBuilder struct {
	entries []bypassEntry
	texts   []string
	seen    map[string]bool
}

func (b *bypassBuilder) add(e bypassEntry, text string) {
	if b.seen[text] {
		return
	}
	if b.seen == nil {
		b.seen = map[string]bool{}
	}
	b.seen[text] = true
	b.entries = append(b.entries, e)
	b.texts = append(b.texts, text)
}

func (b *bypassBuilder) build() Bypass {
	return Bypass{entries: b.entries, text: strings.Join(b.texts, ",")}
}

// Match reports whether address (host:port, or a host) is on the list.
func (b Bypass) Match(address string) bool {
	if len(b.entries) == 0 {
		return false
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		host, portText = address, ""
	}
	port, _ := strconv.Atoi(portText)
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	ip, ipErr := netip.ParseAddr(host)
	isIP := ipErr == nil
	ip = ip.WithZone("").Unmap()
	for _, e := range b.entries {
		if e.port != 0 && e.port != port {
			continue
		}
		switch e.kind {
		case bypassAll:
			return true
		case bypassPrefix:
			if isIP && e.prefix.Contains(ip) {
				return true
			}
		case bypassDomain:
			if !isIP && (host == e.name || strings.HasSuffix(host, "."+e.name)) {
				return true
			}
		case bypassSuffix:
			if !isIP && strings.HasSuffix(host, "."+e.name) {
				return true
			}
		case bypassLocal:
			if !isIP && host != "" && !strings.Contains(host, ".") {
				return true
			}
		}
	}
	return false
}

// parseBypassEntry parses one trimmed entry into the entry and its
// canonical text, or says why it cannot be used.
func parseBypassEntry(s string) (bypassEntry, string, string) {
	switch {
	case s == "":
		return bypassEntry{}, "", "it is empty."
	case len(s) > maxBypassEntryLen:
		return bypassEntry{}, "", "it is longer than 262 characters."
	case strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return bypassEntry{}, "", "it contains a space or a control character."
	}
	s = strings.ToLower(s)
	if s == "*" {
		return bypassEntry{kind: bypassAll}, "*", ""
	}
	if s == "<local>" {
		return bypassEntry{kind: bypassLocal}, "<local>", ""
	}
	// A CIDR range, with the address in brackets or not.
	if strings.Contains(s, "/") {
		p, err := parseBypassPrefix(strings.NewReplacer("[", "", "]", "").Replace(s))
		if err != "" {
			return bypassEntry{}, "", err
		}
		return bypassEntry{kind: bypassPrefix, prefix: p}, p.String(), ""
	}
	// An IP address alone: IPv6 without brackets has colons of its own.
	if ip, err := netip.ParseAddr(s); err == nil {
		return ipEntry(ip, 0)
	}
	host, port := s, 0
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return bypassEntry{}, "", "the IPv6 address has no closing bracket."
		}
		host = s[1:end]
		rest := s[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return bypassEntry{}, "", "only :port may follow the IPv6 address."
			}
			n, msg := parseBypassPort(rest[1:])
			if msg != "" {
				return bypassEntry{}, "", msg
			}
			port = n
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() {
			return bypassEntry{}, "", "the address in brackets is not an IPv6 address."
		}
		return ipEntry(ip, port)
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		host = s[:i]
		if strings.Contains(host, ":") {
			return bypassEntry{}, "", "write an IPv6 address with a port in brackets: [::1]:8080."
		}
		n, msg := parseBypassPort(s[i+1:])
		if msg != "" {
			return bypassEntry{}, "", msg
		}
		port = n
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ipEntry(ip, port)
	}
	kind := bypassDomain
	name := host
	switch {
	case name == "*":
		return bypassEntry{kind: bypassAll, port: port}, withPort("*", port), ""
	case strings.HasPrefix(name, "*."):
		kind, name = bypassSuffix, name[2:]
	case strings.HasPrefix(name, "."):
		kind, name = bypassSuffix, name[1:]
	}
	name = strings.TrimSuffix(name, ".")
	if msg := checkBypassName(name); msg != "" {
		return bypassEntry{}, "", msg
	}
	text := name
	if kind == bypassSuffix {
		text = "." + name
	}
	return bypassEntry{kind: kind, name: name, port: port}, withPort(text, port), ""
}

func ipEntry(ip netip.Addr, port int) (bypassEntry, string, string) {
	if ip.Zone() != "" {
		return bypassEntry{}, "", "an IPv6 zone (%) cannot be used."
	}
	ip = ip.Unmap()
	text := ip.String()
	if port != 0 {
		if ip.Is6() {
			text = "[" + text + "]"
		}
		text += ":" + strconv.Itoa(port)
	}
	return bypassEntry{kind: bypassPrefix, prefix: netip.PrefixFrom(ip, ip.BitLen()), port: port}, text, ""
}

func withPort(text string, port int) string {
	if port == 0 {
		return text
	}
	return text + ":" + strconv.Itoa(port)
}

func parseBypassPort(s string) (int, string) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 || strings.HasPrefix(s, "+") {
		return 0, "the port must be a number from 1 to 65535."
	}
	return n, ""
}

// parseBypassPrefix parses a CIDR range; an IPv4 range may leave out
// trailing zero octets, as macOS writes them (169.254/16).
func parseBypassPrefix(s string) (netip.Prefix, string) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		addr, bits, ok := strings.Cut(s, "/")
		if ok && !strings.Contains(addr, ":") && strings.Count(addr, ".") < 3 && addr != "" {
			addr += strings.Repeat(".0", 3-strings.Count(addr, "."))
			p, err = netip.ParsePrefix(addr + "/" + bits)
		}
	}
	if err != nil {
		return netip.Prefix{}, "it is not a CIDR range such as 10.0.0.0/8 or fd00::/8."
	}
	if p.Addr().Zone() != "" {
		return netip.Prefix{}, "an IPv6 zone (%) cannot be used."
	}
	if p.Addr().Is4In6() {
		bits := p.Bits() - 96
		if bits < 0 {
			return netip.Prefix{}, "it is not a CIDR range such as 10.0.0.0/8 or fd00::/8."
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), bits)
	}
	return p.Masked(), ""
}

// checkBypassName checks a host name of an entry: dot-separated labels of
// letters, digits, "-" and "_", at most 253 characters.
func checkBypassName(name string) string {
	if name == "" {
		return "it has no host name."
	}
	if strings.Contains(name, "*") {
		return "a wildcard is allowed only as * alone or at the start: *.example.com."
	}
	if len(name) > 253 {
		return "the host name is longer than 253 characters."
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" {
			return "the host name has an empty label (two dots in a row)."
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c >= 0x80) {
				return "the host name may only contain letters, digits, \"-\", \"_\" and dots."
			}
		}
	}
	return ""
}
