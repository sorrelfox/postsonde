import { useEffect } from "react";
import { followAutoSave } from "../../commands/autosave";
import { useStores } from "../../stores";

/**
 * Applies Go's auto-save events to the stores and flushes pending edits
 * when the page is hidden. Called once, by App, so saves of a project
 * that was just closed still report.
 */
export function useAutoSaveSync(): void {
  const stores = useStores();
  useEffect(() => followAutoSave(stores), [stores]);
}
