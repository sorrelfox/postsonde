package collection

import (
	"slices"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// RequestProxyDTO is a request's own proxy, settings.proxy of every kind
// (docs/network.md#a-requests-own-proxy, docs/file-format.md#settings).
type RequestProxyDTO struct {
	// Mode is "inherit" (also ""), "custom" or "none". A mode this
	// version does not know is kept, and sending refuses it.
	Mode string `json:"mode"`
	// URL is the custom proxy, scheme://[user@]host[:port], {{variables}}
	// unresolved; kept in the other modes too.
	URL string `json:"url"`
	// Password is the proxy user's password: a {{variable}} reference,
	// or a typed value, which UpdateRequest keeps in the local side file,
	// never in the project file.
	Password string `json:"password"`
	// LocalSecrets is ["password"] when the password is kept in local
	// storage. With Password "" too, it was typed on another machine and
	// is missing here. The editor removes the name when the user edits
	// the password.
	LocalSecrets []string `json:"localSecrets"`
	Extra        string   `json:"extra"`
}

// toProxyDTO maps settings.proxy; nil for the default (inherit, nothing
// else kept).
func toProxyDTO(p projectfile.RequestProxy) *RequestProxyDTO {
	if p.IsZero() {
		return nil
	}
	local := slices.Clone(p.LocalSecrets)
	if local == nil {
		local = []string{}
	}
	return &RequestProxyDTO{
		Mode:         p.EffectiveMode(),
		URL:          p.URL,
		Password:     p.Password,
		LocalSecrets: local,
		Extra:        extraText(p.Unknown),
	}
}

// proxy builds settings.proxy from the editor's; nil inherits.
func (c *converter) proxy(p *RequestProxyDTO) projectfile.RequestProxy {
	if p == nil {
		return projectfile.RequestProxy{}
	}
	mode := p.Mode
	if mode == projectfile.ProxyInherit {
		mode = ""
	}
	var local []string
	if len(p.LocalSecrets) > 0 {
		local = slices.Clone(p.LocalSecrets)
	}
	return projectfile.RequestProxy{
		Mode:         mode,
		URL:          p.URL,
		Password:     p.Password,
		LocalSecrets: local,
		Unknown:      c.extra("settings.proxy.extra", p.Extra),
	}
}
