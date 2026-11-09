// UDP server commands (SSRV-06): send a datagram to a recent peer and
// change how the server replies, through the Go ServersService. The peer
// list itself arrives as servers:peers (commands/servers.ts,
// followServers). See docs/servers.md.
//
// Failed calls reject with an AppError (lib/errors.ts).

import { toAppError } from "../lib/errors";
import * as api from "../lib/servers";
import type { TcpLineEnding, TcpSendMode } from "../lib/tcp";
import type { LocalServer, Stores, UdpServerConfig } from "../stores";
import { toLocalServer } from "./servers";

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Sends the composer's text to a peer as one datagram: as UTF-8 with the
 * line ending, or the bytes it spells in hex. Resolves with its size.
 */
export async function sendUdpDatagram(
  serverId: string,
  peer: string,
  mode: TcpSendMode,
  text: string,
  lineEnding: TcpLineEnding,
): Promise<number> {
  const res = await run(() => api.sendUdpDatagram(serverId, peer, mode, text, lineEnding));
  return res.bytes;
}

/** Replaces a UDP server's replies; a running server uses them from the next datagram. */
export async function updateUdpConfig(stores: Stores, id: string, config: UdpServerConfig): Promise<LocalServer> {
  const dto = await run(() => api.updateServerConfig(id, { mockHttp: null, webSocket: null, webhook: null, udp: { ...config } }));
  const server = toLocalServer(dto);
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

/** The address datagrams go to ("127.0.0.1:9125"), or null when not running. */
export function udpServerAddress(server: LocalServer): string | null {
  const address = server.addresses[0];
  return server.state === "running" && address !== undefined ? address : null;
}

/** A command that sends datagrams to `address` from a terminal (netcat in UDP mode). */
export function netcatUdpCommand(address: string): string {
  const at = address.lastIndexOf(":");
  const host = address.slice(0, at).replace(/^\[(.*)\]$/, "$1");
  return `nc -u ${host} ${address.slice(at + 1)}`;
}
