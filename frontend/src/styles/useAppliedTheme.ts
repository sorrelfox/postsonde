import { useSyncExternalStore } from "react";
import { currentTheme, subscribeTheme, type Theme } from "./theme";

/**
 * The theme shown right now, "dark" or "light": the preference resolved
 * against the OS. Re-renders when it changes, including when 'system'
 * follows an OS change.
 */
export function useAppliedTheme(): Theme {
  return useSyncExternalStore(subscribeTheme, currentTheme);
}
