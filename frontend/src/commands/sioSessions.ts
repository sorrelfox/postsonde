// The Socket.IO connections of open socketio requests (SIO-01, SIO-03…
// SIO-07): one session per request, holding its connection state and its
// message log. A connection runs as a sio operation (docs/events.md);
// sessions live outside React, so switching tabs keeps the connection and
// its log, and closing the tab disconnects and drops them. Go renders
// {{variables}} on connect and on every emit; undefined ones are confirmed
// first, as for an HTTP send (ENV-11). See docs/socketio.md.
import { t, type MessageKey } from "../i18n";
import { toAppError, type AppError } from "../lib/errors";
import { formatUnit } from "../lib/units";
import { UNRESOLVED_CODE } from "../lib/http";
import { MessageSource, type LogMessageInput, type MessageMetaValue } from "../lib/messageSource";
import { startOperation, type OperationHandle, type StartedDTO } from "../lib/operations";
import { connectSio, emitSio, SIO_DOMAIN, type ConnectInputDTO, type EmitResultDTO, type ItemDTO } from "../lib/socketio";
import { confirmUnresolved } from "./httpSends";
import { streamEnded, streamRef, streamStarted } from "./streamTests";
import { onTabClosed } from "./tabs";

/** "idle" before the first connect; "connecting" until the namespace accepted it; "open"; then "closed". */
export type SioStatus = "idle" | "connecting" | "open" | "closed";

export interface SioSessionState {
  readonly status: SioStatus;
  /** The current (or last) connection's operation, null before Go returned it. */
  readonly opId: string | null;
  /** The server connected to (scheme, host and path), once the handshake is done. */
  readonly url: string;
  /** "websocket" or "polling", once known. */
  readonly transport: string;
  readonly namespace: string;
  /** The namespace's session ID, once connected. */
  readonly socketId: string;
  /** Why the last connection failed, or why connecting could not start. */
  readonly error: AppError | null;
  /** Why the last connection ended ("io client disconnect", "ping timeout", …); "" while it runs. */
  readonly reason: string;
  readonly emitted: number;
  readonly received: number;
  /** Received events left out of the log by the listen filter (SIO-04). */
  readonly ignored: number;
}

const INITIAL: SioSessionState = {
  status: "idle",
  opId: null,
  url: "",
  transport: "",
  namespace: "",
  socketId: "",
  error: null,
  reason: "",
  emitted: 0,
  received: 0,
  ignored: 0,
};

function itemTime(at: string): number | undefined {
  const ms = Date.parse(at);
  return Number.isNaN(ms) ? undefined : ms;
}

function errorText(err: AppError): string {
  return err.hint ? t("lib.errors.withHint", { message: err.message, hint: err.hint }) : err.message;
}

const TRANSPORT_NAMES: Readonly<Record<string, MessageKey>> = {
  websocket: "commands.sio.transport.websocket",
  polling: "commands.sio.transport.polling",
};

/** "WebSocket" or "long-polling". */
export function transportName(transport: string): string {
  const key = TRANSPORT_NAMES[transport];
  return key === undefined ? transport : t(key);
}

/** The reasons Go reports (socket.io-client's), in words. */
const REASONS: Readonly<Record<string, MessageKey>> = {
  "io client disconnect": "commands.sio.reason.clientDisconnect",
  "io server disconnect": "commands.sio.reason.serverDisconnect",
  "transport close": "commands.sio.reason.transportClose",
  "transport error": "commands.sio.reason.transportError",
  "ping timeout": "commands.sio.reason.pingTimeout",
  "parse error": "commands.sio.reason.parseError",
  "connect failed": "commands.sio.reason.connectFailed",
};

/** A reason Go reports, in words. */
export function reasonText(reason: string): string {
  const key = REASONS[reason];
  if (key !== undefined) return t(key);
  return reason === "" ? t("commands.sio.closed") : reason;
}

