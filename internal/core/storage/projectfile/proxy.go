package projectfile

import (
	"slices"

	"github.com/sorrelfox/postsonde/internal/core/storage/stablejson"
)

// Values of RequestProxy.Mode.
const (
	// ProxyInherit uses the proxy in effect for the project: the active
	// environment's, the project's, else the app's (docs/network.md). It
	// is the default, written as a missing member.
	ProxyInherit = "inherit"
	// ProxyCustom connects through the request's own proxy, URL.
	ProxyCustom = "custom"
	// ProxyNone connects directly, whatever proxy is in effect.
	ProxyNone = "none"
)

// ProxyPasswordMember names RequestProxy.Password in LocalSecrets.
const ProxyPasswordMember = "password"

// RequestProxy is a request's own proxy, settings.proxy of every request
// kind (docs/network.md#a-requests-own-proxy). The zero value inherits,
// and is left out of the file, so files written before it existed keep
// connecting as they did.
type RequestProxy struct {
	// Mode is ProxyInherit (also when missing), ProxyCustom or ProxyNone.
	// A mode this version does not know is kept, and refused when sending.
	Mode string `json:"mode,omitempty"`
	// URL is the proxy of ProxyCustom, scheme://[user@]host[:port], with
	// {{variables}} unresolved; any scheme netx.ParseProxyURL takes. It is
	// kept in the other modes too, so switching back finds it. It never
	// holds a password: one typed into it is moved to Password before the
	// file is written (requestsecrets.SplitProxy).
	URL string `json:"url,omitempty"`
	// Password is the proxy user's password. Secret, like an auth's: it is
	// written to the file only when made of {{variable}} references; a
	// typed value is kept in the local side file and LocalSecrets names
	// it.
	Password string `json:"password,omitempty"`
	// LocalSecrets is ["password"] when the password is kept in the local
	// side file, keyed "proxy.password".
	LocalSecrets []string `json:"localSecrets,omitempty"`
	Unknown      Unknown  `json:"-"`
}

// EffectiveMode returns Mode with "" read as ProxyInherit.
func (p RequestProxy) EffectiveMode() string {
	if p.Mode == "" {
		return ProxyInherit
	}
	return p.Mode
}

// IsZero reports whether p is the default, which is left out of the file:
// inherit, nothing else set.
func (p RequestProxy) IsZero() bool {
	return (p.Mode == "" || p.Mode == ProxyInherit) && p.URL == "" && p.Password == "" && len(p.LocalSecrets) == 0 && len(p.Unknown) == 0
}

// PasswordIsLocal reports whether the password is kept in the local side
// file.
func (p RequestProxy) PasswordIsLocal() bool {
	return slices.Contains(p.LocalSecrets, ProxyPasswordMember)
}

func (p RequestProxy) MarshalJSON() ([]byte, error) {
	type plain RequestProxy
	// The default is written as a missing member.
	if p.Mode == ProxyInherit {
		p.Mode = ""
	}
	return stablejson.MarshalWithExtra(plain(p), p.Unknown)
}

func (p *RequestProxy) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	type plain RequestProxy
	var q plain
	u, err := decodeObject(data, &q)
	if err != nil {
		return err
	}
	q.Unknown = u
	// Read as it is written: the default as "".
	if q.Mode == ProxyInherit {
		q.Mode = ""
	}
	*p = RequestProxy(q)
	return nil
}

// ProxyOf returns the settings.proxy of request file f, so it can be read
// or changed in place; nil for a file that is not a request of a known
// kind.
func ProxyOf(f File) *RequestProxy {
	switch r := f.(type) {
	case *HTTPRequest:
		return &r.Settings.Proxy
	case *GraphQLRequest:
		return &r.Settings.Proxy
	case *SSERequest:
		return &r.Settings.Proxy
	case *TCPRequest:
		return &r.Settings.Proxy
	case *UDPRequest:
		return &r.Settings.Proxy
	case *WebSocketRequest:
		return &r.Settings.Proxy
	case *SocketIORequest:
		return &r.Settings.Proxy
	case *GRPCRequest:
		return &r.Settings.Proxy
	}
	return nil
}
