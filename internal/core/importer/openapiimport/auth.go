package openapiimport

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/openapi"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// How a security scheme maps.
const (
	mapsDirect  = iota // basic, bearer, digest, API key in a header or the query
	mapsChanged        // OAuth 2.0, OpenID Connect (Bearer), a cookie API key
	mapsNot            // mutualTLS, other HTTP schemes, unknown schemes
)

func (c *conv) scheme(name string) *openapi.SecurityScheme {
	s, _ := c.doc.Components.SecuritySchemes.Get(name)
	return s
}

func (c *conv) classify(name string) int {
	s := c.scheme(name)
	if s == nil {
		return mapsNot
	}
	switch s.Type {
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "basic", "bearer", "digest":
			return mapsDirect
		}
	case "apiKey":
		switch s.In {
		case "header", "query":
			return mapsDirect
		case "cookie":
			return mapsChanged
		}
	case "oauth2", "openIdConnect":
		return mapsChanged
	}
	return mapsNot
}

// security maps a list of security requirements (alternatives) to auth:
// the first alternative whose schemes all map directly, else the first
// whose first scheme maps at all, else no auth with a warning. An empty
// list, or an empty alternative chosen that way, is no auth. In the
// chosen alternative the first scheme is the auth; further API key
// headers become header rows (extra), others are reported. what names
// the owner for messages.
func (c *conv) security(reqs []openapi.SecurityRequirement, what string) (projectfile.Auth, []projectfile.KeyValue, []importer.Diagnostic) {
	none := projectfile.Auth{Type: projectfile.AuthNone}
	if len(reqs) == 0 {
		return none, nil, nil
	}
	chosen := -1
	for i, r := range reqs {
		all := true
		for _, e := range r {
			all = all && c.classify(e.Name) == mapsDirect
		}
		if all {
			chosen = i
			break
		}
	}
	if chosen < 0 {
		for i, r := range reqs {
			if len(r) > 0 && c.classify(r[0].Name) != mapsNot {
				chosen = i
				break
			}
		}
	}
	if chosen < 0 {
		var names []string
		for _, r := range reqs {
			for _, e := range r {
				names = append(names, c.schemeLabel(e.Name))
			}
		}
		return none, nil, []importer.Diagnostic{{
			Code: CodeAuthUnsupported, Severity: importer.SeverityWarning,
			Message: "PostSonde has no auth for " + strings.Join(names, " or ") + ", which " + what + " requires: no auth is sent. " + c.unsupportedHint(reqs),
		}}
	}
	req := reqs[chosen]
	if len(req) == 0 {
		return none, nil, nil
	}
	auth, diags := c.schemeAuth(req[0].Name, req[0].Value)
	var extra []projectfile.KeyValue
	for _, e := range req[1:] {
		s := c.scheme(e.Name)
		if s != nil && s.Type == "apiKey" && s.In == "header" && s.Name != "" {
			v := c.secretVar(e.Name, "key", "The API key for the “"+e.Name+"” scheme (the "+s.Name+" header).", s.Description)
			extra = append(extra, projectfile.KeyValue{Name: s.Name, Value: "{{" + v + "}}", Description: "The “" + e.Name + "” API key."})
			continue
		}
		diags = append(diags, importer.Diagnostic{
			Code: CodeAuthExtra, Severity: importer.SeverityWarning,
			Message: "The security requirement of " + what + " also needs " + c.schemeLabel(e.Name) + " together with " + c.schemeLabel(req[0].Name) + "; it was not set up: add it by hand.",
		})
	}
	return auth, extra, diags
}

// unsupportedHint suggests what to do for schemes without auth.
func (c *conv) unsupportedHint(reqs []openapi.SecurityRequirement) string {
	for _, r := range reqs {
		for _, e := range r {
			if s := c.scheme(e.Name); s != nil && s.Type == "mutualTLS" {
				return "For mutual TLS, set up a client certificate for the host in the settings."
			}
		}
	}
	return "Set up the credentials by hand, e.g. as a header."
}

// schemeLabel names a scheme for messages: “name” (type).
func (c *conv) schemeLabel(name string) string {
	s := c.scheme(name)
	if s == nil {
		return "“" + name + "” (not in components.securitySchemes)"
	}
	t := s.Type
	switch s.Type {
	case "http":
		t = "HTTP " + s.Scheme
	case "oauth2":
		t = "OAuth 2.0"
	case "openIdConnect":
		t = "OpenID Connect"
	case "mutualTLS":
		t = "mutual TLS"
	case "apiKey":
		t = "API key"
	}
	return "“" + name + "” (" + t + ")"
}

