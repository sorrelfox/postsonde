// The UDP sockets of open udp requests (UDP-01, through SOCKS5 UDP
// ASSOCIATE SOCKS-07): one session per request, holding its socket state
// and its message log. Sessions live outside React, so switching tabs
// (which unmounts the editor) keeps the socket and its log; closing the
// tab closes them. See docs/udp.md.
import { t } from "../i18n";
import { toAppError, type AppError } from "../lib/errors";
import { MessageSource, type LogMessageInput, type MessageMetaValue } from "../lib/messageSource";
import { decodeBase64 } from "../lib/tcp";
import {
  closeUdp,
  onUdpData,
  onUdpState,
  openUdp,
  sendUdp,
  type CloseInfoDTO,
  type DataEventDTO,
  type ProxyHopDTO,
  type SendResultDTO,
  type StateEventDTO,
  type UdpOpenOptions,
  type UdpSendMode,
} from "../lib/udp";
import { onTabClosed } from "./tabs";

/** "idle" before the first open; then the states Go reports. */
export type UdpStatus = "idle" | "opening" | "open" | "closing" | "closed";

/** How datagrams are shown in the log. */
export type UdpView = "text" | "hex";

export interface UdpSessionState {
  readonly status: UdpStatus;
  /** The current (or last) socket, null before the first open. */
  readonly connId: string | null;
  /** The address of the current (or last) socket, variables rendered once Go answered. */
  readonly address: string;
  readonly localAddr: string;
  /** The target, or the proxy's relay through a proxy. */
  readonly remoteAddr: string;
  /** The proxy ("socks5h://proxy:1080"), "" when direct. */
  readonly via: string;
  /** The hop through the proxy (UDP ASSOCIATE, the relay), null when direct. */
  readonly proxy: ProxyHopDTO | null;
  /** Whether only the target's datagrams are received. */
  readonly connected: boolean;
  /** The largest datagram that can be sent, 0 before open. */
  readonly maxPayload: number;
  /** Why the last socket failed, or why opening could not start. */
  readonly error: AppError | null;
  /** How the last socket ended. */
  readonly close: CloseInfoDTO | null;
  readonly datagramsSent: number;
  readonly datagramsReceived: number;
  readonly view: UdpView;
}

const INITIAL: UdpSessionState = {
  status: "idle",
  connId: null,
  address: "",
  localAddr: "",
  remoteAddr: "",
  via: "",
  proxy: null,
  connected: false,
  maxPayload: 0,
  error: null,
  close: null,
  datagramsSent: 0,
  datagramsReceived: 0,
  view: "text",
};

function eventTime(at: string): number | undefined {
  const ms = Date.parse(at);
  return Number.isNaN(ms) ? undefined : ms;
}

function errorText(err: AppError): string {
  return err.hint ? t("lib.errors.withHint", { message: err.message, hint: err.hint }) : err.message;
}

/** The hop's fields for a message's details. */
function hopMeta(h: ProxyHopDTO): Record<string, string | number | boolean> {
  return {
    proxy: h.proxy,
    proxyAddr: h.proxyAddr,
    proxyVersion: h.version,
    command: h.command,
    relay: h.relayAddr,
    auth: h.auth,
    dns: h.remoteDns ? "remote" : "local",
    reply: h.reply === null ? "" : `${h.reply} ${h.replyText}`.trim(),
    handshakeMs: h.handshakeMs,
  };
}

/** The system message for a state change. */
function stateMessage(ev: StateEventDTO): LogMessageInput {
  const time = eventTime(ev.at);
  switch (ev.state) {
    case "opening":
      return { time, direction: "system", kind: "connecting", text: t("commands.udp.opening", { address: ev.address }) };
    case "open": {
      const mode = ev.connected ? t("commands.udp.connectedMode") : t("commands.udp.unconnectedMode");
      const hop = ev.proxy ?? null;
      const text =
        ev.via === ""
          ? t("commands.udp.open", { address: ev.address, local: ev.localAddr, mode })
          : t("commands.udp.openVia", { address: ev.address, via: ev.via, relay: hop?.relayAddr ?? ev.remoteAddr, mode });
      return {
        time,
        direction: "system",
        kind: "connected",
        text,
        meta: {
          local: ev.localAddr,
          remote: ev.remoteAddr,
          connected: ev.connected,
          maxDatagram: ev.maxPayload,
          ...(hop !== null ? hopMeta(hop) : {}),
        },
      };
    }
    case "closing":
      return { time, direction: "system", kind: "closing", text: t("commands.udp.closing") };
    default: {
      const close = ev.close;
      const meta = close
        ? {
            initiatedBy: close.initiatedBy,
            datagramsSent: close.datagramsSent,
            datagramsReceived: close.datagramsReceived,
            bytesSent: close.bytesSent,
            bytesReceived: close.bytesReceived,
            notes: close.notes,
            durationMs: close.durationMs,
          }
        : undefined;
      if (ev.error !== null) {
        const err = toAppError(ev.error);
        const hop = ev.proxy ?? null;
        return {
          time,
          direction: "system",
          kind: "error",
          text: errorText(err),
          meta: { ...(meta ?? {}), code: err.code, ...(hop !== null ? hopMeta(hop) : {}) },
        };
      }
      const text = close
        ? t("commands.udp.closedTotals", {
            sent: t("commands.udp.datagrams", { count: close.datagramsSent }),
            received: t("commands.udp.datagrams", { count: close.datagramsReceived }),
          })
        : t("commands.udp.closed");
      return { time, direction: "system", kind: "closed", text, meta };
    }
  }
}

