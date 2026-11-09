import { useMemo, useSyncExternalStore } from "react";
import { sioSessions, type SioSession, type SioSessionState } from "./sioSessions";

/**
 * The Socket.IO session of request `requestId` and its state (SIO-01). The
 * session outlives the editor: it stays while the request's tab is open.
 */
export function useSioSession(requestId: string): { session: SioSession; state: SioSessionState } {
  const session = useMemo(() => sioSessions().get(requestId), [requestId]);
  const state = useSyncExternalStore(session.subscribe, session.getState);
  return { session, state };
}
