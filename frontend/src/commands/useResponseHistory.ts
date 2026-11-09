import { useMemo, useSyncExternalStore } from "react";
import { responseHistories, type ResponseHistory, type ResponseHistoryState } from "./responseHistory";

/**
 * The response history slot of request `requestId` and its state (HTTP-38,
 * HTTP-39): its earlier responses and what the response pane shows. The
 * first subscriber loads the list; the slot stays while the tab is open.
 */
export function useResponseHistory(requestId: string): { slot: ResponseHistory; state: ResponseHistoryState } {
  const slot = useMemo(() => responseHistories().get(requestId), [requestId]);
  const state = useSyncExternalStore(slot.subscribe, slot.getState);
  return { slot, state };
}
