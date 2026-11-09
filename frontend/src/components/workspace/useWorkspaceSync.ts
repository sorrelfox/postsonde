import { useEffect, useRef } from "react";
import { followWindow } from "../../commands/windows";
import { followWorkspaceChanges, loadWorkspace } from "../../commands/workspace";
import type { AppError } from "../../lib/errors";
import { useStores } from "../../stores";

/**
 * Loads the open workspace and the recent list once, and follows
 * workspace:changed events from Go. Then learns this window's ID and opens
 * the project the window was opened for, or is asked to open later
 * (APP-06, commands/windows.ts); one that does not open goes to
 * `onOpenFailed`. Called once, by WorkspaceGate.
 */
export function useWorkspaceSync(onOpenFailed: (err: AppError) => void = () => {}): void {
  const stores = useStores();
  const report = useRef(onOpenFailed);
  report.current = onOpenFailed;
  useEffect(() => {
    // Subscribe first, so a change that happens while loading is not missed.
    const stop = followWorkspaceChanges(stores);
    let stopped = false;
    let stopWindow = () => {};
    void loadWorkspace(stores).then(() => {
      if (!stopped) stopWindow = followWindow(stores, (err) => report.current(err));
    });
    return () => {
      stopped = true;
      stop();
      stopWindow();
    };
  }, [stores]);
}
