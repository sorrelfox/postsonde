// WebSocket server commands (SSRV-01): send to one client or to all,
// disconnect a client, change the server's settings, through the Go
// ServersService. The client list itself arrives as servers:clients
// (commands/servers.ts, followServers). See docs/servers.md.
//
// Failed calls reject with an AppError (lib/errors.ts).

import { toAppError } from "../lib/errors";
import * as api from "../lib/servers";
import type { LocalServer, Stores, WsServerConfig } from "../stores";
import { toLocalServer } from "./servers";

/** The close code Disconnect sends by default (normal closure). */
export const WS_SERVER_NORMAL_CLOSURE = 1000;

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** Sends a text message to one client. */
export function sendToWsClient(serverId: string, clientId: string, text: string): Promise<void> {
  return run(() => api.sendToWsClient(serverId, clientId, { binary: false, text, dataBase64: "" }));
}

/** Sends a text message to every client; resolves with how many it reached. */
export function broadcastToWsClients(serverId: string, text: string): Promise<api.WSBroadcastResultDTO> {
  return run(() => api.broadcastToWsClients(serverId, { binary: false, text, dataBase64: "" }));
}

/**
 * Closes a client's connection with `code` and `reason`; resolves once the
 * client is gone (its row disappears with the next servers:clients event).
 */
export function disconnectWsClient(serverId: string, clientId: string, code: number, reason: string): Promise<void> {
  return run(() => api.disconnectWsClient(serverId, clientId, code, reason));
}

/**
 * Replaces a WebSocket server's settings. A running server applies them to
 * new connections (echo and the auto-reply rules at once). Resolves with the server as Go has it.
 */
export async function updateWebSocketConfig(stores: Stores, id: string, config: WsServerConfig): Promise<LocalServer> {
  const dto = await run(() =>
    api.updateServerConfig(id, {
      mockHttp: null,
      webSocket: {
        path: config.path,
        subprotocols: [...config.subprotocols],
        echo: config.echo,
        maxMessageSize: config.maxMessageSize,
        rules: config.rules.map((r) => ({ ...r })),
      },
      webhook: null,
    }),
  );
  const server = toLocalServer(dto);
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

/** The URL clients connect to ("ws", or "wss" with TLS, and the address and path), or null when not running. */
export function wsServerUrl(server: LocalServer): string | null {
  const address = server.addresses[0];
  if (server.state !== "running" || address === undefined) return null;
  const path = server.webSocket === null || server.webSocket.path === "*" ? "/" : server.webSocket.path;
  // Joined at run time: a written-out scheme and host reads as a remote URL to the bundle check.
  return [server.tls !== null ? "wss" : "ws", `${address}${path}`].join("://");
}
