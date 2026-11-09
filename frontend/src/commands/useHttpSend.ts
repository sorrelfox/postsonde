import { useMemo, useSyncExternalStore } from "react";
import { httpSends, type HttpSend, type HttpSendState } from "./httpSends";

/**
 * The send slot of HTTP request `requestId` and its state (HTTP-20). The
 * slot outlives the editor and the response panel: it stays while the
 * request's tab is open.
 */
export function useHttpSend(requestId: string): { send: HttpSend; state: HttpSendState } {
  const send = useMemo(() => httpSends().get(requestId), [requestId]);
  const state = useSyncExternalStore(send.subscribe, send.getState);
  return { send, state };
}
