import { useEffect } from "react";
import { LOCALE_SYSTEM, setLocale, storeLocalePreference } from "../../i18n";
import { useSettings } from "../../stores";

/**
 * Applies `settings.locale` (UI-10) whenever it changes, whoever changed it
 * (the settings screen, a hand edit of the file), and caches it for the
 * next start. With "system" it also follows the OS language while the app
 * runs (the web view's languagechange event). Called once, by App.
 */
export function useLocaleSync(): void {
  const preference = useSettings((s) => s.locale);
  useEffect(() => {
    setLocale(preference);
    storeLocalePreference(preference);
    if (preference !== LOCALE_SYSTEM) return;
    const follow = () => setLocale(LOCALE_SYSTEM);
    window.addEventListener("languagechange", follow);
    return () => window.removeEventListener("languagechange", follow);
  }, [preference]);
}
