package request

import (
	"context"
	"errors"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Error codes of BuildProxy.
const (
	// CodeProxyPasswordMissing: the password of the request's own proxy
	// was typed on another machine and is not stored on this one (kind
	// invalid_argument). Details: field.
	CodeProxyPasswordMissing = "request.proxy_password_missing"
)

// Fields of a request's own proxy, in error details.
const (
	FieldProxyMode     = "settings.proxy.mode"
	FieldProxyURL      = "settings.proxy.url"
	FieldProxyPassword = "settings.proxy.password"
)

// ProxySourceRequest is the "source" detail of an error about a request's
// own proxy, as "settings", "project" or "environment" name the other
// levels (docs/network.md#proxy-scopes).
const ProxySourceRequest = "request"

// Proxy is a request's own proxy as it is sent (settings.proxy,
// docs/network.md#a-requests-own-proxy). The zero Proxy inherits the
// proxy in effect. It is comparable, so a client cache can key the
// connections of each proxy apart.
type Proxy struct {
	// Set is true when the request decides (custom or none), false when
	// it inherits.
	Set bool
	// URL is the custom proxy, its password included; the zero ProxyURL
	// for none.
	URL netx.ProxyURL
}

// Context returns ctx with the request's proxy for every dial made with it
// (netx.WithRequestProxy); ctx itself when the request inherits.
func (p Proxy) Context(ctx context.Context) context.Context {
	if !p.Set {
		return ctx
	}
	return netx.WithRequestProxy(ctx, p.URL)
}

// Mode returns projectfile.ProxyInherit, ProxyCustom or ProxyNone.
func (p Proxy) Mode() string {
	switch {
	case !p.Set:
		return projectfile.ProxyInherit
	case p.URL.IsZero():
		return projectfile.ProxyNone
	default:
		return projectfile.ProxyCustom
	}
}

// String describes p without a credential, for the timeline and logs:
// "" when it inherits, "none" for none, else the proxy URL without its
// password.
func (p Proxy) String() string {
	switch p.Mode() {
	case projectfile.ProxyInherit:
		return ""
	case projectfile.ProxyNone:
		return projectfile.ProxyNone
	default:
		return p.URL.String()
	}
}

// Secret returns the proxy's password, "" for none: a value to mask
// wherever the request's secrets are masked.
func (p Proxy) Secret() string { return p.URL.Password }

// BuildProxy returns the proxy request settings p ask for, with the
// {{variables}} of its URL and password rendered by expand (nil keeps
// them as written). p's password must have been merged from the local
// side file (requestsecrets.MergeProxy).
//
// It fails with request.invalid_setting (details: field
// "settings.proxy.mode") for a mode this version does not know,
// request.proxy_password_missing for a password saved on another machine,
// and netx.proxy_invalid_url (details: source "request", field) for a
// custom proxy without a usable URL or password. No message or detail
// contains the password.
func BuildProxy(p projectfile.RequestProxy, expand Expand) (Proxy, error) {
	switch p.EffectiveMode() {
	case projectfile.ProxyInherit:
		return Proxy{}, nil
	case projectfile.ProxyNone:
		return Proxy{Set: true}, nil
	case projectfile.ProxyCustom:
	default:
		return Proxy{}, invalidSetting(FieldProxyMode, "The request's proxy setting is not one this version of PostSonde knows.",
			"Choose Inherit, Custom proxy or No proxy under the request's Settings, Proxy.")
	}
	if expand == nil {
		expand = func(s string) (string, error) { return s, nil }
	}
	if requestsecrets.ProxyPasswordMissing(p) {
		return Proxy{}, errs.New(errs.InvalidArgument, "The password of the request's proxy was saved on another computer and is not stored on this one.").
			WithCode(CodeProxyPasswordMissing).
			WithHint("Type it again under the request's Settings, Proxy, or use a {{variable}} from a secret environment variable.").
			WithDetail("field", FieldProxyPassword)
	}
	raw, err := expand(p.URL)
	if err != nil {
		return Proxy{}, err
	}
	if strings.TrimSpace(raw) == "" {
		return Proxy{}, proxyError(errs.New(errs.InvalidArgument, "The request's custom proxy has no URL.").
			WithCode(netx.CodeProxyInvalidURL).
			WithHint("Enter the proxy's host and port under the request's Settings, Proxy, or choose Inherit."), FieldProxyURL)
	}
	u, err := netx.ParseProxyURL(raw)
	if err != nil {
		return Proxy{}, proxyError(err, FieldProxyURL)
	}
	pw, err := expand(p.Password)
	if err != nil {
		return Proxy{}, err
	}
	if u, err = u.WithPassword(pw); err != nil {
		return Proxy{}, proxyError(err, FieldProxyPassword)
	}
	return Proxy{Set: true, URL: u}, nil
}

// proxyError adds the request level and the field to a proxy error.
func proxyError(err error, field string) error {
	e, ok := errors.AsType[*errs.Error](err)
	if !ok {
		return err
	}
	return e.WithDetail("source", ProxySourceRequest).WithDetail("field", field)
}
