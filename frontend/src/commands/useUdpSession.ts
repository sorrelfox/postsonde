import { useMemo, useSyncExternalStore } from "react";
import { udpSessions, type UdpSession, type UdpSessionState } from "./udpSessions";

/**
 * The UDP session of request `requestId` and its state (UDP-01). The
 * session outlives the editor: it stays while the request's tab is open.
 */
export function useUdpSession(requestId: string): { session: UdpSession; state: UdpSessionState } {
  const session = useMemo(() => udpSessions().get(requestId), [requestId]);
  const state = useSyncExternalStore(session.subscribe, session.getState);
  return { session, state };
}
