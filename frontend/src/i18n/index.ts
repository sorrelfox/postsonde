// Localization (UI-10): message catalogues with typed keys, t(),
// plurals and locale-aware formatting. Two entry points: "i18n" (this
// file, no React: for lib/, commands/, stores/) and "i18n/react" (useT,
// useLocale, Trans: for components). See docs/frontend.md#localization.
export type { Catalogue, MessageKey, Namespace, Translation } from "./catalog";
export type { Message, Messages, PluralMessage } from "./messages";
export {
  currentLocale,
  DEFAULT_LOCALE,
  errorText,
  findLocale,
  formatDate,
  formatNumber,
  formatRelativeTime,
  intlLocale,
  isMessageKey,
  LOCALE_CACHE_KEY,
  LOCALE_SYSTEM,
  LOCALES,
  PSEUDO_LOCALE,
  registerLocale,
  resolveLocale,
  setLocale,
  storedLocalePreference,
  storeLocalePreference,
  subscribeLocale,
  t,
  tParts,
  type LocaleInfo,
} from "./locale";
