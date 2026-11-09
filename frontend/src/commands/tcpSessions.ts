// The TCP connections of open tcp requests (TCP-01, over TLS TCP-02, with
// framing TCP-03): one session per request, holding its connection state and its message log. Sessions live
// outside React, so switching tabs (which unmounts the editor) keeps the
// connection and its log; closing the tab disconnects and drops them.
// See docs/tcp.md.
import { t } from "../i18n";
import { toAppError, type AppError } from "../lib/errors";
import { MessageSource, type LogMessageInput } from "../lib/messageSource";
import {
  closeTcp,
  connectTcp,
  decodeBase64,
  onTcpData,
  onTcpState,
  sendTcp,
  type CloseInfoDTO,
  type DataEventDTO,
  type SendResultDTO,
  type StateEventDTO,
  type TLSDTO,
  type TcpConnectOptions,
  type TcpLineEnding,
  type TcpSendMode,
} from "../lib/tcp";
import { onTabClosed } from "./tabs";

/** "idle" before the first connect; then the states Go reports. */
export type TcpStatus = "idle" | "connecting" | "open" | "closing" | "closed";

/** How received and sent bytes are shown in the log. */
export type TcpView = "text" | "hex";

export interface TcpSessionState {
  readonly status: TcpStatus;
  /** The current (or last) connection, null before the first connect. */
  readonly connId: string | null;
  /** The address of the current (or last) connection. */
  readonly address: string;
  readonly localAddr: string;
  readonly remoteAddr: string;
  /** The TLS session of the current (or last) connection, null without TLS. */
  readonly tls: TLSDTO | null;
  /** Whether the current (or last) connection splits messages: chunks are then whole messages. */
  readonly framed: boolean;
  /** Why the last connection failed, or why connecting could not start. */
  readonly error: AppError | null;
  /** How the last connection ended. */
  readonly close: CloseInfoDTO | null;
  readonly bytesSent: number;
  readonly bytesReceived: number;
  readonly view: TcpView;
}

const INITIAL: TcpSessionState = {
  status: "idle",
  connId: null,
  address: "",
  localAddr: "",
  remoteAddr: "",
  tls: null,
  framed: false,
  error: null,
  close: null,
  bytesSent: 0,
  bytesReceived: 0,
  view: "text",
};

function eventTime(at: string): number | undefined {
  const ms = Date.parse(at);
  return Number.isNaN(ms) ? undefined : ms;
}

/** "Connected to 10.0.0.2:7 from 10.0.0.1:5123 with TLS 1.3". */
function connectedText(address: string, local: string, tlsVersion: string): string {
  if (local === "") {
    return tlsVersion === "" ? t("commands.tcp.connected", { address }) : t("commands.tcp.connectedTls", { address, tls: tlsVersion });
  }
  return tlsVersion === ""
    ? t("commands.tcp.connectedFrom", { address, local })
    : t("commands.tcp.connectedFromTls", { address, local, tls: tlsVersion });
}

/** The system message for a state change. */
function stateMessage(ev: StateEventDTO): LogMessageInput {
  const time = eventTime(ev.at);
  switch (ev.state) {
    case "connecting":
      return { time, direction: "system", kind: "connecting", text: t("commands.tcp.connecting", { address: ev.address }) };
    case "open": {
      const tls = ev.tls ?? null;
      return {
        time,
        direction: "system",
        kind: "connected",
        text: connectedText(ev.remoteAddr || ev.address, ev.localAddr, tls?.version ?? ""),
        meta: tls
          ? {
              local: ev.localAddr,
              remote: ev.remoteAddr,
              tlsVersion: tls.version,
              cipherSuite: tls.cipherSuite,
              serverName: tls.serverName,
              serverCertificate: tls.peerSubject,
            }
          : { local: ev.localAddr, remote: ev.remoteAddr },
      };
    }
    case "closing":
      return { time, direction: "system", kind: "closing", text: t("commands.tcp.disconnecting") };
    default: {
      const close = ev.close;
      const meta = close
        ? {
            initiatedBy: close.initiatedBy,
            bytesSent: close.bytesSent,
            bytesReceived: close.bytesReceived,
            chunksSent: close.chunksSent,
            chunksReceived: close.chunksReceived,
            durationMs: close.durationMs,
          }
        : undefined;
      if (ev.error !== null) {
        const err = toAppError(ev.error);
        return {
          time,
          direction: "system",
          kind: "error",
          text: err.hint ? t("lib.errors.withHint", { message: err.message, hint: err.hint }) : err.message,
          meta: meta && { ...meta, code: err.code },
        };
      }
      const by = close?.initiatedBy === "remote" ? t("commands.tcp.closedByServer") : t("commands.tcp.disconnected");
      const text = close
        ? t("commands.tcp.closeTotals", {
            text: by,
            sent: t("commands.tcp.bytes", { count: close.bytesSent }),
            received: t("commands.tcp.bytes", { count: close.bytesReceived }),
          })
        : by;
      return { time, direction: "system", kind: "closed", text, meta };
    }
  }
}

