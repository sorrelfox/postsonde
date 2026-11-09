// Calls into the Go SocketIOService (internal/app/services/socketio):
// Socket.IO v4 connections (SIO-01, SIO-03…SIO-07). Connect runs the
// connection as a sio operation whose items are the handshake, the events
// and acks sent and received, the heartbeat and the end; Emit sends an
// event on it; disconnecting is OpsService.Cancel. Go renders the
// {{variables}} of the URL, headers, path, namespace, auth payload, event
// names and arguments. See docs/socketio.md.
import { t } from "../i18n";
import {
  SocketIOService,
  type ConnectInputDTO,
  type EmitInputDTO,
  type EmitResultDTO,
  type HeaderDTO,
  type ItemDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/socketio";
import type { CancellablePromise } from "./calls";
import type { RequestDTO } from "./collection";
import type { StartedDTO } from "./operations";

export type { ConnectInputDTO, EmitInputDTO, EmitResultDTO, HeaderDTO, ItemDTO };

/** The domain of connection operations: their events are `sio:op:data|progress|done`. */
export const SIO_DOMAIN = "sio";

/** The default Engine.IO handshake path. */
export const SIO_DEFAULT_PATH = "/socket.io/";

/** How the Engine.IO connection is made (SIO-01). */
export type SioTransport = "auto" | "websocket" | "polling" | "upgrade";

/** The transports in the order the editor offers them, with their labels and help (looked up when read, UI-10). */
export const SIO_TRANSPORTS: readonly { readonly value: SioTransport; readonly label: string; readonly help: string }[] = [
  {
    value: "auto",
    get label() {
      return t("lib.socketio.transport.auto");
    },
    get help() {
      return t("lib.socketio.transport.autoHelp");
    },
  },
  {
    value: "websocket",
    get label() {
      return t("lib.socketio.transport.websocket");
    },
    get help() {
      return t("lib.socketio.transport.websocketHelp");
    },
  },
  {
    value: "polling",
    get label() {
      return t("lib.socketio.transport.polling");
    },
    get help() {
      return t("lib.socketio.transport.pollingHelp");
    },
  },
  {
    value: "upgrade",
    get label() {
      return t("lib.socketio.transport.upgrade");
    },
    get help() {
      return t("lib.socketio.transport.upgradeHelp");
    },
  },
];

/** The server's Socket.IO version (SIO-02): "" for v3 and v4, "v2" for socket.io v2 (Engine.IO 3). */
export type SioVersion = "" | "v2";

/** A stored version as the editor knows it (unknown ones read as v3/v4). */
export function sioVersion(value: string | undefined): SioVersion {
  return value === "v2" ? "v2" : "";
}

/** A stored transport as the editor knows it ("" and unknown ones read as auto). */
export function sioTransport(value: string | undefined): SioTransport {
  return SIO_TRANSPORTS.find((transport) => transport.value === value)?.value ?? "auto";
}

/** What Connect needs from a socketio request (its handshake options). */
export function sioConnectInput(request: RequestDTO): ConnectInputDTO {
  return {
    requestId: request.id,
    url: request.url.trim(),
    headers: (request.headers ?? []).map((h) => ({ name: h.name, value: h.value, enabled: h.enabled && h.name !== "" })),
    path: (request.path ?? "").trim(),
    namespace: (request.namespace ?? "").trim(),
    authPayload: request.authPayload ?? "",
    ...(request.authIn !== undefined && request.authIn !== "" && { authIn: request.authIn }),
    transport: sioTransport(request.transport),
    version: sioVersion(request.socketIoVersion),
    connectTimeoutMs: request.settings.connectTimeoutMs,
    ackTimeoutMs: request.settings.ackTimeoutMs ?? null,
    verifyTls: request.settings.verifyTls,
    sendCookies: request.settings.sendCookies,
  };
}

/**
 * Starts connecting. Resolves with the operation ID at once; rejects only
 * for input that cannot be used, and with UNRESOLVED_CODE (lib/http.ts)
 * when {{variables}} are undefined, unless `allowUnresolved`.
 */
export function connectSio(input: ConnectInputDTO, allowUnresolved = false): CancellablePromise<StartedDTO> {
  return SocketIOService.Connect(allowUnresolved ? { ...input, allowUnresolved } : input);
}

/**
 * Emits an event on connection `opId`: `args` is JSON text (an array holds
 * one argument per element; {"$base64": "…"} and {"$hex": "…"} are binary
 * arguments). Rejects with sio.invalid_args, sio.invalid_event, or
 * UNRESOLVED_CODE unless `allowUnresolved`.
 */
export function emitSio(opId: string, event: string, args: string, ack: boolean, allowUnresolved = false): CancellablePromise<EmitResultDTO> {
  return SocketIOService.Emit({ opId, event, args, ack, ...(allowUnresolved && { allowUnresolved }) });
}
