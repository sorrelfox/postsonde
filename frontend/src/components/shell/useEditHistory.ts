import { useEffect } from "react";
import { followEditHistory } from "../../commands/undoRedo";
import { useStores } from "../../stores";

/**
 * Runs undo and redo in request editors and drops the edit history of
 * closed tabs (NAV-07), while the project shell is mounted.
 */
export function useEditHistory(): void {
  const stores = useStores();
  useEffect(() => followEditHistory(stores), [stores]);
}
