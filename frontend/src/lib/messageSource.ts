// The message source behind the connection message log (WS-05): an
// append-only ring buffer of messages with a maximum length, applied at most
// once per animation frame. Framework-free, so commands can feed it from
// operation events and components/messagelog/MessageLog can show it.
// See docs/frontend.md (Message log).

/** Who produced a message: this app, the peer, or the connection itself (connected, closed, error). */
export type MessageDirection = "sent" | "received" | "system";

export const MESSAGE_DIRECTIONS: readonly MessageDirection[] = ["sent", "received", "system"];

/** A frame detail shown in the message's detail view (opcode, FIN, masked, event id…). */
export type MessageMetaValue = string | number | boolean;

export interface LogMessageInput {
  /** When it was sent or received, in epoch milliseconds (fractions allowed). Default: when appended. */
  time?: number;
  direction: MessageDirection;
  /** Generic type: "text", "binary", "ping", "pong", "close", "connected", "error", an SSE event… */
  kind: string;
  /** Optional short label shown next to the kind (an SSE event name, a gRPC method). */
  badge?: string;
  /** Text payload. */
  text?: string;
  /** Binary payload. When set, the message is shown as binary (hex). */
  data?: Uint8Array;
  /** Size in bytes. Default: the UTF-8 length of `text`, or the length of `data`. */
  size?: number;
  meta?: Readonly<Record<string, MessageMetaValue>>;
}

export interface LogMessage extends Readonly<LogMessageInput> {
  /** Position in the stream since the source was created: 0, 1, 2, … never reused, also across clear(). */
  readonly seq: number;
  readonly time: number;
}

export interface MessageSourceSnapshot {
  /** Bumped by every applied change. */
  readonly version: number;
  /** Seq of the oldest message kept. */
  readonly firstSeq: number;
  /** Seq the next message will get; kept messages are firstSeq … nextSeq-1. */
  readonly nextSeq: number;
  /** Messages kept. */
  readonly count: number;
  /** Messages dropped from the front since the last clear, because the buffer was full. */
  readonly dropped: number;
  /** Seq at the last clear (0 before any). A view that saw another value must start over. */
  readonly clearedAt: number;
  /** Time of the first message since the last clear, or null. */
  readonly startTime: number | null;
  /** Every kind seen since the last clear, in order of first appearance. */
  readonly kinds: readonly string[];
  readonly maxMessages: number;
}

export type FrameScheduler = (flush: () => void) => () => void;

export interface MessageSourceOptions {
  /** Messages kept; older ones are dropped. Default 50,000. */
  maxMessages?: number;
  /**
   * Schedules a flush of appended messages. Default: the next animation
   * frame (a 16ms timer without requestAnimationFrame). Tests pass their own.
   */
  schedule?: FrameScheduler;
  /** Clock for messages without a time. Default Date.now. */
  now?: () => number;
}

export const DEFAULT_MAX_MESSAGES = 50_000;

const defaultSchedule: FrameScheduler = (flush) => {
  if (typeof requestAnimationFrame === "function") {
    const id = requestAnimationFrame(() => flush());
    return () => cancelAnimationFrame(id);
  }
  const id = setTimeout(flush, 16);
  return () => clearTimeout(id);
};

/**
 * An append-only log of messages with a maximum length. `append` only queues:
 * the queue is applied in one step on the next animation frame, so listeners
 * (and the log's render) run at most once a frame however fast messages come.
 */
export class MessageSource {
  readonly maxMessages: number;
  private readonly schedule: FrameScheduler;
  private readonly now: () => number;
  private readonly buf: (LogMessage | undefined)[];
  private head = 0;
  private count = 0;
  private nextSeq = 0;
  private clearedAt = 0;
  private startTime: number | null = null;
  private kinds: string[] = [];
  private kindSet = new Set<string>();
  private pending: LogMessageInput[] = [];
  /** Queued messages discarded before a flush because the queue outgrew maxMessages. */
  private skipped = 0;
  private cancelFlush: (() => void) | null = null;
  private version = 0;
  private snapshot: MessageSourceSnapshot;
  private readonly listeners = new Set<() => void>();

  constructor(options: MessageSourceOptions = {}) {
    const max = Math.floor(options.maxMessages ?? DEFAULT_MAX_MESSAGES);
    this.maxMessages = Number.isFinite(max) && max > 0 ? max : DEFAULT_MAX_MESSAGES;
    this.schedule = options.schedule ?? defaultSchedule;
    this.now = options.now ?? Date.now;
    this.buf = new Array<LogMessage | undefined>(this.maxMessages);
    this.snapshot = this.buildSnapshot();
  }

