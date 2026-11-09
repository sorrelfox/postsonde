package sysproxy

import (
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ParseScutil reads the output of scutil --proxy (macOS):
//
//	<dictionary> {
//	  ExceptionsList : <array> {
//	    0 : *.local
//	    1 : 169.254/16
//	  }
//	  ExcludeSimpleHostnames : 1
//	  HTTPEnable : 1
//	  HTTPPort : 8080
//	  HTTPProxy : proxy.example.com
//	  ...
//	}
//
// Only the top-level keys count (not those of __SCOPED__, per
// interface). A kind of proxy counts when its Enable key is 1 and it has
// a host; a missing port takes the kind's default. ExcludeSimpleHostnames
// adds <local> to the bypass list. SOCKS is SOCKS5 with remote DNS.
func ParseScutil(out string) Settings {
	s := Settings{Source: SourceMacOS, SOCKSScheme: "socks5h"}
	values := map[string]string{}
	depth := 0
	inExceptions := false
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case line == "}":
			depth--
			inExceptions = false
			continue
		case strings.HasSuffix(line, "{"):
			key, _, _ := strings.Cut(line, " : ")
			depth++
			inExceptions = depth == 2 && strings.TrimSpace(key) == "ExceptionsList"
			continue
		}
		key, value, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case inExceptions:
			s.Bypass = append(s.Bypass, value)
		case depth == 1:
			values[key] = value
		}
	}
	proxy := func(kind string) string {
		host := values[kind+"Proxy"]
		if values[kind+"Enable"] != "1" || host == "" {
			return ""
		}
		if port := values[kind+"Port"]; port != "" && port != "0" {
			return net.JoinHostPort(host, port)
		}
		return hostOnly(host)
	}
	s.HTTP, s.HTTPS, s.SOCKS = proxy("HTTP"), proxy("HTTPS"), proxy("SOCKS")
	if values["ExcludeSimpleHostnames"] == "1" {
		s.Bypass = append(s.Bypass, "<local>")
	}
	if values["ProxyAutoConfigEnable"] == "1" {
		s.PAC = values["ProxyAutoConfigURLString"]
		if s.PAC == "" {
			s.PAC = "(unnamed)"
		}
	}
	s.AutoDiscovery = values["ProxyAutoDiscoveryEnable"] == "1"
	return s
}

// hostOnly brackets an IPv6 address so a default port can follow.
func hostOnly(host string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}

// RegistryValues are the Windows Internet Settings of the current user
// (HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings).
type RegistryValues struct {
	// ProxyEnable is the "Use a proxy server" switch.
	ProxyEnable bool
	// ProxyServer is "host:port" (every protocol) or
	// "http=host:port;https=host:port;socks=host:port".
	ProxyServer string
	// ProxyOverride is the bypass list, separated by semicolons;
	// "<local>" bypasses names without a dot.
	ProxyOverride string
	// AutoConfigURL is the PAC file of "Use setup script".
	AutoConfigURL string
}

// ParseWindows reads the Internet Settings: with ProxyEnable, ProxyServer
// is one HTTP proxy for every protocol ("host:port") or a proxy per
// protocol ("http=…;https=…;socks=…"); socks= is SOCKS4 as WinINet reads
// it (socks4a here, so host names go to the proxy). AutoConfigURL is a PAC
// file. "Automatically detect settings" (WPAD) is not read.
func ParseWindows(v RegistryValues) Settings {
	s := Settings{Source: SourceWindows, SOCKSScheme: "socks4a", PAC: strings.TrimSpace(v.AutoConfigURL)}
	if !v.ProxyEnable {
		return s
	}
	server := strings.TrimSpace(v.ProxyServer)
	if server == "" {
		return s
	}
	if !strings.Contains(server, "=") {
		s.HTTP, s.HTTPS = server, server
	} else {
		for part := range strings.SplitSeq(server, ";") {
			k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			val = strings.TrimSpace(val)
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "http":
				s.HTTP = val
			case "https":
				s.HTTPS = val
			case "socks":
				s.SOCKS = val
			}
		}
	}
	if o := strings.TrimSpace(v.ProxyOverride); o != "" {
		s.Bypass = strings.Split(o, ";")
	}
	return s
}

