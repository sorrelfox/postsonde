import { useEffect } from "react";
import { followTabs } from "../../commands/tabs";
import { useStores } from "../../stores";

/**
 * Restores the open project's tabs once its tree has loaded, closes tabs
 * whose item is gone when the tree reloads, and remembers the open tabs
 * per workspace. Called once, by the shell of the open project.
 */
export function useTabsSync(): void {
  const stores = useStores();
  useEffect(() => followTabs(stores), [stores]);
}
