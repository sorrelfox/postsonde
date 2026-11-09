package collection

import (
	"slices"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// AuthDTO is a request's authentication (docs/auth.md). Type says
// which members are used; the members of the other types are kept in the
// DTO while the editor is open but left out of the file.
//
// The secret members (password, token, value, secret, consumerSecret,
// tokenSecret, secretKey, sessionToken, hawkKey and clientSecret) hold the value as the
// user typed it, including one kept in
// local storage rather than the project file: Request fills it in and
// UpdateRequest moves a typed value back out (package requestsecrets).
type AuthDTO struct {
	// Type is "inherit", "none", "basic", "bearer", "apikey", "digest",
	// "jwt", "oauth1", "awsv4", "hawk", "netrc", "oauth2", or a type of a newer
	// PostSonde; "" is written as "inherit".
	Type string `json:"type"`
	// Username and Password are basic and digest only, and the oauth2
	// password grant's (AUTH-09).
	Username string `json:"username"`
	Password string `json:"password"`
	// Prefix and Token are bearer only (Prefix jwt too). A null Prefix is
	// the default, "Bearer"; "" sends the token alone.
	Prefix *string `json:"prefix"`
	Token  string  `json:"token"`
	// Key, Value and In are apikey only (Key and In jwt too, In oauth1
	// and awsv4 too). In is "header" (also "") or "query", for oauth1
	// also "body".
	Key   string `json:"key"`
	Value string `json:"value"`
	In    string `json:"in"`
	// Secret is the jwt signing key as text (secret): an HMAC secret or a
	// PEM private key. Left out when "".
	Secret string `json:"secret,omitempty"`
	// JWT holds the other members of a jwt auth (AUTH-16); left out when
	// the auth has none.
	JWT *JWTAuthDTO `json:"jwt,omitempty"`
	// ConsumerSecret and TokenSecret are the oauth1 secrets (for
	// RSA-SHA1, ConsumerSecret is the PEM private key). Left out when "".
	ConsumerSecret string `json:"consumerSecret,omitempty"`
	TokenSecret    string `json:"tokenSecret,omitempty"`
	// OAuth1 holds the other members of an oauth1 auth (AUTH-15); left
	// out when the auth has none.
	OAuth1 *OAuth1AuthDTO `json:"oauth1,omitempty"`
	// SecretKey and SessionToken are the awsv4 secrets. Left out when "".
	SecretKey    string `json:"secretKey,omitempty"`
	SessionToken string `json:"sessionToken,omitempty"`
	// AWS holds the other members of an awsv4 auth (AUTH-17); left out
	// when the auth has none.
	AWS *AWSAuthDTO `json:"aws,omitempty"`
	// HawkKey is the hawk secret. Left out when "".
	HawkKey string `json:"hawkKey,omitempty"`
	// Hawk holds the other members of a hawk auth (AUTH-18); left out
	// when the auth has none.
	Hawk *HawkAuthDTO `json:"hawk,omitempty"`
	// ClientSecret is the oauth2 client secret. Left out when "".
	ClientSecret string `json:"clientSecret,omitempty"`
	// OAuth2 holds the other members of an oauth2 auth (AUTH-08, AUTH-12;
	// the token goes where Prefix, Key and In say); left out when the
	// auth has none.
	OAuth2 *OAuth2AuthDTO `json:"oauth2,omitempty"`
	// NetrcFile is the netrc file of a netrc auth (AUTH-19): absolute,
	// "~/" for the home directory, or relative to the project root; ""
	// uses NETRC or ~/.netrc. Left out when "".
	NetrcFile string `json:"netrcFile,omitempty"`
	// Disabled keeps a basic, bearer, apikey, digest, jwt, oauth1, awsv4,
	// hawk, netrc or oauth2 auth without sending it.
	Disabled bool `json:"disabled"`
	// LocalSecrets names the secret members whose value is kept in local
	// storage. When the member is also "", the value was typed on another
	// machine and is missing here. The editor removes the name when the
	// user edits the member; UpdateRequest adds it again for a typed value.
	LocalSecrets []string `json:"localSecrets"`
	Extra        string   `json:"extra"`
}

// JWTAuthDTO is a jwt auth's members besides the secret, prefix, key and
// in (projectfile.Auth, docs/auth.md).
type JWTAuthDTO struct {
	// Algorithm is HS256/384/512, RS256/384/512, PS256/384/512,
	// ES256/384/512 or EdDSA; "" is HS256.
	Algorithm string `json:"algorithm"`
	// KeyFile is the key's file, absolute or relative to the project
	// root; read instead of the secret when set.
	KeyFile string `json:"keyFile"`
	// SecretBase64 decodes an HMAC secret from base64.
	SecretBase64 bool `json:"secretBase64"`
	// Header and Payload are the JWT's header parameters and claims: JSON
	// object text with {{variables}}.
	Header  string `json:"header"`
	Payload string `json:"payload"`
	// IssuedAt, NotBefore and JWTID add iat, nbf and jti at send time;
	// ExpiresIn, when positive, adds exp that many seconds later.
	IssuedAt  bool  `json:"issuedAt"`
	NotBefore bool  `json:"notBefore"`
	JWTID     bool  `json:"jwtId"`
	ExpiresIn int64 `json:"expiresIn"`
}

// OAuth1AuthDTO is an oauth1 auth's members besides the secrets and in
// (projectfile.Auth, docs/auth.md).
type OAuth1AuthDTO struct {
	ConsumerKey string `json:"consumerKey"`
	AccessToken string `json:"accessToken"`
	// SignatureMethod is HMAC-SHA1 (also ""), HMAC-SHA256, RSA-SHA1 or
	// PLAINTEXT.
	SignatureMethod string `json:"signatureMethod"`
	// Callback, Verifier and Realm are sent when not "".
	Callback string `json:"callback"`
	Verifier string `json:"verifier"`
	Realm    string `json:"realm"`
	// OmitVersion leaves oauth_version out.
	OmitVersion bool `json:"omitVersion"`
	// Nonce and Timestamp override the values generated on every send.
	Nonce     string `json:"nonce"`
	Timestamp string `json:"timestamp"`
	// BodyHash adds oauth_body_hash to a request whose body is not a form.
	BodyHash bool `json:"bodyHash"`
}

// AWSAuthDTO is an awsv4 auth's members besides the secrets and in
// (projectfile.Auth, docs/auth.md).
type AWSAuthDTO struct {
	AccessKey string `json:"accessKey"`
	// Region and Service make the credential scope ("us-east-1", "s3").
	Region  string `json:"region"`
	Service string `json:"service"`
	// UnsignedPayload signs UNSIGNED-PAYLOAD instead of the body's hash.
	UnsignedPayload bool `json:"unsignedPayload"`
	// Expires is how long a presigned URL (in "query") is valid, in
	// seconds; 0 is the default, 3600.
	Expires int64 `json:"expires"`
}

// HawkAuthDTO is a hawk auth's members besides the key (projectfile.Auth,
// docs/auth.md).
type HawkAuthDTO struct {
	ID string `json:"id"`
	// Algorithm is sha256 (also "") or sha1.
	Algorithm string `json:"algorithm"`
	// Ext, App and Dlg are sent when not "" (Dlg only with App).
	Ext string `json:"ext"`
	App string `json:"app"`
	Dlg string `json:"dlg"`
	// PayloadHash covers the body with the MAC.
	PayloadHash bool `json:"payloadHash"`
}

// OAuth2AuthDTO is an oauth2 auth's members besides the client secret,
// prefix, key and in (projectfile.Auth, docs/auth.md).
type OAuth2AuthDTO struct {
	// GrantType is "client_credentials" (also ""), "authorization_code",
	// "password" or "implicit".
	GrantType string `json:"grantType"`
	// TokenURL is the token endpoint (not used by the implicit grant);
	// ClientID the client's ID.
	TokenURL string `json:"tokenUrl"`
	// AuthURL is the authorization endpoint of the authorization code and
	// implicit grants, RedirectURL their loopback redirect URL ("" is
	// http://127.0.0.1:<random port>/callback).
	AuthURL     string `json:"authUrl"`
	RedirectURL string `json:"redirectUrl"`
	ClientID    string `json:"clientId"`
	// ClientAuth is "header" (also "", client_secret_basic) or "body"
	// (client_secret_post).
	ClientAuth string `json:"clientAuth"`
	// Scope is the space-separated scope list; Audience and Resource are
	// sent when not ""; State is for the redirect grants.
	Scope    string `json:"scope"`
	Audience string `json:"audience"`
	Resource string `json:"resource"`
	State    string `json:"state"`
	// TokenParams are extra parameters of the token request.
	TokenParams []KeyValueDTO `json:"tokenParams"`
	// Issuer, when set, is the iss an OpenID Connect ID token must name;
	// IDToken sends the ID token instead of the access token (AUTH-14).
	Issuer  string `json:"issuer"`
	IDToken bool   `json:"idToken"`
}

// toOAuth2DTO returns a's oauth2 members; nil when a is not an oauth2
// auth and has none.
func toOAuth2DTO(a projectfile.Auth) *OAuth2AuthDTO {
	if a.Type != projectfile.AuthOAuth2 && a.GrantType == "" && a.TokenURL == "" && a.ClientID == "" && a.ClientAuth == "" &&
		a.Scope == "" && a.Audience == "" && a.Resource == "" && a.State == "" && len(a.TokenParams) == 0 && a.AuthURL == "" && a.RedirectURL == "" &&
		a.Issuer == "" && !a.IDToken {
		return nil
	}
	return &OAuth2AuthDTO{
		GrantType: a.GrantType, TokenURL: a.TokenURL, AuthURL: a.AuthURL, RedirectURL: a.RedirectURL, ClientID: a.ClientID, ClientAuth: a.ClientAuth,
		Scope: a.Scope, Audience: a.Audience, Resource: a.Resource, State: a.State,
		TokenParams: toKeyValueDTOs(a.TokenParams), Issuer: a.Issuer, IDToken: a.IDToken,
	}
}

func toAuthDTO(a projectfile.Auth) AuthDTO {
	local := slices.Clone(a.LocalSecrets)
	if local == nil {
		local = make([]string, 0)
	}
	var j *JWTAuthDTO
	if a.Type == projectfile.AuthJWT || a.Algorithm != "" || a.KeyFile != "" || a.JWTHeader != "" || a.Payload != "" {
		j = &JWTAuthDTO{
			Algorithm: a.Algorithm, KeyFile: a.KeyFile, SecretBase64: a.SecretBase64,
			Header: a.JWTHeader, Payload: a.Payload,
			IssuedAt: a.IssuedAt, NotBefore: a.NotBefore, JWTID: a.JWTID, ExpiresIn: a.ExpiresIn,
		}
	}
	o := &OAuth1AuthDTO{
		ConsumerKey: a.ConsumerKey, AccessToken: a.AccessToken, SignatureMethod: a.SignatureMethod,
		Callback: a.Callback, Verifier: a.Verifier, Realm: a.Realm, OmitVersion: a.OmitVersion,
		Nonce: a.Nonce, Timestamp: a.Timestamp, BodyHash: a.BodyHash,
	}
	if a.Type != projectfile.AuthOAuth1 && *o == (OAuth1AuthDTO{}) {
		o = nil
	}
	aws := &AWSAuthDTO{
		AccessKey: a.AccessKey, Region: a.Region, Service: a.Service,
		UnsignedPayload: a.UnsignedPayload, Expires: a.Expires,
	}
	if a.Type != projectfile.AuthAWSV4 && *aws == (AWSAuthDTO{}) {
		aws = nil
	}
	hawk := &HawkAuthDTO{
		ID: a.HawkID, Algorithm: a.HawkAlgorithm, Ext: a.Ext, App: a.App, Dlg: a.Dlg, PayloadHash: a.PayloadHash,
	}
	if a.Type != projectfile.AuthHawk && *hawk == (HawkAuthDTO{}) {
		hawk = nil
	}
	return AuthDTO{
		Type:           a.Type,
		Username:       a.Username,
		Password:       a.Password,
		Prefix:         clonePtr(a.Prefix),
		Token:          a.Token,
		Key:            a.Key,
		Value:          a.Value,
		In:             a.In,
		Secret:         a.JWTSecret,
		JWT:            j,
		ConsumerSecret: a.ConsumerSecret,
		TokenSecret:    a.TokenSecret,
		OAuth1:         o,
		SecretKey:      a.SecretKey,
		SessionToken:   a.SessionToken,
		AWS:            aws,
		HawkKey:        a.HawkKey,
		Hawk:           hawk,
		ClientSecret:   a.ClientSecret,
		OAuth2:         toOAuth2DTO(a),
		NetrcFile:      a.NetrcFile,
		Disabled:       a.Disabled,
		LocalSecrets:   local,
		Extra:          extraText(a.Unknown),
	}
}

// handshakeAuth is auth for a websocket, socketio or grpc request, whose
// auth was not part of the DTO before AUTH-31: a type "" is "none", as
// the file without an auth member reads.
func (c *converter) handshakeAuth(a AuthDTO) projectfile.Auth {
	if a.Type == "" {
		a.Type = projectfile.AuthNone
	}
	return c.auth(a)
}

func (c *converter) auth(a AuthDTO) projectfile.Auth {
	var local []string
	if len(a.LocalSecrets) > 0 {
		local = slices.Clone(a.LocalSecrets)
	}
	out := projectfile.Auth{
		Type:         a.Type,
		Username:     a.Username,
		Password:     a.Password,
		Prefix:       clonePtr(a.Prefix),
		Token:        a.Token,
		Key:          a.Key,
		Value:        a.Value,
		In:           a.In,
		JWTSecret:    a.Secret,
		NetrcFile:    a.NetrcFile,
		Disabled:     a.Disabled,
		LocalSecrets: local,
		Unknown:      c.extra("auth.extra", a.Extra),
	}
	if j := a.JWT; j != nil {
		out.Algorithm, out.KeyFile, out.SecretBase64 = j.Algorithm, j.KeyFile, j.SecretBase64
		out.JWTHeader, out.Payload = j.Header, j.Payload
		out.IssuedAt, out.NotBefore, out.JWTID, out.ExpiresIn = j.IssuedAt, j.NotBefore, j.JWTID, j.ExpiresIn
	}
	out.ConsumerSecret, out.TokenSecret = a.ConsumerSecret, a.TokenSecret
	if o := a.OAuth1; o != nil {
		out.ConsumerKey, out.AccessToken, out.SignatureMethod = o.ConsumerKey, o.AccessToken, o.SignatureMethod
		out.Callback, out.Verifier, out.Realm, out.OmitVersion = o.Callback, o.Verifier, o.Realm, o.OmitVersion
		out.Nonce, out.Timestamp, out.BodyHash = o.Nonce, o.Timestamp, o.BodyHash
	}
	out.SecretKey, out.SessionToken, out.HawkKey = a.SecretKey, a.SessionToken, a.HawkKey
	if w := a.AWS; w != nil {
		out.AccessKey, out.Region, out.Service = w.AccessKey, w.Region, w.Service
		out.UnsignedPayload, out.Expires = w.UnsignedPayload, w.Expires
	}
	if h := a.Hawk; h != nil {
		out.HawkID, out.HawkAlgorithm, out.Ext, out.App, out.Dlg, out.PayloadHash = h.ID, h.Algorithm, h.Ext, h.App, h.Dlg, h.PayloadHash
	}
	out.ClientSecret = a.ClientSecret
	if o := a.OAuth2; o != nil {
		out.GrantType, out.TokenURL, out.ClientID, out.ClientAuth = o.GrantType, o.TokenURL, o.ClientID, o.ClientAuth
		out.AuthURL, out.RedirectURL = o.AuthURL, o.RedirectURL
		out.Scope, out.Audience, out.Resource, out.State = o.Scope, o.Audience, o.Resource, o.State
		out.TokenParams = c.keyValues("auth.oauth2.tokenParams", o.TokenParams)
		out.Issuer, out.IDToken = o.Issuer, o.IDToken
	}

	return out
}
