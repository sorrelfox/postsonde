package projectfile

import "slices"

// Auth types this version knows. A type this version does not know is
// kept with all its members.
const (
	// AuthInherit uses the closest parent folder's auth (AUTH-01).
	AuthInherit = "inherit"
	// AuthNone sends no credentials (AUTH-02).
	AuthNone = "none"
	// AuthBasic sends Username and Password as Authorization: Basic
	// (AUTH-03).
	AuthBasic = "basic"
	// AuthBearer sends Authorization: <Prefix> <Token> (AUTH-04).
	AuthBearer = "bearer"
	// AuthAPIKey sends Key and Value in a header or the query string
	// (AUTH-05).
	AuthAPIKey = "apikey"
	// AuthDigest answers a server's Digest challenge with Username and
	// Password (AUTH-06, RFC 7616).
	AuthDigest = "digest"
	// AuthJWT builds and signs a JWT at send time from Algorithm,
	// JWTHeader, Payload and the key (Secret or KeyFile), and sends it
	// as Authorization: <Prefix> <jwt>, another header or a query
	// parameter (AUTH-16).
	AuthJWT = "jwt"
	// AuthOAuth1 signs the request with OAuth 1.0 at send time (RFC 5849,
	// AUTH-15): ConsumerKey, ConsumerSecret, AccessToken, TokenSecret and
	// SignatureMethod, sent in the Authorization header, the query string
	// or a form body (In).
	AuthOAuth1 = "oauth1"
	// AuthAWSV4 signs the request with AWS Signature Version 4 at send
	// time (AUTH-17): AccessKey, SecretKey, SessionToken, Region and
	// Service, sent in the Authorization header or, with In "query", as a
	// presigned URL.
	AuthAWSV4 = "awsv4"
	// AuthHawk signs the request with Hawk at send time (AUTH-18): HawkID,
	// HawkKey and HawkAlgorithm, sent in the Authorization header.
	AuthHawk = "hawk"
	// AuthNetrc sends Authorization: Basic with the login and password of
	// the request host's entry in a .netrc file, read at send time
	// (AUTH-19): NetrcFile, else the NETRC environment variable, else
	// ~/.netrc (~/_netrc on Windows). Nothing of the file is stored.
	AuthNetrc = "netrc"
	// AuthOAuth2 gets an OAuth 2.0 access token from a token endpoint
	// (AUTH-07…10, AUTH-12, AUTH-14): GrantType, TokenURL, AuthURL,
	// RedirectURL, ClientID, ClientSecret, ClientAuth, Scope, Audience,
	// Resource, State, TokenParams, Username and Password (the password
	// grant), Issuer and IDToken (OpenID Connect); the
	// token is sent as Authorization: <Prefix> <token>, another header
	// (Key) or a query parameter (In "query"). Tokens are never stored in
	// the file (package oauth2).
	AuthOAuth2 = "oauth2"
)

// Where an API key is sent, for Auth.In. An OAuth 1.0 auth can also be
// sent in a form body (AuthInBody).
const (
	APIKeyInHeader = "header"
	APIKeyInQuery  = "query"
	AuthInBody     = "body"
)

// DefaultBearerPrefix is the prefix of a bearer token when the file has no
// prefix member.
const DefaultBearerPrefix = "Bearer"

// DefaultJWTQueryParam is the query parameter a JWT sent in the query
// string goes in when Key is empty (RFC 6750 section 2.3).
const DefaultJWTQueryParam = "access_token"

// OAuth 2.0 grant types (Auth.GrantType); "" is OAuth2ClientCredentials.
// This version gets tokens with all four; another is kept as it is and
// refused at send time.
const (
	OAuth2ClientCredentials = "client_credentials"
	OAuth2AuthorizationCode = "authorization_code"
	OAuth2Password          = "password"
	OAuth2Implicit          = "implicit"
)

// Where an oauth2 auth sends its client credentials (Auth.ClientAuth):
// in the Authorization header (client_secret_basic, also "") or in the
// token request's body (client_secret_post).
const (
	OAuth2ClientAuthHeader = "header"
	OAuth2ClientAuthBody   = "body"
)

// DefaultOAuth2QueryParam is the query parameter an OAuth 2.0 token sent
// in the query string goes in when Key is empty (RFC 6750 section 2.3).
const DefaultOAuth2QueryParam = "access_token"

