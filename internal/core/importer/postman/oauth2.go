package postman

import (
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// Postman's OAuth 2.0 grant types (grant_type) and PostSonde's.
var postmanGrants = map[string]string{
	"client_credentials":           projectfile.OAuth2ClientCredentials,
	"authorization_code":           projectfile.OAuth2AuthorizationCode,
	"authorization_code_with_pkce": projectfile.OAuth2AuthorizationCode,
	"password_credentials":         projectfile.OAuth2Password,
	"implicit":                     projectfile.OAuth2Implicit,
}

// oauth2Auth maps Postman's OAuth 2.0 auth (AUTH-08, AUTH-12): the grant
// (Postman's default is the authorization code grant), the access token
// URL, client ID and secret, scope, state, client_authentication (header
// or body), where the token goes (addTokenTo: the header with
// headerPrefix, or the access_token query parameter), and the token
// request's extra parameters (tokenRequestParams sent in the body;
// audience and resource become their own fields).
//
// PostSonde runs all four grants. The authorization code grant (with or
// without Postman's PKCE, PostSonde always uses S256) and the implicit
// grant import their authUrl, and their redirect_uri when it is a loopback
// URL (PostSonde receives the redirect on this computer; a web callback
// such as Postman's own is reported and left out). The password grant
// (password_credentials) imports its username and password, which the
// import keeps out of the project like any typed secret. A token Postman
// obtained is never imported: PostSonde gets its own and keeps it out of
// the project.
func oauth2Auth(n *yaml.Node, params map[string]string, conv func(string) string, loc importer.Location) (projectfile.Auth, []importer.Diagnostic) {
	p := func(key string) string { return conv(params[key]) }
	pgrant := strings.TrimSpace(params["grant_type"])
	if pgrant == "" {
		pgrant = "authorization_code"
	}
	a := projectfile.Auth{
		Type:         projectfile.AuthOAuth2,
		GrantType:    postmanGrants[pgrant],
		TokenURL:     p("accessTokenUrl"),
		ClientID:     p("clientId"),
		ClientSecret: p("clientSecret"),
		Scope:        p("scope"),
		State:        p("state"),
		Audience:     p("audience"),
		Resource:     p("resource"),
	}
	if strings.EqualFold(strings.TrimSpace(params["client_authentication"]), "body") {
		a.ClientAuth = projectfile.OAuth2ClientAuthBody
	}
	if strings.EqualFold(strings.TrimSpace(params["addTokenTo"]), "queryParams") {
		a.In = projectfile.APIKeyInQuery
	} else if prefix, ok := params["headerPrefix"]; ok && prefix != projectfile.DefaultBearerPrefix {
		v := p("headerPrefix")
		a.Prefix = &v
	}
	var changed []string
	switch a.GrantType {
	case projectfile.OAuth2AuthorizationCode, projectfile.OAuth2Implicit:
		changed = append(changed, authCodeFields(&a, p("authUrl"), p("redirect_uri"))...)
	case projectfile.OAuth2Password:
		a.Username, a.Password = p("username"), p("password")
	case "":
		a.GrantType = pgrant
		changed = append(changed, "PostSonde does not know the grant "+clip(pgrant)+".")
	}
	var skipped []string
	for _, it := range items(authParamNode(n, "tokenRequestParams")) {
		key := sget(it, "key")
		if strings.TrimSpace(key) == "" {
			continue
		}
		sendAs := strings.TrimSpace(sget(it, "send_as"))
		if sendAs != "" && sendAs != "request_body" {
			skipped = append(skipped, clip(key))
			continue
		}
		value := conv(str(get(it, "value")))
		enabled := !isFalse(get(it, "enabled"))
		switch {
		case key == "audience" && a.Audience == "" && enabled:
			a.Audience = value
		case key == "resource" && a.Resource == "" && enabled:
			a.Resource = value
		default:
			a.TokenParams = append(a.TokenParams, projectfile.KeyValue{Name: key, Value: value, Disabled: !enabled})
		}
	}
	if len(skipped) > 0 {
		changed = append(changed, "Token request parameters sent in a header or the URL are not imported: "+strings.Join(skipped, ", ")+".")
	}
	if len(changed) == 0 {
		return a, nil
	}
	return a, []importer.Diagnostic{{
		Code: CodeAuthChanged, Severity: importer.SeverityWarning, Source: loc,
		Message: "The OAuth 2.0 auth was imported with a change. " + strings.Join(changed, " "),
	}}
}

// authCodeFields sets the authorization URL and the redirect URL of an
// authorization code or implicit grant and returns what changed: a missing
// authorization URL, a redirect URL that is not on this computer (left
// out, so PostSonde uses http://127.0.0.1:<random port>/callback). A
// redirect URL with {{variables}} is kept as it is.
func authCodeFields(a *projectfile.Auth, authURL, redirect string) []string {
	var changed []string
	a.AuthURL = authURL
	if strings.TrimSpace(authURL) == "" {
		changed = append(changed, "Its authorization URL is not set: enter it before sending the request.")
	}
	redirect = strings.TrimSpace(redirect)
	if _, err := oauth2.ParseRedirectURL(redirect); err == nil || strings.Contains(redirect, "{{") {
		a.RedirectURL = redirect
	} else {
		changed = append(changed, "Its callback URL "+strconv.Quote(clip(redirect))+
			" is not on this computer and is left out: PostSonde receives the redirect on http://127.0.0.1:<port>/callback, which the authorization server must allow.")
	}
	return changed
}

// authParamNode returns the value node of parameter key of an auth type's
// parameters (v2.1's list of {key, value}, or v2.0's object); nil when it
// has none.
func authParamNode(n *yaml.Node, key string) *yaml.Node {
	n = n.Resolve()
	switch {
	case n == nil:
	case n.Kind == yaml.SequenceNode:
		for _, it := range n.Items {
			if sget(it, "key") == key {
				return get(it, "value")
			}
		}
	case isMap(n):
		return get(n, key)
	}
	return nil
}

// pmTokenParam is an item of Postman's tokenRequestParams.
type pmTokenParam struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
	SendAs  string `json:"send_as"`
}

