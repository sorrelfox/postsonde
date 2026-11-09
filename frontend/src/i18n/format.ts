// Formats one message: picks the plural form, then fills in {name}
// placeholders (UI-10). Pure: the locale comes in as an argument. See
// messages.ts for the message syntax.
import type { Message, PluralMessage } from "./messages";

/**
 * A placeholder, `{name}` with a name made of letters, digits and "_"
 * (group 1), or a literal `{{` or `}}`, matched first so that `{{name}}`
 * (a template variable) stays text.
 */
export const PLACEHOLDER = /\{\{|\}\}|\{([A-Za-z0-9_]+)\}/g;

/** Whether a message is a plural message (an object) rather than a string. */
export function isPluralMessage(message: Message): message is PluralMessage {
  return typeof message === "object" && message !== null;
}

const pluralRules = new Map<string, Intl.PluralRules>();
const numberFormats = new Map<string, Intl.NumberFormat>();

function rulesFor(locale: string): Intl.PluralRules {
  let rules = pluralRules.get(locale);
  if (rules === undefined) {
    rules = new Intl.PluralRules(locale);
    pluralRules.set(locale, rules);
  }
  return rules;
}

/** A number as a placeholder shows it: the locale's digits and decimal sign, no grouping (ports, years). */
export function plainNumber(locale: string, n: number): string {
  let format = numberFormats.get(locale);
  if (format === undefined) {
    format = new Intl.NumberFormat(locale, { useGrouping: false, maximumFractionDigits: 20 });
    numberFormats.set(locale, format);
  }
  return format.format(n);
}

/** The form of a plural message for `count`: an exact match ("=0"), else its plural category, else `other`. */
export function selectPlural(locale: string, message: PluralMessage, count: number): string {
  const exact = message[`=${count}`];
  if (exact !== undefined) return exact;
  return message[rulesFor(locale).select(count)] ?? message.other;
}

/**
 * Splits a message into text and parameter values, in order. A value is
 * kept as it is (a React node stays a node); a number becomes text. A
 * placeholder without a value is left as written, `{name}`, so a missing
 * parameter shows instead of vanishing.
 */
export function formatParts<V>(
  locale: string,
  message: Message,
  params: Readonly<Record<string, V | string | number>> = {},
): (string | V)[] {
  let text: string;
  if (isPluralMessage(message)) {
    const count = params.count;
    text = selectPlural(locale, message, typeof count === "number" ? count : Number.NaN);
  } else {
    text = message;
  }
  const parts: (string | V)[] = [];
  let last = 0;
  let pending = "";
  for (const match of text.matchAll(PLACEHOLDER)) {
    const index = match.index ?? 0;
    pending += text.slice(last, index);
    last = index + match[0].length;
    const name = match[1];
    if (name === undefined || !Object.prototype.hasOwnProperty.call(params, name)) {
      pending += match[0];
      continue;
    }
    const value = params[name];
    if (typeof value === "string") {
      pending += value;
    } else if (typeof value === "number") {
      pending += plainNumber(locale, value);
    } else {
      if (pending !== "") parts.push(pending);
      pending = "";
      parts.push(value);
    }
  }
  pending += text.slice(last);
  if (pending !== "" || parts.length === 0) parts.push(pending);
  return parts;
}

/** The message as text, with its placeholders filled in. */
export function formatMessage(
  locale: string,
  message: Message,
  params?: Readonly<Record<string, string | number>>,
): string {
  return formatParts<never>(locale, message, params).join("");
}