// DefaultAWSExpires is how long, in seconds, a presigned AWS URL is valid
// when Expires is 0.
const DefaultAWSExpires = 3600

// Auth is the authentication of a request or folder. Type says which of
// the other members apply; for a type this version knows, the members of
// the other types are left out of the file (see MarshalJSON). docs/auth.md
// describes the schemes.
//
// Secret members (Password, Token, Value, JWTSecret, ConsumerSecret,
// TokenSecret, SecretKey, SessionToken, HawkKey, ClientSecret) should hold only what may be
// shared: "" or a value made of {{variable}} references. A value typed as
// it is lives in PostSonde's local storage (package requestsecrets) and
// LocalSecrets names the member. Marshal does not enforce this, so a
// hand-written file can still be renamed or moved; the collection service
// moves typed secrets out of every request it saves.
type Auth struct {
	// Type is "inherit", "none", "basic", "bearer", "apikey", "digest",
	// "jwt", "oauth1", "awsv4", "hawk", "netrc", "oauth2", or a type of a newer
	// PostSonde. Empty is written as "inherit".
	Type string `json:"type"`
	// Username is the Basic or Digest user-id; Password its password
	// (secret).
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// Prefix goes before a bearer token or a JWT sent in a header,
	// separated by a space. nil means DefaultBearerPrefix, which is always
	// written as nil; "" sends the token alone.
	Prefix *string `json:"prefix,omitempty"`
	// Token is the bearer token (secret).
	Token string `json:"token,omitempty"`
	// Key is the API key's header or query parameter name, Value the key
	// itself (secret), and In "header" (also when empty) or "query". A
	// JWT uses Key and In the same way: its header ("" is
	// Authorization) or query parameter ("" is DefaultJWTQueryParam).
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
	In    string `json:"in,omitempty"`
	// Algorithm is the JWT's "alg" (HS256…EdDSA, package jwt); "" is
	// HS256.
	Algorithm string `json:"algorithm,omitempty"`
	// JWTSecret is the JWT signing key as text (secret): the HMAC secret, or
	// a PEM private key. KeyFile, when set, is read instead: a path,
	// absolute or relative to the project root.
	JWTSecret string `json:"secret,omitempty"`
	KeyFile   string `json:"keyFile,omitempty"`
	// SecretBase64 decodes an HMAC secret (typed or from KeyFile) from
	// base64 before signing.
	SecretBase64 bool `json:"secretBase64,omitempty"`
	// JWTHeader is JSON object text of the JWT header's parameters other
	// than "alg"; Payload the claims set, JSON object text. Both may hold
	// {{variables}}.
	JWTHeader string `json:"jwtHeader,omitempty"`
	Payload   string `json:"payload,omitempty"`
	// IssuedAt, NotBefore and JWTID add the "iat", "nbf" and "jti" claims
	// at send time; ExpiresIn, when positive, adds "exp" that many
	// seconds after it. A claim the payload has is kept as typed.
	IssuedAt  bool  `json:"issuedAt,omitempty"`
	NotBefore bool  `json:"notBefore,omitempty"`
	JWTID     bool  `json:"jwtId,omitempty"`
	ExpiresIn int64 `json:"expiresIn,omitempty"`
	// ConsumerKey and ConsumerSecret (secret) are an OAuth 1.0 client's
	// credentials, AccessToken and TokenSecret (secret) its token
	// credentials; for RSA-SHA1, ConsumerSecret holds the PEM private key.
	// SignatureMethod is HMAC-SHA1 (also ""), HMAC-SHA256, RSA-SHA1 or
	// PLAINTEXT. Callback, Verifier and Realm are sent when not "";
	// OmitVersion leaves oauth_version out. Nonce and Timestamp, when "",
	// are generated on every send. BodyHash adds oauth_body_hash to a
	// request whose body is not a form. In is "header" (also ""), "query"
	// or "body".
	ConsumerKey     string `json:"consumerKey,omitempty"`
	ConsumerSecret  string `json:"consumerSecret,omitempty"`
	AccessToken     string `json:"accessToken,omitempty"`
	TokenSecret     string `json:"tokenSecret,omitempty"`
	SignatureMethod string `json:"signatureMethod,omitempty"`
	Callback        string `json:"callback,omitempty"`
	Verifier        string `json:"verifier,omitempty"`
	Realm           string `json:"realm,omitempty"`
	OmitVersion     bool   `json:"omitVersion,omitempty"`
	Nonce           string `json:"nonce,omitempty"`
	Timestamp       string `json:"timestamp,omitempty"`
	BodyHash        bool   `json:"bodyHash,omitempty"`
	// AccessKey and SecretKey (secret) are the AWS credentials,
	// SessionToken (secret) a temporary credential's token, sent as
	// X-Amz-Security-Token when not "". Region and Service make the
	// credential scope. UnsignedPayload signs UNSIGNED-PAYLOAD instead of
	// the body's hash. In is "header" (also "") or "query" (a presigned
	// URL valid for Expires seconds; 0 is DefaultAWSExpires).
	AccessKey       string `json:"accessKey,omitempty"`
	SecretKey       string `json:"secretKey,omitempty"`
	SessionToken    string `json:"sessionToken,omitempty"`
	Region          string `json:"region,omitempty"`
	Service         string `json:"service,omitempty"`
	UnsignedPayload bool   `json:"unsignedPayload,omitempty"`
	Expires         int64  `json:"expires,omitempty"`
	// HawkID and HawkKey (secret) are the Hawk credentials, HawkAlgorithm
	// "sha256" (also "") or "sha1". Ext is sent and covered as it is;
	// App and Dlg are Oz's application and delegated-by IDs. PayloadHash
	// covers the body with the MAC.
	HawkID        string `json:"hawkId,omitempty"`
	HawkKey       string `json:"hawkKey,omitempty"`
	HawkAlgorithm string `json:"hawkAlgorithm,omitempty"`
	Ext           string `json:"ext,omitempty"`
	App           string `json:"app,omitempty"`
	Dlg           string `json:"dlg,omitempty"`
	PayloadHash   bool   `json:"payloadHash,omitempty"`
	// NetrcFile is the netrc file of a netrc auth: absolute, "~/" for the
	// home directory, or relative to the project root; "" uses NETRC or
	// the home directory's file. Only the path is stored.
	NetrcFile string `json:"netrcFile,omitempty"`
	// GrantType is an oauth2 auth's grant ("client_credentials", also "",
	// "authorization_code", "password" or "implicit"). TokenURL is the
	// token endpoint (not used by the implicit grant); ClientID and ClientSecret
	// (secret) the client's credentials, sent in the Authorization header
	// (ClientAuth "header", also "") or the body ("body"). Scope is the
	// space-separated scope list; Audience and Resource are sent when not
	// "". AuthURL is the authorization endpoint of the authorization code
	// and implicit grants and RedirectURL their loopback redirect URL ("" is
	// http://127.0.0.1:<random port>/callback); State is sent with the
	// redirect grants' authorization request ("" is a random one). The
	// password grant sends Username and Password (secret), the members of
	// basic auth. Issuer, when set, is the iss an OpenID Connect ID token
	// must name; IDToken sends the ID token instead of the access token
	// (AUTH-14).
	// TokenParams are extra parameters of the token request. The token
	// goes where Key, In and Prefix say, as for a JWT: a header ("" is
	// Authorization) with the prefix, or with In "query" a query parameter
	// ("" is access_token).
	GrantType    string     `json:"grantType,omitempty"`
	TokenURL     string     `json:"tokenUrl,omitempty"`
	AuthURL      string     `json:"authUrl,omitempty"`
	RedirectURL  string     `json:"redirectUrl,omitempty"`
	ClientID     string     `json:"clientId,omitempty"`
	ClientSecret string     `json:"clientSecret,omitempty"`
	ClientAuth   string     `json:"clientAuth,omitempty"`
	Scope        string     `json:"scope,omitempty"`
	Audience     string     `json:"audience,omitempty"`
	Resource     string     `json:"resource,omitempty"`
	State        string     `json:"state,omitempty"`
	TokenParams  []KeyValue `json:"tokenParams,omitempty"`
	Issuer       string     `json:"issuer,omitempty"`
	IDToken      bool       `json:"idToken,omitempty"`
	// Disabled keeps a basic, bearer, apikey, digest, jwt, oauth1, awsv4,
	// hawk, netrc or oauth2 configuration without sending it: nothing is sent, and
	// no parent's auth is used either.
	Disabled bool `json:"disabled,omitempty"`
	// LocalSecrets names the secret members ("password", "token", "value",
	// "secret", "consumerSecret", "tokenSecret", "secretKey",
	// "sessionToken", "hawkKey", "clientSecret") whose value is kept in
	// local storage on the machine where it was typed. The member itself
	// is then "" in the file.
	LocalSecrets []string `json:"localSecrets,omitempty"`
	Unknown      Unknown  `json:"-"`
}

