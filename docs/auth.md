# Auth

A request's auth adds its credentials: no auth, Basic, a bearer token or an
API key (AUTH-02…05), Digest (AUTH-06), a JWT built and signed at send time
(AUTH-16), an OAuth 1.0 signature (AUTH-15), an AWS Signature Version 4
(AUTH-17), a Hawk signature (AUTH-18), the credentials of a `.netrc` file
(AUTH-19), an OAuth 2.0 token got with the client credentials grant,
the authorization code grant with PKCE through the system browser, the
password grant or the implicit grant (AUTH-07…10, AUTH-12, with its
exchange in the timeline, AUTH-33), or the OpenID Connect ID token that
came with it (AUTH-14), kept on this computer and refreshed (AUTH-11,
AUTH-13), or the auth of the closest folder that sets one (AUTH-01). The
same auth is sent with the handshakes of WebSocket, Socket.IO and gRPC
requests ([below](#auth-on-every-protocol-auth-31), AUTH-31). Other
schemes come later.
Client certificates for mTLS (AUTH-30) are not a request's auth but an
app setting per host, applied to every protocol: see
[network.md](network.md#client-certificates).

| Piece | Where |
|---|---|
| File format (`auth` member) | `internal/core/storage/projectfile/auth.go`, [file-format.md](file-format.md#auth) |
| Secrets kept out of project files | `internal/core/requestsecrets` |
| Auth → headers and query parameters, inheritance | `internal/core/request/auth.go` (`authjwt.go` for JWT, `authoauth1.go` for OAuth 1.0, `authawsv4.go` for AWS, `authhawk.go` for Hawk, `authsigned.go` shared by those two, `authnetrc.go` for netrc) |
| JWT building and signing (HS/RS/PS/ES/EdDSA) | `internal/core/jwt` |
| OAuth 1.0 base string and signatures (RFC 5849) | `internal/core/oauth1` |
| AWS Signature Version 4: canonical request, signing key, presigned parameters | `internal/core/awssigv4` |
| Hawk: normalized string, MAC, payload hash, header | `internal/core/hawk` |
| `.netrc` parsing, lookup and file location | `internal/core/netrc`; the auth in `internal/core/request/authnetrc.go` |
| OAuth 2.0: grants, token request and response, refresh, the authorization code grant's sign-in (`authcode.go`), the implicit grant's (`implicit.go`), the OpenID Connect ID token (`idtoken.go`) | `internal/core/oauth2`; the auth in `internal/core/request/authoauth2.go`; getting the token in a send in `internal/app/services/http/oauth2.go`; the Auth tab's token in `oauth2token.go` |
| OAuth 2.0 token store (AUTH-13), in the local side files | `internal/core/oauth2/tokenstore` ([below](#token-store)) |
| Netrc file and check (`HTTPService.NetrcFile`, `NetrcCheck`) | `internal/app/services/http/netrc.go` |
| Auth on WebSocket, Socket.IO and gRPC handshakes (AUTH-31) | `internal/core/request/handshake.go`; `internal/app/services/http/handshake.go`; `collection.LoadAuthSource`; `WithAuth` of the websocket, socketio and grpc services ([below](#auth-on-every-protocol-auth-31)) |
| JWT preview (`HTTPService.JWTPreview`) | `internal/app/services/http/jwt.go` |
| Digest challenge and response | `internal/core/httpclient` (`digest.go`, `authtransport.go`), [http-client.md](http-client.md#digest-authentication-auth-06) |
| Bound service (`Request`, `UpdateRequest`, `InheritedAuth`) | `internal/app/services/collection` (`auth.go`, `auth_dto.go`) |
| Frontend | `frontend/src/lib/auth.ts`, `frontend/src/lib/jwt.ts` (decoder), `frontend/src/commands/auth.ts`, `frontend/src/components/editor/AuthEditor.tsx`, `JwtTools.tsx`, `NetrcTools.tsx`, `OAuth2Tools.tsx` ([frontend.md](frontend.md#auth-editor)) |

## Schemes

`request.Auth(auth, secrets, expand)` returns the headers and query
parameters an auth adds, or for Digest the credentials to answer a
challenge with. `expand` renders `{{variables}}` in every field
first (AUTH-32); the rendered text is what is checked and sent. Sending
(`request.BuildHTTP`, [http-client.md](http-client.md#sending-from-the-app))
calls it after `ResolveAuth`.

| Type | Sends | Nothing is sent when | Refused (`request.auth_invalid`) |
|---|---|---|---|
| `none` | nothing | | |
| `basic` | `Authorization: Basic ` + base64 of `username:password`, the UTF-8 bytes as typed (RFC 7617, `charset=UTF-8`) | both are empty | a `:` in the username; a control character in either |
| `bearer` | `Authorization: <prefix> <token>`; with an empty prefix, `Authorization: <token>` | the token is empty | a line break or other control character (tab allowed) in the prefix or token |
| `apikey`, `in` header | `<key>: <value>` | the key is empty as typed | a key that is not a header name; a control character in the value |
| `apikey`, `in` query | `key=value` at the end of the query string | the key is empty as typed | a key that renders empty |
| `digest` | first nothing; on a `401` with a Digest challenge, the request again with `Authorization: Digest …` computed from the challenge (RFC 7616) | both are empty | a control character in either |
| `jwt` | a JWT signed now: `<key or Authorization>: <prefix> <jwt>`, or with `in` query `<key or access_token>=<jwt>` ([below](#jwt-bearer-auth-16)) | there is no key: `secret` and `keyFile` are empty | JSON that is not an object in the header or payload; a key that does not fit the algorithm; a header name that is not a token; a control character in the prefix. An unknown algorithm is `request.auth_unsupported`; a key file that cannot be read, `request.auth_key_file_failed` |
| `oauth1` | the request signed now (RFC 5849): `Authorization: OAuth …`, or the `oauth_*` parameters in the query string or the form body ([below](#oauth-10-auth-15)) | the consumer key is empty | `in` body with a body that is not a URL-encoded form; RSA-SHA1 without a readable RSA private key; a control character in the realm. An unknown signature method or `in` is `request.auth_unsupported` |
| `awsv4` | the request signed now with AWS Signature Version 4: `X-Amz-Date` (`X-Amz-Security-Token`, `X-Amz-Content-Sha256`) and `Authorization: AWS4-HMAC-SHA256 …`, or with `in` query the `X-Amz-*` parameters of a presigned URL ([below](#aws-signature-v4-auth-17)) | the access key is empty | an empty region or service, or one with `/` or a space; a control character in any field; `in` query with `expires` outside 1…604800; a typed `X-Amz-Date` row that is not `yyyymmddThhmmssZ`. An `in` other than header and query is `request.auth_unsupported` |
| `hawk` | the request signed now with Hawk: `Authorization: Hawk id="…", ts="…", nonce="…", …, mac="…"` ([below](#hawk-auth-18)) | the Hawk ID is empty | a `"`, `\`, control or non-ASCII character in the ID, app or dlg; a control or non-ASCII character in ext; a control character in the key. An algorithm other than sha256 and sha1 is `request.auth_unsupported` |
| `oauth2` | a token from the token endpoint (for the implicit grant, from the sign-in's redirect), got when the request is sent (or the stored one, refreshed once it expires), or its ID token: `<key or Authorization>: <prefix> <token>`, or with `in` query `<key or access_token>=<token>` ([below](#oauth-20-auth-08-auth-12-auth-33)) | the token URL is empty (for the implicit grant, the authorization URL) | a token URL that is not an absolute http or https URL; a control character in the client ID or secret, scope, audience, resource, state, username, password or issuer; a header name that is not a token; a control character in the prefix; for the authorization code and implicit grants, an authorization URL that is not an absolute http or https URL or a redirect URL that is not a loopback http URL; for the password grant, an empty username. A grant PostSonde does not know, a `clientAuth` other than header and body, or an `in` other than header and query is `request.auth_unsupported`. The token request's own errors are `oauth2.*` |
| `netrc` | `Authorization: Basic ` + base64 of the `login:password` of the URL host's entry in the `.netrc` file, read now ([below](#netrc-auth-19)) | the URL has a user and password of its own (they are sent instead) | an entry with neither login nor password, a `:` in the login, a control character in either; a URL without a host (field `url`). A file that is missing, unreadable or not netrc is `request.auth_netrc_failed`; no entry for the host, `request.auth_netrc_no_entry` |

- **Digest** (AUTH-06) cannot be computed before the server's challenge,
  so `Auth` returns `AuthResult.Digest` (username and password, rendered)
  and `BuildHTTP` puts it in `httpclient.Request.Digest`; the client sends
  the request, answers the 401 and sends it again
  ([http-client.md](http-client.md#digest-authentication-auth-06)): MD5,
  SHA-256 and SHA-512-256, each also `-sess`; qop `auth` and `auth-int`;
  RFC 2617 servers without qop; `userhash`; a `stale=true` nonce retried
  once. The password is never sent, only the hash. Both requests are in
  the Timeline tab, the first as an "Auth challenge" hop; the challenge
  does not count as a redirect. A wrong password gives the server's 401,
  with an `auth_unanswered` event saying the credentials were rejected.
  An enabled `Authorization` row, or URL userinfo, wins: the request is
  then sent with it and a 401 is not answered.
- **Disabled** (`"disabled": true`) sends nothing, and does not fall back to a
  folder's auth: disabling is "no auth for now", without losing the values
  (AUTH-04).
- The bearer **prefix** defaults to `Bearer` (the member is left out of the
  file). It is sent as typed, not trimmed; `""` sends the bare token, for
  servers that expect `Authorization: <token>`.
- An unknown auth type, or an API key `in` other than `header` and `query`,
  is `request.auth_unsupported`; the file keeps it.
- Errors carry the details `type` and `field` (`username`, `password`,
  `prefix`, `token`, `key`, `value`, `in`; for `jwt` also `algorithm`,
  `secret`, `keyFile`, `jwtHeader`, `payload`; for `oauth1` also
  `consumerKey`, `consumerSecret`, `accessToken`, `tokenSecret`,
  `signatureMethod`, `callback`, `verifier`, `realm`, `nonce`,
  `timestamp`; for `awsv4` `accessKey`, `secretKey`, `sessionToken`,
  `region`, `service`, `expires`; for `hawk` `hawkId`, `hawkKey`,
  `hawkAlgorithm`, `ext`, `app`, `dlg`; for `netrc` also `netrcFile` and
  `url`, with `path`, `host` and `line` where they apply; for `oauth2`
  `grantType`, `tokenUrl`, `clientId`, `clientSecret`, `clientAuth`,
  `scope`, `audience`, `resource`, `state`, `tokenParams`, `authUrl`,
  `redirectUrl`, `username`, `password`, `issuer`). Values never
  appear in errors.

### JWT bearer (AUTH-16)

A `jwt` auth makes its token when the request is built: on every send,
and on every copy as cURL, HAR export and header preview. Nothing is
cached; each build signs a new token with its own time claims.

1. **Fields are rendered** like every auth field (AUTH-32): the algorithm,
   the secret, the key file path, the header name and prefix, then the
   header and payload JSON text. Dynamic values work in the payload
   (`"iat": {{$timestamp}}`, `"jti": "{{$uuid}}"`). A value is inserted as
   text, so a string variable goes inside quotes in the JSON.
2. **The key.** `keyFile`, when set, is read (absolute, `~/…`, or relative
   to the project root; at most 1 MiB; a relative path without a project
   counts as missing), else the typed `secret`. The file must be in the
   project folder or an allowed directory of the settings, links followed:
   any other is refused with `fileaccess.not_allowed` (field `keyFile`)
   before anything is sent ([privacy.md](privacy.md#local-file-access-prv-10)). For HS256/384/512 its bytes are
   the HMAC secret (a key file's bytes as they are, a trailing line break
   included), base64-decoded first with `secretBase64` (standard or URL
   alphabet, padding and whitespace optional). The other algorithms take
   an unencrypted PEM private key: PKCS#8 (`PRIVATE KEY`), PKCS#1
   (`RSA PRIVATE KEY`) or SEC 1 (`EC PRIVATE KEY`, an `EC PARAMETERS`
   block before it is skipped). RS\* and PS\* need an RSA key, ES256,
   ES384 and ES512 an EC key on P-256, P-384 and P-521, EdDSA an Ed25519
   key. Encrypted and OpenSSH keys, public keys and certificates are
   refused with a hint.
3. **The header** is `{"alg": <algorithm>, "typ": "JWT"}` followed by the
   members of `jwtHeader` in the order typed; `alg` is always the
   algorithm, `typ` can be overridden.
4. **The payload** is `payload` (blank is `{}`) with its members in the
   order typed, compacted, then the claims turned on that it does not
   have: `iat` and `nbf` (the build's time, Unix seconds), `exp` (that plus
   `expiresIn` seconds, when positive) and `jti` (a random version 4 UUID).
   A claim the payload has is kept as typed. A member that appears twice is
   refused.
5. **Signing** (RFC 7515 compact serialization, RFC 7518, RFC 8037):
   `base64url(header) "." base64url(payload)` signed with HMAC-SHA-2
   (HS\*), RSASSA-PKCS1-v1_5 (RS\*), RSASSA-PSS with a salt as long as the
   hash (PS\*), ECDSA with the signature as fixed-size R ‖ S (ES\*), or
   Ed25519 (EdDSA), with the Go standard library only. ES and PS signatures
   are randomized, so two tokens of the same input differ.
6. **Sending**: in a header, `key` (empty: `Authorization`) with the
   `prefix` (default `Bearer`; `""` sends the bare token, also for a custom
   header); in the query string, the parameter `key` (empty:
   `access_token`, RFC 6750). An enabled header row or URL parameter of the
   same name wins, as for the other types.

`HTTPService.JWTPreview(requestId)` builds only the auth of a request (its
own or inherited), with its variables and the current time, and returns
the token with its header and payload indented, the algorithm and the
folder it came from; `http.no_jwt` when the auth makes no token. The Auth
tab calls it from **Preview token** and shows the token, its decoded parts
and its time claims until **Hide**; the token is never stored. The tab
also has a **Decode a token** box that decodes any pasted JWT in the
frontend (`frontend/src/lib/jwt.ts`), without checking its signature.

**Secrets.** The typed `secret` (an HMAC secret or a PEM key) is the
type's secret member, split off to the local side file like a password
(`auth.secret`), or written as a `{{variable}}` reference. A key file's
path is written to the project; the file itself is never copied. With
`keyFile` set, a `secret` marked local but missing does not stop the send.
The signed token and the typed secret are masked in the timeline, history,
header preview, copy as cURL and HAR export, wherever they appear
(`AuthResult.Secrets`, carried by `request.HTTPBuild.AuthSecrets`: the
values of that very build).

Tests: `internal/core/jwt` checks the RFC 7515 A.1 (HS256) and RFC 8037
A.4 (Ed25519) signatures byte for byte and verifies the RFC 7515 A.3
ES256 signature with the R ‖ S layout; every algorithm's token verifies
with the standard library's `crypto` packages (`TestBuildEveryAlgorithmVerifies`,
`TestAuthJWTEveryAlgorithm`), with fixed clocks for the claims;
`TestSendJWT` (internal/app/services/http) sends an inherited JWT and checks
the server's token and the masking.

### OAuth 1.0 (AUTH-15)

An `oauth1` auth signs the request with OAuth 1.0 (RFC 5849) when it is
built: on every send, copy as cURL, HAR export and header preview. Its
fields are rendered like every auth field (AUTH-32) and checked with the
rest of the auth; the signature itself is computed **last**, once the
request is complete (`HTTPBuild.signOAuth1`, called at the end of
`BuildHTTP`, and of `BuildGraphQL` after it adds its query parameters or
body), because it covers the method, the URL and a form body.

1. **Parameters signed** (section 3.4.1.3): the query parameters of the URL
   **as it goes on the wire** (`httpclient.SentURL` with the request's URL
   encoding, so a typed space is signed as the `%20` that is sent), the
   parameters of an `application/x-www-form-urlencoded` body (the body's
   own type, or an enabled `Content-Type` row naming it; `+` is a space,
   `%XX` decoded, a pair without `=` has an empty value), and the protocol
   parameters: `oauth_consumer_key`, `oauth_token` (when there is an access
   token), `oauth_signature_method`, `oauth_timestamp`, `oauth_nonce`,
   `oauth_version` (`1.0`, unless `omitVersion`), `oauth_callback` and
   `oauth_verifier` (when set) and `oauth_body_hash` (below). Names and
   values are encoded (RFC 3986 unreserved characters kept, everything
   else `%XX` in upper case, space `%20`), sorted by name then value (byte
   order, so duplicate names keep every value) and joined.
2. **Base string** (3.4.1.1): the method, the base string URI (scheme and
   host in lower case, the default port left out, the path as sent, no
   query or fragment) and the normalized parameters, each encoded, joined
   with `&`.
3. **Signature** (3.4.2 to 3.4.4): HMAC-SHA1 (the default), HMAC-SHA256,
   or PLAINTEXT with the key `encode(consumer secret) & encode(token
   secret)`; RSA-SHA1 signs with RSASSA-PKCS1-v1_5 and the client's RSA
   private key, typed in the consumer secret field as unencrypted PEM
   (PKCS#8 or PKCS#1; the token secret is not used).
4. **Nonce and timestamp**: generated on every build (32 random letters and
   digits; the current Unix time) unless `nonce` or `timestamp` is set,
   for servers or tests that need fixed values.
5. **Body hash** (`bodyHash`, the OAuth Request Body Hash extension): for a
   body that is not a form, `oauth_body_hash` is base64 of the SHA-1
   (SHA-256 with HMAC-SHA256) of the body's bytes, of nothing without a
   body. A form body never gets one: its parameters are signed.
6. **Where the parameters go** (`in`): `header` (default) sends
   `Authorization: OAuth realm="…", oauth_consumer_key="…", …,
   oauth_signature="…"` (the realm, when set, is a quoted string and not
   signed; the values are encoded), and an enabled `Authorization` row
   wins, or `settings.omitHeaders` leaves it out, as for the other types;
   `query` appends the parameters to the URL (before any fragment);
   `body` appends them to a URL-encoded form body, or makes one (with that
   Content-Type) when the request has no body. Another body with `in`
   body is refused (`request.auth_invalid`, field `in`).

`HTTPBuild.OAuth1` holds the parameters, the base string and the
signature of the build. **Secrets**: `consumerSecret` and `tokenSecret`
are both secret members, each split off to the side file on its own
(`auth.consumerSecret`, `auth.tokenSecret`) or written as a
`{{variable}}` reference; the consumer key and the access token are not
secrets. The two secrets and the signature (as it is and encoded) are
masked in the timeline, history, header preview, copy as cURL and HAR
export; the whole `Authorization` header is masked by name.

**Import**: Postman's and Insomnia's OAuth 1.0 auth become `oauth1` auth,
their typed secrets going to the side file like any imported secret;
[import.md](import.md) lists how each option maps and what is reported.

Tests: `internal/core/oauth1` reproduces the three requests of the
section 1.2 walkthrough (base string, HMAC-SHA1 signature and header byte
for byte) and the base string of section 3.4.1.1, and covers encoding,
sorting with duplicate names, PLAINTEXT, HMAC-SHA256 (checked against
`openssl dgst -hmac`), RSA-SHA1 (verified with `crypto/rsa`) and the body
hash; `authoauth1_test.go` (internal/core/request) builds requests with
form bodies, each `in`, the body hash, overrides and GraphQL over GET;
`TestSendOAuth1` (internal/app/services/http) sends inherited OAuth 1.0
auth in the header, the query string and a form body to a server that
verifies the signature on its own, and checks the masking.

### AWS Signature v4 (AUTH-17)

An `awsv4` auth signs the request with AWS Signature Version 4 when it is
built: on every send, copy as cURL, HAR export and header preview. Its
fields (access key, secret key, session token, region, service) are
rendered like every auth field (AUTH-32) and checked with the rest of the
auth; the signature is computed **last** (`HTTPBuild.signAWSV4`, from
`HTTPBuild.sign` at the end of `BuildHTTP`, and of `BuildGraphQL` once its
query parameters, body and `Accept` header are added), because it covers
the request as it goes on the wire.

1. **Canonical request** (package `awssigv4`): the method; the canonical
   URI; the canonical query; the canonical headers; the signed headers;
   the hashed payload, joined with line breaks.
   - **Path**: the path **as sent** (`httpclient.SentURL` with the
     request's URL encoding). For every service but S3 it is normalized
     (`.` and `..` segments and empty segments removed, a trailing `/`
     kept) and each segment URI-encoded again, so an escape is
     double-encoded (`/a%20b` is signed as `/a%2520b`), as AWS does. For
     `s3` each segment is decoded and encoded once, and the path is not
     normalized.
   - **Query**: every `name=value` pair of the query as sent, decoded
     (`+` is a space) and encoded again (everything but `A–Z a–z 0–9 - . _
     ~` as `%XX`, upper case), a pair without `=` given an empty value,
     sorted by name, then value.
   - **Headers**: `host` (an enabled Host row, else the URL's host and
     port as sent), every enabled header row except `Authorization`,
     `User-Agent`, `Expect`, `X-Amzn-Trace-Id`, `Content-Length` and the
     hop-by-hop fields, the body's implied `Content-Type`, and the headers
     the signature adds; names in lower case and sorted, values trimmed
     with runs of spaces collapsed, the values of a repeated name joined
     with `,`.
   - **Payload**: the hex SHA-256 of the body's bytes (of nothing without
     a body), `UNSIGNED-PAYLOAD` with `unsignedPayload` (and for a
     presigned S3 URL), or the value of an enabled `X-Amz-Content-Sha256`
     row.
2. **String to sign** `AWS4-HMAC-SHA256\n<X-Amz-Date>\n<date>/<region>/<service>/aws4_request\n<hex SHA-256 of the canonical request>`,
   signed with HMAC-SHA256 and the key derived from `"AWS4" + secret key`
   through the date, region, service and `aws4_request`.
3. **In the header** (`in` left out): `X-Amz-Date` (the time of the
   build, UTC), `X-Amz-Security-Token` with a session token,
   `X-Amz-Content-Sha256` for `s3` or an unsigned payload, all signed, then
   `Authorization: AWS4-HMAC-SHA256 Credential=<access key>/<scope>,
   SignedHeaders=…, Signature=…`. An enabled row of one of the `X-Amz-*`
   headers is signed as it is and not added again (a typed `X-Amz-Date`
   also gives the date of the scope). An enabled `Authorization` row, or
   `settings.omitHeaders`, wins: then nothing is added.
4. **In the query** (`in` `"query"`, a presigned URL): `X-Amz-Algorithm`,
   `X-Amz-Credential`, `X-Amz-Date`, `X-Amz-Expires` (`expires`, left out:
   3600 seconds; at most 7 days), `X-Amz-SignedHeaders` and
   `X-Amz-Security-Token` are appended to the URL and signed with it, then
   `X-Amz-Signature`. No header is added.

`HTTPBuild.AWSV4` holds the canonical request, string to sign and
signature of the build. **Secrets**: `secretKey` and `sessionToken` are
both secret members, split off to the side file each on its own
(`auth.secretKey`, `auth.sessionToken`) or written as `{{variable}}`
references; the access key, region and service are not secrets. The secret
key, the session token (as it is and encoded) and the signature are
masked in the timeline, history, header preview, copy as cURL and HAR
export; the `Authorization` header is masked by name.

Tests: `internal/core/awssigv4` reproduces published vectors byte for
byte (the file says which source each comes from): 26 cases of the
aws-sig-v4-test-suite (2015-08-30 edition: the vanilla GET and POST
requests, query ordering, unreserved and UTF-8 paths and queries, header
case, sorting, trimming, duplicates and value order, the form bodies, and
the path-normalization cases `get-space`, `get-slash`, `get-slashes`,
`get-slash-dotslash`, `get-slash-pointless-dot`, `get-relative`,
`get-relative-relative`) and its session-token case; the IAM
`ListUsers` example of the General Reference (canonical request, string
to sign, signing key, signature); and the S3 API Reference's GET Object,
PUT Object, GET Bucket Lifecycle, List Objects and presigned URL examples.
`authawsv4_test.go` (internal/core/request) builds the IAM example, the S3
GET Object example and the S3 presigned URL from request files with a
fixed clock and gets the published header and URL; `TestSendAWSV4AndHawk`
(internal/app/services/http) sends inherited AWS auth in the header and
as a presigned URL to a server that verifies the signature on its own, and
checks the masking.

### OAuth 2.0 (AUTH-08, AUTH-12, AUTH-33)

An `oauth2` auth gets an access token from the authorization server's
token endpoint and sends it with the request. This version runs the
**client credentials** grant (RFC 6749 section 4.4), the
**authorization code** grant with PKCE (section 4.1, RFC 7636; the user
signs in in the system browser, [below](#authorization-code-with-pkce)),
the **password** grant (section 4.3, AUTH-09, [below](#password-grant))
and the **implicit** grant (section 4.2, AUTH-10, deprecated; the token
comes back in the browser's redirect, [below](#implicit-grant)). An
OpenID Connect **ID token** that comes with a token is checked, shown in
the Auth tab and can be sent instead of the access token (AUTH-14,
[below](#oidc-id-token)). The pieces:

| Piece | Where |
|---|---|
| Grants, token request, client authentication, token response and its errors, expiry, `Store` | `internal/core/oauth2` |
| The auth's fields rendered into a token request; adding the token (`UseOAuth2Token`) | `internal/core/request/authoauth2.go` |
| Getting the token in a send, the token store, the timeline | `internal/app/services/http/oauth2.go` |

1. **The build** renders every field (AUTH-32) and checks them: the grant
   (`client_credentials`, also left out, `authorization_code`, `password`
   or `implicit`), the token URL (an absolute http or https URL; left out
   or empty: nothing is sent; not used by the implicit grant, for which an
   empty authorization URL sends nothing), for the redirect grants the
   authorization URL and the redirect URL, for the password grant the
   username (required) and password, the client ID and secret, scope,
   audience, resource, state and issuer (no control characters), the
   extra token parameters, and where the token goes. It
   adds no header: the build carries the token request
   (`HTTPBuild.OAuth2`), because getting a token is network traffic.
2. **The token** is got when the request is sent, as part of the send's
   operation (so the dial guard of PRV-05 applies): the stored token
   ([Token store](#token-store)) while it has not expired (30 seconds
   before its `expires_in` runs out, `oauth2.ExpiryLeeway`); an expired
   one with a refresh token is refreshed (AUTH-11); else, or when the
   refresh fails, a new one from the token endpoint. The token got is
   stored. The progress says "Refreshing the OAuth 2.0 token" or "Getting
   an OAuth 2.0 token". A token without `expires_in` is kept until the
   server refuses it: a `401` answer to a stored token drops it, so the
   next send gets a new one (event `oauth2_token_dropped`). Sends of one
   auth at the same time get one token: the others wait for it.
3. **The token request** (`oauth2.TokenRequest`) is a `POST` of an
   `application/x-www-form-urlencoded` body with `Accept:
   application/json`: `grant_type=client_credentials` (for the password
   grant `grant_type=password`, `username` and `password`), `scope`,
   `audience` and `resource` when set (audience as Auth0 and Okta take it,
   resource as RFC 8707 defines it), then the enabled extra parameters in
   order. The client authenticates with `clientAuth`: `header` (the
   default, `client_secret_basic`: `Authorization: Basic` of the
   form-encoded ID and secret, section 2.3.1) or `body`
   (`client_secret_post`: `client_id` and `client_secret` in the form). A
   public client (no secret) does not authenticate: it sends `client_id`
   in the form, with no Authorization header. It goes through the app's
   HTTP client (the dialer with its guard and connection log, the custom
   CA bundles, the per-host client certificates, the proxy settings), with
   the request's TLS verification, timeout and HTTP version, without
   cookies, and never follows a redirect (the client secret stays with the
   token URL). State is not sent: it belongs to the redirect grants.
4. **The token response** (`oauth2.ParseResponse`, section 5.1) is JSON,
   or a form (some servers still answer `application/x-www-form-urlencoded`).
   `access_token` is required; `token_type`, `expires_in` (a number, or a
   string of digits), `refresh_token`, `scope` and `id_token` are read
   when present. Errors: an error response (section 5.2) is
   `oauth2.token_error` with the server's `error`, `error_description` and
   `error_uri` as details and a hint per code (`invalid_client` is kind
   `permission_denied`: check the client ID, secret and client
   authentication; `invalid_scope`, `invalid_grant` (for the password
   grant, a wrong username or password), `unauthorized_client`,
   `unsupported_grant_type`, `invalid_request`); another status without an
   error is `oauth2.token_http_status`; a 2xx that is not a token (not
   JSON, no `access_token`, a member of the wrong type, a token with a
   space or control character) is `oauth2.malformed_response`; a request
   that got no answer keeps its cause's code (a timeout, a refused
   connection, TLS) with "The OAuth 2.0 token request failed" first. An
   `id_token` in the response is checked on arrival
   ([OIDC ID token](#oidc-id-token)): a bad one fails the exchange
   (`oauth2.id_token_*`). A failed token request fails the send: nothing
   is sent to the API.
5. **Sending the token** (the ID token instead with `idToken`,
   [below](#oidc-id-token)): in a header, `key` (empty: `Authorization`)
   with the `prefix` (default `Bearer`; `""` sends the bare token), or in
   the query string, the parameter `key` (empty: `access_token`, RFC
   6750), as for a JWT. The prefix is the auth's, not the server's
   `token_type`. An enabled header row of the same name, or
   `settings.omitHeaders`, wins; a URL parameter of the same name wins.

**The exchange log (AUTH-33).** The token request and its response are the
first hops of the send's timeline, kind `token` ("Token request 1 of 2"),
with the token response's body (masked) under its head, then the
request's own hops, their times shifted after the token request's. The
event `oauth2_token` says the grant (`refresh_token` for a refresh), the
token URL, the status and the lifetime; a send that used the stored token
has the event `oauth2_token_stored` instead. A refresh that failed before
a new grant is a hop of its own with the event `oauth2_refresh_failed`
(the status and the error). A token request that fails fails the send
with its error; the history records it as a failed send.

**Other kinds of request.** GraphQL over HTTP and the schema fetch
(introspection) get the token as a send does; an SSE stream gets it on
every connection, so a reconnection after expiry gets a new one. A GraphQL
subscription over WebSocket refuses an oauth2 auth
(`request.auth_unsupported`) until the handshake runs as a user operation.

**Copies and previews** never ask the token endpoint: copy as cURL, the
HAR export and the header preview use the stored token when it has not
expired, else the placeholder `Bearer …`.

**Secrets.** `clientSecret` and the password grant's `password` are the
type's secret members, split off to the local side file
(`auth.clientSecret`, `auth.password`) or written as a `{{variable}}`
reference. Tokens are never written to a project file: they are kept in
the workspace's local side files ([Token store](#token-store)). The
client secret (as typed and as the Basic credentials carry it), the
password, the access token, and a refresh or ID token the server sent
(the old refresh token of a refresh too, and the tokens of a response
refused for its ID token) are masked in the timeline (the token
request's body and Authorization header, the token response's body, the
request's own header or URL), the history, the header preview, copy as
cURL and the HAR export; copied and saved logs are masked with every
stored token ([privacy.md](privacy.md#copied-and-saved-logs)).

#### Token store

AUTH-11, AUTH-13. `internal/core/oauth2/tokenstore` implements
`oauth2.Store` over one side file per workspace, never inside the
project:

```
<local>/secrets/oauth2/tokens.json   {"version": 1, "tokens": {"<key>": {"accessToken": "…", "tokenType": "Bearer", "expiresIn": 3600, "expiry": "2026-09-23T13:00:00Z", "refreshToken": "…", "scope": "…", "obtained": "…"}}}
```

- **One token per auth configuration.** The key is a hash of the grant,
  token URL, client ID and secret, client authentication, scope,
  audience, resource and extra parameters (`oauth2.TokenRequest.Key`), so
  two auths never share a token, and a changed field (a new scope,
  another environment's client ID) gets its own. The key never holds a
  value. A copy of a project has its own local directory, so its own
  tokens ([workspace.md](workspace.md#moved-and-copied-projects)).
- **The file** is `0600` in a `0700` directory, written atomically on
  every change; its tokens are read once per workspace, then served from
  memory. A file that cannot be read gives no tokens (they are got again)
  and is replaced with the next one; a write that fails is logged and the
  token kept for the session. No project open: memory only. With a master
  password set the file is encrypted ([privacy.md](privacy.md#encrypted-secrets));
  while the secrets are locked it has no tokens (none is cached, so it is
  read again after the unlock), a new token is not written, and every lock
  drops the tokens held in memory (`Store.Forget`).
- **Refresh (AUTH-11).** A send refreshes the stored token once it expires
  (the same 30 seconds early), with `grant_type=refresh_token` and the
  refresh token (`oauth2.RefreshRequest`: the client authenticates as for
  the grant; the scope and the extra parameters are left out, so the
  server grants the original scope). A response without a refresh token
  keeps the old one (RFC 6749 section 6). A refresh that fails (the
  server refuses the refresh token, or any other failure but a
  cancellation) drops the token and the send gets a new one by the grant.
- **The Auth tab** of a request with an oauth2 auth (its own or
  inherited) shows the stored token (`HTTPService.OAuth2Token`): none, or
  `••••••` and its last four characters with "valid until" or "expired at"
  the time, its scope, and whether a send refreshes it; with an ID token,
  its claims ([below](#oidc-id-token)). The token itself never reaches
  the UI. **Get new token** runs the grant now, whatever is
  stored; **Refresh** refreshes with the refresh token (disabled without
  one; a failure is shown and the token kept); **Clear** removes it
  (`OAuth2GetToken`, `OAuth2ClearToken`). Getting a token is a user
  action: it dials through the guarded dialer marked `http.oauth2_token`,
  and **Cancel** cancels the call. Only the auth is built for the status,
  so a problem elsewhere in the request does not hide it; Get new token
  builds the request as a send does. The tab asks again when the auth's
  fields change and on `http:oauth2-token-changed` (`{key}`), which the
  HTTP service sends whenever it stores, refreshes or removes a token.

Seams: `tokenstore.Store.Clear` removes every token of the open workspace
(the one-action clear of local data); `List` and `Values` list them (the
masking of copied logs uses `Values`). The file format is known only to
`readFile` and `writeFile`, which an encrypted store replaces.

**Grants.** `grantType` is `client_credentials` (written when left out),
`authorization_code`, `password` or `implicit`; all four are run. Another
is kept in the file (and by the importers) and refused when sent,
`request.auth_unsupported` with field `grantType`.

#### Authorization code with PKCE

AUTH-07. The user signs in in the system browser, never in the app, and
the code comes back to a listener on this computer (RFC 8252), so no
vendor callback page is involved. `oauth2.Authorize`
(`internal/core/oauth2/authcode.go`) runs the browser leg when a send, or
**Get new token** in the Auth tab, needs a new token:

1. **The redirect URL** is `redirectUrl`, or when empty
   `http://127.0.0.1:<random port>/callback`. It must be an `http://` URL
   on this computer: `127.0.0.0/8`, `[::1]` or `localhost`, with an
   optional port (none or `0`: a random one) and path, no query or
   fragment (`oauth2.ParseRedirectURL`; anything else is
   `request.auth_invalid`, field `redirectUrl`). A fixed port suits a
   server that registers the exact URL; RFC 8252 servers accept any port.
2. **The listener** is opened through `listen.Listen`
   ([network.md](network.md#listening)), so it binds loopback only
   (`localhost` binds `127.0.0.1` and `::1`); a port in use fails with
   `listen.address_in_use`. It answers only `GET <path>`: other paths are
   404 and do not end the wait.
3. **PKCE and state**: a new code verifier (32 random bytes, base64url, 43
   characters) and its S256 challenge for every sign-in; the state is
   `state` when set, else 16 random bytes.
4. **The browser** opens the authorization URL: `authUrl` with its own
   query kept, then `response_type=code`, `client_id`, `redirect_uri`,
   `scope`, `state`, `code_challenge`, `code_challenge_method=S256`, and
   `audience` and `resource` when set. The app opens it with Wails'
   `Browser.OpenURL` (`HTTPService` option `WithOAuth2Browser`); the user
   asked for the token, so nothing asks again. The progress says "Waiting
   for the sign-in in the browser"; the Auth tab's button too.
5. **The redirect** is checked in this order: a `state` other than the
   one sent is refused (`oauth2.state_mismatch`, permission_denied; its
   code is never used), then an `error` (`access_denied` is
   `oauth2.access_denied`, another `oauth2.authorization_error`, with the
   `error`, `error_description` and `error_uri` as details), then a
   missing `code` (`oauth2.authorization_error`). The browser gets a short
   page saying it worked, or why not, with nothing loaded from anywhere
   (CSP `default-src 'none'`, `no-store`, `no-referrer`).
6. **The wait** ends after 5 minutes (`oauth2.authorization_timeout`),
   when the send's operation or the Auth tab's call is cancelled, or with
   the first redirect. The listener is closed before `Authorize` returns,
   whatever the outcome.
7. **The token request** redeems the code: `grant_type=authorization_code`,
   `code`, `redirect_uri` (as sent) and `code_verifier`, the client
   authenticating as configured (a public client sends `client_id`). The
   code and the verifier are masked in the timeline; the event
   `oauth2_authorized` says the redirect URI, then `oauth2_token` as for
   any grant. The token is stored and refreshed like any other; only a
   new grant opens the browser again.

Sends of one auth at the same time sign in once: the others wait for the
token. Without a browser opener (the service in tests) the grant fails
with `oauth2.browser_failed`.

#### Password grant

AUTH-09. The resource owner password credentials grant (RFC 6749 section
4.3), a legacy grant still common for internal servers: the app sends
the user's `username` and `password` to the token endpoint
(`grant_type=password&username=…&password=…`, then scope, audience,
resource and the extra parameters), the client authenticating as for the
client credentials grant. There is no browser. The username is required
(`request.auth_invalid`, field `username`); a wrong password is the
server's `invalid_grant` (`oauth2.token_error`). The password is a secret
member (`auth.password` in the local side file, or a `{{variable}}`), is
part of the token's store key (hashed, like the client secret, so a new
password gets a new token), and is masked by value wherever the token
request is shown. A refresh token the server sends is used as for any
grant.

#### Implicit grant

AUTH-10. The implicit grant (RFC 6749 section 4.2) is deprecated (the
OAuth 2.0 Security BCP): the Auth tab says so and suggests the
authorization code grant. PostSonde runs it for legacy servers
(`oauth2.AuthorizeImplicit`, `internal/core/oauth2/implicit.go`) with the
authorization code grant's browser leg: the same redirect URL rules, the
loopback listener through `listen.Listen`, the state, the 5-minute wait,
the cancel, and the listener closed before it returns.

1. **The authorization request** is `authUrl` with `response_type=token`
   (`id_token token` when the scope asks for `openid`, OpenID Connect
   Core 3.2.2.1), `client_id`, `redirect_uri`, `scope`, `state`, a random
   `nonce` (always), `audience` and `resource`; no PKCE (there is no
   token request to bind it to). There is no token URL, and the client
   secret is never sent (a public client).
2. **The fragment.** The server redirects to
   `<redirect URL>#access_token=…&state=…`. A browser never sends a URL's
   fragment to a server, so the listener answers `GET <path>` with a page
   whose one inline script reads `location.hash`, removes it from the
   address bar and the history entry (`history.replaceState`), and posts
   it (`fetch`, a form body, no credentials, no referrer) to the same
   path; it then shows the listener's answer as text. The token never
   goes into a URL the listener sees, a server log or the history. The
   page's Content Security Policy is `default-src 'none'; style-src
   'unsafe-inline'; script-src 'sha256-…'; connect-src 'self';
   base-uri 'none'; form-action 'none'`: the script is allowed by the
   SHA-256 hash of its exact text (`oauth2.FragmentScriptHash`), so
   nothing else can run, and it can connect only to its own origin. The
   page is `no-store` and `no-referrer`.
3. **The post** is taken only from the page itself: its `Origin` must be
   the listener's own (`http://127.0.0.1:<port>`, as the redirect URI
   names it) and its `Host` the listener's (against DNS rebinding), with
   a form body of at most 64 KiB. A post from anywhere else (a web page
   guessing the port) is `403`, one of another type `415`, and neither
   ends the wait. A redirect with the answer in its query instead (some
   servers send their errors so) is read too.
4. **The checks**, in order: the state (`oauth2.state_mismatch`: a
   fragment with another state is refused, nothing of it used), an error
   (`oauth2.access_denied`, `oauth2.authorization_error`), the access
   token (none: `oauth2.authorization_error`; one that is not a token,
   or an `expires_in` that is not a number: `oauth2.malformed_response`),
   then the ID token ([below](#oidc-id-token)): its nonce must be the one
   sent (`oauth2.id_token_nonce`), and its `at_hash` must match the
   access token. The browser's page says how it went.
5. **The token** (access token, `token_type`, `expires_in`, `scope`,
   `id_token`; never a refresh token, section 4.2.2) is stored like any
   other. It cannot be refreshed: once it expires the next send signs in
   again. The timeline has no token hop, only the events
   `oauth2_authorized` (the redirect URI) and `oauth2_token`
   ("implicit from the redirect, expires in 3600 s").

#### OIDC ID token

AUTH-14. An `id_token` in a token response (any grant, a refresh too) or
in the implicit grant's redirect is decoded and checked when it arrives
(`oauth2.CheckIDToken`, `internal/core/oauth2/idtoken.go`; the JWT
package only builds and signs tokens, so the decoding is its own, with
the standard library):

- **Form**: a JWS in compact serialization (three base64url parts, a JSON
  header with `alg`, a JSON payload) with `iss`, `sub`, `aud` (a string
  or an array) and `exp`. An encrypted ID token (JWE), a claim of the
  wrong type or a missing one is `oauth2.id_token_malformed`.
- **Nonce**: when the sign-in sent one (the implicit grant always; the
  authorization code grant when the scope asks for `openid`, which now
  sends a random `nonce` and carries it to the token request), the
  `nonce` claim must be it (`oauth2.id_token_nonce`). The password and
  client credentials grants and a refresh send none, so their ID token's
  nonce is not checked.
- **Audience**: `aud` must name the client ID, and `azp`, when present,
  must be it (`oauth2.id_token_audience`); not checked without a client
  ID.
- **Issuer**: `iss` must be exactly the auth's `issuer` when it is set
  (`oauth2.id_token_issuer`, with the `issuer` and `expected` details);
  left out, any non-empty `iss`.
- **Expiry**: `exp` must not have passed, nor `nbf` be ahead, beyond one
  minute of clock skew (`oauth2.IDTokenLeeway`; `oauth2.id_token_expired`).
- **at_hash**, when present: the base64url left half of the SHA-256,
  -384 or -512 of the access token, as the header's `alg` says
  (`oauth2.id_token_at_hash`; not checked for an algorithm whose hash is
  unknown). This binds the access token to the ID token, which matters
  for the implicit grant.

These failures are kind `permission_denied`. A bad ID token fails the
exchange as a whole, even when the access token is sent: nothing is
stored, and the send fails with nothing sent to the API.

**The signature is not verified.** Verifying it needs the issuer's keys
(its JWKS, found through its discovery document), a network fetch
PostSonde would make only as an explicit user action; this version does
not offer one. For the grants whose ID token comes straight from the
token endpoint over TLS (authorization code, password, client
credentials, refresh), OpenID Connect Core 3.1.3.7 step 6 lets the TLS
server validation stand in for the signature. The implicit grant's ID
token comes through the browser: its claims are checked, the state and
the nonce tie it to this sign-in and `at_hash` to its access token, but
its origin is not proven. The Auth tab says the signature is not
verified.

**Sending the ID token** (`idToken: true`, "Send the ID token instead of
the access token"): the ID token goes where the access token would (the
header with its prefix, or the query parameter), in a send, the header
preview, copy as cURL and the HAR export. A token counts as expired when
either the access token or the ID token has (`Token.ExpiredFor`, 30
seconds early), so a send gets a new one (refreshing when it can) once
the ID token expires. A server that sends no ID token fails the send
with `oauth2.no_id_token` (the hint says to add `openid` to the scope).

**The Auth tab** shows the stored token's ID token under the access
token (`OAuth2TokenDTO.idToken`, decoded in Go): the token masked like the
access token (`••••••` and its last four characters) with its algorithm,
then the issuer, subject, audience, expiry (marked when it has passed),
and the name and email when present, with the note that the claims were
checked on arrival and the signature is not verified. The token itself
never reaches the UI.

#### OAuth 2.0 import, export and tests

**Import and export**: Postman's and Insomnia's OAuth 2.0 auth become
`oauth2` auth: the client credentials grant as it is; the authorization
code and implicit grants with their authorization URL, and their
redirect URL when it is on this computer (a web callback such as
Postman's own is left out, with a warning); the password grant with its
username and password (a typed password goes to the local side file);
Insomnia's implicit `responseType` `id_token` as `idToken`. A token they
had obtained is never imported. The Postman export writes it back as
Postman's `oauth2` auth (the authorization code grant as
`authorization_code_with_pkce` with `authUrl`, `redirect_uri` and
`challengeAlgorithm` S256; the implicit grant with `authUrl` and
`redirect_uri`; the password grant as `password_credentials` with
`username` and `password`, which is exported only as a `{{variable}}`),
without a token; `idToken` and `issuer` have no Postman member and are
reported ([import.md](import.md)).

Tests: `internal/core/oauth2` runs the client credentials grant against a
test identity provider on `httptest` (client authentication in the header
and the body, `invalid_client`, `invalid_scope`), and covers the form and
Basic encoding, the store key, every token response case (JSON, form,
`expires_in` as a string or float, malformed JSON, missing or bad
members, error responses, other statuses), expiry and a refused
connection; `authoauth2_test.go` (internal/core/request) builds requests
with each placement of the token, overrides, errors and GraphQL;
`oauth2_test.go` (internal/app/services/http) sends through the app's
guarded dialer to a test IdP and an API that checks the token: the token
used in the header and the query, the token hops and events in the
timeline, the stored token reused and got again after expiry, the
refused token dropped, errors that stop the send, and the masking in the
timeline, history, cURL and HAR; an SSE stream gets its token too.
For the token store: `tokenstore_test.go` (persistence in a new
store, 0600, per workspace, a broken file, a failed write, Clear, and
concurrent use); `refresh_test.go` (internal/core/oauth2, the refresh
request); `oauth2refresh_test.go` (internal/app/services/http) refreshes
20 seconds before the expiry, keeps a refresh token the server does not
replace, falls back to a new grant when the refresh is refused, keeps
tokens across a restart, and covers the Auth tab's status (masked), Get
new token, Refresh, Clear and their events, and one token for concurrent
sends; `TestOAuth2TokensStayOutOfTheProject` (internal/app) gets a token
through the bound services, scans the project folder for it, and finds it
again after a restart.
For the authorization code grant: `authcode_test.go`
(internal/core/oauth2) checks the RFC 7636 challenge, the verifier's
alphabet and length, the redirect URL rules, the authorization URL, and
runs `Authorize` against a test identity provider with a stand-in browser
that follows the redirects itself (no real browser is opened): the code
redeemed with the verifier, a wrong or missing state refused, the user
declining (`access_denied`), another error, no code, a fixed port and
path, a timeout and a cancel, a browser that cannot be opened, and the
listener closed after each; `oauth2authcode_test.go`
(internal/app/services/http) runs it inside a send and from the Auth tab:
the masked code and verifier, the stored token reused and refreshed
without a browser, a wrong state and a declined sign-in failing the send
with nothing sent or stored, the timeout, a cancel, and one sign-in for
concurrent sends.
For the password and implicit grants and ID tokens (run with
`-race -count=20`): `password_test.go`
(internal/core/oauth2) gets a token with the password grant from a test
identity provider, checks the form and that the password is masked, a
new key for another username or password, and `invalid_grant` for a
wrong password; `implicit_test.go` runs `AuthorizeImplicit` against a
test identity provider with a stand-in browser that follows the
redirects, checks that the page's CSP allows exactly its script (by
hash), and posts the fragment back as the script does: the token and ID
token delivered, `response_type` and the nonce, the listener closed; a
fragment with another or no state, an ID token with another nonce or
for another client, a swapped access token (`at_hash`), `access_denied`,
no access token and a bad `expires_in` refused; posts from another
origin, without one or of another type refused without ending the wait;
an answer in the query. `idtoken_test.go` decodes ID tokens (every
malformed case) and checks them: valid, within the leeway, expired, not
yet valid, another audience or authorized party, another or no nonce,
another issuer, a wrong `at_hash`, and the OpenID Connect Core example
`at_hash`. `oauth2oidc_test.go` (internal/app/services/http) sends with
the password grant (the token used; the password, access token and ID
token masked in the timeline, the Auth tab's status, copy as cURL, the
HAR export and the history; `invalid_grant` failing the send), sends the
ID token instead of the access token (a new token once the ID token
expires; `oauth2.no_id_token`), fails sends with an expired ID token,
one for another audience or another issuer (nothing stored), and runs
the implicit grant in a send (no token hop, its events, the tokens
masked, the stored token reused) and refuses a wrong state and a wrong
nonce.

### Hawk (AUTH-18)

A `hawk` auth signs the request with Hawk when it is built, like the other
signing types (`HTTPBuild.signHawk`, the last step of the build).

1. **Artifacts**: the timestamp (the build's time, Unix seconds) and a
   nonce (6 random letters and digits), both new on every build; the
   method; the request target **as sent** (path and query,
   `httpclient.SentURL`); the host in lower case and the port (an enabled
   Host row, else the URL's; the scheme's default port when there is
   none); the payload hash; `ext`; `app` and `dlg`.
2. **Payload hash** (`payloadHash`): base64 of the hash of
   `hawk.1.payload\n<content type>\n<body>\n`, the content type being the
   request's (an enabled row, else the body's) media type in lower case,
   without parameters.
3. **MAC**: base64 HMAC-SHA256 (`hawkAlgorithm` left out or `sha256`) or
   HMAC-SHA1 (`sha1`) with the key over the normalized string
   `hawk.1.header\n<ts>\n<nonce>\n<METHOD>\n<resource>\n<host>\n<port>\n<hash>\n<ext>\n`,
   followed by `<app>\n<dlg>\n` when there is an app (ext with `\` and line
   breaks escaped).
4. **Header**: `Authorization: Hawk id="…", ts="…", nonce="…", hash="…",
   ext="…", mac="…", app="…", dlg="…"`, `hash`, `ext` and `app` only when
   set, `dlg` only with `app`, in the order of the reference
   implementation; `"` and `\` in ext are escaped. An enabled
   `Authorization` row, or `settings.omitHeaders`, wins.

`HTTPBuild.Hawk` holds the artifacts of the build. **Secrets**: `hawkKey`
is the secret member (`auth.hawkKey` in the side file); the ID is not a
secret. The key and the MAC are masked in the timeline, history, header
preview, copy as cURL and HAR export; the `Authorization` header is masked
by name. The server's `Server-Authorization` response header is not
checked.

Tests: `internal/core/hawk` reproduces the header example of the Hawk
specification (the reference implementation's README: normalized string,
MAC `6R4rV5iE…` and header) and its payload example (hash and MAC);
`TestHawkSpecExample` (internal/core/request) gets the same header from a
request file with a fixed clock and nonce; `TestSendAWSV4AndHawk` sends
inherited Hawk auth with a payload hash to a server that verifies the MAC
and the hash on its own.

### Netrc (AUTH-19)

A `netrc` auth takes its credentials from a `.netrc` file, as curl's
`--netrc` does, when the request is built for a send, a copy as cURL or
a HAR export (and for **Check**, below). Nothing else reads the file: not
the app's start, not another auth type, not a disabled netrc auth, and
not the header preview computed after every edit, which shows
`Authorization: Basic ••••••` instead (`NetrcOptions.Defer`).

1. **The file** (`netrc.Env.Locate`): the auth's `netrcFile`, rendered
   (AUTH-32): absolute, `~/…` in the home directory, or relative to the
   project root (on Windows a path from the root of a drive, `\netrc`, is
   on the project's drive); without one, the file the `NETRC` environment variable
   names (as in curl); else `.netrc` in the home directory. On Windows,
   `_netrc`, then `.netrc` when it does not exist, the home directory being
   `HOME` when set (as in curl), else the user profile. At most 1 MiB; a
   directory is refused. A file the auth names (`netrcFile`) comes from the
   project, so it must be in the project folder or an allowed directory
   of the settings, links followed, and is read through `os.Root`; any
   other is `fileaccess.not_allowed` (field `netrcFile`). The `NETRC` file
   and the home directory's are the user's own choice and are not limited
   ([privacy.md](privacy.md#local-file-access-prv-10)).
2. **The format** (`netrc.Parse`): whitespace-separated tokens. `machine
   <host>` starts an entry, `default` the entry for every other host;
   `login`, `password` and `account` set its values (`account` is read,
   not used); `macdef <name>` skips its macro, up to the first empty or
   blank line. A line whose first non-blank character is `#` is a comment;
   a `#` inside a line is part of a token, so a password may hold one (as
   in curl). A token may be quoted (`"two words"`, with `\"`, `\\`, `\n`,
   `\r`, `\t` escapes; curl 7.84 and later); a quoted token is never a
   keyword. Unknown words are skipped. A keyword without a value, an
   unclosed quote or a `login` before any entry is a syntax error with its
   line (`request.auth_netrc_failed`, kind `parse`, detail `line`); the
   message never shows a value.
3. **The entry** (`File.Lookup`): the first `machine` entry whose name is
   the URL's host (ignoring case; the port does not count), else the first
   `default` entry, wherever it is in the file. With a user in the URL
   (`https://bob@host/`) only entries with that login count, as in curl.
   A URL with a user **and** password is sent with those (httpclient turns
   them into Basic auth) and the file is not read.
4. **Sending**: `Authorization: Basic ` + base64 of `login:password`, as a
   `basic` auth sends it; an enabled `Authorization` row wins, and
   `settings.omitHeaders` can leave it out. The URL is the one of the
   request (GraphQL: its endpoint, also for a subscription), after its
   variables and path parameters.
5. **Permissions**: on Unix, a file the group or others may read or write
   (`chmod` other than `600`) is used anyway, as curl does, with the
   warning `request.netrc_insecure` on the response.

**Nothing from the file is stored.** The project file holds only the
type and `netrcFile`; the history, the timeline, copy as cURL and the HAR
export mask the password (`AuthResult.Secrets`) and, by name, the
`Authorization` header, unless copied or exported with secrets.

**Auth tab.** The File field says which file will be read
(`HTTPService.NetrcFile`, which resolves the path without reading it).
**Check** (requests only, also under an inherited netrc auth) calls
`HTTPService.NetrcCheck`, which reads the file as a send now would and
returns the host, the entry (`machine` name or `default`) and its line,
the file, the login and whether there is a password; never the password.
It fails like a send (`request.auth_netrc_failed`,
`request.auth_netrc_no_entry` naming the host), and with `http.no_netrc`
when the auth is not an enabled netrc auth.

**Import**: Insomnia's `netrc` auth becomes a `netrc` auth without
`netrcFile` (`disabled` kept); Postman has no netrc auth, and exporting
one to Postman gives `noauth` with a warning ([import.md](import.md)).

Tests: `internal/core/netrc` covers the parser (one-line and multi-line
entries, `default`, comments and `#` in a password, quoted tokens and
escapes, quoted keywords, `macdef` bodies ending at a blank line or the end
of the file, CRLF, unknown words, syntax errors), the lookup (case,
`default` placement, the URL's login), the file location (NETRC,
Windows, `~/`, relative paths) and loading (permissions, the Windows
fallback, a directory, a large file). `authnetrc_test.go`
(internal/core/request) builds requests over an in-memory `netrc.Env`,
and `TestNetrcNotReadForOtherTypes` gives every other type (and a
disabled netrc auth) an `Env` that fails the test if anything is looked
up or opened. `TestSendNetrc` (internal/app/services/http) sends an
inherited netrc auth with a file in a temporary directory named by an
injected `NETRC`, checks the server's credentials and the masking in the
timeline, history, cURL and HAR, that the header preview does not read
the file, and the check; `TestNetrcNotOpenedForOtherAuthTypes` sends,
previews, copies and exports every other type with an opener that fails
the test. No test reads the real `~/.netrc`.

### Explicit headers and parameters win

`request.WithAuthHeaders(headers, authHeaders)` appends the auth's headers
after the request's own rows, **except** when an enabled row of the same
name (ignoring case) exists: a typed `Authorization` row replaces the Auth
tab, as in Postman, and as URL userinfo already works in httpclient
([http-client.md](http-client.md#url-what-is-kept)). A disabled row does not
count.

`request.WithAuthQuery(url, params)` appends the API key to the query of the
rendered URL, before any `#fragment`, unless the query already has a
parameter with that name (compared encoded): a parameter typed in the URL
wins. The name and value are credentials, not URL text, so unlike the Params
table (which writes text as typed, [frontend.md](frontend.md#request-editor))
they are encoded completely: everything but `A–Z a–z 0–9 - . _ ~` becomes
`%XX`, space as `%20`, `+` as `%2B`. The rest of the URL is not touched.

## Inheritance

`request.ResolveAuth(own, parents)` returns the auth a request sends. An auth
other than `inherit` (or `""`) is the request's own; `none` and a disabled
auth stop there. Otherwise the folders are searched from the closest one up:
the first whose auth is not `inherit` supplies it, and the result says which
folder. Top-level requests, and requests with no such folder, send nothing.
The project root has no auth. A folder whose `_folder.json` did not load
stops the search with `request.auth_inherit_failed` (detail `folder`), rather
than silently sending the auth of a folder further up.

`CollectionService.InheritedAuth(id)` gives the Auth tab the result:
`{type, disabled, folderId, folderName, auth}`; for a folder ID it is what
the folder's own `inherit` resolves to, from the folders above it. `auth`
is that folder's auth (pending edit included) for the tab to list its
fields read-only; its secret member is kept only when it is `{{references}}`
(`requestsecrets.IsReference`). A typed secret comes back `""` with the
member in `localSecrets`, so a credential never leaves the folder's own
editor (the note shows `•••••• stored with “Users”`).

A request any number of folders deep uses the nearest folder that sets
auth: folders on the way with `inherit` (or no auth) are skipped
(`TestInheritedAuthResolvesVariables` sends from four levels down).

## Auth on every protocol (AUTH-31)

WebSocket, Socket.IO and gRPC requests have an `auth` member and an Auth
tab like HTTP's, with the same types, folder inheritance (`inherit`),
`{{variables}}` and secrets kept on this computer. The auth goes with the
connection's handshake:

| Protocol | Where it goes | Doc |
|---|---|---|
| WebSocket | The upgrade request: headers after the Headers rows, query parameters at the end of the URL | [websocket.md](websocket.md#auth) |
| Socket.IO | Every transport request (long-polling and the WebSocket upgrade): headers and query, as WebSocket; or, with `authIn` `"payload"`, members of the CONNECT packet's auth payload (on a v2 server, which has none, the same members as URL query parameters, read as `socket.handshake.query`) | [socketio.md](socketio.md#auth) |
| gRPC | The call's metadata: each header as a key in lower case (`authorization: Bearer …`) | [grpc.md](grpc.md#auth) |

| Type | WebSocket and Socket.IO | gRPC |
|---|---|---|
| `none`, `inherit` | as for HTTP (a folder's auth is resolved by `ResolveAuth`) | same |
| `basic`, `bearer`, `jwt`, `netrc`, `oauth2` | the header (netrc: the URL host's entry) | the header as metadata (netrc: the address's host) |
| `apikey` | the header, or `in` `query` the query parameter | the header as metadata; `in` `query` is refused |
| `jwt`, `oauth2` with `in` `query` | the query parameter | refused: gRPC has no query string |
| `digest`, `oauth1`, `awsv4`, `hawk` | refused | refused |

Digest answers an HTTP 401 challenge, and OAuth 1.0, AWS Signature and
Hawk sign an HTTP request's method, URL and body: none of that exists for
a handshake. Rather than connect without the credentials the Auth tab
shows, the connect (or call) is refused with `request.auth_not_applicable`
(kind `unsupported`, details `type`, `protocol`, and `field` `in` for a
location), and the Auth tab says so before connecting, for the request's
own type and for the type it inherits (`AuthEditor`'s `protocol`). A
disabled auth sends nothing, as for HTTP.

The pieces:

- `request.HandshakeAuth(auth, protocol, opts)` is `AuthWith` with those
  checks (`internal/core/request/handshake.go`). Its result also has
  `Credentials`, the rendered values (a token without its prefix, a
  username and password, an API key's name and value), for the Socket.IO
  auth payload (`request.WithPayloadCredentials`), and adds them to
  `Secrets`. `request.HandshakeHeaders` drops the auth headers an enabled
  row of the same name overrides (a typed `Authorization` header or
  metadata row wins, as for HTTP) and puts gRPC keys in lower case.
- `httpservice.HandshakeAuth(ctx, s, input)` (not bound) is the app's
  `request.HandshakeAuthFunc`: it loads the request's auth with
  `collection.LoadAuthSource` (pending edits, folder inheritance, local
  secrets; the HTTP service's `WithAuthLoader`), applies
  `request.HandshakeAuth` with the project root and allowed directories
  of a JWT key file and the netrc settings, and for `oauth2` gets the token
  as a send does (the stored token while it is good, else refreshed or a
  new one from the token endpoint or the browser sign-in, stored after;
  the token request is the user's, PRV-05, and its errors are masked).
  The token is fetched only after the connection's own `{{variables}}` are
  all defined (`HandshakeInput.Ready`), so a connect refused for an
  undefined variable sends nothing.
- `websocket.WithAuth`, `socketio.WithAuth` and `grpc.WithAuth` take that
  function (`internal/app/services.go`). The connection renders the auth
  fields with its own `{{variables}}` rendering, so undefined names of the
  auth and of the URL or headers come in one `template.unresolved` error
  and "Connect anyway" covers them too; gRPC, which has no such answer,
  refuses an undefined name in the auth.
- The auth's values are masked (`httpclient.SecretMasker`) in what the UI
  is shown: the WebSocket handshake's URL and request headers and the
  connection URL, the Socket.IO `open` URL and CONNECT packet item, and the
  gRPC request item's metadata. Logs name the scheme and host only.
- The Auth tab's tools (JWT preview, netrc check, OAuth 2.0 token panel)
  work on these requests too: `JWTPreview`, `NetrcFile`, `NetrcCheck` and
  the `OAuth2*` methods load the request with the auth loader
  (`collection.HTTPSource.Handshake` names the kind; such a source holds
  only the URL, a grpc request's address, and the auth, and is never sent
  over HTTP).

Files: the `auth` member of a `websocket`, `socketio` or `grpc` file is
optional; a missing one (every file written before this) reads as
`"none"`, so old requests connect exactly as before, and `"none"` is
written as a missing member, so they are not changed by a save. Requests
created in the app start with `"inherit"`, as HTTP requests do
([file-format.md](file-format.md#websocket-request-kind-websocket)). The
typed secret goes to the local side file like an HTTP request's
(`requestsecrets.SplitFile`); `TestProjectFilesNeverHoldSecrets` covers
the three kinds.

Tests: `TestHandshakesSendTheFolderAuth` (internal/app) sends one folder's
bearer auth, typed in its Auth tab, through the WebSocket, Socket.IO and
gRPC services of the app to the in-process test servers, which see it,
and checks that no project file holds the token; `TestConnectSendsAuth`
(websocket, socketio), `TestInvokeSendsAuth` (grpc) and
`TestHandshakeAuthOAuth2` (http) cover each protocol's placement,
overrides, masking and refusals, and `TestHandshakeAuthApplies`,
`TestWithPayloadCredentials` (request) and `TestHandshakeAuthMember`
(projectfile) the core.

## Variables in auth fields (AUTH-32)

Every field of every type is rendered before it is checked and sent:
Basic and Digest username and password, the bearer token and prefix, the
API key's name and value (header or query). Any `{{variable}}` of the
[precedence chain](templates.md#scopes-and-precedence) works, secret ones
included (their values are masked in the timeline), and dynamic values
too (`{{$uuid}}`).

**Scope of an inherited auth.** A folder's auth is rendered with the
variables of the **request that sends it**, not of the folder that
defines it: the global variables, the active environment, the request's
collection and all its folders down to the nearest. So a folder can set
`Authorization: Bearer {{token}}` once and each subfolder (or environment)
supply its own `token`, and a variable defined below the folder that sets
the auth is used. This is the chain the rest of the request uses, so one
request never resolves one name two ways. `TestInheritedAuthResolvesVariables`
and `TestInheritedDigestResolvesVariables` (internal/app/services/http)
cover each type, per environment.

**In the editors.** The text fields draw `{{variables}}` as chips resolved
with the edited item's variables; a secret field shows them once revealed
(a reference starts revealed). For a request with **Inherit**, the tab
lists the inherited folder's fields read-only below the note, their
references as chips resolved with the request's variables, as they will be
sent. A folder's own Auth tab resolves its chips with the folder's
variables; the requests inside may resolve them differently, and the note
says so. An undefined name in an auth field is reported with the others
before sending (`template.unresolved`, "Send anyway").

A folder's auth is edited in the Auth tab of its `DIR` tab, the folder
settings view (COL-03, [collection.md](collection.md#folder-settings)),
with the same `AuthEditor` as a request (`folder` set, which changes only
the notes). On a folder, `inherit` passes on the auth of the folder above,
`none` makes the requests inside that inherit send nothing, and a disabled
auth sends nothing and stops the search too. A folder's typed secret is
split off like a request's and kept under the folder's ID
(`<local>/secrets/requests/<folder id>.json`); a request that inherits the
auth gets it merged in at send time, from the folder's pending edit when
it has one. Duplicating a folder copies its side file to the copy.

## Secrets

Passwords, tokens, API keys, JWT signing keys, OAuth 1.0 consumer and token secrets, AWS secret keys and session tokens, Hawk keys and OAuth 2.0 client secrets (and the tokens OAuth 2.0 gets, kept in `<local>/secrets/oauth2/tokens.json`, [Token store](#token-store)) must not end up in project files, which are
meant to be committed and shared (STO-04, SEC-01). There are two ways to
fill a secret field:

1. **A `{{variable}}` (recommended).** `"token": "{{token}}"`, with `token` a
   secret variable of the environment ([environments.md](environments.md)).
   The reference is written to the project file, so everyone who opens the
   project gets a working request with their own value.
2. **The value itself.** It is kept in local storage on this computer:

   ```
   <local>/secrets/requests/<request id>.json    {"version": 1, "itemId": "req_…", "values": {"auth.token": "…"}}
   ```

   `<local>` is the workspace's side-file directory
   ([workspace.md](workspace.md#local-side-files)), never inside the project.
   The file is `0600` in a `0700` directory. The project file keeps the
   other members and `"localSecrets": ["token"]`, which marks the value as
   stored locally.

The rule (`requestsecrets.IsReference`) is strict: a value goes to the
project only when it is one or more `{{variable}}` references and
whitespace. `Bearer {{t}}`, `sk_live_{{suffix}}`, an escaped `\{{x}}` or an
unterminated `{{x` count as typed values, since their text may be part of a
secret.

> **Encryption.** Like the environment secrets and the token store, the
> side file is plain JSON until the user sets a master password; it is then
> encrypted with AES-256-GCM, and while the secrets are locked reading it
> fails with `secretstore.locked`
> ([privacy.md](privacy.md#encrypted-secrets)). The key can also be kept in
> the OS keychain ([privacy.md](privacy.md#keychain)).

The same side file holds the values of the item's **secret variables** (a
folder's, and for the workspace ID the global variables of
`postsonde.json`), keyed `var.<name>`:

```
<local>/secrets/requests/<folder id>.json   {"version": 1, "itemId": "fld_…", "values": {"auth.token": "…", "var.apiKey": "…"}}
```

See [environments.md](environments.md#folder-and-global-secret-variables).
`requestsecrets.SplitFile`, `MergeFile` and `Store.MoveOut` handle a whole
project file (a request's or folder's auth, a folder's or the workspace's
variables), so writers need not know which members are secret.

**What is not a secret field.** Header, query, metadata and body values are
written as typed: PostSonde does not guess which of them is a credential.
Put a credential there as a `{{variable}}` of a secret environment, folder
or global variable; the timeline masks it and the project file holds only
the reference. The one exception the timeline knows is `Authorization`
(and `Proxy-Authorization`) typed in the headers table: masked when shown,
but written to the project as typed.

### How it flows

- **Save.** `UpdateRequest` maps the DTO to a file and calls
  `requestsecrets.Split`: a typed value is removed from the file and marked
  local; a reference stays and loses any mark. The pending auto-save holds
  both parts; the write puts the side file first, then the project file. A
  type without a secret (and a type this version does not know) is not
  touched. So auto-save, flush on close and flush at quit never write a typed
  secret into the project.
- **Every other write, and hand-written secrets (PRV-09).** A secret
  typed into a project file by hand (a `"token"` without `localSecrets`) is
  read and used as it is: loading never writes. Every project file
  PostSonde writes goes through `requestsecrets.Store.MoveOut` first: the
  collection tree calls it for every write (`Tree.KeepSecretsLocal`, set by
  the collection service: saves, renames, reorders, ID fixes, creates,
  duplicates, restores), and so does the import writer. It moves the typed
  value to the side file, then the file is written without it; if the side
  file cannot be read or written, the project file is not written either,
  so the secret is never lost. The value in the file replaces an older local
  one, since it is the one that was shown and sent.
- **Load.** `Request` merges the value back (`requestsecrets.Merge`), from the
  pending edit or from the side file; the side file is read only when the
  file marks a secret. A side file that cannot be parsed fails the load with
  `requestsecrets.local_data_failed` (kind `parse`), so a save never
  overwrites values it could not read.
- **Missing.** A marked member with no local value (another computer, a copy
  of the project, a deleted side file) comes back as `""` with the mark. The
  Auth tab shows a warning; editing other fields keeps the mark; sending
  fails with `request.auth_secret_missing` until the value is typed again.
  Editing the secret field itself removes the mark in the UI, and Go marks
  it again if the new value is typed.
- **Switching type.** Only the current type's members are written; a local
  secret of the old type is removed from the side file with the next save
  (the draft keeps it while the tab is open, so switching back restores it).
- **Two secrets** (OAuth 1.0's consumer and token secrets, AWS's secret
  key and session token): each member is split, marked in `localSecrets`
  and merged on its own; a missing one names itself in
  `request.auth_secret_missing` (field `consumerSecret`, `tokenSecret`,
  `secretKey` or `sessionToken`).
- **Rename, move:** nothing to do, the side file is keyed by ID.
- **Duplicate:** the side files of every copied request and folder are
  added to the new IDs' (`Store.Copy`; a failure is logged; the copy then
  shows the value as missing).
- **Delete:** the side file stays, so Restore brings the credentials back.
  A restored item that had to get a new ID (its ID was taken meanwhile)
  loses them. Purging the trash does not remove side files yet.

## Not handled yet

- Auth headers in the Headers tab's implicit rows, and a preview of the
  exact header (the Auth tab says what is sent in words).
- Removing side files of purged requests.
- JWT: encrypted (JWE) tokens, encrypted PEM keys, JWK keys, and a
  preview for a folder's own auth (a request inside shows it).
- OAuth 2.0: verifying the ID token's signature (it needs the issuer's
  JWKS, a fetch that would have to be an explicit user action), OpenID
  Connect discovery, the nonce of a refreshed ID token, `c_hash`, and the
  implicit grant's `response_type` `id_token` alone; the `plain` PKCE method and the grant without PKCE (S256 is
  always sent; servers ignore it when they do not use PKCE); extra
  parameters of the authorization request; the `iss` check of RFC 9207; a
  token for GraphQL
  subscriptions over WebSocket; the token request's extra parameters sent
  in a header or the URL (Postman's `send_as`); `private_key_jwt` and
  mTLS client authentication; clearing every stored token in one action
  and encrypting them; revoking a token at the server (RFC
  7009); picking one of several stored tokens for an auth (each auth
  configuration has one); importing OAuth 2.0 from Altair and from OpenAPI security schemes
  (still reported as not imported; Postman's and Insomnia's are imported,
  [import.md](import.md)). The other schemes.
- Handshakes (AUTH-31): Digest on a WebSocket upgrade (answering a 401
  to the upgrade request), OAuth 1.0, AWS and Hawk signatures of the
  upgrade request (AWS IoT presigned WebSocket URLs, say), a token for
  GraphQL subscriptions over WebSocket, and the OAuth 2.0 timeline of a
  handshake's token request (the HTTP send's timeline shows it; a
  connection only logs it).
- AWS: chunked (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`) and SigV4a
  signing, and credentials from `~/.aws` or the environment; `omitHeaders`
  entries for the `X-Amz-*` headers are not honoured (they are signed).
  Hawk: checking the server's `Server-Authorization`, bewit URLs, fixed
  timestamps and nonces, and correcting clock skew from a server's
  `WWW-Authenticate` timestamp. Importing AWS and Hawk auth from Insomnia
  (still reported as not supported; Postman's are imported and exported,
  [import.md](import.md#postman-export)).
- Netrc: curl's `--netrc-optional`
  (sending without credentials when the file has no entry); netrc for a
  proxy's credentials.
- OAuth 1.0: the three-legged flow itself (obtaining temporary and token
  credentials is done by sending the requests by hand), the HMAC-SHA512,
  RSA-SHA256 and RSA-SHA512 methods and signing empty parameters that
  Postman offers (imported with a warning, [import.md](import.md)), RSA
  keys from a file, and signing bodies of
  other form types. Digest for WebSocket and GraphQL subscriptions, and
  `Proxy-Authenticate` challenges.
- Reusing a Digest nonce across sends: each send starts without
  credentials.