/** A connection item as a message of the log. */
export function sioLogMessage(it: ItemDTO): LogMessageInput {
  const time = itemTime(it.at);
  const direction = it.direction === "sent" ? "sent" : it.direction === "received" ? "received" : "system";
  const err = it.error !== null ? toAppError(it.error) : null;
  switch (it.type) {
    case "open": {
      const meta: Record<string, MessageMetaValue> = {
        transport: it.transport,
        sid: it.sid,
        pingInterval: formatUnit("ms", it.pingIntervalMs),
        pingTimeout: formatUnit("ms", it.pingTimeoutMs),
      };
      if (it.maxPayload > 0) meta.maxPayload = it.maxPayload;
      if ((it.upgrades ?? []).length > 0) meta.upgrades = (it.upgrades ?? []).join(", ");
      return { time, direction: "system", kind: "open", badge: it.transport, text: t("commands.sio.handshake", { url: it.url, transport: transportName(it.transport) }), meta };
    }
    case "fallback":
      return { time, direction: "system", kind: "fallback", text: err ? t("commands.sio.fallbackError", { error: errorText(err) }) : t("commands.sio.fallback"), meta: err ? { errorCode: err.code } : undefined };
    case "upgrade":
      return { time, direction: "system", kind: "upgrade", text: t("commands.sio.upgraded") };
    case "upgrade_failed":
      return { time, direction: "system", kind: "upgrade", text: err ? t("commands.sio.upgradeFailedError", { error: errorText(err) }) : t("commands.sio.upgradeFailed"), meta: err ? { errorCode: err.code } : undefined };
    case "connected":
      return {
        time,
        direction: "system",
        kind: "connected",
        text: t("commands.sio.connected", { namespace: it.namespace, transport: transportName(it.transport) }),
        meta: { namespace: it.namespace, socketId: it.socketId, transport: it.transport },
      };
    case "event": {
      const meta: Record<string, MessageMetaValue> = { event: it.event, namespace: it.namespace };
      if (it.ackId !== null) meta.ackId = it.ackId;
      if (it.attachments > 0) meta.attachments = it.attachments;
      return { time, direction, kind: "event", badge: it.event, text: it.args, size: it.size, meta };
    }
    case "ack": {
      const meta: Record<string, MessageMetaValue> = { ackId: it.ackId ?? 0, namespace: it.namespace };
      if (it.event !== "") {
        meta.event = it.event;
        meta.roundTrip = formatUnit("ms", it.elapsedMs);
      }
      if (it.attachments > 0) meta.attachments = it.attachments;
      return { time, direction, kind: "ack", badge: it.event !== "" ? `${it.event} #${it.ackId ?? 0}` : `#${it.ackId ?? 0}`, text: it.args, size: it.size, meta };
    }
    case "packet":
      return {
        time,
        direction,
        kind: it.packetType.toLowerCase(),
        badge: it.namespace,
        text: it.data,
        size: it.size,
        meta: { packet: it.packetType, namespace: it.namespace },
      };
    case "ping":
    case "pong":
      return { time, direction, kind: it.type };
    case "ack_timeout":
      return {
        time,
        direction: "system",
        kind: "error",
        badge: t("commands.sio.ackTimeout"),
        text: t("commands.sio.ackTimeoutText", { event: it.event, id: it.ackId ?? 0, ms: it.elapsedMs }),
        meta: { event: it.event, ackId: it.ackId ?? 0 },
      };
    default: {
      if (err !== null) {
        return { time, direction: "system", kind: "error", text: errorText(err), meta: { reason: it.reason, errorCode: err.code } };
      }
      return { time, direction: "system", kind: "closed", text: reasonText(it.reason), meta: { reason: it.reason } };
    }
  }
}

/** The event names of a listen filter: trimmed, without blanks and repeats. */
export function listenNames(list: readonly string[] | null | undefined): string[] {
  const out: string[] = [];
  for (const raw of list ?? []) {
    const name = raw.trim();
    if (name !== "" && !out.includes(name)) out.push(name);
  }
  return out;
}

function unresolvedNames(err: AppError): string[] | null {
  if (err.code !== UNRESOLVED_CODE) return null;
  return (err.details.names ?? "").split(",").filter((n) => n !== "");
}

/** One socketio request's connection and log. */
export class SioSession {
  readonly requestId: string;
  readonly log: MessageSource;
  private state: SioSessionState = INITIAL;
  private readonly listeners = new Set<() => void>();
  private handle: OperationHandle | null = null;
  /** Bumped by every connect, so callbacks of an older connection do nothing. */
  private generation = 0;
  private listen: ReadonlySet<string> = new Set();

  constructor(requestId: string, options: { log?: MessageSource } = {}) {
    this.requestId = requestId;
    this.log = options.log ?? new MessageSource();
  }

  getState = (): SioSessionState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<SioSessionState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  /** Whether a connection is being made or is open. */
  get active(): boolean {
    return this.state.status === "connecting" || this.state.status === "open";
  }

  /** The events shown in the log (SIO-04); none shows every event. Applies to events received from now on. */
  setListen(names: readonly string[] | null | undefined): void {
    this.listen = new Set(listenNames(names));
  }

