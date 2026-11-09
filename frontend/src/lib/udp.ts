// Calls into the Go UDPService (internal/app/services/udp): UDP requests
// (UDP-01), directly or through a SOCKS5 proxy's UDP ASSOCIATE relay
// (SOCKS-07). Open returns a socket ID at once; the outcome and the
// datagrams arrive as udp:state and udp:data events. See docs/udp.md.
import { Events } from "@wailsio/runtime";
import {
  UDPService,
  type CloseInfoDTO,
  type ConnectionDTO,
  type DataEventDTO,
  type DatagramDTO,
  type OpenInputDTO,
  type ProxyHopDTO,
  type SendInputDTO,
  type SendResultDTO,
  type StateEventDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/udp";
import type { CancellablePromise } from "./calls";

export type {
  CloseInfoDTO,
  ConnectionDTO,
  DataEventDTO,
  DatagramDTO,
  OpenInputDTO,
  ProxyHopDTO,
  SendInputDTO,
  SendResultDTO,
  StateEventDTO,
};

/** Sent on every state change of a socket. */
export const UDP_STATE_EVENT = "udp:state";
/** Sent with the datagrams and notes since the last one, at most every 50 ms per socket. */
export const UDP_DATA_EVENT = "udp:data";

/** How the composer's text becomes a datagram. */
export type UdpSendMode = "text" | "hex" | "base64";
/** A socket's state as udp:state reports it. */
export type UdpState = "opening" | "open" | "closing" | "closed";
/** The IP version a request asks for: "" for either. */
export type UdpIpVersion = "" | "ipv4" | "ipv6";

/** The largest datagram UDP can carry, and the largest maximum a request may set. */
export const UDP_MAX_DATAGRAM = 65535;

/** The settings of a socket, as a udp request stores them. */
export interface UdpOpenOptions {
  readonly ipVersion?: string;
  /** Receive only from the target (and see ICMP "port unreachable"); otherwise from any sender. */
  readonly connected?: boolean;
  /** Null for 65,535. */
  readonly maxDatagramBytes?: number | null;
  /** Null for the default (10 s), 0 for no limit. */
  readonly openTimeoutMs?: number | null;
}

/**
 * Starts opening a socket to `address` (host:port, {{variables}} rendered
 * with the request's). Resolves with the socket ID at once; rejects only
 * for input that cannot be used.
 */
export function openUdp(requestId: string, address: string, options: UdpOpenOptions = {}): CancellablePromise<ConnectionDTO> {
  return UDPService.Open({
    requestId,
    address,
    ipVersion: options.ipVersion ?? "",
    connected: options.connected === true,
    maxDatagramBytes: options.maxDatagramBytes ?? null,
    openTimeoutMs: options.openTimeoutMs ?? null,
  });
}

/** Sends one datagram: text (variables rendered), hex or Base64. Resolves with its size. */
export function sendUdp(connId: string, mode: UdpSendMode, text: string): CancellablePromise<SendResultDTO> {
  return UDPService.Send({ connId, mode, text });
}

/** Closes a socket, or stops opening; resolves once it is closed. */
export function closeUdp(connId: string): CancellablePromise<void> {
  return UDPService.Close(connId);
}

/** Closes every socket; resolves with how many there were. */
export function closeAllUdp(): CancellablePromise<number> {
  return UDPService.CloseAll();
}

/** Calls `callback` with every udp:state event. Returns the unsubscribe function. */
export function onUdpState(callback: (event: StateEventDTO) => void): () => void {
  return Events.On(UDP_STATE_EVENT, (ev) => callback(ev.data));
}

/** Calls `callback` with every udp:data event. Returns the unsubscribe function. */
export function onUdpData(callback: (event: DataEventDTO) => void): () => void {
  return Events.On(UDP_DATA_EVENT, (ev) => callback(ev.data));
}
