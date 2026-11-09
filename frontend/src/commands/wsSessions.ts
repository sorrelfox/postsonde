// The WebSocket connections of open websocket requests (WS-01, WS-03,
// WS-05, WS-08, WS-09, WS-10): one session per request, holding its connection state and
// its message log. Sessions live outside React, so switching tabs (which
// unmounts the editor) keeps the connection and its log; closing the tab
// disconnects and drops them. Go renders {{variables}} on connect and on
// every send; undefined ones are confirmed first, as for an HTTP send
// (WS-11, ENV-11). Pings and pongs (with the round trip) and Go's
// auto-reconnect decisions are logged too. See docs/websocket.md.
import { t, type MessageKey } from "../i18n";
import { toAppError, type AppError } from "../lib/errors";
import { formatUnit } from "../lib/units";
import { UNRESOLVED_CODE } from "../lib/http";
import { MessageSource, type LogMessageInput } from "../lib/messageSource";
import { decodeBase64 } from "../lib/tcp";
import {
  closeWs,
  connectWs,
  onWsMessage,
  onWsState,
  pingWs,
  sendWs,
  WS_NORMAL_CLOSURE,
  type CloseInfoDTO,
  type ConnectInputDTO,
  type ConnectionDTO,
  type HandshakeDTO,
  type MessageDTO,
  type MessageEventDTO,
  type PingResultDTO,
  type ReconnectDTO,
  type SendResultDTO,
  type StateEventDTO,
  type WsMessageFormat,
} from "../lib/websocket";
import { confirmUnresolved } from "./httpSends";
import { streamEnded, streamRef, streamStarted } from "./streamTests";
import { onTabClosed } from "./tabs";

/** The undefined names of a template.unresolved error (details.names), or null for another error. */
function unresolvedNamesOf(err: AppError): string[] | null {
  if (err.code !== UNRESOLVED_CODE) return null;
  return (err.details.names ?? "").split(",").filter((n) => n !== "");
}

/** Thrown inside connect when the user declined to connect with undefined variables. */
const DECLINED = Symbol("declined");

/**
 * "idle" before the first connect; then the states Go reports, plus
 * "reconnecting" while Go waits to connect again (WS-10).
 */
export type WsStatus = "idle" | "connecting" | "open" | "closing" | "closed" | "reconnecting";

export interface WsSessionState {
  readonly status: WsStatus;
  /** The current (or last) connection, null before the first connect. */
  readonly connId: string | null;
  /** The URL of the current (or last) connection. */
  readonly url: string;
  /** The handshake of the current (or last) connection, once known. */
  readonly handshake: HandshakeDTO | null;
  /** Why the last connection failed, or why connecting could not start. */
  readonly error: AppError | null;
  /** How the last connection ended. */
  readonly close: CloseInfoDTO | null;
  readonly messagesSent: number;
  readonly messagesReceived: number;
  /** The connection attempt of the current connection, 1 for the first (WS-10). */
  readonly attempt: number;
  /** Go's auto-reconnect decision after the last attempt ended, or null. */
  readonly reconnect: ReconnectDTO | null;
  /** The round trip of the last answered ping, in ms, or null (WS-09). */
  readonly lastRttMs: number | null;
}

const INITIAL: WsSessionState = {
  status: "idle",
  connId: null,
  url: "",
  handshake: null,
  error: null,
  close: null,
  messagesSent: 0,
  messagesReceived: 0,
  attempt: 0,
  reconnect: null,
  lastRttMs: null,
};

/** Log kinds of pings and pongs, and of reconnect notes: the log can hide them. */
export const WS_PING_KINDS: readonly string[] = ["ping", "pong"];
export const WS_RECONNECT_KINDS: readonly string[] = ["reconnect"];

/** "1.5 s" or "250 ms". */
export function formatDelay(ms: number): string {
  if (ms >= 1000) {
    const s = ms / 1000;
    return formatUnit("s", s, Number.isInteger(s) ? 0 : 1);
  }
  return formatUnit("ms", ms);
}

/** "12.3 ms", a round trip. */
export function formatRtt(ms: number): string {
  return ms < 10 ? formatUnit("ms", ms, 2) : ms < 100 ? formatUnit("ms", ms, 1) : formatUnit("ms", Math.round(ms));
}

