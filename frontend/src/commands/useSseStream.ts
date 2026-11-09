import { useMemo, useSyncExternalStore } from "react";
import { sseStreams, type SseStream, type SseStreamState } from "./sseStreams";

/**
 * The stream slot of sse request `requestId` and its state (SSE-01). The
 * slot outlives the editor: it stays while the request's tab is open.
 */
export function useSseStream(requestId: string): { stream: SseStream; state: SseStreamState } {
  const stream = useMemo(() => sseStreams().get(requestId), [requestId]);
  const state = useSyncExternalStore(stream.subscribe, stream.getState);
  return { stream, state };
}
