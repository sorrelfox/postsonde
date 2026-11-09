import { useMemo, useSyncExternalStore } from "react";
import { tcpSessions, type TcpSession, type TcpSessionState } from "./tcpSessions";

/**
 * The TCP session of request `requestId` and its state (TCP-01). The session
 * outlives the editor: it stays while the request's tab is open.
 */
export function useTcpSession(requestId: string): { session: TcpSession; state: TcpSessionState } {
  const session = useMemo(() => tcpSessions().get(requestId), [requestId]);
  const state = useSyncExternalStore(session.subscribe, session.getState);
  return { session, state };
}
