// The event streams of open sse requests (SSE-01, SSE-02): one stream slot
// per request, holding the state of its latest connection and a message
// log of what it carried. Streams run as sse operations (docs/events.md).
// The slots live outside React, so switching tabs (which unmounts the
// editor and the response pane) keeps a running stream and its events;
// closing the tab stops the stream and drops them. See docs/sse.md.
import { t, type MessageKey } from "../i18n";
import { toAppError, type AppError } from "../lib/errors";
import { formatUnit } from "../lib/units";
import { UNRESOLVED_CODE } from "../lib/http";
import { MessageSource, type LogMessageInput, type MessageMetaValue } from "../lib/messageSource";
import { startOperation, type OperationHandle, type StartedDTO } from "../lib/operations";
import { connectSse, SSE_DOMAIN, type HeaderDTO, type StreamItemDTO } from "../lib/sse";
import type { RequestId, Stores } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { confirmUnresolved, type PrepareSend } from "./httpSends";
import { streamEnded, streamRef, streamStarted } from "./streamTests";
import { onTabClosed } from "./tabs";

/** "idle" before the first connect; "starting" until Go has accepted it; "running" while connected; then how it ended. */
export type SseStreamStatus = "idle" | "starting" | "running" | "ok" | "error" | "cancelled";

/** The response's status line and headers, once they are in. */
export interface SseResponseHead {
  readonly method: string;
  readonly url: string;
  readonly status: number;
  readonly statusText: string;
  readonly proto: string;
  readonly headers: readonly HeaderDTO[];
}

export interface SseStreamState {
  readonly status: SseStreamStatus;
  /** Why the stream failed or could not start; null otherwise. */
  readonly error: AppError | null;
  /** The response head, once received; null before (and for a stream that failed to connect). */
  readonly head: SseResponseHead | null;
  /** Events received so far. */
  readonly events: number;
  /** Bytes of event data received so far. */
  readonly dataBytes: number;
  /** The last event ID so far, "" for none. */
  readonly lastEventId: string;
  /** The reconnection time the server asked for, in ms; null when it set none. */
  readonly retryMs: number | null;
  /** When the connect started (Date.now()), for a live timer; null before the first. */
  readonly startedAt: number | null;
  /** How long the stream ran, once it ended. */
  readonly elapsedMs: number | null;
  /** Why the stream ended: "closed" by the server, "stopped", "error"; "" while it runs. */
  readonly endReason: string;
  /** While the stream waits to reconnect (SSE-03): which attempt, and when it is made; null otherwise. */
  readonly reconnect: SseReconnectWait | null;
  /** Reconnections made so far. */
  readonly reconnects: number;
}

/** A wait before a reconnection attempt (SSE-03). */
export interface SseReconnectWait {
  /** Attempts in a row that delivered nothing, from 1. */
  readonly attempt: number;
  /** The policy's limit; 0 for none. */
  readonly maxAttempts: number;
  readonly delayMs: number;
  /** When the attempt is made (epoch ms), for a countdown. */
  readonly until: number;
}

const INITIAL: SseStreamState = {
  status: "idle",
  error: null,
  head: null,
  events: 0,
  dataBytes: 0,
  lastEventId: "",
  retryMs: null,
  startedAt: null,
  elapsedMs: null,
  endReason: "",
  reconnect: null,
  reconnects: 0,
};

function itemTime(at: string): number | undefined {
  const ms = Date.parse(at);
  return Number.isNaN(ms) ? undefined : ms;
}

/** "2 of 5", or "2" when there is no limit. */
export function attemptText(attempt: number, maxAttempts: number): string {
  return maxAttempts > 0 ? t("commands.sse.attemptOf", { attempt, max: maxAttempts }) : String(attempt);
}

const END_TEXT: Readonly<Record<string, MessageKey>> = {
  closed: "commands.sse.end.closed",
  stopped: "commands.sse.end.stopped",
  error: "commands.sse.end.error",
};