/** "attempt 2 of 5", or "attempt 2" without a limit. */
function attemptText(r: ReconnectDTO): string {
  return r.maxAttempts > 0
    ? t("commands.ws.attemptOf", { attempt: r.attempt, max: r.maxAttempts })
    : t("commands.ws.attempt", { attempt: r.attempt });
}

/** What the log and the status line say about an auto-reconnect decision. */
export function reconnectText(r: ReconnectDTO): string {
  switch (r.stopped) {
    case "":
      return t("commands.ws.reconnecting", { delay: formatDelay(r.delayMs), attempt: attemptText(r) });
    case "manual":
      return t("commands.ws.reconnectCancelled");
    case "max_attempts":
      return t("commands.ws.gaveUp", { count: r.attempt });
    case "close_code":
      return t("commands.ws.notReconnectingCloseCode");
    default:
      return t("commands.ws.notReconnectingError");
  }
}

function eventTime(at: string): number | undefined {
  const ms = Date.parse(at);
  return Number.isNaN(ms) ? undefined : ms;
}

/** "1000 Normal closure “bye”", as close info is shown. */
export function closeCodeText(code: number, reason: string): string {
  const key = CLOSE_CODE_NAMES[code];
  const named = key !== undefined ? t("commands.ws.closeCodeNamed", { code, name: t(key) }) : String(code);
  return reason ? t("commands.ws.closeReason", { close: named, reason }) : named;
}

const CLOSE_CODE_NAMES: Readonly<Record<number, MessageKey>> = {
  1000: "commands.ws.close.1000",
  1001: "commands.ws.close.1001",
  1002: "commands.ws.close.1002",
  1003: "commands.ws.close.1003",
  1005: "commands.ws.close.1005",
  1006: "commands.ws.close.1006",
  1007: "commands.ws.close.1007",
  1008: "commands.ws.close.1008",
  1009: "commands.ws.close.1009",
  1010: "commands.ws.close.1010",
  1011: "commands.ws.close.1011",
  1012: "commands.ws.close.1012",
  1013: "commands.ws.close.1013",
  1014: "commands.ws.close.1014",
};

function errorText(err: AppError): string {
  return err.hint ? t("lib.errors.withHint", { message: err.message, hint: err.hint }) : err.message;
}

/** The system message for a state change. */
function stateMessage(ev: StateEventDTO): LogMessageInput {
  const time = eventTime(ev.at);
  switch (ev.state) {
    case "connecting":
      return {
        time,
        direction: "system",
        kind: "connecting",
        text: ev.attempt > 1 ? t("commands.ws.reconnectingTo", { url: ev.url, attempt: ev.attempt }) : t("commands.ws.connecting", { url: ev.url }),
        ...(ev.attempt > 1 ? { meta: { attempt: ev.attempt } } : {}),
      };
    case "open": {
      const hs = ev.handshake;
      const meta: Record<string, string | number> = {};
      if (hs !== null) {
        meta.status = hs.response?.status ?? "";
        if (hs.subprotocol !== "") meta.subprotocol = hs.subprotocol;
        if (hs.remoteAddr !== "") meta.remote = hs.remoteAddr;
        if (hs.tlsVersion !== "") meta.tls = hs.tlsVersion;
        meta.durationMs = hs.durationMs;
      }
      const text =
        hs !== null && hs.subprotocol !== ""
          ? t("commands.ws.connectedSubprotocol", { url: ev.url, subprotocol: hs.subprotocol })
          : t("commands.ws.connected", { url: ev.url });
      return { time, direction: "system", kind: "connected", text, meta };
    }
    case "closing":
      return { time, direction: "system", kind: "closing", text: t("commands.ws.closing") };
    default: {
      const close = ev.close;
      const meta: Record<string, string | number | boolean> = {};
      if (close !== null) {
        meta.code = close.code;
        meta.reason = close.reason;
        meta.wasClean = close.wasClean;
        meta.initiatedBy = close.initiatedBy;
        meta.messagesSent = close.messagesSent;
        meta.messagesReceived = close.messagesReceived;
        meta.durationMs = close.durationMs;
      }
      const status = ev.handshake?.response?.status;
      if (ev.error !== null) {
        const err = toAppError(ev.error);
        return {
          time,
          direction: "system",
          kind: "error",
          text: errorText(err),
          meta: { ...meta, errorCode: err.code, ...(status ? { status } : {}) },
        };
      }
      if (close === null) return { time, direction: "system", kind: "close", text: t("commands.ws.closed"), meta };
      const by =
        close.initiatedBy === "remote"
          ? t("commands.ws.closedByServer", { close: closeCodeText(close.code, close.reason) })
          : t("commands.ws.disconnected", { close: closeCodeText(close.sent?.code ?? close.code, close.sent?.reason ?? close.reason) });
      const text = t("commands.ws.closeTotals", {
        text: by,
        sent: t("commands.ws.messages", { count: close.messagesSent }),
        received: t("commands.ws.messages", { count: close.messagesReceived }),
      });
      return { time, direction: "system", kind: "close", text, meta };
    }
  }
}

