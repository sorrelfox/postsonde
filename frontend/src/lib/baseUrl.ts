// Relative request URLs (COL-08): a URL that starts with one "/" is sent
// to the {{baseUrl}} variable followed by that path, as Go's
// request.ExpandURL does, so switching environments switches servers.
// baseUrl is an ordinary variable: each environment defines its own, the
// global variables (the base environment) a default. See
// docs/environments.md#base-urls.

import { lookupVariable, SECRET_MASK, type TemplateVariableSet, type VariableDTO } from "./templateVariables";

/** The variable a relative URL is sent to (Go: request.BaseURLVariable). */
export const BASE_URL_VARIABLE = "baseUrl"; // i18n-ignore: a variable name

/** Whether `url` is relative: it starts with exactly one "/" (not "//host"). */
export function isRelativeUrl(url: string): boolean {
  const u = url.trim();
  return u.startsWith("/") && !u.startsWith("//");
}

/** `base` followed by `path`, with one "/" between them (Go: request.JoinBaseURL). */
export function joinBaseUrl(base: string, path: string): string {
  return base.replace(/\/+$/, "") + path;
}

/** Where a relative URL goes. */
export interface RelativeUrl {
  /** The baseUrl variable as the scopes resolve it; undefined when no scope defines it. */
  readonly base: VariableDTO | undefined;
  /** The URL it is sent to, the path as typed; a secret base is masked. "" when the base does not resolve. */
  readonly url: string;
}

/**
 * Where relative `url` goes with the request's variables `set`, or null
 * when it is not relative. What the editor can tell without rendering the
 * path: its own {{variables}} are shown as typed; a URL that becomes
 * relative only once rendered ("{{path}}/x") is not recognised here, but
 * is sent to the base all the same.
 */
export function relativeUrl(url: string, set: TemplateVariableSet): RelativeUrl | null {
  if (!isRelativeUrl(url)) return null;
  const base = lookupVariable(set, BASE_URL_VARIABLE);
  if (base === undefined || !base.resolved) return { base, url: "" };
  return { base, url: joinBaseUrl(base.secret ? SECRET_MASK : base.value.trim(), url.trim()) };
}
