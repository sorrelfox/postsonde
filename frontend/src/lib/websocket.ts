// Calls into the Go WebSocketService (internal/app/services/websocket):
// WebSocket connections (WS-01, WS-02, WS-03, WS-08), pings with keepalive
// (WS-09) and auto-reconnect (WS-10). Connect returns a
// connection ID at once; the outcome and the messages arrive as ws:state
// and ws:message events. Go renders the {{variables}} of the URL, headers,
// subprotocols and messages on every connect and send (WS-11). See
// docs/websocket.md.
import { Events } from "@wailsio/runtime";
import {
  WebSocketService,
  type CloseFrameDTO,
  type CloseInfoDTO,
  type ConnectInputDTO,
  type ConnectionDTO,
  type HandshakeDTO,
  type HandshakeResponseDTO,
  type HeaderDTO,
  type MessageDTO,
  type MessageEventDTO,
  type PingResultDTO,
  type ReconnectDTO,
  type SendResultDTO,
  type StateEventDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/websocket";
import type { CancellablePromise } from "./calls";
import type { RequestDTO, SavedMessageDTO } from "./collection";
import type { SnippetDTO, SnippetLanguageDTO, SnippetMode } from "./http";

export type {
  CloseFrameDTO,
  CloseInfoDTO,
  ConnectInputDTO,
  ConnectionDTO,
  HandshakeDTO,
  HandshakeResponseDTO,
  HeaderDTO,
  MessageDTO,
  MessageEventDTO,
  PingResultDTO,
  ReconnectDTO,
  SavedMessageDTO,
  SendResultDTO,
  StateEventDTO,
};

/** Sent on every state change of a connection. */
export const WS_STATE_EVENT = "ws:state";
/** Sent with the messages sent and received since the last one, at most every 50 ms per connection. */
export const WS_MESSAGE_EVENT = "ws:message";

/**
 * How the composer's text is edited and sent: plain text, or JSON with
 * validation and beautify, both sent as text messages with their
 * {{variables}} rendered; or the bytes of a binary message typed as hex or
 * Base64 (WS-04), sent as written.
 */
export type WsMessageFormat = "text" | "json" | "hex" | "base64";

/** The formats in the order the composer offers them. */
export const WS_MESSAGE_FORMATS: readonly WsMessageFormat[] = ["text", "json", "hex", "base64"];

/** A stored format as the composer knows it ("" and formats of a newer version read as text). */
export function wsMessageFormat(format: string | undefined): WsMessageFormat {
  return WS_MESSAGE_FORMATS.find((f) => f === format) ?? "text";
}

/** Whether `format` is sent as a binary message. */
export function isBinaryFormat(format: WsMessageFormat): boolean {
  return format === "hex" || format === "base64";
}

/** The largest file "Load file…" puts into the composer (as Base64). */
export const WS_FILE_MAX_BYTES = 1024 * 1024;

/** The close code sent by Disconnect unless another is chosen. */
export const WS_NORMAL_CLOSURE = 1000;

/** What Connect needs from a websocket request (its handshake options). */
export function connectInput(request: RequestDTO): ConnectInputDTO {
  return {
    requestId: request.id,
    url: request.url.trim(),
    headers: (request.headers ?? []).map((h) => ({ name: h.name, value: h.value, enabled: h.enabled && h.name !== "" })),
    subprotocols: (request.subprotocols ?? []).map((p) => p.trim()).filter((p) => p !== ""),
    connectTimeoutMs: request.settings.connectTimeoutMs,
    verifyTls: request.settings.verifyTls,
    sendCookies: request.settings.sendCookies,
    ...keepaliveAndReconnect(request.settings),
  };
}

/** The keepalive (WS-09) and reconnect (WS-10) settings that are set; Go uses its defaults for the rest. */
function keepaliveAndReconnect(settings: RequestDTO["settings"]): Partial<ConnectInputDTO> {
  const out: Partial<ConnectInputDTO> = {};
  if (settings.pingIntervalMs != null) out.pingIntervalMs = settings.pingIntervalMs;
  if (settings.pongTimeoutMs != null) out.pongTimeoutMs = settings.pongTimeoutMs;
  if (settings.autoReconnect != null) out.autoReconnect = settings.autoReconnect;
  if (settings.reconnectMaxAttempts != null) out.reconnectMaxAttempts = settings.reconnectMaxAttempts;
  if (settings.reconnectDelayMs != null) out.reconnectDelayMs = settings.reconnectDelayMs;
  if (settings.reconnectMaxDelayMs != null) out.reconnectMaxDelayMs = settings.reconnectMaxDelayMs;
  return out;
}

/** The largest ping payload in bytes (RFC 6455 control frames). */
export const WS_PING_MAX_BYTES = 125;

/** Defaults Go uses for null keepalive and reconnect settings (WS-09, WS-10). */
export const WS_DEFAULT_PONG_TIMEOUT_MS = 10_000;
export const WS_DEFAULT_RECONNECT_ATTEMPTS = 5;
export const WS_DEFAULT_RECONNECT_DELAY_MS = 1_000;
export const WS_DEFAULT_RECONNECT_MAX_DELAY_MS = 30_000;

/**
 * Starts connecting. Resolves with the connection ID at once; rejects only
 * for input that cannot be used, and with UNRESOLVED_CODE (lib/http.ts)
 * when {{variables}} are undefined, unless `allowUnresolved`, which uses
 * them as written (ENV-11).
 */
export function connectWs(input: ConnectInputDTO, allowUnresolved = false): CancellablePromise<ConnectionDTO> {
  return WebSocketService.Connect(allowUnresolved ? { ...input, allowUnresolved } : input);
}

/**
 * Sends one message: text and JSON as a text message, its {{variables}}
 * rendered by Go; hex and Base64 as the binary message they spell (bad
 * bytes reject with hexbytes.* or ws.invalid_base64). Resolves with its
 * size in bytes; rejects with UNRESOLVED_CODE when variables are
 * undefined, unless `allowUnresolved`.
 */
export function sendWs(
  connId: string,
  text: string,
  format: WsMessageFormat = "text",
  allowUnresolved = false,
): CancellablePromise<SendResultDTO> {
  // Text and JSON are the same to Go, so only the binary formats are passed.
  return WebSocketService.Send({ connId, text, ...(isBinaryFormat(format) && { format }), ...(allowUnresolved && { allowUnresolved }) });
}

/**
 * Sends a ping with `payload` as text (at most 125 bytes, ws.ping_too_long
 * otherwise; no {{variables}}). The ping and its pong, with the round-trip
 * time, arrive in ws:message (WS-09).
 */
export function pingWs(connId: string, payload = ""): CancellablePromise<PingResultDTO> {
  return WebSocketService.Ping({ connId, payload });
}

/** Closes a connection with `code` and `reason`, or stops connecting; resolves once it is closed. */
export function closeWs(connId: string, code: number = WS_NORMAL_CLOSURE, reason = ""): CancellablePromise<void> {
  return WebSocketService.Close({ connId, code, reason });
}

/** Calls `callback` with every ws:state event. Returns the unsubscribe function. */
export function onWsState(callback: (event: StateEventDTO) => void): () => void {
  return Events.On(WS_STATE_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every ws:message event. Returns the unsubscribe function. */
export function onWsMessage(callback: (event: MessageEventDTO) => void): () => void {
  return Events.On(WS_MESSAGE_EVENT, (ev) => callback(ev.data));
}

/**
 * The handshake of `connect` as code in `language` (a
 * `SnippetLanguageDTO.id` of wsSnippetLanguages), for the Code tab
 * (GEN-11): resolved with the request's variables and cookie jar, or
 * templated, secrets masked unless `revealSecrets` (resolved only).
 * Nothing is dialed. See docs/snippets.md#websocat-and-wscat.
 */
export function wsSnippet(
  connect: ConnectInputDTO,
  language: string,
  mode: SnippetMode,
  revealSecrets: boolean,
): CancellablePromise<SnippetDTO> {
  return WebSocketService.Snippet({ connect, language, mode, revealSecrets });
}

/** The languages of the WebSocket Code tab, in the order it lists them. */
export async function wsSnippetLanguages(): Promise<SnippetLanguageDTO[]> {
  return (await WebSocketService.SnippetLanguages()) ?? [];
}
