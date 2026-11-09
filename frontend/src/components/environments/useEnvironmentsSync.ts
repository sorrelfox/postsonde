import { useEffect } from "react";
import { followEnvironmentsWorkspace } from "../../commands/environments";
import { useStores } from "../../stores";

/**
 * Loads the open project's environments and reloads them whenever another
 * project is opened. Called once, by the shell of the open project; clears
 * the environments when it unmounts (the project was closed).
 */
export function useEnvironmentsSync(): void {
  const stores = useStores();
  useEffect(() => {
    const stop = followEnvironmentsWorkspace(stores);
    return () => {
      stop();
      stores.environments.dispatch({ type: "environments/clear" });
    };
  }, [stores]);
}
