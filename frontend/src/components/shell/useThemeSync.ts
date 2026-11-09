import { useEffect } from "react";
import { useSettings } from "../../stores";
import { applyTheme } from "../../styles/theme";

/**
 * Applies `settings.theme` to <html data-theme> whenever it changes, whoever
 * changed it (the title-bar toggle, `settings/load` from Go, …).
 * Called once, by App, so it also covers the welcome screen.
 */
export function useThemeSync(): void {
  const theme = useSettings((s) => s.theme);
  useEffect(() => {
    applyTheme(theme);
  }, [theme]);
}
