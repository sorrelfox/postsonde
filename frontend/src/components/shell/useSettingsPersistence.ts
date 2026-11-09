import { useEffect, useLayoutEffect } from "react";
import { flushSettings, followSettingsChanges, loadSettings, trackSettings } from "../../commands/settings";
import { useStores } from "../../stores";

/**
 * Loads the saved UI settings once at startup, saves theme and sidebar
 * changes, applies hand edits of the settings file as they arrive, and
 * writes queued changes when the window is hidden or closed. Called once, by
 * App. The backend is chosen in commands/settings.ts.
 */
export function useSettingsPersistence(): void {
  const stores = useStores();

  // A layout effect, so a saved split is applied before the first paint.
  useLayoutEffect(() => {
    void loadSettings(stores);
  }, [stores]);

  useEffect(() => {
    const stopTracking = trackSettings(stores);
    const stopFollowing = followSettingsChanges(stores);
    return () => {
      stopTracking();
      stopFollowing();
    };
  }, [stores]);

  useEffect(() => {
    const flush = () => void flushSettings();
    const onVisibility = () => {
      if (document.visibilityState === "hidden") flush();
    };
    window.addEventListener("pagehide", flush);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.removeEventListener("pagehide", flush);
      document.removeEventListener("visibilitychange", onVisibility);
      flush();
    };
  }, []);
}
