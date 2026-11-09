// Request auth (AUTH-01…12, AUTH-14…19): the auth types, their secret members and
// the inherited-auth call into CollectionService. The mapping to headers and
// query parameters, and where secrets are kept, are decided in Go
// (internal/core/request/auth.go, internal/core/requestsecrets); see
// docs/auth.md.
import { Events } from "@wailsio/runtime";
import {
  CollectionService,
  type AuthDTO,
  type AWSAuthDTO,
  type HawkAuthDTO,
  type InheritedAuthDTO,
  type JWTAuthDTO,
  type OAuth1AuthDTO,
  type OAuth2AuthDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import {
  HTTPService,
  type JWTPreviewDTO,
  type NetrcCheckDTO,
  type NetrcFileDTO,
  type OAuth2IDTokenDTO,
  type OAuth2TokenChangedDTO,
  type OAuth2TokenDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import { t } from "../i18n";
import type { CancellablePromise } from "./calls";
import { scanTemplate } from "./templateSyntax";

export type {
  AuthDTO,
  AWSAuthDTO,
  HawkAuthDTO,
  InheritedAuthDTO,
  JWTAuthDTO,
  JWTPreviewDTO,
  NetrcCheckDTO,
  NetrcFileDTO,
  OAuth1AuthDTO,
  OAuth2AuthDTO,
  OAuth2IDTokenDTO,
  OAuth2TokenChangedDTO,
  OAuth2TokenDTO,
};

/**
 * The auth types of the file format, in the order of the type picker
 * (mockup). A label is looked up when read, in the language in use (UI-10);
 * scheme names (Bearer, Digest, JWT, Hawk…) are not translated.
 */
export const AUTH_TYPES: readonly { readonly type: string; readonly label: string }[] = [
  {
    type: "inherit",
    get label() {
      return t("lib.auth.type.inherit");
    },
  },
  {
    type: "none",
    get label() {
      return t("lib.auth.type.none");
    },
  },
  // i18n-ignore-start: scheme names
  { type: "bearer", label: "Bearer" },
  { type: "basic", label: "Basic" },
  { type: "digest", label: "Digest" },
  // i18n-ignore-end
  {
    type: "apikey",
    get label() {
      return t("lib.auth.type.apikey");
    },
  },
  { type: "jwt", label: "JWT" },
  // i18n-ignore-start: scheme names
  { type: "oauth1", label: "OAuth 1.0" },
  { type: "oauth2", label: "OAuth 2.0" },
  // i18n-ignore-end
  {
    type: "awsv4",
    get label() {
      return t("lib.auth.type.awsv4");
    },
  },
  // i18n-ignore-start: scheme names
  { type: "hawk", label: "Hawk" },
  { type: "netrc", label: "Netrc" },
  // i18n-ignore-end
];

/** Long names, for notes ("Inherited from … : Bearer token"), looked up when read. */
export const AUTH_TYPE_NAMES: Readonly<Record<string, string>> = {
  get inherit() {
    return t("lib.auth.name.inherit");
  },
  get none() {
    return t("lib.auth.name.none");
  },
  get bearer() {
    return t("lib.auth.name.bearer");
  },
  get basic() {
    return t("lib.auth.name.basic");
  },
  get digest() {
    return t("lib.auth.name.digest");
  },
  get apikey() {
    return t("lib.auth.name.apikey");
  },
  get jwt() {
    return t("lib.auth.name.jwt");
  },
  oauth1: "OAuth 1.0", // i18n-ignore: a scheme name
  oauth2: "OAuth 2.0", // i18n-ignore: a scheme name
  get awsv4() {
    return t("lib.auth.name.awsv4");
  },
  hawk: "Hawk", // i18n-ignore: a scheme name
  get netrc() {
    return t("lib.auth.name.netrc");
  },
};

/** The prefix a bearer token is sent with when the file sets none. */
export const DEFAULT_BEARER_PREFIX = "Bearer"; // i18n-ignore: an auth scheme token

/** The type an auth is in; "" is written as "inherit". */
export function authType(auth: AuthDTO): string {
  return auth.type === "" ? "inherit" : auth.type;
}

export type SecretMember =
  | "password"
  | "token"
  | "value"
  | "secret"
  | "consumerSecret"
  | "tokenSecret"
  | "secretKey"
  | "sessionToken"
  | "hawkKey"
  | "clientSecret";

/** The members of `type` that hold its secrets; none for a type without one. */
export function secretMembers(type: string): readonly SecretMember[] {
  switch (type) {
    case "basic":
    case "digest":
      return ["password"];
    case "bearer":
      return ["token"];
    case "apikey":
      return ["value"];
    case "jwt":
      return ["secret"];
    case "oauth1":
      return ["consumerSecret", "tokenSecret"];
    case "awsv4":
      return ["secretKey", "sessionToken"];
    case "hawk":
      return ["hawkKey"];
    case "oauth2":
      // The client secret, and the password of the password grant.
      return ["clientSecret", "password"];
    default:
      return [];
  }
}

/** The first member of `type` that holds a secret, or null for a type without one. */
export function secretMember(type: string): SecretMember | null {
  return secretMembers(type)[0] ?? null;
}

/** True when the local-secret mark names `member`. */
export function isMarkedLocal(auth: AuthDTO, member: SecretMember): boolean {
  return (auth.localSecrets ?? []).includes(member);
}

/**
 * True when a secret value may be written to the project file: at least one
 * `{{variable}}` and nothing else but whitespace. Follows IsReference in
 * internal/core/requestsecrets; Go decides, this only picks the hint.
 */
export function isTemplateReference(text: string): boolean {
  const spans = scanTemplate(text);
  if (spans.length === 0 || spans.some((s) => s.kind !== "var")) return false;
  let rest = "";
  let at = 0;
  for (const s of spans) {
    rest += text.slice(at, s.start);
    at = s.end;
  }
  rest += text.slice(at);
  return rest.trim() === "";
}

/** Where a secret value is kept, for the hint under its field. */
export type SecretStorage =
  /** Nothing typed. */
  | "empty"
  /** A `{{variable}}` reference, saved in the project file. */
  | "reference"
  /** A typed value, kept in local storage on this computer. */
  | "local"
  /** Typed on another computer; not stored on this one. */
  | "missing";

/** The value of a secret member; the jwt and oauth1 secrets are left out of the DTO when "". */
export function secretValue(auth: AuthDTO, member: SecretMember): string {
  return auth[member] ?? "";
}

export function secretStorage(auth: AuthDTO, member: SecretMember): SecretStorage {
  const value = secretValue(auth, member);
  if (value === "") return isMarkedLocal(auth, member) ? "missing" : "empty";
  return isTemplateReference(value) ? "reference" : "local";
}

/** The JWT signing algorithms (internal/core/jwt), in the order of the picker. */
export const JWT_ALGORITHMS: readonly string[] = [
  "HS256",
  "HS384",
  "HS512",
  "RS256",
  "RS384",
  "RS512",
  "PS256",
  "PS384",
  "PS512",
  "ES256",
  "ES384",
  "ES512",
  "EdDSA",
];

/** The algorithm of a jwt auth that names none. */
export const DEFAULT_JWT_ALGORITHM = "HS256";

/** The query parameter a JWT goes in when the auth names none (RFC 6750). */
export const DEFAULT_JWT_QUERY_PARAM = "access_token";

/** True for the algorithms that sign with a shared secret rather than a private key. */
export function isHmacAlgorithm(alg: string): boolean {
  return alg === "" || alg.startsWith("HS");
}

/** The jwt members of `auth`, with the defaults of a new jwt auth when it has none. */
export function jwtOf(auth: AuthDTO): JWTAuthDTO {
  return (
    auth.jwt ?? {
      algorithm: DEFAULT_JWT_ALGORITHM,
      keyFile: "",
      secretBase64: false,
      header: "",
      payload: "",
      issuedAt: false,
      notBefore: false,
      jwtId: false,
      expiresIn: 0,
    }
  );
}

/** The OAuth 1.0 signature methods (internal/core/oauth1), in the order of the picker. */
export const OAUTH1_SIGNATURE_METHODS: readonly string[] = ["HMAC-SHA1", "HMAC-SHA256", "RSA-SHA1", "PLAINTEXT"];

/** The signature method of an oauth1 auth that names none. */
export const DEFAULT_OAUTH1_SIGNATURE_METHOD = "HMAC-SHA1";

/** The oauth1 members of `auth`, with the defaults of a new oauth1 auth when it has none. */
export function oauth1Of(auth: AuthDTO): OAuth1AuthDTO {
  return (
    auth.oauth1 ?? {
      consumerKey: "",
      accessToken: "",
      signatureMethod: DEFAULT_OAUTH1_SIGNATURE_METHOD,
      callback: "",
      verifier: "",
      realm: "",
      omitVersion: false,
      nonce: "",
      timestamp: "",
      bodyHash: false,
    }
  );
}

/** How long a presigned AWS URL is valid, in seconds, when the auth sets no expiry. */
export const DEFAULT_AWS_EXPIRES = 3600;

/** The awsv4 members of `auth`, with the defaults of a new awsv4 auth when it has none. */
export function awsOf(auth: AuthDTO): AWSAuthDTO {
  return auth.aws ?? { accessKey: "", region: "", service: "", unsignedPayload: false, expires: 0 };
}

/** The Hawk algorithms (internal/core/hawk), in the order of the picker. */
export const HAWK_ALGORITHMS: readonly string[] = ["sha256", "sha1"];

/** The algorithm of a hawk auth that names none. */
export const DEFAULT_HAWK_ALGORITHM = "sha256";

/** The hawk members of `auth`, with the defaults of a new hawk auth when it has none. */
export function hawkOf(auth: AuthDTO): HawkAuthDTO {
  return auth.hawk ?? { id: "", algorithm: DEFAULT_HAWK_ALGORITHM, ext: "", app: "", dlg: "", payloadHash: false };
}

/** The OAuth 2.0 grant types (internal/core/oauth2), in the order of the picker; all are run. */
export const OAUTH2_GRANTS: readonly string[] = ["client_credentials", "authorization_code", "password", "implicit"];

/** The grant of an oauth2 auth that names none. */
export const DEFAULT_OAUTH2_GRANT = "client_credentials";

/** The grants PostSonde gets tokens with; another (of a newer PostSonde) is kept, and refused when sent. */
export function isRunnableGrant(grant: string): boolean {
  return grant === "" || OAUTH2_GRANTS.includes(grant);
}

/** The authorization code grant (AUTH-07): the user signs in in the system browser. */
export const OAUTH2_AUTHORIZATION_CODE = "authorization_code";

/** The resource owner password credentials grant (AUTH-09): the username and password go to the token URL. */
export const OAUTH2_PASSWORD = "password";

/** The implicit grant (AUTH-10, deprecated): the user signs in in the browser and the token comes back in the redirect. */
export const OAUTH2_IMPLICIT = "implicit";

/** True for the grants whose user signs in in the system browser. */
export function isBrowserGrant(grant: string): boolean {
  return grant === OAUTH2_AUTHORIZATION_CODE || grant === OAUTH2_IMPLICIT;
}

/** The query parameter an OAuth 2.0 token goes in when the auth names none (RFC 6750). */
export const DEFAULT_OAUTH2_QUERY_PARAM = "access_token";

/** The oauth2 members of `auth`, with the defaults of a new oauth2 auth when it has none. */
export function oauth2Of(auth: AuthDTO): OAuth2AuthDTO {
  return (
    auth.oauth2 ?? {
      grantType: DEFAULT_OAUTH2_GRANT,
      tokenUrl: "",
      authUrl: "",
      redirectUrl: "",
      clientId: "",
      clientAuth: "",
      scope: "",
      audience: "",
      resource: "",
      state: "",
      tokenParams: [],
      issuer: "",
      idToken: false,
    }
  );
}

/** The JWT request `requestId` would send now, signed and decoded (AUTH-16). */

export function jwtPreview(requestId: string): CancellablePromise<JWTPreviewDTO> {
  return HTTPService.JWTPreview({ requestId });
}

/**
 * The netrc file a netrc auth with file field `file` reads (AUTH-19),
 * resolved with request `requestId`'s variables and project folder ("" for
 * none). Go does not read the file for this.
 */
export function netrcFile(requestId: string, file: string): CancellablePromise<NetrcFileDTO> {
  return HTTPService.NetrcFile({ requestId, file });
}

/** Reads request `requestId`'s netrc file and names the entry its URL's host matches, never the password. */
export function netrcCheck(requestId: string): CancellablePromise<NetrcCheckDTO> {
  return HTTPService.NetrcCheck({ requestId });
}

/**
 * The stored OAuth 2.0 token of request `requestId`'s oauth2 auth, own or
 * inherited (AUTH-13): its status, with the token masked by Go. Nothing
 * is sent.
 */
export function oauth2Token(requestId: string): CancellablePromise<OAuth2TokenDTO> {
  return HTTPService.OAuth2Token({ requestId });
}

/**
 * Gets a token for request `requestId`'s oauth2 auth now and stores it:
 * with `refresh` the stored token refreshed with its refresh token
 * (AUTH-11), else a new one by the auth's grant. Cancel the promise to
 * stop it.
 */
export function getOAuth2Token(requestId: string, refresh: boolean): CancellablePromise<OAuth2TokenDTO> {
  return HTTPService.OAuth2GetToken({ requestId, refresh });
}

/** Removes the stored token of request `requestId`'s oauth2 auth. */
export function clearOAuth2Token(requestId: string): CancellablePromise<OAuth2TokenDTO> {
  return HTTPService.OAuth2ClearToken({ requestId });
}

/** Go's event when a stored OAuth 2.0 token was stored, refreshed or removed. */
export const OAUTH2_TOKEN_CHANGED_EVENT = "http:oauth2-token-changed";

/** Calls `callback` whenever a stored OAuth 2.0 token changes; returns the unsubscribe function. */
export function onOAuth2TokenChanged(callback: (event: OAuth2TokenChangedDTO) => void): () => void {
  return Events.On(OAUTH2_TOKEN_CHANGED_EVENT, (ev) => callback(ev.data as OAuth2TokenChangedDTO));
}

/** The auth request `id` would inherit from its folders. */
export function getInheritedAuth(id: string): CancellablePromise<InheritedAuthDTO> {
  return CollectionService.InheritedAuth(id);
}
