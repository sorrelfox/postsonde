// The UI language in use and t() (UI-10). Framework-free: commands, lib
// and stores call t() directly; components use useT() (react.ts), which
// also re-renders them when the language changes. See
// docs/frontend.md#localization.
import { en, type Catalogue, type ErrorMessages, type MessageKey, type TArgs, type TPartsArgs, type Translation } from "./catalog";
import englishErrors from "./en/errors";
import { formatMessage, formatParts } from "./format";
import type { Message } from "./messages";
import { PSEUDO_LOCALE, pseudoCatalogue } from "./pseudo";

/** The locale setting that follows the OS language (ui.locale in settings.json). */
export const LOCALE_SYSTEM = "system";

/** The language every message exists in, and the fallback for everything else. */
export const DEFAULT_LOCALE = "en";

export { PSEUDO_LOCALE };

export interface LocaleInfo {
  /** BCP 47 tag, as ui.locale holds it. */
  readonly tag: string;
  /** The language's name in itself ("English", "Deutsch"), as the settings screen lists it. */
  readonly name: string;
  /** The locale Intl formats numbers, dates and plurals with. */
  readonly intl: string;
  /** The pseudo-locale: listed only in dev builds, never picked for "system". */
  readonly pseudo?: boolean;
  /** Its messages; a message it lacks falls back to English. */
  readonly messages: () => Translation;
  /** Its messages for Go error codes (en/errors.ts). */
  readonly errors?: () => ErrorMessages;
}

let pseudoMessages: Catalogue | null = null;

const locales: LocaleInfo[] = [
  { tag: DEFAULT_LOCALE, name: "English", intl: "en", messages: () => en, errors: () => englishErrors },
  {
    tag: PSEUDO_LOCALE,
    name: "Pseudo-locale (en-XA)",
    intl: "en",
    pseudo: true,
    messages: () => (pseudoMessages ??= pseudoCatalogue(en)),
  },
];

/** The locales the app has, English first. A new locale is one entry above and its message modules. */
export const LOCALES: readonly LocaleInfo[] = locales;

/**
 * Adds a locale while the app runs (tests; a translation loaded later).
 * Returns the function that removes it again.
 */
export function registerLocale(info: LocaleInfo): () => void {
  locales.push(info);
  return () => {
    const i = locales.indexOf(info);
    if (i >= 0) locales.splice(i, 1);
  };
}

/** The locale of a tag, matched without regard to case; undefined when the app does not have it. */
export function findLocale(tag: string): LocaleInfo | undefined {
  const lower = tag.toLowerCase();
  return LOCALES.find((l) => l.tag.toLowerCase() === lower);
}

/**
 * The locale to use for a preference: "system" takes the first of the OS
 * languages (the web view's navigator.languages) the app has, by exact tag
 * or by language ("en-GB" → "en"), never the pseudo-locale; a tag the same
 * way. Anything else is English.
 */
export function resolveLocale(preference: string, systemLanguages: readonly string[] = osLanguages()): string {
  const candidates = preference === LOCALE_SYSTEM ? systemLanguages : [preference];
  for (const tag of candidates) {
    const exact = findLocale(tag);
    if (exact !== undefined && (preference !== LOCALE_SYSTEM || !exact.pseudo)) return exact.tag;
    const language = tag.split("-")[0].toLowerCase();
    const byLanguage = LOCALES.find((l) => !l.pseudo && l.tag.toLowerCase() === language);
    if (byLanguage !== undefined) return byLanguage.tag;
  }
  return DEFAULT_LOCALE;
}

function osLanguages(): readonly string[] {
  if (typeof navigator === "undefined") return [];
  return navigator.languages?.length ? navigator.languages : navigator.language ? [navigator.language] : [];
}

interface Active {
  readonly info: LocaleInfo;
  readonly messages: Translation;
  readonly errors: ErrorMessages;
}

const english = LOCALES[0];
let active: Active = { info: english, messages: en, errors: englishErrors };
const listeners = new Set<() => void>();

/**
 * Switches the UI language: `preference` is ui.locale ("system" or a tag).
 * Sets <html lang> and notifies subscribers (useT re-renders every
 * component that shows text). Returns the tag in use.
 */
export function setLocale(preference: string, systemLanguages?: readonly string[]): string {
  const tag = resolveLocale(preference, systemLanguages);
  if (tag !== active.info.tag) {
    const info = findLocale(tag) ?? english;
    active = { info, messages: info.messages(), errors: info.errors?.() ?? {} };
    for (const listener of [...listeners]) listener();
  }
  if (typeof document !== "undefined") document.documentElement.lang = active.info.tag;
  return active.info.tag;
}

/**
 * localStorage key of the last locale preference, so the first frames
 * show the right language before the settings file has loaded (like the
 * theme's postsonde.theme). The settings file has the final say.
 */
export const LOCALE_CACHE_KEY = "postsonde.locale";

