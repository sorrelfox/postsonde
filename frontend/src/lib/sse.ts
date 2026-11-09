// Calls into the Go SSEService (internal/app/services/sse): connecting to
// an sse request's event stream (SSE-01), which runs as an sse operation
// whose items are the stream's status, events, comments, retry fields and
// end (SSE-02). Stopping is OpsService.Cancel. See docs/sse.md.
import {
  SSEService,
  type HeaderDTO,
  type StreamItemDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/sse";
import type { CancellablePromise } from "./calls";
import type { StartedDTO } from "./operations";

export type { HeaderDTO, StreamItemDTO };

/** The domain of stream operations: their events are `sse:op:data|progress|done`. */
export const SSE_DOMAIN = "sse";

/** Error code of a response that is not 2xx (the body is in a notice item). */
export const SSE_BAD_STATUS = "sse.bad_status";

/**
 * Connects to sse request `requestId`. Resolves with the operation ID at
 * once; the stream arrives as `sse:op:data` items. With `allowUnresolved`
 * undefined {{variables}} are sent as written (ENV-11). A `lastEventId`
 * other than "" resumes a stream: the first connection sends it as
 * Last-Event-ID (SSE-03).
 */
export function connectSse(requestId: string, allowUnresolved = false, lastEventId = ""): CancellablePromise<StartedDTO> {
  return SSEService.Connect({ requestId, allowUnresolved, lastEventId });
}
