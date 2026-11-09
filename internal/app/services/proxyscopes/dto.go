package proxyscopes

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
)

// ScopeDTO is the proxy of one scope. The password never leaves Go.
type ScopeDTO struct {
	// Mode is "inherit" (the next level out decides), "manual" (URL) or
	// "off" (no proxy).
	Mode string `json:"mode"`
	// URL is the scope's proxy without its password; kept while Mode is
	// not "manual". "" when none is set.
	URL string `json:"url"`
	// HasPassword is true when a password is stored for the URL's user.
	HasPassword bool `json:"hasPassword"`
	// Bypass is the scope's own bypass list; the app's applies too.
	Bypass []string `json:"bypass"`
}

// EnvironmentScopeDTO is the proxy scope of one environment.
type EnvironmentScopeDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Active is true for the active environment: only its scope applies.
	Active bool     `json:"active"`
	Scope  ScopeDTO `json:"scope"`
}

// EffectiveDTO is the proxy connections go through now.
type EffectiveDTO struct {
	// Proxy is the proxy without its password; "" for direct connections
	// (or when Error is set).
	Proxy string `json:"proxy"`
	// HTTPProxy is the proxy of http:// requests when it differs from
	// Proxy (the system proxy names one per scheme, NET-01); "" when it is
	// the same or http:// requests go directly while Proxy is set.
	// HTTPSplit is true when the system proxy chose them separately.
	HTTPProxy string `json:"httpProxy"`
	HTTPSplit bool   `json:"httpSplit"`
	// Level is the scope that decided: "app", "project" or "environment".
	Level string `json:"level"`
	// Source is, for the app level, where its proxy comes from
	// ("settings", "system", "ALL_PROXY", ...); the level otherwise; ""
	// for none.
	Source string `json:"source"`
	// RemoteDNS is true when the proxy resolves host names itself.
	RemoteDNS bool `json:"remoteDns"`
	// Bypass is the whole bypass list in effect (canonical entries), []
	// without a proxy. localhost is always bypassed too.
	Bypass []string `json:"bypass"`
	// NoProxyVar names the environment variable whose entries are in
	// Bypass (NO_PROXY of the ALL_PROXY proxy), "" for none.
	NoProxyVar string `json:"noProxyVar"`
	// Error is why connections fail now; null when they do not.
	Error *apperr.ErrorDTO `json:"error"`
}

// ScopesDTO is what Scopes returns.
type ScopesDTO struct {
	// Open is true while a project is open; the scopes need one.
	Open bool `json:"open"`
	// Project is the open project's scope.
	Project ScopeDTO `json:"project"`
	// Environments are the project's environments with their scopes,
	// sorted by name.
	Environments []EnvironmentScopeDTO `json:"environments"`
	// Effective is the proxy in effect now.
	Effective EffectiveDTO `json:"effective"`
	// Error is set when the scopes file or the scope passwords could not
	// be read (proxyscope.local_data_failed, requestsecrets.local_data_failed).
	Error *apperr.ErrorDTO `json:"error"`
}

// ScopeInputDTO is a change of one scope.
type ScopeInputDTO struct {
	// Level is "project" or "environment".
	Level string `json:"level"`
	// EnvironmentID names the environment of level "environment".
	EnvironmentID string `json:"environmentId"`
	// Mode is "inherit", "manual" or "off".
	Mode string `json:"mode"`
	// URL is the scope's proxy; required for "manual", kept for the
	// others.
	URL string `json:"url"`
	// Password of the URL's user: null keeps the stored one, "" removes it.
	Password *string `json:"password"`
	// Bypass replaces the scope's bypass list.
	Bypass []string `json:"bypass"`
}

// EventChanged is sent after SetScope changed a scope.
const EventChanged = Domain + ":changed"

// ChangedDTO is the payload of EventChanged.
type ChangedDTO struct {
	Level         string `json:"level"`
	EnvironmentID string `json:"environmentId"`
}

// Registering the event types its payload in the generated bindings and
// makes Wails reject an emit with the wrong payload type.
func init() {
	application.RegisterEvent[ChangedDTO](EventChanged)
}