/** The log entry of an auto-reconnect decision, or null when there is nothing to add. */
function reconnectMessage(ev: StateEventDTO): LogMessageInput | null {
  const r = ev.reconnect;
  if (r === null || r === undefined) return null;
  // A disconnect of an open connection is already logged as such.
  if (r.stopped === "manual" && ev.close !== null) return null;
  const meta: Record<string, string | number> = { attempt: r.attempt, maxAttempts: r.maxAttempts };
  if (r.stopped === "") meta.delayMs = r.delayMs;
  else meta.stopped = r.stopped;
  return { time: eventTime(ev.at), direction: "system", kind: "reconnect", text: reconnectText(r), meta };
}

/** A ping or pong as a log entry (WS-09): its payload as text, the round trip as a badge. */
function pingPongMessage(m: MessageDTO): LogMessageInput {
  const direction = m.direction === "sent" ? "sent" : "received";
  const rtt = m.rttMs ?? null;
  const badges = [m.keepalive ? "keepalive" : "", rtt !== null ? formatRtt(rtt) : ""].filter((b) => b !== "");
  const meta: Record<string, string | number | boolean> = {};
  if (m.keepalive) meta.keepalive = true;
  if (rtt !== null) meta.rttMs = rtt;
  // Go sets text only for a UTF-8 payload; other payloads are shown as bytes.
  const utf8 = m.text !== "" || m.size === 0;
  return {
    time: eventTime(m.at),
    direction,
    kind: m.opcode,
    ...(badges.length > 0 ? { badge: badges.join(" · ") } : {}),
    ...(utf8 ? { text: m.text } : { data: decodeBase64(m.data) }),
    size: m.size,
    meta,
  };
}

/** A message of ws:message as a log entry: text as text, binary as bytes (shown as hex). */
function logMessage(m: MessageDTO): LogMessageInput {
  if (m.opcode === "ping" || m.opcode === "pong") return pingPongMessage(m);
  const direction = m.direction === "sent" ? "sent" : "received";
  const time = eventTime(m.at);
  if (m.opcode === "binary") {
    return { time, direction, kind: "binary", data: decodeBase64(m.data), size: m.size };
  }
  return { time, direction, kind: "text", text: m.text, size: m.size };
}

/** One websocket request's connection and log. */
export class WsSession {
  readonly requestId: string;
  readonly log: MessageSource;
  private state: WsSessionState = INITIAL;
  private readonly listeners = new Set<() => void>();
  /** Connect was called and has not resolved: events of unknown connections are kept for it. */
  private pending = false;
  private buffered: { state?: StateEventDTO; message?: MessageEventDTO }[] = [];
  /** Disconnect was asked for while Connect had not resolved yet, with this code and reason. */
  private closeWhenStarted: { code: number; reason: string } | null = null;

  constructor(requestId: string, options: { log?: MessageSource } = {}) {
    this.requestId = requestId;
    this.log = options.log ?? new MessageSource();
  }

  getState = (): WsSessionState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<WsSessionState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  /** Whether a connection is being made, is open or is closing. */
  get active(): boolean {
    const s = this.state.status;
    return this.pending || s === "connecting" || s === "open" || s === "closing" || s === "reconnecting";
  }

