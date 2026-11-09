// React side of the localization (UI-10): useT() gives a component t()
// and re-renders it when the language changes. No provider is needed: the
// language lives in locale.ts, which useSyncExternalStore follows. See
// docs/frontend.md#localization.
import { createElement, Fragment, useSyncExternalStore, type ReactNode } from "react";
import type { MessageKey, TPartsArgs } from "./catalog";
import { currentLocale, subscribeLocale, t, tParts } from "./locale";

/** The type of t(). */
export type Translate = typeof t;

// One function per language, so that a t from useT() changes identity when
// the language does: memoised values and callbacks that list it as a
// dependency are made again.
const translators = new Map<string, Translate>();

function translatorFor(locale: string): Translate {
  let fn = translators.get(locale);
  if (fn === undefined) {
    const call = t as unknown as (key: MessageKey, params?: unknown) => string;
    fn = ((key: MessageKey, params?: unknown) => call(key, params)) as unknown as Translate;
    translators.set(locale, fn);
  }
  return fn;
}

/** The tag of the language in use; the component re-renders when it changes. */
export function useLocale(): string {
  return useSyncExternalStore(subscribeLocale, currentLocale, currentLocale);
}

/**
 * t() for a component: `const t = useT();` at the top, then `t("shell.x")`
 * in render. The component re-renders in the new language when it changes.
 */
export function useT(): Translate {
  return translatorFor(useLocale());
}

export type TransProps<K extends MessageKey> = {
  /** The message key. */
  readonly k: K;
} & (TPartsArgs<K, ReactNode> extends [params?: undefined]
  ? { readonly values?: undefined }
  : { readonly values: TPartsArgs<K, ReactNode>[0] });

/**
 * A message with React nodes in its placeholders:
 * `<Trans k="settings.file.hint" values={{ file: <code>settings.json</code> }} />`.
 */
export function Trans<K extends MessageKey>({ k, values }: TransProps<K>): ReactNode {
  useLocale();
  const parts = (tParts as (key: MessageKey, params?: unknown) => (string | ReactNode)[])(k, values);
  return createElement(Fragment, null, ...parts.map((part, i) => createElement(Fragment, { key: i }, part)));
}
