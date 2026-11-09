import { useEffect } from "react";
import { followServers, syncProjectServers } from "../../commands/servers";
import { useStores, type CollectionState } from "../../stores";

/** How long the tree must be still before the saved servers are synced, in ms. */
const SYNC_DELAY_MS = 100;

/** What decides whether the saved servers must be synced: the project, a reload, folders added or deleted. */
function syncKey(s: CollectionState): string | null {
  if (s.status !== "ready" || s.workspaceId === null) return null;
  let folders = 0;
  for (const id in s.nodes) if (s.nodes[id]?.kind === "folder") folders++;
  return `${s.workspaceId}|${folders}`;
}

/**
 * Follows the local servers and their traffic (commands/followServers) and
 * closes the server view when another tab is activated. Called once, by the
 * shell of the open project. Servers keep running when the project closes;
 * reopening loads them and their recent traffic again.
 *
 * It also brings the servers in line with the saved server configs of the
 * open project (SRV-04, syncProjectServers) whenever its tree is loaded,
 * reloaded, or gets or loses folders. Syncing never starts a server.
 */
export function useServersSync(): void {
  const stores = useStores();
  useEffect(() => followServers(stores), [stores]);

  useEffect(() => {
    let key: string | null = null;
    let rootIds = stores.collection.getState().rootIds;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const check = () => {
      const state = stores.collection.getState();
      const next = syncKey(state);
      // A reload replaces the root list, so a hand-edited file is read again.
      if (next === key && state.rootIds === rootIds) return;
      key = next;
      rootIds = state.rootIds;
      if (next === null) return;
      if (timer !== undefined) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = undefined;
        void syncProjectServers(stores);
      }, SYNC_DELAY_MS);
    };
    check();
    const off = stores.collection.subscribe(check);
    return () => {
      off();
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [stores]);

  useEffect(() => {
    let activeId = stores.tabs.getState().activeId;
    return stores.tabs.subscribe(() => {
      const next = stores.tabs.getState().activeId;
      if (next === activeId) return;
      activeId = next;
      if (next !== null) stores.servers.dispatch({ type: "servers/select", id: null });
    });
  }, [stores]);
}