/** A stream item as a message of the log (SSE-02: event, id, data and retry are shown). */
export function streamLogMessage(it: StreamItemDTO): LogMessageInput {
  const time = itemTime(it.at);
  switch (it.type) {
    case "open": {
      const headers = (it.headers ?? []).map((h) => `${h.name}: ${h.value}`).join("\n");
      return {
        time,
        direction: "system",
        kind: "open",
        badge: String(it.status),
        text: `${it.proto} ${it.statusText}\n${headers}`,
        meta: { method: it.method, url: it.url },
      };
    }
    case "event": {
      const meta: Record<string, MessageMetaValue> = { event: it.event, index: it.index };
      if (it.id !== "") meta.id = it.id;
      if (it.retryMs !== null) meta.retry = formatUnit("ms", it.retryMs);
      if (it.line > 0) meta.line = it.line;
      if (it.truncated) meta.shown = t("commands.sse.truncated", { shown: it.data.length, count: it.size });
      return { time, direction: "received", kind: "event", badge: it.event, text: it.data, size: it.size, meta };
    }
    case "comment":
      return { time, direction: "received", kind: "comment", text: it.text, meta: { line: it.line } };
    case "retry":
      return {
        time,
        direction: "received",
        kind: "retry",
        text: formatUnit("ms", it.retryMs ?? 0),
        meta: { retry: formatUnit("ms", it.retryMs ?? 0), line: it.line },
      };
    case "notice":
      return { time, direction: "system", kind: "notice", text: it.text };
    case "reconnect": {
      const meta: Record<string, MessageMetaValue> = { attempt: attemptText(it.attempt, it.maxAttempts), delay: formatUnit("ms", it.delayMs) };
      if (it.id !== "") meta["last event id"] = it.id;
      const attempt = attemptText(it.attempt, it.maxAttempts);
      return {
        time,
        direction: "system",
        kind: "reconnect",
        text:
          it.id !== ""
            ? t("commands.sse.reconnectingResume", { reason: it.text, ms: it.delayMs, id: it.id, attempt })
            : t("commands.sse.reconnecting", { reason: it.text, ms: it.delayMs, attempt }),
        meta,
      };
    }
    default: {
      const meta: Record<string, MessageMetaValue> = { events: it.events, bytes: it.bytes, elapsed: formatUnit("ms", it.elapsedMs) };
      if (it.id !== "") meta["last event id"] = it.id;
      if (it.retryMs !== null) meta.retry = formatUnit("ms", it.retryMs);
      if (it.reconnects > 0) meta.reconnects = it.reconnects;
      let text = t("commands.sse.endedAfter", { end: t(END_TEXT[it.reason] ?? "commands.sse.end.ended"), count: it.events });
      if (it.discarded) text = t("commands.sse.discarded", { text });
      return { time, direction: "system", kind: "closed", text, meta };
    }
  }
}

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** One sse request's latest stream and its log. */
export class SseStream {
  readonly requestId: string;
  readonly log: MessageSource;
  private state: SseStreamState = INITIAL;
  private readonly listeners = new Set<() => void>();
  private handle: OperationHandle | null = null;
  /** Bumped by every connect and stop, so a stale callback does nothing. */
  private generation = 0;
  /** Reconnections of the streams before a resumed one (SSE-03). */
  private reconnectsBefore = 0;

  constructor(requestId: string, options: { log?: MessageSource } = {}) {
    this.requestId = requestId;
    this.log = options.log ?? new MessageSource();
  }

  getState = (): SseStreamState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<SseStreamState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  /** Whether a stream is starting or running. */
  get running(): boolean {
    return this.state.status === "starting" || this.state.status === "running";
  }

  /**
   * Connects, unless a stream is running. `prepare` runs first (it flushes
   * the editor's queued edits to Go). The log is cleared. Undefined
   * {{variables}} ask whether to connect anyway (ENV-11).
   *
   * With `resume`, the stream is resumed instead (SSE-03): the first
   * connection sends the last event ID as Last-Event-ID, and the log and
   * the counts carry on.
   */
  connect(prepare?: PrepareSend, options: { now?: number; resume?: boolean } = {}): void {
    if (this.running) return;
    const generation = ++this.generation;
    const before = this.state;
    const lastEventId = options.resume === true ? before.lastEventId : "";
    const startedAt = options.now ?? Date.now();
    this.reconnectsBefore = lastEventId === "" ? 0 : before.reconnects;
    if (lastEventId === "") {
      this.log.clear();
      this.set({ ...INITIAL, status: "starting", startedAt });
    } else {
      this.set({ status: "starting", error: null, head: null, startedAt, elapsedMs: null, endReason: "", reconnect: null });
    }
    const mine = () => this.generation === generation;
    let declined = false;
    const start = async (): Promise<StartedDTO> => {
      try {
        return await connectSse(this.requestId, false, lastEventId);
      } catch (err: unknown) {
        const e = toAppError(err);
        if (e.code !== UNRESOLVED_CODE || !mine()) throw err;
        const names = (e.details.names ?? "").split(",").filter((n) => n !== "");
        if (!(await confirmUnresolved(names, "connect")) || !mine()) {
          declined = true;
          throw err;
        }
        return await connectSse(this.requestId, true, lastEventId);
      }
    };
    void (async () => {
      try {
        await prepare?.();
      } catch (err: unknown) {
        console.warn("sse: the edits were not flushed before connecting", toAppError(err));
      }
      if (!mine()) return;
      streamStarted(this.requestId, "sse");
      const handle = startOperation<StreamItemDTO>(SSE_DOMAIN, start, {
        onStarted: (opId) => {
          if (!mine()) return;
          this.set({ status: "running" });
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
          if (result.error !== null) this.log.append({ direction: "system", kind: "error", text: errorText(result.error) });
          this.set({
            status: result.outcome,
            error: result.error,
            reconnect: null,
            elapsedMs: this.state.elapsedMs ?? (result.opId !== null ? result.elapsedMs : null),
          });
        },
      });
      if (mine()) this.handle = handle;
      else {
        handle.cancel();
        handle.dispose();
      }
    })();
  }

