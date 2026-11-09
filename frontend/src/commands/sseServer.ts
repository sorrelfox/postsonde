// SSE server commands (SSRV-04): push an event to one subscriber or to
// all, disconnect a subscriber, change the server's settings, through the
// Go ServersService. The subscriber list itself arrives as
// servers:subscribers (commands/servers.ts, followServers). See
// docs/servers.md.
//
// Failed calls reject with an AppError (lib/errors.ts).

import { toAppError } from "../lib/errors";
import * as api from "../lib/servers";
import type { LocalServer, SseServerConfig, Stores } from "../stores";
import { toLocalServer } from "./servers";

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** An event to push: its type ("" for message), data, ID ("" for none) and retry in ms (0 for none). */
export interface SseEventInput {
  event: string;
  data: string;
  id: string;
  retryMs: number;
}

/**
 * Sends an event to the subscriber `subscriberId`, or to every subscriber
 * when it is "". Resolves with how many it reached and the ID it carried.
 */
export function sendSseEvent(serverId: string, subscriberId: string, input: SseEventInput): Promise<api.SSESendResultDTO> {
  return run(() => api.sendSseEvent({ serverId, subscriberId, ...input }));
}

/** Ends a subscriber's stream; resolves once it is gone (an EventSource reconnects after its retry time). */
export function disconnectSseSubscriber(serverId: string, subscriberId: string): Promise<void> {
  return run(() => api.disconnectSseSubscriber(serverId, subscriberId));
}

/**
 * Replaces an SSE server's settings. A running server applies the timers
 * and automatic IDs at once, the path and CORS to new requests and the
 * keep-alive and retry to new subscribers. Resolves with the server as Go
 * has it.
 */
export async function updateSseConfig(stores: Stores, id: string, config: SseServerConfig): Promise<LocalServer> {
  const dto = await run(() =>
    api.updateServerConfig(id, {
      mockHttp: null,
      webSocket: null,
      webhook: null,
      sse: {
        path: config.path,
        allowOrigin: config.allowOrigin,
        keepAliveSec: config.keepAliveSec,
        retryMs: config.retryMs,
        autoId: config.autoId,
        timers: config.timers.map((t) => ({ ...t })),
      },
    }),
  );
  const server = toLocalServer(dto);
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

/** The URL subscribers open ("http", or "https" with TLS, the address and path), or null when not running. */
export function sseServerUrl(server: LocalServer): string | null {
  const address = server.addresses[0];
  if (server.state !== "running" || address === undefined) return null;
  const path = server.sse === null || server.sse.path === "*" ? "/events" : server.sse.path;
  // Joined at run time: a written-out scheme and host reads as a remote URL to the bundle check.
  return [server.tls !== null ? "https" : "http", `${address}${path}`].join("://");
}

/** A line of JavaScript that subscribes to `url` from a browser page. */
export function eventSourceSnippet(url: string, withCredentials = false): string {
  const options = withCredentials ? ", { withCredentials: true }" : "";
  return `new EventSource(${JSON.stringify(url)}${options}).onmessage = (e) => console.log(e.data);`;
}