/** The cached locale preference; "system" when there is none. */
export function storedLocalePreference(): string {
  try {
    return localStorage.getItem(LOCALE_CACHE_KEY) || LOCALE_SYSTEM;
  } catch {
    return LOCALE_SYSTEM;
  }
}

/** Caches the locale preference for the next start. */
export function storeLocalePreference(preference: string): void {
  try {
    if (preference === LOCALE_SYSTEM) localStorage.removeItem(LOCALE_CACHE_KEY);
    else localStorage.setItem(LOCALE_CACHE_KEY, preference);
  } catch {
    // Storage unavailable: the next start shows the OS language until the settings load.
  }
}

/** The tag of the language in use ("en", "en-XA"). */
export function currentLocale(): string {
  return active.info.tag;
}

/** The locale Intl uses for the language in use. */
export function intlLocale(): string {
  return active.info.intl;
}

/** Calls `listener` whenever the language changes. Returns the unsubscribe function. */
export function subscribeLocale(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

const warned = new Set<string>();

function lookup(key: string): Message {
  const dot = key.indexOf(".");
  const namespace = key.slice(0, dot);
  const id = key.slice(dot + 1);
  const translated = (active.messages as Record<string, Record<string, Message> | undefined>)[namespace]?.[id];
  if (translated !== undefined) return translated;
  const english = (en as Record<string, Record<string, Message> | undefined>)[namespace]?.[id];
  if (english !== undefined) return english;
  if (!warned.has(key)) {
    warned.add(key);
    console.warn(`i18n: no message "${key}"`);
  }
  return key;
}

/**
 * The text of a message in the language in use, its placeholders filled
 * in: t("shell.statusBar.ready"), t("shell.theme.label", { name }).
 * TypeScript checks that the key exists and that every placeholder gets a
 * value (and `count` for a plural message).
 */
export function t<K extends MessageKey>(key: K, ...args: TArgs<K>): string {
  return formatMessage(active.info.intl, lookup(key), args[0] as Readonly<Record<string, string | number>> | undefined);
}

/**
 * Like t(), but a parameter may be any value, a React node for example,
 * and comes back as it is between the text parts: for a message with a
 * link or <code> in the middle. <Trans> (react.ts) renders it.
 */
export function tParts<K extends MessageKey, V>(key: K, ...args: TPartsArgs<K, V>): (string | V)[] {
  return formatParts<V>(active.info.intl, lookup(key), args[0] as Readonly<Record<string, V | string | number>> | undefined);
}

/** Whether a string is a message key (for props that take a key or text). */
export function isMessageKey(key: string): key is MessageKey {
  const dot = key.indexOf(".");
  if (dot < 1) return false;
  const ns = (en as Record<string, Record<string, Message> | undefined>)[key.slice(0, dot)];
  return ns !== undefined && Object.prototype.hasOwnProperty.call(ns, key.slice(dot + 1));
}

/**
 * The message of a Go error in the language in use, by its code, with
 * its details as placeholders; `fallback` (Go's English text) when the
 * locale has none. `part` "hint" looks up "<code>.hint".
 */
export function errorText(code: string, part: "message" | "hint", fallback: string, details: Readonly<Record<string, string>> = {}): string {
  if (code === "") return fallback;
  const message = active.errors[part === "hint" ? `${code}.hint` : code];
  return message === undefined ? fallback : formatMessage(active.info.intl, message, details);
}

// Formatting bound to the language in use. Formatters are cached per
// locale and options.

const formatters = new Map<string, unknown>();

function cached<F>(kind: string, options: object | undefined, make: (locale: string) => F): F {
  const locale = active.info.intl;
  const key = `${kind}|${locale}|${options === undefined ? "" : JSON.stringify(options)}`;
  let f = formatters.get(key) as F | undefined;
  if (f === undefined) {
    f = make(locale);
    formatters.set(key, f);
  }
  return f;
}

/** A number for people: grouped digits and the locale's decimal sign (Intl.NumberFormat). */
export function formatNumber(n: number | bigint, options?: Intl.NumberFormatOptions): string {
  return cached("number", options, (l) => new Intl.NumberFormat(l, options)).format(n);
}

/** A date and time in the locale's style (Intl.DateTimeFormat); `options` pick the parts. */
export function formatDate(date: Date | number, options?: Intl.DateTimeFormatOptions): string {
  return cached("date", options, (l) => new Intl.DateTimeFormat(l, options)).format(date);
}

/** A time relative to now, "3 minutes ago", "in 2 days" (Intl.RelativeTimeFormat). */
export function formatRelativeTime(
  value: number,
  unit: Intl.RelativeTimeFormatUnit,
  options: Intl.RelativeTimeFormatOptions = { numeric: "auto" },
): string {
  return cached("relative", options, (l) => new Intl.RelativeTimeFormat(l, options)).format(value, unit);
}