  /** Applies items of the running stream. */
  handleItems(items: readonly StreamItemDTO[], now: number = Date.now()): void {
    const patch: { -readonly [K in keyof SseStreamState]?: SseStreamState[K] } = {};
    let events = this.state.events;
    let dataBytes = this.state.dataBytes;
    // A wait that is followed by anything is an attempt made.
    let waiting = this.state.reconnect !== null;
    let reconnects = this.state.reconnects;
    for (const it of items) {
      if (waiting && it.type !== "end") reconnects++;
      waiting = false;
      switch (it.type) {
        case "reconnect":
          patch.reconnect = { attempt: it.attempt, maxAttempts: it.maxAttempts, delayMs: it.delayMs, until: now + it.delayMs };
          if (it.id !== "") patch.lastEventId = it.id;
          waiting = true;
          break;
        case "open":
          patch.reconnect = null;
          patch.head = {
            method: it.method,
            url: it.url,
            status: it.status,
            statusText: it.statusText,
            proto: it.proto,
            headers: it.headers ?? [],
          };
          break;
        case "event":
          events++;
          dataBytes += it.size;
          patch.lastEventId = it.id;
          break;
        case "retry":
          patch.retryMs = it.retryMs;
          break;
        case "end":
          patch.elapsedMs = it.elapsedMs;
          patch.endReason = it.reason;
          patch.lastEventId = it.id;
          patch.reconnect = null;
          reconnects = this.reconnectsBefore + it.reconnects;
          if (it.retryMs !== null) patch.retryMs = it.retryMs;
          streamEnded(this.requestId, it.tests);
          break;
      }
    }
    patch.events = events;
    patch.dataBytes = dataBytes;
    patch.reconnects = reconnects;
    this.log.append(items.map(streamLogMessage));
    this.set(patch);
  }

  /** Stops the running stream, if any. The state follows when Go has stopped it. */
  stop(): void {
    if (!this.running) return;
    if (this.handle !== null) {
      this.handle.cancel();
      return;
    }
    // Still flushing the edits: nothing was connected yet.
    this.generation++;
    this.set({ status: "cancelled" });
  }

  /** Stops a running stream and releases the log. */
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

/** The stream slots of every open sse request. */
export class SseStreams {
  private readonly streams = new Map<string, SseStream>();
  private readonly unsubscribe: () => void;

  constructor() {
    // Closing a request's tab stops its stream and drops its log.
    this.unsubscribe = onTabClosed((ids) => this.drop(ids));
  }

  /** The stream slot of request `requestId`, created on first use. */
  get(requestId: string): SseStream {
    let s = this.streams.get(requestId);
    if (s === undefined) {
      s = new SseStream(requestId);
      this.streams.set(requestId, s);
    }
    return s;
  }

  /** The stream slot of `requestId` if it has one. */
  peek(requestId: string): SseStream | undefined {
    return this.streams.get(requestId);
  }

  /** Stops and forgets the streams of these requests. */
  drop(requestIds: readonly string[]): void {
    for (const id of requestIds) {
      const s = this.streams.get(id);
      if (s === undefined) continue;
      this.streams.delete(id);
      s.dispose();
    }
  }

  /** Stops every stream and stops following tab closes. */
  dispose(): void {
    this.drop([...this.streams.keys()]);
    this.unsubscribe();
  }
}

let registry: SseStreams | null = null;

/** The app's SSE stream slots, created on first use. */
export function sseStreams(): SseStreams {
  registry ??= new SseStreams();
  return registry;
}

/** Drops the registry, for tests. */
export function resetSseStreams(): void {
  registry?.dispose();
  registry = null;
}

/**
 * Connects sse request `id` (the Connect button and the request.send
 * shortcut): its queued edits reach Go first, so the stream is opened with
 * what the editor shows. Does nothing while a stream of it runs.
 */
export function connectStream(stores: Stores, id: RequestId): void {
  sseStreams()
    .get(id)
    .connect(() => waitForQueuedEdits(stores, id));
}

/**
 * Resumes the ended stream of sse request `id` from its last event ID
 * (SSE-03): as connectStream, but the first connection sends
 * Last-Event-ID and the log carries on. Without a last event ID it
 * connects afresh.
 */
export function resumeStream(stores: Stores, id: RequestId): void {
  sseStreams()
    .get(id)
    .connect(() => waitForQueuedEdits(stores, id), { resume: true });
}

/** Stops the running stream of request `id`, if any. */
export function stopStream(id: RequestId): void {
  sseStreams().peek(id)?.stop();
}
