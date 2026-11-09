package insomnia

import (
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/oauth2"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// oauth2Auth maps Insomnia's OAuth 2.0 auth (AUTH-08, AUTH-12), which
// Insomnia sends in the Authorization header with tokenPrefix (default
// "Bearer"): grantType, accessTokenUrl, clientId, clientSecret, scope,
// state, audience, resource and credentialsInBody (client_secret_post).
//
// PostSonde runs all four grants. The authorization code grant (always
// with PKCE S256) and the implicit grant import their authorizationUrl,
// and their redirectUrl when it is a loopback URL (a web callback is
// reported and left out); the implicit grant's responseType "id_token"
// (Insomnia then sends the ID token) sends the ID token (AUTH-14). The
// password grant imports its username and password, which the import
// keeps out of the project like any typed secret. A token Insomnia
// obtained (its oauth2_token resources) is never imported.
func oauth2Auth(n *yaml.Node, c func(string) string, disabled bool, warn func(code, msg string) []importer.Diagnostic) (projectfile.Auth, []importer.Diagnostic) {
	grant := strings.TrimSpace(sget(n, "grantType"))
	if grant == "" {
		grant = projectfile.OAuth2AuthorizationCode
	}
	a := projectfile.Auth{
		Type:         projectfile.AuthOAuth2,
		GrantType:    grant,
		TokenURL:     c("accessTokenUrl"),
		ClientID:     c("clientId"),
		ClientSecret: c("clientSecret"),
		Scope:        c("scope"),
		State:        c("state"),
		Audience:     c("audience"),
		Resource:     c("resource"),
		Disabled:     disabled,
	}
	if isTrue(get(n, "credentialsInBody")) {
		a.ClientAuth = projectfile.OAuth2ClientAuthBody
	}
	if p := c("tokenPrefix"); strings.TrimSpace(p) != "" && p != projectfile.DefaultBearerPrefix {
		a.Prefix = &p
	}
	switch grant {
	case projectfile.OAuth2ClientCredentials:
		return a, nil
	case projectfile.OAuth2Password:
		a.Username, a.Password = c("username"), c("password")
		return a, nil
	case projectfile.OAuth2AuthorizationCode, projectfile.OAuth2Implicit:
		var changed []string
		a.AuthURL = c("authorizationUrl")
		if strings.TrimSpace(a.AuthURL) == "" {
			changed = append(changed, "Its authorization URL is not set: enter it before sending the request.")
		}
		redirect := strings.TrimSpace(c("redirectUrl"))
		if _, err := oauth2.ParseRedirectURL(redirect); err == nil || strings.Contains(redirect, "{{") {
			a.RedirectURL = redirect
		} else {
			changed = append(changed, "Its redirect URL "+strconv.Quote(clipText(redirect))+
				" is not on this computer and is left out: PostSonde receives the redirect on http://127.0.0.1:<port>/callback, which the authorization server must allow.")
		}
		if grant == projectfile.OAuth2Implicit && strings.TrimSpace(sget(n, "responseType")) == "id_token" {
			a.IDToken = true
			if !oauth2.HasOpenIDScope(a.Scope) {
				changed = append(changed, "It sends the ID token, which the server sends only when the scope asks for openid: add openid to the scope.")
			}
		}
		if len(changed) == 0 {
			return a, nil
		}
		return a, warn(CodeAuthChanged, "The OAuth 2.0 auth was imported with a change. "+strings.Join(changed, " "))
	}
	return a, warn(CodeAuthChanged, "The OAuth 2.0 auth was imported with a change. PostSonde does not know the grant "+clipText(grant)+".")
}
