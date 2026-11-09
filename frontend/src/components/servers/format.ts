// Display helpers for the local servers (SRV-01).
import { formatNumber, t, type MessageKey } from "../../i18n";
import { ECHO_HTTP_TYPE, GRAPHQL_MOCK_TYPE, GRPC_MOCK_TYPE, MOCK_HTTP_TYPE, SOCKETIO_TYPE, SSE_TYPE, TCP_TYPE, UDP_TYPE, WEBHOOK_TYPE, WEBSOCKET_TYPE } from "../../lib/servers";
import type { LocalServer, ServerState } from "../../stores";

/** Uptime as "42s", "3m 07s", "2h 05m", "3d 04h" (in the language in use). */
export function formatUptime(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const n = (v: number) => formatNumber(v, { useGrouping: false });
  const pad = (v: number) => formatNumber(v, { minimumIntegerDigits: 2, useGrouping: false });
  if (total < 60) return t("servers.uptime.seconds", { s: n(total) });
  const minutes = Math.floor(total / 60);
  if (minutes < 60) return t("servers.uptime.minutes", { m: n(minutes), s: pad(total % 60) });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t("servers.uptime.hours", { h: n(hours), m: pad(minutes % 60) });
  return t("servers.uptime.days", { d: n(Math.floor(hours / 24)), h: pad(hours % 24) });
}

/** The message key of each server state; t(STATE_LABELS[state]) where it is shown. */
export const STATE_LABELS: Readonly<Record<ServerState, MessageKey>> = {
  stopped: "servers.state.stopped",
  starting: "servers.state.starting",
  running: "servers.state.running",
  stopping: "servers.state.stopping",
  failed: "servers.state.failed",
};

/**
 * The address to show and copy: the first bound address while running
 * ("127.0.0.1:52341"), else the defined host and port ("localhost:8080",
 * "localhost:any port").
 */
export function serverAddress(server: LocalServer): string {
  if (server.addresses.length > 0) return server.addresses[0];
  const host = server.host === "" ? "localhost" : server.host.includes(":") && !server.host.startsWith("[") ? `[${server.host}]` : server.host;
  return server.port === 0 ? t("servers.address.anyPort", { host }) : `${host}:${server.port}`;
}

/** Whether the address is a real one to copy (bound, or a fixed port). */
export function canCopyAddress(server: LocalServer): boolean {
  return server.addresses.length > 0;
}

/** The kinds of server the form creates, with what they do. */
export const NEW_SERVER_TYPES: readonly { type: string; labelKey: MessageKey; noteKey: MessageKey }[] = [
  { type: MOCK_HTTP_TYPE, labelKey: "servers.type.mock", noteKey: "servers.type.mock.note" },
  { type: ECHO_HTTP_TYPE, labelKey: "servers.type.echo", noteKey: "servers.type.echo.note" },
  { type: WEBSOCKET_TYPE, labelKey: "servers.type.websocket", noteKey: "servers.type.websocket.note" },
  { type: TCP_TYPE, labelKey: "servers.type.tcp", noteKey: "servers.type.tcp.note" },
  { type: WEBHOOK_TYPE, labelKey: "servers.type.webhook", noteKey: "servers.type.webhook.note" },
  { type: SSE_TYPE, labelKey: "servers.type.sse", noteKey: "servers.type.sse.note" },
  { type: UDP_TYPE, labelKey: "servers.type.udp", noteKey: "servers.type.udp.note" },
  { type: GRPC_MOCK_TYPE, labelKey: "servers.type.grpc", noteKey: "servers.type.grpc.note" },
  { type: SOCKETIO_TYPE, labelKey: "servers.type.socketio", noteKey: "servers.type.socketio.note" },
  { type: GRAPHQL_MOCK_TYPE, labelKey: "servers.type.graphql", noteKey: "servers.type.graphql.note" },
];

/** The message key of the note on a kind of server in the new-server forms. */
export function newServerNote(type: string): MessageKey | undefined {
  return NEW_SERVER_TYPES.find((k) => k.type === type)?.noteKey;
}

/** A server's kind in the language in use; Go's label for a kind the UI does not know. */
export function serverTypeLabel(server: LocalServer): string {
  const key = NEW_SERVER_TYPES.find((k) => k.type === server.type)?.labelKey;
  return key === undefined ? server.typeLabel : t(key);
}