  /**
   * Connects with a request's handshake options. Resolves once Go has
   * started connecting; the outcome arrives as state changes. Input Go
   * refuses is shown as the session's error and in the log, and rejects.
   * Undefined {{variables}} are confirmed first ("Connect anyway" connects
   * with them as written); declining puts the session back as it was and
   * resolves.
   */
  async connect(input: ConnectInputDTO): Promise<void> {
    if (this.active) return;
    const before = this.state;
    this.pending = true;
    this.closeWhenStarted = null;
    this.buffered = [];
    this.set({ ...INITIAL, status: "connecting", url: input.url });
    try {
      streamStarted(this.requestId, "websocket");
      const conn = await this.start(input);
      this.pending = false;
      this.set({ connId: conn.connId, url: conn.url });
      streamRef(this.requestId, conn.connId);
      const buffered = this.buffered;
      this.buffered = [];
      for (const ev of buffered) {
        if (ev.state?.connId === conn.connId) this.handleState(ev.state);
        if (ev.message?.connId === conn.connId) this.handleMessages(ev.message);
      }
      const close = this.closeWhenStarted as { code: number; reason: string } | null;
      if (close !== null) await this.disconnect(close.code, close.reason);
    } catch (reason: unknown) {
      this.pending = false;
      this.buffered = [];
      if (reason === DECLINED) {
        this.set(before);
        return;
      }
      const error = toAppError(reason);
      this.set({ status: "idle", error });
      this.log.append({ direction: "system", kind: "error", text: errorText(error) });
      throw error;
    }
  }

  /** Connect, asking whether to go ahead when {{variables}} are undefined. */
  private async start(input: ConnectInputDTO): Promise<ConnectionDTO> {
    try {
      return await connectWs(input);
    } catch (reason: unknown) {
      const names = unresolvedNamesOf(toAppError(reason));
      if (names === null) throw reason;
      if (!(await confirmUnresolved(names, "connect"))) throw DECLINED;
      return await connectWs(input, true);
    }
  }

  /**
   * Sends one message: text and JSON as a text message whose
   * {{variables}} Go renders, hex and Base64 as the binary message they
   * spell (WS-04). Undefined variables are confirmed first ("Send anyway"
   * sends them as written); declining resolves null without sending.
   * Rejects with the AppError Go returned.
   */
  async send(text: string, format: WsMessageFormat = "text"): Promise<SendResultDTO | null> {
    const connId = this.state.connId;
    if (connId === null || this.state.status !== "open") {
      // i18n-ignore: Go-style error text, localized by its code (toAppError, errors.ts)
      throw toAppError({ kind: "invalid_argument", code: "ws.not_open", message: "The connection is not open." });
    }
    try {
      return await sendWs(connId, text, format);
    } catch (reason: unknown) {
      const error = toAppError(reason);
      const names = unresolvedNamesOf(error);
      if (names === null) throw error;
      if (!(await confirmUnresolved(names))) return null;
    }
    try {
      return await sendWs(connId, text, format, true);
    } catch (reason: unknown) {
      throw toAppError(reason);
    }
  }

  /**
   * Sends a ping with `payload` as text (WS-09); the ping and its pong
   * arrive in the log. Rejects with the AppError Go returned
   * (ws.ping_too_long, ws.not_open…).
   */
  async ping(payload = ""): Promise<PingResultDTO> {
    const connId = this.state.connId;
    if (connId === null || this.state.status !== "open") {
      // i18n-ignore: Go-style error text, localized by its code (toAppError, errors.ts)
      throw toAppError({ kind: "invalid_argument", code: "ws.not_open", message: "The connection is not open." });
    }
    try {
      return await pingWs(connId, payload);
    } catch (reason: unknown) {
      throw toAppError(reason);
    }
  }

  /**
   * Closes the connection with `code` and `reason` (a close code or reason
   * Go refuses rejects), stops connecting, or stops waiting to reconnect.
   */
  async disconnect(code: number = WS_NORMAL_CLOSURE, reason = ""): Promise<void> {
    if (this.pending) {
      this.closeWhenStarted = { code, reason };
      return;
    }
    const connId = this.state.connId;
    if (connId === null || !this.active) return;
    try {
      await closeWs(connId, code, reason);
    } catch (err: unknown) {
      const error = toAppError(err);
      if (error.kind === "invalid_argument") throw error;
      console.warn("ws: closing the connection failed", error);
    }
  }