/**
 * One udp request's socket and log. The log is kept twice, as text and as
 * hex, so switching the view shows the whole history in the other form.
 * Each datagram is one message, decoded on its own; a received one shows
 * its sender as the badge.
 */
export class UdpSession {
  readonly requestId: string;
  readonly textLog: MessageSource;
  readonly hexLog: MessageSource;
  private state: UdpSessionState = INITIAL;
  private readonly listeners = new Set<() => void>();
  /** Open was called and has not resolved: events of unknown sockets are kept for it. */
  private pending = false;
  private buffered: { state?: StateEventDTO; data?: DataEventDTO }[] = [];
  /** Close was asked for while Open had not resolved yet. */
  private closeWhenStarted = false;

  constructor(requestId: string, options: { textLog?: MessageSource; hexLog?: MessageSource } = {}) {
    this.requestId = requestId;
    this.textLog = options.textLog ?? new MessageSource();
    this.hexLog = options.hexLog ?? new MessageSource();
  }

  getState = (): UdpSessionState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<UdpSessionState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  private append(message: LogMessageInput): void {
    this.textLog.append(message);
    this.hexLog.append(message);
  }

  /** Whether a socket is being opened or is open. */
  get active(): boolean {
    return this.pending || this.state.status === "opening" || this.state.status === "open" || this.state.status === "closing";
  }

  /**
   * Opens a socket to `address` with `options`. Resolves once Go has
   * started opening; the outcome arrives as state changes. Input Go
   * refuses is shown as the session's error and in the log, and rejects.
   */
  async open(address: string, options: UdpOpenOptions = {}): Promise<void> {
    if (this.active) return;
    this.pending = true;
    this.closeWhenStarted = false;
    this.buffered = [];
    this.set({ ...INITIAL, view: this.state.view, status: "opening", address });
    try {
      const conn = await openUdp(this.requestId, address, options);
      this.pending = false;
      this.set({ connId: conn.connId, address: conn.address });
      const buffered = this.buffered;
      this.buffered = [];
      for (const ev of buffered) {
        if (ev.state?.connId === conn.connId) this.handleState(ev.state);
        if (ev.data?.connId === conn.connId) this.handleData(ev.data);
      }
      if (this.closeWhenStarted) await this.close();
    } catch (reason: unknown) {
      this.pending = false;
      this.buffered = [];
      const error = toAppError(reason);
      this.set({ status: "idle", error });
      this.append({ direction: "system", kind: "error", text: errorText(error) });
      throw error;
    }
  }

  /** Sends the composer's text as one datagram. Rejects with the AppError Go returned. */
  async send(mode: UdpSendMode, text: string): Promise<SendResultDTO> {
    const connId = this.state.connId;
    if (connId === null || this.state.status !== "open") {
      // i18n-ignore: Go-style error text, localized by its code (toAppError, errors.ts)
      throw toAppError({ kind: "invalid_argument", code: "udp.not_open", message: "The UDP socket is not open." });
    }
    try {
      return await sendUdp(connId, mode, text);
    } catch (reason: unknown) {
      throw toAppError(reason);
    }
  }

  /** Closes the socket, or stops opening. */
  async close(): Promise<void> {
    if (this.pending) {
      this.closeWhenStarted = true;
      return;
    }
    const connId = this.state.connId;
    if (connId === null || !this.active) return;
    try {
      await closeUdp(connId);
    } catch (reason: unknown) {
      console.warn("udp: closing the socket failed", toAppError(reason));
    }
  }

  setView(view: UdpView): void {
    if (this.state.view !== view) this.set({ view });
  }

  /** Empties both logs. */
  clear(): void {
    this.textLog.clear();
    this.hexLog.clear();
  }

