// Calls into the Go TCPService (internal/app/services/tcp): raw TCP
// connections (TCP-01), over TLS (TCP-02) and with message framing
// (TCP-03). Connect returns a connection ID at once; the
// outcome and the bytes arrive as tcp:state and tcp:data events. See
// docs/tcp.md.
import { Events } from "@wailsio/runtime";
import {
  TCPService,
  type ChunkDTO,
  type CloseInfoDTO,
  type ConnectInputDTO,
  type ConnectionDTO,
  type DataEventDTO,
  type FramingDTO,
  type SendInputDTO,
  type SendResultDTO,
  type StateEventDTO,
  type TLSDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/tcp";
import type { CancellablePromise } from "./calls";

export type {
  ChunkDTO,
  CloseInfoDTO,
  ConnectInputDTO,
  ConnectionDTO,
  DataEventDTO,
  FramingDTO,
  SendInputDTO,
  SendResultDTO,
  StateEventDTO,
  TLSDTO,
};

/** Sent on every state change of a connection. */
export const TCP_STATE_EVENT = "tcp:state";
/** Sent with the chunks sent and received since the last one, at most every 50 ms per connection. */
export const TCP_DATA_EVENT = "tcp:data";

/** How the composer's text becomes bytes. */
export type TcpSendMode = "text" | "hex";
/** What is appended to a text message. */
export type TcpLineEnding = "none" | "lf" | "crlf";
/** A connection's state as tcp:state reports it. */
export type TcpState = "connecting" | "open" | "closing" | "closed";
/** How received bytes are split into messages (docs/tcp.md, Framing). */
export type TcpFramingMode = "none" | "newline" | "length";

/** The settings of a connection. */
export interface TcpConnectOptions {
  /** Null for the default (10 s), 0 for no limit; covers the TLS handshake too. */
  readonly connectTimeoutMs?: number | null;
  /** Connect with TLS, using the app's trusted CAs and client certificates. */
  readonly tls?: boolean;
  /** False accepts any server certificate; null verifies. */
  readonly verifyTls?: boolean | null;
  /** Null (or mode "none") for no framing: chunks as read. */
  readonly framing?: FramingDTO | null;
}

/**
 * Starts connecting to `address` (host:port). Resolves with the connection
 * ID at once; rejects only for input that cannot be used (an address, a
 * framing, a CA bundle or client certificate that cannot be loaded).
 */
export function connectTcp(
  requestId: string,
  address: string,
  options: TcpConnectOptions = {},
): CancellablePromise<ConnectionDTO> {
  return TCPService.Connect({
    requestId,
    address,
    connectTimeoutMs: options.connectTimeoutMs ?? null,
    tls: options.tls === true,
    verifyTls: options.verifyTls ?? null,
    framing: options.framing ?? null,
  });
}

/**
 * Sends text (with the line ending, which a framing replaces) or hex bytes.
 * Resolves with the number of bytes of the message.
 */
export function sendTcp(
  connId: string,
  mode: TcpSendMode,
  text: string,
  lineEnding: TcpLineEnding,
): CancellablePromise<SendResultDTO> {
  return TCPService.Send({ connId, mode, text, lineEnding });
}

/** Closes a connection, or stops connecting; resolves once it is closed. */
export function closeTcp(connId: string): CancellablePromise<void> {
  return TCPService.Close(connId);
}

/** Closes every connection; resolves with how many there were. */
export function closeAllTcp(): CancellablePromise<number> {
  return TCPService.CloseAll();
}

/** Calls `callback` with every tcp:state event. Returns the unsubscribe function. */
export function onTcpState(callback: (event: StateEventDTO) => void): () => void {
  return Events.On(TCP_STATE_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every tcp:data event. Returns the unsubscribe function. */
export function onTcpData(callback: (event: DataEventDTO) => void): () => void {
  return Events.On(TCP_DATA_EVENT, (ev) => callback(ev.data));
}

/** Decodes a chunk's base64 data. */
export function decodeBase64(data: string): Uint8Array {
  const binary = atob(data);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}
