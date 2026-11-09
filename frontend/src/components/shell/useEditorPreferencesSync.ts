import { useLayoutEffect } from "react";
import { editorSettingsOf, shallowEqual, useSettings } from "../../stores";
import { setEditorPreferences } from "../editor/preferences";

/**
 * Hands the editor settings (UI-08) to every CodeMirror editor whenever
 * they change, whoever changed them (the settings screen, a hand edit of
 * the settings file). Called once, by App. A layout effect, so editors are
 * restyled in the same frame.
 */
export function useEditorPreferencesSync(): void {
  const prefs = useSettings(editorSettingsOf, shallowEqual);
  useLayoutEffect(() => {
    setEditorPreferences(prefs);
  }, [prefs]);
}