// schemeAuth maps one scheme that maps (directly or changed). The
// credentials are {{variables}} of the root folder: <scheme>.username,
// <scheme>.password, <scheme>.token or <scheme>.key; the secret ones are
// secret variables without a value, so they are kept on this computer
// once filled in, and an environment can supply them.
func (c *conv) schemeAuth(name string, scopes []string) (projectfile.Auth, []importer.Diagnostic) {
	s := c.scheme(name)
	warn := func(msg string) []importer.Diagnostic {
		return []importer.Diagnostic{{Code: CodeAuthChanged, Severity: importer.SeverityWarning, Message: msg}}
	}
	switch s.Type {
	case "http":
		sch := strings.ToLower(s.Scheme)
		if sch == "bearer" {
			d := "The bearer token for the “" + name + "” scheme."
			if s.BearerFormat != "" {
				d = "The bearer token (" + s.BearerFormat + ") for the “" + name + "” scheme."
			}
			v := c.secretVar(name, "token", d, s.Description)
			return projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{" + v + "}}"}, nil
		}
		user := varName(name) + ".username"
		c.addVar(projectfile.Variable{Name: user, Description: joinSentences("The user name for the “"+name+"” scheme.", strings.TrimSpace(s.Description))})
		pass := c.secretVar(name, "password", "The password for the “"+name+"” scheme.", "")
		a := projectfile.Auth{Type: projectfile.AuthBasic, Username: "{{" + user + "}}", Password: "{{" + pass + "}}"}
		if sch == "digest" {
			a.Type = projectfile.AuthDigest
		}
		return a, nil
	case "apiKey":
		v := c.secretVar(name, "key", "The API key for the “"+name+"” scheme.", s.Description)
		switch s.In {
		case "query":
			return projectfile.Auth{Type: projectfile.AuthAPIKey, Key: s.Name, Value: "{{" + v + "}}", In: projectfile.APIKeyInQuery}, nil
		case "cookie":
			return projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "Cookie", Value: s.Name + "={{" + v + "}}", In: projectfile.APIKeyInHeader},
				warn("The “" + name + "” API key is sent as a cookie; it is imported as a Cookie header holding the key.")
		}
		return projectfile.Auth{Type: projectfile.AuthAPIKey, Key: s.Name, Value: "{{" + v + "}}", In: projectfile.APIKeyInHeader}, nil
	case "oauth2", "openIdConnect":
		v := c.secretVar(name, "token", "An access token for the “"+name+"” scheme.", s.Description)
		where := ""
		if s.Type == "openIdConnect" {
			if s.OpenIDConnectURL != "" {
				where = " (discovery document " + s.OpenIDConnectURL + ")"
			}
		} else if f := s.Flows; f != nil {
			for _, fl := range []*openapi.OAuthFlow{f.ClientCredentials, f.AuthorizationCode, f.Password, f.Implicit} {
				if fl == nil {
					continue
				}
				if fl.TokenURL != "" {
					where = " (token URL " + fl.TokenURL + ")"
				} else if fl.AuthorizationURL != "" {
					where = " (authorization URL " + fl.AuthorizationURL + ")"
				}
				break
			}
		}
		scope := ""
		if len(scopes) > 0 {
			scope = " with the scopes " + strings.Join(scopes, " ")
		}
		kind := "OAuth 2.0"
		if s.Type == "openIdConnect" {
			kind = "OpenID Connect"
		}
		return projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{" + v + "}}"},
			warn("PostSonde has no " + kind + " flow: the “" + name + "” scheme became Bearer auth with {{" + v + "}}. Get an access token" + scope + where + " and put it into that variable.")
	}
	return projectfile.Auth{Type: projectfile.AuthNone}, nil
}

// secretVar declares the secret variable <scheme>.<member> and returns
// its name.
func (c *conv) secretVar(scheme, member, desc, schemeDesc string) string {
	n := varName(scheme) + "." + member
	c.addVar(projectfile.Variable{Name: n, Secret: true, Description: joinSentences(desc, strings.TrimSpace(schemeDesc))})
	return n
}

// varName makes a scheme name safe inside {{ }}.
func varName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "auth"
	}
	return b.String()
}
