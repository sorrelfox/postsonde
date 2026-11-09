// The network activity indicator and connection log (PRV-06): follows Go's
// connection log into the ConnectionFeed the status bar indicator and the
// console's Connections tab read. See docs/frontend.md (Network activity).
import { useEffect, useSyncExternalStore } from "react";
import { formatError } from "../lib/errors";
import { clearConnections, ConnectionFeed, listConnections, onNetworkActivity, type ConnectionSnapshot } from "../lib/network";

/** The app's connection feed. Tests may use their own with followNetwork. */
export const networkFeed = new ConnectionFeed();

let followers = 0;
let stopFollowing: (() => void) | null = null;

/**
 * Keeps `feed` up to date with Go: subscribes to the activity event first,
 * then loads what Go kept, so nothing in between is lost (the feed keeps the
 * newer view of each connection). Returns the function that stops following.
 */
export function followNetwork(feed: ConnectionFeed): () => void {
  let stopped = false;
  const off = onNetworkActivity((ev) => feed.apply(ev.conns ?? []));
  const call = listConnections();
  call.then(
    (list) => {
      if (!stopped) feed.load(list);
    },
    (err: unknown) => {
      if (!stopped) console.warn(`network: ${formatError(err)}`);
    },
  );
  return () => {
    stopped = true;
    off();
    call.cancel?.();
  };
}

/** Follows the app's connection feed while any component using it is mounted, and returns its snapshot. */
export function useNetworkFeed(feed: ConnectionFeed = networkFeed): ConnectionSnapshot {
  useEffect(() => {
    if (feed !== networkFeed) return followNetwork(feed);
    followers++;
    if (followers === 1) stopFollowing = followNetwork(feed);
    return () => {
      followers--;
      if (followers === 0) {
        stopFollowing?.();
        stopFollowing = null;
      }
    };
  }, [feed]);
  return useSyncExternalStore(feed.subscribe, feed.getSnapshot);
}

/** Clears the connection log: ended connections go from the feed at once, then from Go. */
export async function clearConnectionLog(feed: ConnectionFeed = networkFeed): Promise<void> {
  feed.clearEnded();
  await clearConnections();
}