  /** Applies a udp:state event of this request. */
  handleState(ev: StateEventDTO): void {
    if (ev.connId !== this.state.connId) {
      if (this.pending && ev.requestId === this.requestId) this.buffered.push({ state: ev });
      return;
    }
    const patch: { -readonly [K in keyof UdpSessionState]?: UdpSessionState[K] } = { status: ev.state as UdpStatus };
    if (ev.state === "open") {
      patch.localAddr = ev.localAddr;
      patch.remoteAddr = ev.remoteAddr;
      patch.via = ev.via;
      patch.proxy = ev.proxy ?? null;
      patch.connected = ev.connected;
      patch.maxPayload = ev.maxPayload;
    }
    if (ev.state === "closed") {
      patch.close = ev.close;
      patch.error = ev.error === null ? null : toAppError(ev.error);
      if (ev.proxy) patch.proxy = ev.proxy;
      if (ev.close) {
        patch.datagramsSent = ev.close.datagramsSent;
        patch.datagramsReceived = ev.close.datagramsReceived;
      }
    }
    this.set(patch);
    this.append(stateMessage(ev));
  }

  /** Applies a udp:data event of this request. */
  handleData(ev: DataEventDTO): void {
    if (ev.connId !== this.state.connId) {
      if (this.pending && ev.requestId === this.requestId) this.buffered.push({ data: ev });
      return;
    }
    let sent = this.state.datagramsSent;
    let received = this.state.datagramsReceived;
    const text: LogMessageInput[] = [];
    const hex: LogMessageInput[] = [];
    for (const d of ev.datagrams ?? []) {
      const time = eventTime(d.at);
      if (d.direction === "note") {
        const err = d.error ? toAppError(d.error) : null;
        const note: LogMessageInput = {
          time,
          direction: "system",
          kind: "dropped",
          text: err !== null ? errorText(err) : t("commands.udp.dropped"),
          meta: err !== null ? { code: err.code } : undefined,
        };
        text.push(note);
        hex.push(note);
        continue;
      }
      const direction = d.direction === "sent" ? "sent" : "received";
      const bytes = decodeBase64(d.data);
      if (direction === "sent") sent++;
      else received++;
      const badge =
        direction === "received"
          ? d.truncated
            ? t("commands.udp.peerTruncated", { peer: d.peer })
            : d.peer
          : d.truncated
            ? t("commands.udp.truncated")
            : undefined;
      const meta: Record<string, MessageMetaValue> = direction === "received" ? { from: d.peer, truncated: d.truncated } : { to: d.peer };
      text.push({ time, direction, kind: "text", badge, text: new TextDecoder().decode(bytes), size: d.size, meta });
      hex.push({ time, direction, kind: "binary", badge, data: bytes, size: d.size, meta });
    }
    this.textLog.append(text);
    this.hexLog.append(hex);
    this.set({ datagramsSent: sent, datagramsReceived: received });
  }

  /** Closes the socket and releases the logs. */
  dispose(): void {
    void this.close();
    this.listeners.clear();
    this.textLog.dispose();
    this.hexLog.dispose();
  }
}

/** The sessions of every open udp request, following the udp events. */
export class UdpSessions {
  private readonly sessions = new Map<string, UdpSession>();
  private readonly unsubscribe: () => void;

  constructor() {
    const offState = onUdpState((ev) => this.sessions.get(ev.requestId)?.handleState(ev));
    const offData = onUdpData((ev) => this.sessions.get(ev.requestId)?.handleData(ev));
    // Closing a request's tab closes its socket and drops its log.
    const offTabs = onTabClosed((ids) => this.drop(ids));
    this.unsubscribe = () => {
      offState();
      offData();
      offTabs();
    };
  }

  /** The session of request `requestId`, created on first use. */
  get(requestId: string): UdpSession {
    let session = this.sessions.get(requestId);
    if (session === undefined) {
      session = new UdpSession(requestId);
      this.sessions.set(requestId, session);
    }
    return session;
  }

  /** The session of `requestId` if it has one. */
  peek(requestId: string): UdpSession | undefined {
    return this.sessions.get(requestId);
  }

  /** Closes and forgets the sessions of these requests. */
  drop(requestIds: readonly string[]): void {
    for (const id of requestIds) {
      const session = this.sessions.get(id);
      if (session === undefined) continue;
      this.sessions.delete(id);
      session.dispose();
    }
  }

  /** Closes every session and stops following events. */
  dispose(): void {
    this.drop([...this.sessions.keys()]);
    this.unsubscribe();
  }
}

let registry: UdpSessions | null = null;

/** The app's UDP sessions, created (and subscribed to events) on first use. */
export function udpSessions(): UdpSessions {
  registry ??= new UdpSessions();
  return registry;
}

/** Drops the registry, for tests. */
export function resetUdpSessions(): void {
  registry?.dispose();
  registry = null;
}