// IsKnownAuthType reports whether this version knows the auth type t; ""
// counts as "inherit".
func IsKnownAuthType(t string) bool {
	switch t {
	case "", AuthInherit, AuthNone, AuthBasic, AuthBearer, AuthAPIKey, AuthDigest, AuthJWT, AuthOAuth1, AuthAWSV4, AuthHawk, AuthNetrc, AuthOAuth2:
		return true
	}
	return false
}

// SecretMembers returns the names of the secret members of a's type, in
// order: "password", "token", "value" or "secret", and for oauth1
// "consumerSecret" and "tokenSecret", for awsv4 "secretKey" and
// "sessionToken", for hawk "hawkKey", for oauth2 "clientSecret" and
// "password" (the password grant's); none for a type without one
// (inherit, none, and types this version does not know).
func (a Auth) SecretMembers() []string {
	switch a.Type {
	case AuthBasic, AuthDigest:
		return []string{"password"}
	case AuthBearer:
		return []string{"token"}
	case AuthAPIKey:
		return []string{"value"}
	case AuthJWT:
		return []string{"secret"}
	case AuthOAuth1:
		return []string{"consumerSecret", "tokenSecret"}
	case AuthAWSV4:
		return []string{"secretKey", "sessionToken"}
	case AuthHawk:
		return []string{"hawkKey"}
	case AuthOAuth2:
		return []string{"clientSecret", "password"}
	}
	return nil
}