/**
 * One tcp request's connection and log. The log is kept twice, as text and
 * as hex, so switching the view shows the whole history in the other form.
 */
export class TcpSession {
  readonly requestId: string;
  readonly textLog: MessageSource;
  readonly hexLog: MessageSource;
  private state: TcpSessionState = INITIAL;
  private readonly listeners = new Set<() => void>();
  /** Connect was called and has not resolved: events of unknown connections are kept for it. */
  private pending = false;
  private buffered: { state?: StateEventDTO; data?: DataEventDTO }[] = [];
  /** Disconnect was asked for while Connect had not resolved yet. */
  private closeWhenStarted = false;
  /** Streaming decoders, so a UTF-8 sequence split across chunks is decoded whole. */
  private decoders: Record<"sent" | "received", TextDecoder> = {
    sent: new TextDecoder(),
    received: new TextDecoder(),
  };

  constructor(requestId: string, options: { textLog?: MessageSource; hexLog?: MessageSource } = {}) {
    this.requestId = requestId;
    this.textLog = options.textLog ?? new MessageSource();
    this.hexLog = options.hexLog ?? new MessageSource();
  }

  getState = (): TcpSessionState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<TcpSessionState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  private append(message: LogMessageInput, hexMessage: LogMessageInput = message): void {
    this.textLog.append(message);
    this.hexLog.append(hexMessage);
  }

  /** Whether a connection is being made or is open. */
  get active(): boolean {
    return this.pending || this.state.status === "connecting" || this.state.status === "open" || this.state.status === "closing";
  }

  /**
   * Connects to `address` with `options` (timeout, TLS, framing). Resolves
   * once Go has started connecting; the outcome arrives as state changes.
   * Input Go refuses (an address, a framing, a CA bundle or client
   * certificate that cannot be loaded) is shown as the session's error and
   * in the log, and rejects.
   */
  async connect(address: string, options: TcpConnectOptions = {}): Promise<void> {
    if (this.active) return;
    this.pending = true;
    this.closeWhenStarted = false;
    this.buffered = [];
    this.decoders = { sent: new TextDecoder(), received: new TextDecoder() };
    const framed = options.framing != null && options.framing.mode !== "" && options.framing.mode !== "none";
    this.set({ ...INITIAL, view: this.state.view, status: "connecting", address, framed });
    try {
      const conn = await connectTcp(this.requestId, address, options);
      this.pending = false;
      this.set({ connId: conn.connId });
      const buffered = this.buffered;
      this.buffered = [];
      for (const ev of buffered) {
        if (ev.state?.connId === conn.connId) this.handleState(ev.state);
        if (ev.data?.connId === conn.connId) this.handleData(ev.data);
      }
      if (this.closeWhenStarted) await this.disconnect();
    } catch (reason: unknown) {
      this.pending = false;
      this.buffered = [];
      const error = toAppError(reason);
      this.set({ status: "idle", error });
      this.append({ direction: "system", kind: "error", text: error.hint ? `${error.message} ${error.hint}` : error.message });
      throw error;
    }
  }

  /** Sends the composer's text. Rejects with the AppError Go returned (e.g. invalid hex). */
  async send(mode: TcpSendMode, text: string, lineEnding: TcpLineEnding): Promise<SendResultDTO> {
    const connId = this.state.connId;
    if (connId === null || this.state.status !== "open") {
      // i18n-ignore: Go-style error text, localized by its code (toAppError, errors.ts)
      throw toAppError({ kind: "invalid_argument", code: "tcp.not_open", message: "The connection is not open." });
    }
    try {
      return await sendTcp(connId, mode, text, lineEnding);
    } catch (reason: unknown) {
      throw toAppError(reason);
    }
  }

  /** Closes the connection, or stops connecting. */
  async disconnect(): Promise<void> {
    if (this.pending) {
      this.closeWhenStarted = true;
      return;
    }
    const connId = this.state.connId;
    if (connId === null || !this.active) return;
    try {
      await closeTcp(connId);
    } catch (reason: unknown) {
      console.warn("tcp: closing the connection failed", toAppError(reason));
    }
  }