// ParseGSettings reads the output of gsettings list-recursively
// org.gnome.system.proxy:
//
//	org.gnome.system.proxy mode 'manual'
//	org.gnome.system.proxy ignore-hosts ['localhost', '127.0.0.0/8', '::1']
//	org.gnome.system.proxy.http host 'proxy.example.com'
//	org.gnome.system.proxy.http port 8080
//
// ok is false when the mode is not "manual" or "auto" (GNOME's default
// "none"), so the environment is read instead. With use-same-proxy the
// HTTP proxy serves HTTPS too; the HTTP proxy's credentials are used when
// use-authentication is on. Mode "auto" is a PAC file (autoconfig-url) or,
// without one, WPAD.
func ParseGSettings(out string) (Settings, bool) {
	values := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		schema, rest, ok := strings.Cut(line, " ")
		if !ok || !strings.HasPrefix(schema, "org.gnome.system.proxy") {
			continue
		}
		key, value, ok := strings.Cut(rest, " ")
		if !ok {
			continue
		}
		values[strings.TrimPrefix(strings.TrimPrefix(schema, "org.gnome.system.proxy"), ".")+" "+key] = strings.TrimSpace(value)
	}
	str := func(k string) string { return gvariantString(values[k]) }
	s := Settings{Source: SourceGNOME, SOCKSScheme: "socks5h"}
	switch str(" mode") {
	case "manual":
	case "auto":
		s.PAC = str(" autoconfig-url")
		s.AutoDiscovery = s.PAC == ""
		return s, true
	default:
		return s, false
	}
	proxy := func(kind string) string {
		host := str(kind + " host")
		port, _ := strconv.Atoi(values[kind+" port"])
		if host == "" {
			return ""
		}
		if port > 0 {
			return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
		}
		return hostOnly(host)
	}
	s.HTTP, s.HTTPS, s.SOCKS = proxy("http"), proxy("https"), proxy("socks")
	if values[" use-same-proxy"] == "true" && s.HTTPS == "" {
		s.HTTPS = s.HTTP
	}
	if values["http use-authentication"] == "true" {
		s.HTTPUser, s.HTTPPassword = str("http authentication-user"), str("http authentication-password")
	}
	s.Bypass = gvariantStrings(values[" ignore-hosts"])
	return s, true
}

// gvariantString decodes a GVariant string as gsettings prints it: 'text'
// or "text", with backslash escapes; anything else is returned as it is.
func gvariantString(v string) string {
	s, _ := cutGVariantString(strings.TrimSpace(v))
	return s
}

// cutGVariantString decodes the string at the start of v and returns the
// rest after it.
func cutGVariantString(v string) (string, string) {
	if v == "" || (v[0] != '\'' && v[0] != '"') {
		return v, ""
	}
	quote := v[0]
	var b strings.Builder
	for i := 1; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\' && i+1 < len(v):
			i++
			b.WriteByte(v[i])
		case c == quote:
			return b.String(), v[i+1:]
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), ""
}

// gvariantStrings decodes a GVariant string array: ['a', 'b'] or @as [].
func gvariantStrings(v string) []string {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "@as"))
	v = strings.TrimSpace(strings.TrimPrefix(v, "["))
	var out []string
	for v != "" && v[0] != ']' {
		var s string
		s, v = cutGVariantString(v)
		if s != "" {
			out = append(out, s)
		}
		v = strings.TrimLeft(v, " ,")
		if v != "" && v[0] != '\'' && v[0] != '"' && v[0] != ']' {
			break
		}
	}
	return out
}

// regDWORD reads a REG_DWORD (or the first four bytes of a REG_BINARY,
// which old systems wrote for ProxyEnable).
func regDWORD(b []byte) uint32 {
	if len(b) < 4 {
		if len(b) > 0 {
			return uint32(b[0])
		}
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

// regString decodes a REG_SZ: UTF-16LE up to the first NUL.
func regString(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}
