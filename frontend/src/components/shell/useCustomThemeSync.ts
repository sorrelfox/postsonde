import { useEffect, useSyncExternalStore } from "react";
import { getThemesState, reloadThemes, subscribeThemes, syncCustomTheme } from "../../commands/themes";
import { useSettings } from "../../stores";

/**
 * Applies the custom theme of `settings.customTheme` (UI-05) whenever it or
 * the loaded theme files change, and loads the theme files once at start.
 * Called once, by App, next to useThemeSync. See docs/themes.md.
 */
export function useCustomThemeSync(): void {
  const id = useSettings((s) => s.customTheme);
  const themes = useSyncExternalStore(subscribeThemes, getThemesState);

  useEffect(() => {
    if (getThemesState().status === "idle") void reloadThemes();
  }, []);

  useEffect(() => {
    syncCustomTheme(id, themes);
  }, [id, themes]);
}