  /** Applies a ws:state event of this request. */
  handleState(ev: StateEventDTO): void {
    if (ev.connId !== this.state.connId) {
      if (this.pending && ev.requestId === this.requestId) this.buffered.push({ state: ev });
      return;
    }
    const patch: { -readonly [K in keyof WsSessionState]?: WsSessionState[K] } = { status: ev.state as WsStatus };
    if (ev.attempt > 0) patch.attempt = ev.attempt;
    if (ev.handshake !== null) patch.handshake = ev.handshake;
    if (ev.state === "connecting") {
      patch.url = ev.url;
      patch.reconnect = null;
      if (ev.attempt > 1) {
        patch.error = null;
        patch.handshake = null;
      }
    }
    if (ev.state === "closed") {
      streamEnded(this.requestId, ev.tests);
      const reconnect = ev.reconnect ?? null;
      patch.reconnect = reconnect;
      if (reconnect !== null && reconnect.stopped === "") patch.status = "reconnecting";
      // A cancelled reconnect has no close info of its own: keep the last one.
      if (ev.close !== null || reconnect === null) {
        patch.close = ev.close;
        patch.error = ev.error === null ? null : toAppError(ev.error);
      }
      if (ev.close) {
        patch.messagesSent = ev.close.messagesSent;
        patch.messagesReceived = ev.close.messagesReceived;
      }
    }
    this.set(patch);
    // The cancel event of a reconnect only adds its note.
    if (!(ev.state === "closed" && ev.close === null && ev.reconnect)) this.log.append(stateMessage(ev));
    const note = reconnectMessage(ev);
    if (note !== null) this.log.append(note);
  }

  /** Applies a ws:message event of this request. */
  handleMessages(ev: MessageEventDTO): void {
    if (ev.connId !== this.state.connId) {
      if (this.pending && ev.requestId === this.requestId) this.buffered.push({ message: ev });
      return;
    }
    const messages = ev.messages ?? [];
    let sent = this.state.messagesSent;
    let received = this.state.messagesReceived;
    let rtt = this.state.lastRttMs;
    for (const m of messages) {
      if (m.opcode === "ping" || m.opcode === "pong") {
        if (m.rttMs !== undefined && m.rttMs !== null) rtt = m.rttMs;
        continue;
      }
      if (m.direction === "sent") sent++;
      else received++;
    }
    this.log.append(messages.map(logMessage));
    this.set({ messagesSent: sent, messagesReceived: received, lastRttMs: rtt });
  }

  /** Disconnects and releases the log. */
  dispose(): void {
    void this.disconnect(1001).catch(() => {});
    this.listeners.clear();
    this.log.dispose();
  }
}

/** The sessions of every open websocket request, following the ws events. */
export class WsSessions {
  private readonly sessions = new Map<string, WsSession>();
  private readonly unsubscribe: () => void;

  constructor() {
    const offState = onWsState((ev) => this.sessions.get(ev.requestId)?.handleState(ev));
    const offMessage = onWsMessage((ev) => this.sessions.get(ev.requestId)?.handleMessages(ev));
    // Closing a request's tab disconnects it and drops its log.
    const offTabs = onTabClosed((ids) => this.drop(ids));
    this.unsubscribe = () => {
      offState();
      offMessage();
      offTabs();
    };
  }

  /** The session of request `requestId`, created on first use. */
  get(requestId: string): WsSession {
    let session = this.sessions.get(requestId);
    if (session === undefined) {
      session = new WsSession(requestId);
      this.sessions.set(requestId, session);
    }
    return session;
  }

  /** The session of `requestId` if it has one. */
  peek(requestId: string): WsSession | undefined {
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

let registry: WsSessions | null = null;

/** The app's WebSocket sessions, created (and subscribed to events) on first use. */
export function wsSessions(): WsSessions {
  registry ??= new WsSessions();
  return registry;
}

/** Drops the registry, for tests. */
export function resetWsSessions(): void {
  registry?.dispose();
  registry = null;
}