  /** Queues messages; they are applied on the next frame (or by flush()). */
  append(messages: LogMessageInput | readonly LogMessageInput[]): void {
    const list = Array.isArray(messages) ? (messages as readonly LogMessageInput[]) : [messages as LogMessageInput];
    if (list.length === 0) return;
    const at = this.now();
    for (const m of list) this.pending.push(m.time === undefined ? { ...m, time: at } : m);
    // A queue that cannot be flushed (a hidden window gets no frames) must not grow without bound:
    // messages beyond maxMessages would be dropped by the flush anyway.
    const excess = this.pending.length - this.maxMessages;
    if (excess > this.maxMessages) {
      this.pending.splice(0, excess);
      this.skipped += excess;
    }
    if (this.cancelFlush === null) this.cancelFlush = this.schedule(() => this.flush());
  }

  /** Applies queued messages now and notifies listeners once. */
  flush(): void {
    if (this.cancelFlush !== null) {
      const cancel = this.cancelFlush;
      this.cancelFlush = null;
      cancel();
    }
    if (this.pending.length === 0 && this.skipped === 0) return;
    const batch = this.pending;
    this.pending = [];
    let skip = this.skipped;
    this.skipped = 0;
    // Messages that would be dropped by this same flush are never stored.
    const overflow = batch.length - this.maxMessages;
    let start = 0;
    if (overflow > 0) {
      start = overflow;
      skip += overflow;
    }
    if (skip > 0) {
      // Skipping only happens when a full buffer's worth (maxMessages) is left in the batch, which
      // would push out every kept message anyway. Dropping them keeps the kept seqs contiguous.
      this.dropFront(this.count);
      this.nextSeq += skip;
    }
    for (let i = start; i < batch.length; i++) this.push(batch[i]);
    this.commit();
  }

  /** Drops every message, applied at once. Queued messages are discarded too. */
  clear(): void {
    if (this.cancelFlush !== null) {
      this.cancelFlush();
      this.cancelFlush = null;
    }
    this.pending = [];
    this.skipped = 0;
    this.buf.fill(undefined);
    this.head = 0;
    this.count = 0;
    this.clearedAt = this.nextSeq;
    this.startTime = null;
    this.kinds = [];
    this.kindSet = new Set();
    this.commit();
  }

  /** The kept message with this seq, or undefined once dropped. */
  get(seq: number): LogMessage | undefined {
    const firstSeq = this.nextSeq - this.count;
    if (seq < firstSeq || seq >= this.nextSeq || !Number.isInteger(seq)) return undefined;
    return this.buf[(this.head + (seq - firstSeq)) % this.maxMessages];
  }

  getSnapshot = (): MessageSourceSnapshot => this.snapshot;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  /** Stops a scheduled flush (for an owner that goes away). Queued messages stay queued. */
  dispose(): void {
    if (this.cancelFlush !== null) {
      this.cancelFlush();
      this.cancelFlush = null;
    }
    this.listeners.clear();
  }

  private push(input: LogMessageInput): void {
    const message: LogMessage = { ...input, seq: this.nextSeq, time: input.time ?? this.now() };
    this.nextSeq++;
    if (this.startTime === null) this.startTime = message.time;
    if (!this.kindSet.has(message.kind)) {
      this.kindSet.add(message.kind);
      this.kinds = [...this.kinds, message.kind];
    }
    const cap = this.maxMessages;
    if (this.count < cap) {
      this.buf[(this.head + this.count) % cap] = message;
      this.count++;
    } else {
      this.buf[this.head] = message;
      this.head = (this.head + 1) % cap;
    }
  }

  private dropFront(n: number): void {
    for (let i = 0; i < n; i++) {
      this.buf[this.head] = undefined;
      this.head = (this.head + 1) % this.maxMessages;
    }
    this.count -= n;
  }

  private buildSnapshot(): MessageSourceSnapshot {
    const firstSeq = this.nextSeq - this.count;
    return {
      version: this.version,
      firstSeq,
      nextSeq: this.nextSeq,
      count: this.count,
      dropped: firstSeq - this.clearedAt,
      clearedAt: this.clearedAt,
      startTime: this.startTime,
      kinds: this.kinds,
      maxMessages: this.maxMessages,
    };
  }

  private commit(): void {
    this.version++;
    this.snapshot = this.buildSnapshot();
    for (const listener of [...this.listeners]) listener();
  }
}
