import { useEffect } from "react";
import { followCollection } from "../../commands/collection";
import { followExternalChanges } from "../../commands/externalChanges";
import { useStores } from "../../stores";

/**
 * Loads the open project's request tree, reloads it when another project is
 * opened or Go reports a change (collection:changed, and
 * collection:external-change for files changed outside PostSonde, with
 * the drafts it concerns: commands/externalChanges.ts), and remembers the
 * expanded folders per workspace. Called once, by the shell of the open
 * project; clears the tree when it unmounts (the project was closed).
 */
export function useCollectionSync(): void {
  const stores = useStores();
  useEffect(() => {
    const stop = followCollection(stores);
    // Files changed outside PostSonde (STO-05).
    const stopExternal = followExternalChanges(stores);
    return () => {
      stopExternal();
      stop();
      stores.collection.dispatch({ type: "collection/clear" });
    };
  }, [stores]);
}