// SecretMember returns the first secret member of a's type
// (SecretMembers); "" for a type without one.
func (a Auth) SecretMember() string {
	if m := a.SecretMembers(); len(m) > 0 {
		return m[0]
	}
	return ""
}

// Secret returns the value of a's first secret member; "" without one.
func (a Auth) Secret() string { return a.SecretOf(a.SecretMember()) }

// WithSecret returns a with its first secret member set to v. A type
// without a secret member is returned unchanged.
func (a Auth) WithSecret(v string) Auth { return a.WithSecretOf(a.SecretMember(), v) }

// SecretOf returns the value of the secret member named member of a's
// type; "" for a member the type does not have.
func (a Auth) SecretOf(member string) string {
	if p := a.secretField(member); p != nil {
		return *p
	}
	return ""
}

// WithSecretOf returns a with the secret member named member set to v; a
// is returned unchanged when its type has no such member.
func (a Auth) WithSecretOf(member, v string) Auth {
	if p := a.secretField(member); p != nil {
		*p = v
	}
	return a
}

func (a *Auth) secretField(member string) *string {
	if !slices.Contains(a.SecretMembers(), member) {
		return nil
	}
	switch member {
	case "password":
		return &a.Password
	case "token":
		return &a.Token
	case "value":
		return &a.Value
	case "secret":
		return &a.JWTSecret
	case "consumerSecret":
		return &a.ConsumerSecret
	case "tokenSecret":
		return &a.TokenSecret
	case "secretKey":
		return &a.SecretKey
	case "sessionToken":
		return &a.SessionToken
	case "hawkKey":
		return &a.HawkKey
	case "clientSecret":
		return &a.ClientSecret
	}
	return nil
}

// SecretIsLocal reports whether LocalSecrets names one of a's secret
// members.
func (a Auth) SecretIsLocal() bool {
	for _, m := range a.SecretMembers() {
		if slices.Contains(a.LocalSecrets, m) {
			return true
		}
	}
	return false
}

// IsLocal reports whether LocalSecrets names member, a secret member of
// a's type.
func (a Auth) IsLocal(member string) bool {
	return slices.Contains(a.SecretMembers(), member) && slices.Contains(a.LocalSecrets, member)
}

// BearerPrefix returns the prefix a bearer token is sent with.
func (a Auth) BearerPrefix() string {
	if a.Prefix == nil {
		return DefaultBearerPrefix
	}
	return *a.Prefix
}

