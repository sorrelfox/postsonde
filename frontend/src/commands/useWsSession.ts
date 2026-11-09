import { useMemo, useSyncExternalStore } from "react";
import { wsSessions, type WsSession, type WsSessionState } from "./wsSessions";

/**
 * The WebSocket session of request `requestId` and its state (WS-01). The
 * session outlives the editor: it stays while the request's tab is open.
 */
export function useWsSession(requestId: string): { session: WsSession; state: WsSessionState } {
  const session = useMemo(() => wsSessions().get(requestId), [requestId]);
  const state = useSyncExternalStore(session.subscribe, session.getState);
  return { session, state };
}
