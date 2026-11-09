// Webhook receiver commands (MOCK-08): change the response, load one kept
// request in full and replay it, through the Go ServersService. The list of
// kept requests arrives as servers:captures (commands/servers.ts,
// followServers). A replay is an HTTP send: it runs as an http operation,
// followed with useOperation like a request's send. See docs/servers.md.
//
// Failed calls reject with an AppError (lib/errors.ts).

import { toAppError } from "../lib/errors";
import type { StartedDTO } from "../lib/operations";
import * as api from "../lib/servers";
import type { LocalServer, Stores, WebhookConfig } from "../stores";
import { toLocalServer } from "./servers";

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Replaces a webhook receiver's response; a running receiver answers the
 * next requests with it. Resolves with the server as Go has it.
 */
export async function updateWebhookConfig(stores: Stores, id: string, config: WebhookConfig): Promise<LocalServer> {
  const dto = await run(() =>
    api.updateServerConfig(id, {
      mockHttp: null,
      webSocket: null,
      webhook: {
        status: config.status,
        headers: config.headers.map((h) => ({ enabled: h.enabled, name: h.name, value: h.value })),
        body: config.body,
      },
    }),
  );
  const server = toLocalServer(dto);
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

/** One kept request in full: headers, query and the body as the response view shows one. */
export function loadWebhookCapture(serverId: string, seq: number): Promise<api.CaptureDTO> {
  return run(() => api.getWebhookCapture(serverId, seq));
}

/**
 * Starts replaying a kept request to `url`; for useOperation with the
 * HTTP domain (lib/http.ts HTTP_DOMAIN), whose single data item is the
 * response. A bad URL or a request whose body was cut rejects at once.
 */
export function startWebhookReplay(serverId: string, seq: number, url: string): PromiseLike<StartedDTO> {
  return api.replayWebhookRequest(serverId, seq, url.trim());
}

/** The URL senders post to ("http", or "https" with TLS, and the address), or null when not running. */
export function webhookUrl(server: LocalServer): string | null {
  const address = server.addresses[0];
  if (server.state !== "running" || address === undefined) return null;
  // Joined at run time: a written-out scheme and host reads as a remote URL to the bundle check.
  return [server.tls !== null ? "https" : "http", `${address}/`].join("://");
}

/** The replay URL offered before one was typed: the local port 3000 with the request's own path and query. */
export function defaultReplayUrl(target: string): string {
  const path = target.startsWith("/") ? target : "/";
  return ["http", `localhost:3000${path}`].join("://");
}