// normalized returns a as it is written: for a known type only its own
// members, with the defaults (prefix "Bearer", in "header") left out. A
// type this version does not know keeps every member.
func (a Auth) normalized() Auth {
	if a.Type == "" {
		a.Type = AuthInherit
	}
	if !IsKnownAuthType(a.Type) {
		return a
	}
	n := Auth{Type: a.Type, Unknown: a.Unknown}
	switch a.Type {
	case AuthBasic, AuthDigest:
		n.Username, n.Password = a.Username, a.Password
	case AuthBearer:
		if a.Prefix != nil && *a.Prefix != DefaultBearerPrefix {
			p := *a.Prefix
			n.Prefix = &p
		}
		n.Token = a.Token
	case AuthAPIKey:
		n.Key, n.Value = a.Key, a.Value
		if a.In != APIKeyInHeader {
			n.In = a.In
		}
	case AuthJWT:
		if a.Prefix != nil && *a.Prefix != DefaultBearerPrefix {
			p := *a.Prefix
			n.Prefix = &p
		}
		n.Key = a.Key
		if a.In != APIKeyInHeader {
			n.In = a.In
		}
		n.Algorithm, n.JWTSecret, n.KeyFile, n.SecretBase64 = a.Algorithm, a.JWTSecret, a.KeyFile, a.SecretBase64
		n.JWTHeader, n.Payload = a.JWTHeader, a.Payload
		n.IssuedAt, n.NotBefore, n.JWTID, n.ExpiresIn = a.IssuedAt, a.NotBefore, a.JWTID, a.ExpiresIn
	case AuthOAuth1:
		n.ConsumerKey, n.ConsumerSecret, n.AccessToken, n.TokenSecret = a.ConsumerKey, a.ConsumerSecret, a.AccessToken, a.TokenSecret
		n.SignatureMethod, n.Callback, n.Verifier, n.Realm = a.SignatureMethod, a.Callback, a.Verifier, a.Realm
		n.OmitVersion, n.Nonce, n.Timestamp, n.BodyHash = a.OmitVersion, a.Nonce, a.Timestamp, a.BodyHash
		if a.In != APIKeyInHeader {
			n.In = a.In
		}
	case AuthAWSV4:
		n.AccessKey, n.SecretKey, n.SessionToken = a.AccessKey, a.SecretKey, a.SessionToken
		n.Region, n.Service, n.UnsignedPayload, n.Expires = a.Region, a.Service, a.UnsignedPayload, a.Expires
		if a.In != APIKeyInHeader {
			n.In = a.In
		}
	case AuthHawk:
		n.HawkID, n.HawkKey, n.HawkAlgorithm = a.HawkID, a.HawkKey, a.HawkAlgorithm
		n.Ext, n.App, n.Dlg, n.PayloadHash = a.Ext, a.App, a.Dlg, a.PayloadHash
	case AuthNetrc:
		n.NetrcFile = a.NetrcFile
	case AuthOAuth2:
		n.GrantType = a.GrantType
		if n.GrantType == "" {
			n.GrantType = OAuth2ClientCredentials
		}
		n.TokenURL, n.ClientID, n.ClientSecret = a.TokenURL, a.ClientID, a.ClientSecret
		if a.ClientAuth != OAuth2ClientAuthHeader {
			n.ClientAuth = a.ClientAuth
		}
		n.Scope, n.Audience, n.Resource, n.State = a.Scope, a.Audience, a.Resource, a.State
		n.AuthURL, n.RedirectURL = a.AuthURL, a.RedirectURL
		n.TokenParams = a.TokenParams
		n.Username, n.Password = a.Username, a.Password
		n.Issuer, n.IDToken = a.Issuer, a.IDToken
		if a.Prefix != nil && *a.Prefix != DefaultBearerPrefix {
			p := *a.Prefix
			n.Prefix = &p
		}
		n.Key = a.Key
		if a.In != APIKeyInHeader {
			n.In = a.In
		}
	default: // inherit, none
		return n
	}
	n.Disabled = a.Disabled
	for _, m := range a.SecretMembers() {
		if a.IsLocal(m) {
			n.LocalSecrets = append(n.LocalSecrets, m)
		}
	}
	return n
}