  setView(view: TcpView): void {
    if (this.state.view !== view) this.set({ view });
  }

  /** Empties both logs. */
  clear(): void {
    this.textLog.clear();
    this.hexLog.clear();
  }

  /** Applies a tcp:state event of this request. */
  handleState(ev: StateEventDTO): void {
    if (ev.connId !== this.state.connId) {
      if (this.pending && ev.requestId === this.requestId) this.buffered.push({ state: ev });
      return;
    }
    const patch: { -readonly [K in keyof TcpSessionState]?: TcpSessionState[K] } = { status: ev.state as TcpStatus };
    if (ev.localAddr !== "") patch.localAddr = ev.localAddr;
    if (ev.remoteAddr !== "") patch.remoteAddr = ev.remoteAddr;
    if (ev.tls) patch.tls = ev.tls;
    if (ev.state === "closed") {
      patch.close = ev.close;
      patch.error = ev.error === null ? null : toAppError(ev.error);
      if (ev.close) {
        patch.bytesSent = ev.close.bytesSent;
        patch.bytesReceived = ev.close.bytesReceived;
      }
    }
    this.set(patch);
    this.append(stateMessage(ev));
  }

  /** Applies a tcp:data event of this request. */
  handleData(ev: DataEventDTO): void {
    if (ev.connId !== this.state.connId) {
      if (this.pending && ev.requestId === this.requestId) this.buffered.push({ data: ev });
      return;
    }
    let sent = this.state.bytesSent;
    let received = this.state.bytesReceived;
    const text: LogMessageInput[] = [];
    const hex: LogMessageInput[] = [];
    for (const chunk of ev.chunks ?? []) {
      const direction = chunk.direction === "sent" ? "sent" : "received";
      const bytes = decodeBase64(chunk.data);
      const time = eventTime(chunk.at);
      if (direction === "sent") sent += bytes.length;
      else received += bytes.length;
      // A framed chunk is a whole message: decoded on its own. Raw chunks
      // share a streaming decoder, so a character split across reads is
      // decoded whole.
      const decoded = this.state.framed ? new TextDecoder().decode(bytes) : this.decoders[direction].decode(bytes, { stream: true });
      const badge = chunk.incomplete ? "incomplete" : undefined;
      text.push({ time, direction, kind: "text", badge, text: decoded, size: bytes.length });
      hex.push({ time, direction, kind: "binary", badge, data: bytes });
    }
    this.textLog.append(text);
    this.hexLog.append(hex);
    this.set({ bytesSent: sent, bytesReceived: received });
  }

  /** Disconnects and releases the logs. */
  dispose(): void {
    void this.disconnect();
    this.listeners.clear();
    this.textLog.dispose();
    this.hexLog.dispose();
  }
}

/** The sessions of every open tcp request, following the tcp events. */
export class TcpSessions {
  private readonly sessions = new Map<string, TcpSession>();
  private readonly unsubscribe: () => void;

  constructor() {
    const offState = onTcpState((ev) => this.sessions.get(ev.requestId)?.handleState(ev));
    const offData = onTcpData((ev) => this.sessions.get(ev.requestId)?.handleData(ev));
    // Closing a request's tab disconnects it and drops its log.
    const offTabs = onTabClosed((ids) => this.drop(ids));
    this.unsubscribe = () => {
      offState();
      offData();
      offTabs();
    };
  }

  /** The session of request `requestId`, created on first use. */
  get(requestId: string): TcpSession {
    let session = this.sessions.get(requestId);
    if (session === undefined) {
      session = new TcpSession(requestId);
      this.sessions.set(requestId, session);
    }
    return session;
  }

  /** The session of `requestId` if it has one. */
  peek(requestId: string): TcpSession | undefined {
    return this.sessions.get(requestId);
  }

  /** Disconnects and forgets the sessions of these requests. */
  drop(requestIds: readonly string[]): void {
    for (const id of requestIds) {
      const session = this.sessions.get(id);
      if (session === undefined) continue;
      this.sessions.delete(id);
      session.dispose();
    }
  }

  /** Disconnects every session and stops following events. */
  dispose(): void {
    this.drop([...this.sessions.keys()]);
    this.unsubscribe();
  }
}

let registry: TcpSessions | null = null;

/** The app's TCP sessions, created (and subscribed to events) on first use. */
export function tcpSessions(): TcpSessions {
  registry ??= new TcpSessions();
  return registry;
}

/** Drops the registry, for tests. */
export function resetTcpSessions(): void {
  registry?.dispose();
  registry = null;
}
