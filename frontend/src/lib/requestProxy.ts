// A request's own proxy, settings.proxy of every request kind
// (docs/file-format.md#proxy): inherit the proxy in effect (the default,
// left out of the file), a custom proxy URL, or none. The editor shows a
// custom URL as fields (type, host, port, user name) and writes the URL
// back from them; {{variables}} may stand in any part. The password is
// separate and never in the URL.
import type { RequestProxyDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import { isTemplateReference, type SecretStorage } from "./auth";
import { scanTemplate } from "./templateSyntax";

export type { RequestProxyDTO };

/** The modes the editor offers. */
export type RequestProxyMode = "inherit" | "custom" | "none";

export const REQUEST_PROXY_MODES: readonly RequestProxyMode[] = ["inherit", "custom", "none"];

/** The member of localSecrets that marks the password as kept on this computer. */
export const PROXY_PASSWORD_SECRET = "password";

/**
 * The mode a stored proxy means: inherit for none at all, "" or
 * "inherit". Null for a mode this version does not know (kept, and
 * refused when sending).
 */
export function requestProxyModeOf(proxy: RequestProxyDTO | null | undefined): RequestProxyMode | null {
  const mode = proxy?.mode ?? "";
  if (mode === "" || mode === "inherit") return "inherit";
  if (mode === "custom" || mode === "none") return mode;
  return null;
}

/**
 * The settings member after a change, keeping what `patch` does not name
 * (the URL and password in the other modes, the unknown members): null,
 * left out of the file, when the request inherits and keeps nothing else.
 */
export function requestProxyPatch(current: RequestProxyDTO | null | undefined, patch: Partial<RequestProxyDTO>): RequestProxyDTO | null {
  const next: RequestProxyDTO = { mode: "", url: "", password: "", localSecrets: [], extra: "", ...current, ...patch };
  const inherit = next.mode === "" || next.mode === "inherit";
  if (inherit && next.url === "" && next.password === "" && (next.localSecrets ?? []).length === 0 && next.extra === "") return null;
  return next;
}

/**
 * The proxy after the user typed `password`: editing it removes the
 * local-secret mark, as the auth editor does (Go marks it again when the
 * new value is kept locally).
 */
export function withProxyPassword(current: RequestProxyDTO | null | undefined, password: string): RequestProxyDTO | null {
  const localSecrets = (current?.localSecrets ?? []).filter((m) => m !== PROXY_PASSWORD_SECRET);
  return requestProxyPatch(current, { password, localSecrets });
}

/** Where the proxy's password is kept, for the hint under its field. */
export function proxyPasswordStorage(proxy: RequestProxyDTO | null | undefined): SecretStorage {
  const password = proxy?.password ?? "";
  if (password === "") return (proxy?.localSecrets ?? []).includes(PROXY_PASSWORD_SECRET) ? "missing" : "empty";
  return isTemplateReference(password) ? "reference" : "local";
}

/** The proxy types of the editor, SOCKS5 first: the scheme without its DNS variant. */
export type ProxyType = "socks5" | "socks4" | "http" | "https";

export const PROXY_TYPES: readonly ProxyType[] = ["socks5", "socks4", "http", "https"];

/** The port Go uses when a proxy URL names none (netx.DefaultSOCKSPort, DefaultHTTPProxyPort; 443 for https). */
export function defaultProxyPort(type: ProxyType): number {
  return type === "https" ? 443 : 1080;
}

/** A custom proxy URL as the editor's fields. */
export interface ProxyFields {
  readonly type: ProxyType;
  /** socks5h rather than socks5, socks4a rather than socks4; not written for HTTP proxies, which always resolve. */
  readonly remoteDns: boolean;
  /** As typed: an IPv6 address without brackets. */
  readonly host: string;
  /** "" for the default port. */
  readonly port: string;
  /** Percent-decoded; "" for none. */
  readonly user: string;
}

export const DEFAULT_PROXY_FIELDS: ProxyFields = { type: "socks5", remoteDns: true, host: "", port: "", user: "" };

const SCHEMES: Readonly<Record<string, { type: ProxyType; remoteDns: boolean }>> = {
  socks5h: { type: "socks5", remoteDns: true },
  socks5: { type: "socks5", remoteDns: false },
  socks4a: { type: "socks4", remoteDns: true },
  socks4: { type: "socks4", remoteDns: false },
  http: { type: "http", remoteDns: true },
  https: { type: "https", remoteDns: true },
};

/** The scheme the fields stand for. */
export function proxyScheme(fields: Pick<ProxyFields, "type" | "remoteDns">): string {
  if (fields.type === "socks5") return fields.remoteDns ? "socks5h" : "socks5";
  if (fields.type === "socks4") return fields.remoteDns ? "socks4a" : "socks4";
  return fields.type;
}

/**
 * `text` with every {{variable}} replaced by as many "x", so that the
 * characters that split a URL are found only outside variables, at the
 * same offsets.
 */
function maskVariables(text: string): string {
  let out = "";
  let at = 0;
  for (const span of scanTemplate(text)) {
    if (span.kind !== "var") continue;
    out += text.slice(at, span.start) + "x".repeat(span.end - span.start);
    at = span.end;
  }
  return out + text.slice(at);
}

/** Whether `text` holds a {{variable}}. */
export function hasVariable(text: string): boolean {
  return scanTemplate(text).some((s) => s.kind === "var");
}

/** Applies `fn` to the parts of `text` outside its {{variables}}, keeping these as typed. */
function mapOutsideVariables(text: string, fn: (part: string) => string): string {
  let out = "";
  let at = 0;
  for (const span of scanTemplate(text)) {
    if (span.kind !== "var") continue;
    out += fn(text.slice(at, span.start)) + text.slice(span.start, span.end);
    at = span.end;
  }
  return out + fn(text.slice(at));
}

/** Percent-encodes a user name for the URL: every reserved character, but {{variables}} as typed. */
export function encodeProxyUser(user: string): string {
  return mapOutsideVariables(user, (part) => encodeURIComponent(part));
}

/** The user name a URL holds, percent-decoded outside its {{variables}}; as written when it does not decode. */
function decodeProxyUser(user: string): string {
  try {
    return mapOutsideVariables(user, (part) => decodeURIComponent(part));
  } catch {
    return user;
  }
}

/**
 * Splits a custom proxy URL, scheme://[user@]host[:port], into fields:
 * {{variables}} may stand in any part but the scheme. "" is the default
 * fields. Null when the URL cannot be shown as fields without losing
 * something (an unknown scheme, a password, a path, an IPv6 address
 * without brackets): the editor then shows it as one text field.
 */
export function parseProxyUrl(url: string): ProxyFields | null {
  const text = url.trim();
  if (text === "") return DEFAULT_PROXY_FIELDS;
  const scheme = /^([A-Za-z][A-Za-z0-9+.-]*):\/\//.exec(text);
  if (scheme === null) return null;
  const known = SCHEMES[(scheme[1] ?? "").toLowerCase()];
  if (known === undefined) return null;
  let rest = text.slice(scheme[0].length);
  let masked = maskVariables(rest);
  // Go allows a path of "/".
  if (masked.endsWith("/")) {
    rest = rest.slice(0, -1);
    masked = masked.slice(0, -1);
  }
  if (/[/?#\s]/.test(masked)) return null;

  const at = masked.lastIndexOf("@");
  const userinfo = at < 0 ? "" : rest.slice(0, at);
  if (at >= 0 && masked.slice(0, at).includes(":")) return null; // a password in the URL
  const hostport = at < 0 ? rest : rest.slice(at + 1);
  const hostMasked = at < 0 ? masked : masked.slice(at + 1);
  if (hostMasked.includes("@")) return null;

  let host: string;
  let port = "";
  if (hostMasked.startsWith("[")) {
    const close = hostMasked.indexOf("]");
    if (close < 0) return null;
    host = hostport.slice(1, close);
    const after = hostMasked.slice(close + 1);
    if (after !== "" && !after.startsWith(":")) return null;
    port = hostport.slice(close + 2);
  } else {
    const colon = hostMasked.lastIndexOf(":");
    host = colon < 0 ? hostport : hostport.slice(0, colon);
    port = colon < 0 ? "" : hostport.slice(colon + 1);
    if (colon >= 0 && hostMasked.slice(0, colon).includes(":")) return null; // IPv6 without brackets
  }
  if (maskVariables(port).includes(":")) return null;
  return { ...known, host, port, user: decodeProxyUser(userinfo) };
}

/**
 * The URL of the fields: "" when they are the default and empty, so a
 * request that never got a proxy keeps none. An IPv6 host gets its
 * brackets, the user name is percent-encoded around its {{variables}}.
 */
export function composeProxyUrl(fields: ProxyFields): string {
  const host = fields.host.trim();
  const port = fields.port.trim();
  const user = fields.user;
  const scheme = proxyScheme(fields);
  if (host === "" && port === "" && user === "" && scheme === proxyScheme(DEFAULT_PROXY_FIELDS)) return "";
  const bracketed = maskVariables(host).includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return `${scheme}://${user === "" ? "" : `${encodeProxyUser(user)}@`}${bracketed}${port === "" ? "" : `:${port}`}`;
}

/** Problems of a custom proxy that sending would refuse; the editor shows them but saves what was typed. */
export type ProxyProblem =
  | "hostRequired"
  | "hostInvalid"
  | "portInvalid"
  | "passwordNeedsUser"
  | "socks4Password"
  | "httpUserColon";

/**
 * Checks the fields and password of a custom proxy, as Go would after
 * the {{variables}} are filled in: a part made with a variable is not
 * judged.
 */
export function proxyProblems(fields: ProxyFields, password: string): Partial<Record<"host" | "port" | "user" | "password", ProxyProblem>> {
  const problems: Partial<Record<"host" | "port" | "user" | "password", ProxyProblem>> = {};
  const host = fields.host.trim();
  if (host === "") problems.host = "hostRequired";
  else if (/[\s/?#@]/.test(maskVariables(host))) problems.host = "hostInvalid";
  const port = fields.port.trim();
  if (port !== "" && !hasVariable(port)) {
    const n = /^\d+$/.test(port) ? Number(port) : NaN;
    if (!(n >= 1 && n <= 65535)) problems.port = "portInvalid";
  }
  if ((fields.type === "http" || fields.type === "https") && maskVariables(fields.user).includes(":")) problems.user = "httpUserColon";
  if (password !== "") {
    if (fields.type === "socks4") problems.password = "socks4Password";
    else if (fields.user === "") problems.password = "passwordNeedsUser";
  }
  return problems;
}
