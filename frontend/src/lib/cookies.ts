// Calls into the Go CookiesService (internal/app/services/cookies): the
// cookie jar sends use now (the open project's, or its active
// environment's with separate cookies per environment, CK-03) for the
// cookie manager (CK-02), and the pure helpers the manager uses to label
// the jar and to group, filter and edit cookies. See docs/cookies.md.
import {
  CookiesService,
  type CookieDTO,
  type CookiesDTO,
  type JarDTO,
  type ProblemDTO,
  type SetInputDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/cookies";
import { formatDate, t } from "../i18n";
import type { CancellablePromise } from "./calls";

export type { CookieDTO, CookiesDTO, JarDTO, ProblemDTO, SetInputDTO };

/** Error codes the UI reacts to (internal/core/cookies, internal/app/services/cookies). */
export const COOKIE_CODES = {
  invalidCookie: "cookies.invalid_cookie",
  exists: "cookies.exists",
  notFound: "cookies.not_found",
  invalidKey: "cookies.invalid_key",
} as const;

// i18n-ignore-start: SameSite attribute values
export type SameSite = "" | "Strict" | "Lax" | "None";

export const SAME_SITE_VALUES: readonly SameSite[] = ["", "Lax", "Strict", "None"];
// i18n-ignore-end

/** Every cookie of the jar sends use now, which jar that is, and why its cookie file could not be loaded. */
export function listCookies(): CancellablePromise<CookiesDTO> {
  return CookiesService.List();
}

/**
 * Turns the open project's "separate cookies per environment" setting on
 * or off; resolves to the jar sends use from now on. No cookie moves.
 */
export function setCookiesPerEnvironment(on: boolean): CancellablePromise<CookiesDTO> {
  return CookiesService.SetPerEnvironment(on);
}

/**
 * Names the jar the manager shows: the project, then the environment when
 * the project keeps separate cookies per environment. `scope` says which
 * sends use it.
 */
export function jarLabel(jar: JarDTO): { name: string; scope: string } {
  if (!jar.perEnvironment) return { name: jar.workspace, scope: t("lib.cookies.jar.shared") };
  if (jar.environmentId === "") {
    return { name: t("lib.cookies.jar.noEnvironmentName", { project: jar.workspace }), scope: t("lib.cookies.jar.noEnvironment") };
  }
  return { name: t("lib.cookies.jar.environmentName", { project: jar.workspace, environment: jar.environment }), scope: t("lib.cookies.jar.environment") };
}

/** Adds a cookie (key "") or replaces the cookie with `key`; resolves to the cookie as stored. */
export function setCookie(key: string, cookie: CookieDTO): CancellablePromise<CookieDTO> {
  return CookiesService.Set({ key, cookie });
}

/** Deletes the cookie with `key`; one already gone is not an error. */
export function deleteCookie(key: string): CancellablePromise<void> {
  return CookiesService.Delete(key);
}

/** Deletes every cookie of a domain group; resolves to how many there were. */
export function deleteCookieDomain(domain: string): CancellablePromise<number> {
  return CookiesService.DeleteDomain(domain);
}

/** Deletes every cookie of the jar sends use now; other environments' jars keep theirs. */
export function clearCookies(): CancellablePromise<void> {
  return CookiesService.Clear();
}

/** Cookies sharing a Domain (host-only and domain cookies alike). */
export interface CookieGroup {
  domain: string;
  cookies: CookieDTO[];
}

/**
 * Groups cookies by domain, domains sorted by their labels from the right
 * (so api.example.com sits next to example.com), cookies in list order.
 */
export function groupCookies(cookies: readonly CookieDTO[]): CookieGroup[] {
  const groups = new Map<string, CookieDTO[]>();
  for (const c of cookies) {
    const list = groups.get(c.domain);
    if (list === undefined) groups.set(c.domain, [c]);
    else list.push(c);
  }
  const reversed = (d: string) => d.split(".").reverse().join(".");
  return [...groups.entries()]
    .map(([domain, list]) => ({ domain, cookies: list }))
    .sort((a, b) => {
      const ra = reversed(a.domain);
      const rb = reversed(b.domain);
      return ra < rb ? -1 : ra > rb ? 1 : 0;
    });
}

/** The cookies whose domain, name or value contains `query` (case-insensitive). */
export function filterCookies(cookies: readonly CookieDTO[], query: string): CookieDTO[] {
  const q = query.trim().toLowerCase();
  if (q === "") return [...cookies];
  return cookies.filter((c) => c.domain.includes(q) || c.name.toLowerCase().includes(q) || c.value.toLowerCase().includes(q));
}

/** A cookie as the edit form holds it: expiry as a datetime-local value. */
export interface CookieDraft {
  name: string;
  value: string;
  domain: string;
  /** Sent to subdomains too (not host-only). */
  includeSubdomains: boolean;
  path: string;
  secure: boolean;
  httpOnly: boolean;
  sameSite: SameSite;
  /** Session cookie: no expiry. */
  session: boolean;
  /** "YYYY-MM-DDTHH:MM" in local time, for <input type="datetime-local">. */
  expires: string;
}

const DAY_MS = 24 * 60 * 60 * 1000;

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/** A Date as a datetime-local value in local time. */
export function toLocalInput(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** A new cookie's draft, for `domain` when given; the expiry field starts a week ahead. */
export function emptyDraft(domain = "", now = new Date()): CookieDraft {
  return {
    name: "",
    value: "",
    domain,
    includeSubdomains: false,
    path: "/",
    secure: false,
    httpOnly: false,
    sameSite: "",
    session: true,
    expires: toLocalInput(new Date(now.getTime() + 7 * DAY_MS)),
  };
}

function toSameSite(s: string): SameSite {
  return (SAME_SITE_VALUES as readonly string[]).includes(s) ? (s as SameSite) : "";
}

/** The draft to edit a stored cookie. */
export function draftFromCookie(c: CookieDTO, now = new Date()): CookieDraft {
  const expires = c.expires === "" ? null : new Date(c.expires);
  return {
    name: c.name,
    value: c.value,
    domain: c.domain,
    includeSubdomains: !c.hostOnly,
    path: c.path,
    secure: c.secure,
    httpOnly: c.httpOnly,
    sameSite: toSameSite(c.sameSite),
    session: expires === null,
    expires: toLocalInput(expires !== null && !Number.isNaN(expires.getTime()) ? expires : new Date(now.getTime() + 7 * DAY_MS)),
  };
}

/** A draft checked by cookieFromDraft: the cookie to save, or what is wrong with it. */
export type DraftResult = { cookie: CookieDTO } | { error: string; field: keyof CookieDraft };

/**
 * The cookie to send to CookiesService.Set, or an error message for the
 * field the form can check itself (the service checks the rest).
 */
export function cookieFromDraft(d: CookieDraft): DraftResult {
  if (d.name.trim() === "" && d.value === "") return { error: t("lib.cookies.draft.name"), field: "name" };
  if (d.domain.trim() === "") return { error: t("lib.cookies.draft.domain"), field: "domain" };
  let expires = "";
  if (!d.session) {
    const date = new Date(d.expires);
    if (d.expires === "" || Number.isNaN(date.getTime())) return { error: t("lib.cookies.draft.expires"), field: "expires" };
    expires = date.toISOString();
  }
  return {
    cookie: {
      key: "",
      name: d.name,
      value: d.value,
      domain: d.domain.trim(),
      hostOnly: !d.includeSubdomains,
      path: d.path.trim(),
      secure: d.secure,
      httpOnly: d.httpOnly,
      sameSite: d.sameSite,
      expires,
      created: "",
      lastAccess: "",
      binary: false,
    },
  };
}

/** How the manager shows an expiry: "Session", or the local date and time. */
export function formatExpires(expires: string): string {
  if (expires === "") return t("lib.cookies.session");
  const date = new Date(expires);
  return Number.isNaN(date.getTime()) ? expires : formatDate(date, { dateStyle: "medium", timeStyle: "short" });
}
