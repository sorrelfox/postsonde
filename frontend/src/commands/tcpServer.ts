// TCP server commands (SSRV-05): send text or hex bytes to a connection
// and close a connection, through the Go ServersService. The connection
// list itself arrives as servers:connections (commands/servers.ts,
// followServers). See docs/servers.md.
//
// Failed calls reject with an AppError (lib/errors.ts).

import { toAppError } from "../lib/errors";
import * as api from "../lib/servers";
import type { TcpLineEnding, TcpSendMode } from "../lib/tcp";
import type { LocalServer } from "../stores";

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Sends the composer's text to a connection: as UTF-8 with the line ending,
 * or the bytes it spells in hex. Resolves with the number of bytes sent.
 */
export async function sendToTcpConnection(
  serverId: string,
  connectionId: string,
  mode: TcpSendMode,
  text: string,
  lineEnding: TcpLineEnding,
): Promise<number> {
  const res = await run(() => api.sendToTcpConnection(serverId, connectionId, mode, text, lineEnding));
  return res.bytes;
}

/** Closes a connection; resolves once it is closed (its row goes with the next servers:connections event). */
export function closeTcpConnection(serverId: string, connectionId: string): Promise<void> {
  return run(() => api.closeTcpConnection(serverId, connectionId));
}

/** The address clients connect to ("127.0.0.1:7000"), or null when not running. */
export function tcpServerAddress(server: LocalServer): string | null {
  const address = server.addresses[0];
  return server.state === "running" && address !== undefined ? address : null;
}

/**
 * A command that connects to `address`, for the connect note: netcat, or
 * openssl s_client for a TLS server (SRV-05).
 */
export function netcatCommand(address: string, tls = false): string {
  if (tls) return `openssl s_client -connect ${address}`; // i18n-ignore: a shell command
  const at = address.lastIndexOf(":");
  const host = address.slice(0, at).replace(/^\[(.*)\]$/, "$1");
  return `nc ${host} ${address.slice(at + 1)}`;
}