// oauth2Params writes an OAuth 2.0 auth as Postman's (EXP-03): the grant,
// the token URL, the client, scope and state, client_authentication, where
// the token goes, and the audience, resource and extra parameters as
// tokenRequestParams sent in the body; the authorization URL and callback
// of the authorization code and implicit grants; the username and password
// of the password grant (the password as a secret is: a {{variable}}
// reference, or empty). Postman sends the token in the Authorization
// header or the access_token parameter only: another header or parameter
// name is reported, as are the ID token option and the expected issuer,
// which Postman's collection format has no member for. No token is
// exported: Postman gets its own.
func oauth2Params(a projectfile.Auth, secret func(string, string) string, plain func(string) string) ([]pmAuthParam, []string) {
	var changed []string
	grant := a.GrantType
	switch grant {
	case "", projectfile.OAuth2ClientCredentials:
		grant = "client_credentials"
	case projectfile.OAuth2AuthorizationCode:
		// PostSonde always sends an S256 challenge.
		grant = "authorization_code_with_pkce"
	case projectfile.OAuth2Password:
		grant = "password_credentials"
	}
	clientAuth := "header"
	if a.ClientAuth == projectfile.OAuth2ClientAuthBody {
		clientAuth = "body"
	}
	ps := []pmAuthParam{
		textParam("grant_type", grant),
		textParam("accessTokenUrl", plain(a.TokenURL)),
		textParam("clientId", plain(a.ClientID)),
		textParam("clientSecret", secret("clientSecret", "client secret")),
		textParam("scope", plain(a.Scope)),
		textParam("state", plain(a.State)),
		textParam("client_authentication", clientAuth),
	}
	switch a.GrantType {
	case projectfile.OAuth2AuthorizationCode, projectfile.OAuth2Implicit:
		ps = append(ps, textParam("authUrl", plain(a.AuthURL)))
		if a.GrantType == projectfile.OAuth2AuthorizationCode {
			ps = append(ps, textParam("challengeAlgorithm", "S256"))
		}
		if a.RedirectURL != "" {
			ps = append(ps, textParam("redirect_uri", plain(a.RedirectURL)))
		}
	case projectfile.OAuth2Password:
		ps = append(ps, textParam("username", plain(a.Username)), textParam("password", secret("password", "password")))
	}
	if a.IDToken {
		changed = append(changed, "Postman's collection has no option to send the ID token: it sends the access token.")
	}
	if a.Issuer != "" {
		changed = append(changed, "The expected ID token issuer is not exported.")
	}
	if a.In == projectfile.APIKeyInQuery {
		ps = append(ps, textParam("addTokenTo", "queryParams"))
		if k := strings.TrimSpace(a.Key); k != "" && k != projectfile.DefaultOAuth2QueryParam {
			changed = append(changed, "Postman sends the token in the access_token parameter, not "+strconv.Quote(clip(k))+".")
		}
	} else {
		ps = append(ps, textParam("addTokenTo", "header"), textParam("headerPrefix", plain(a.BearerPrefix())))
		if k := strings.TrimSpace(a.Key); k != "" && !strings.EqualFold(k, "Authorization") {
			changed = append(changed, "Postman sends the token in the Authorization header, not "+strconv.Quote(clip(k))+".")
		}
	}
	var extra []pmTokenParam
	for _, p := range []struct{ name, value string }{{"audience", a.Audience}, {"resource", a.Resource}} {
		if p.value != "" {
			extra = append(extra, pmTokenParam{Key: p.name, Value: plain(p.value), Enabled: true, SendAs: "request_body"})
		}
	}
	for _, p := range a.TokenParams {
		extra = append(extra, pmTokenParam{Key: plain(p.Name), Value: plain(p.Value), Enabled: !p.Disabled, SendAs: "request_body"})
	}
	if len(extra) > 0 {
		ps = append(ps, pmAuthParam{Key: "tokenRequestParams", Value: extra, Type: "any"})
	}
	return ps, changed
}
