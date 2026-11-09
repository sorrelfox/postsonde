// The OS theme as Go sees it (UI-04), for the 'system' theme preference.
//
// styles/theme.ts follows prefers-color-scheme. That is enough in WKWebView
// (macOS) and WebView2 (Windows), but WebKitGTK (Linux) does not always see
// the desktop's light/dark preference (the freedesktop portal's
// color-scheme, which GNOME sets). Go watches the OS through Wails and sends
// system:themeChanged (internal/app/theme.go); this feeds it to the theme.
// See docs/frontend.md, "Themes".
import { Events, System } from "@wailsio/runtime";
import { setSystemDark } from "../styles/theme";

export const THEME_CHANGED_EVENT = "system:themeChanged";

/**
 * Follows OS theme changes reported by Go. On Linux it also asks Go for the
 * OS theme once, and takes it when it is dark: the portal cannot tell "light"
 * from "no preference", so a light answer is left to prefers-color-scheme.
 * main.tsx calls it once. Returns the unsubscribe function.
 */
export function followOSTheme(): () => void {
  const stop = Events.On(THEME_CHANGED_EVENT, (ev) => setSystemDark(ev.data.dark));
  if (System.IsLinux()) {
    System.IsDarkMode().then(
      (dark) => {
        if (dark) setSystemDark(true);
      },
      () => {
        // No answer: prefers-color-scheme stays in charge.
      },
    );
  }
  return stop;
}