  /**
   * Connects with a request's handshake options, unless connected. The
   * outcome arrives as items; input Go refuses is shown as the session's
   * error. Undefined {{variables}} are confirmed first ("Connect anyway");
   * declining puts the session back as it was.
   */
  connect(input: ConnectInputDTO): void {
    if (this.active) return;
    const generation = ++this.generation;
    const mine = () => this.generation === generation;
    const before = this.state;
    this.set({ ...INITIAL, status: "connecting", url: input.url });
    let declined = false;
    const start = async (): Promise<StartedDTO> => {
      try {
        return await connectSio(input);
      } catch (reason: unknown) {
        const names = unresolvedNames(toAppError(reason));
        if (names === null || !mine()) throw reason;
        if (!(await confirmUnresolved(names, "connect")) || !mine()) {
          declined = true;
          throw reason;
        }
        return await connectSio(input, true);
      }
    };
    streamStarted(this.requestId, "socketio");
    const handle = startOperation<ItemDTO>(SIO_DOMAIN, start, {
      onStarted: (opId) => {
        if (!mine()) return;
        this.set({ opId });
        streamRef(this.requestId, opId);
      },
      onData: (items) => {
        if (mine()) this.handleItems(items);
      },
      onDone: (result) => {
        if (!mine()) return;
        this.handle = null;
        if (declined) {
          this.set(before);
          return;
        }
        if (result.opId === null && result.error !== null) {
          // Refused before anything ran: no closed item comes.
          this.log.append({ direction: "system", kind: "error", text: errorText(result.error) });
        }
        this.set({ status: "closed", error: result.error });
      },
    });
    this.handle = handle;
  }

  /** Applies items of the running connection. */
  handleItems(items: readonly ItemDTO[]): void {
    const patch: { -readonly [K in keyof SioSessionState]?: SioSessionState[K] } = {};
    let { emitted, received, ignored } = this.state;
    const shown: LogMessageInput[] = [];
    for (const it of items) {
      switch (it.type) {
        case "open":
          patch.url = it.url;
          patch.transport = it.transport;
          break;
        case "upgrade":
          patch.transport = it.transport;
          break;
        case "connected":
          patch.status = "open";
          patch.socketId = it.socketId;
          patch.namespace = it.namespace;
          patch.transport = it.transport;
          break;
        case "event":
          if (it.direction === "sent") emitted++;
          else {
            received++;
            if (this.listen.size > 0 && !this.listen.has(it.event)) {
              ignored++;
              continue;
            }
          }
          break;
        case "closed":
          patch.reason = it.reason;
          streamEnded(this.requestId, it.tests);
          break;
      }
      shown.push(sioLogMessage(it));
    }
    this.log.append(shown);
    this.set({ ...patch, emitted, received, ignored });
  }

  /**
   * Emits one event (SIO-03), asking for an ack with `ack` (SIO-05).
   * Undefined {{variables}} are confirmed first ("Send anyway"); declining
   * resolves null without sending. Rejects with the AppError Go returned.
   */
  async emit(event: string, args: string, ack: boolean): Promise<EmitResultDTO | null> {
    const opId = this.state.opId;
    if (opId === null || this.state.status !== "open") {
      // i18n-ignore: Go-style error text, localized by its code (toAppError, errors.ts)
      throw toAppError({ kind: "invalid_argument", code: "sioclient.not_open", message: "The connection is not open." });
    }
    try {
      return await emitSio(opId, event, args, ack);
    } catch (reason: unknown) {
      const error = toAppError(reason);
      const names = unresolvedNames(error);
      if (names === null) throw error;
      if (!(await confirmUnresolved(names))) return null;
    }
    try {
      return await emitSio(opId, event, args, ack, true);
    } catch (reason: unknown) {
      throw toAppError(reason);
    }
  }

  /** Disconnects, or stops connecting. The state follows when Go has closed it. */
  disconnect(): void {
    this.handle?.cancel();
  }

  /** Disconnects and releases the log. */
  dispose(): void {
    this.generation++;
    const handle = this.handle;
    this.handle = null;
    handle?.cancel();
    handle?.dispose();
    this.listeners.clear();
    this.log.dispose();
  }
}

/** The sessions of every open socketio request. */
export class SioSessions {
  private readonly sessions = new Map<string, SioSession>();
  private readonly unsubscribe: () => void;

  constructor() {
    // Closing a request's tab disconnects it and drops its log.
    this.unsubscribe = onTabClosed((ids) => this.drop(ids));
  }

  /** The session of request `requestId`, created on first use. */
  get(requestId: string): SioSession {
    let session = this.sessions.get(requestId);
    if (session === undefined) {
      session = new SioSession(requestId);
      this.sessions.set(requestId, session);
    }
    return session;
  }

  /** The session of `requestId` if it has one. */
  peek(requestId: string): SioSession | undefined {
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

  /** Disconnects every session and stops following tab closes. */
  dispose(): void {
    this.drop([...this.sessions.keys()]);
    this.unsubscribe();
  }
}

let registry: SioSessions | null = null;

/** The app's Socket.IO sessions, created on first use. */
export function sioSessions(): SioSessions {
  registry ??= new SioSessions();
  return registry;
}

/** Drops the registry, for tests. */
export function resetSioSessions(): void {
  registry?.dispose();
  registry = null;
}
