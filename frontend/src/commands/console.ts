// The in-app console (DBG-06): follows the app log from Go into the
// ConsoleFeed the console panel and its status bar button read. See
// docs/frontend.md (Console).
import { useEffect, useSyncExternalStore } from "react";
import { clearConsole, ConsoleFeed, listConsole, onConsoleEntries, type ConsoleSnapshot } from "../lib/console";
import { formatError } from "../lib/errors";

/** The app's console feed. Tests may use their own with followConsole. */
export const consoleFeed = new ConsoleFeed();

let followers = 0;
let stopFollowing: (() => void) | null = null;

/**
 * Keeps `feed` up to date with Go: subscribes to the entries event first,
 * then loads what Go kept, so nothing logged in between is lost (the feed
 * drops duplicates by seq). Returns the function that stops following.
 */
export function followConsole(feed: ConsoleFeed): () => void {
  let stopped = false;
  const off = onConsoleEntries((ev) => feed.add(ev.entries ?? []));
  const call = listConsole();
  call.then(
    (list) => {
      if (!stopped) feed.load(list);
    },
    (err: unknown) => {
      if (!stopped) console.warn(`console: ${formatError(err)}`);
    },
  );
  return () => {
    stopped = true;
    off();
    call.cancel?.();
  };
}

/** Follows the app's console feed while any component using it is mounted, and returns its snapshot. */
export function useConsoleFeed(feed: ConsoleFeed = consoleFeed): ConsoleSnapshot {
  useEffect(() => {
    if (feed !== consoleFeed) return followConsole(feed);
    followers++;
    if (followers === 1) stopFollowing = followConsole(feed);
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

/** Clears the console: the feed at once, then what Go keeps. */
export async function clearConsoleLog(feed: ConsoleFeed = consoleFeed): Promise<void> {
  feed.clear();
  await clearConsole();
}
