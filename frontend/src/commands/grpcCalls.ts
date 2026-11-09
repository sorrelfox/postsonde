// The calls of open grpc requests (GRPC-11 to GRPC-14, GRPC-19 to GRPC-22):
// one call slot per request, holding the state of its latest call and a
// message log of it, the streaming timeline: what was sent and received,
// the headers, the half-close and the status, in the order they happened.
// Client-streaming and bidi calls send their messages one at a time
// (send) and end the request stream with closeSend. Calls run as grpc operations (docs/events.md). The
// slots live outside React, so switching tabs (which unmounts the editor)
// keeps a running stream and its responses; closing the tab cancels the
// call and drops them. See docs/grpc.md.
import { t } from "../i18n";
import { formatUnit } from "../lib/units";
import { toAppError, type AppError } from "../lib/errors";
import {
  closeGrpcSend,
  GRPC_DOMAIN,
  invokeGrpc,
  sendGrpcMessage,
  statusText,
  streamsFromClient,
  type CallEventDTO,
  type InvokeInputDTO,
  type MetadataDTO,
  type StatusDTO,
} from "../lib/grpc";
import { MessageSource, type LogMessageInput } from "../lib/messageSource";
import { startOperation, type OperationHandle } from "../lib/operations";
import { decodeBase64 } from "../lib/tcp";
import { streamEnded, streamRef, streamStarted } from "./streamTests";
import { onTabClosed } from "./tabs";

/** "idle" before the first call; "starting" until Go has accepted it; then how it ended. */
export type GrpcCallStatus = "idle" | "starting" | "running" | "ok" | "error" | "cancelled";

export interface GrpcCallState {
  readonly status: GrpcCallStatus;
  /** "pkg.Service/Method" of the current (or last) call. */
  readonly method: string;
  /** Why the call failed or could not start; null otherwise. */
  readonly error: AppError | null;
  /** The final gRPC status, once the call has ended on the wire. */
  readonly grpcStatus: StatusDTO | null;
  /** Response headers and trailers, once received. */
  readonly headers: readonly MetadataDTO[] | null;
  readonly trailers: readonly MetadataDTO[] | null;
  /** Response messages received so far. */
  readonly messages: number;
  /** How long the call took, once it ended. */
  readonly elapsedMs: number | null;
  /** The method's call type at invoke ("unary", "server_stream", …; "" when not given). */
  readonly kind: string;
  /** When the call was started (Date.now()), for a live timer; null before the first call. */
  readonly startedAt: number | null;
  /** Bytes of response messages received so far, uncompressed. */
  readonly receivedBytes: number;
  /** The first response message, shown as the result of a unary call (GRPC-11); null before it arrives. */
  readonly firstMessage: GrpcResponseMessage | null;
  /** The call streams from the client (client streaming, bidi): messages are sent with send(). */
  readonly clientStreaming: boolean;
  /** Messages sent on the request stream so far (streams from the client), and their bytes. */
  readonly sent: number;
  readonly sentBytes: number;
  /** Sends that have not been answered by Go yet. */
  readonly sending: number;
  /** The request stream has been ended (closeSend). */
  readonly halfClosed: boolean;
  /** Why the latest send or closeSend was refused; cleared by the next one. */
  readonly sendError: AppError | null;
}

/** A response message as the call event carried it. */
export interface GrpcResponseMessage {
  /** Indented protobuf JSON; "" when it could not be decoded. */
  readonly json: string;
  /** Why it could not be decoded, "" otherwise; `raw` then holds its bytes in Base64. */
  readonly decodeError: string;
  readonly raw: string;
  readonly size: number;
}

const INITIAL: GrpcCallState = {
  status: "idle",
  method: "",
  error: null,
  grpcStatus: null,
  headers: null,
  trailers: null,
  messages: 0,
  elapsedMs: null,
  kind: "",
  startedAt: null,
  receivedBytes: 0,
  firstMessage: null,
  clientStreaming: false,
  sent: 0,
  sentBytes: 0,
  sending: 0,
  halfClosed: false,
  sendError: null,
};

function eventTime(at: string): number | undefined {
  const ms = Date.parse(at);
  return Number.isNaN(ms) ? undefined : ms;
}

function metadataText(md: readonly MetadataDTO[]): string {
  return md.map((e) => `${e.key}: ${e.value}`).join("\n");
}

/** A call event as a message of the log. */
export function callLogMessage(ev: CallEventDTO): LogMessageInput {
  const time = eventTime(ev.at);
  switch (ev.type) {
    case "request": {
      const meta: Record<string, string | number> = { method: ev.method };
      if (ev.deadlineMs > 0) meta.deadline = formatUnit("ms", ev.deadlineMs);
      for (const e of ev.metadata ?? []) meta[`metadata ${e.key}`] = e.binary ? `${e.value} (Base64)` : e.value;
      if (ev.clientStreaming) {
        // The stream is open; its messages follow as "sent" items.
        return { time, direction: "system", kind: "open", text: t("commands.grpc.streamOpened", { method: ev.method }), size: 0, meta };
      }
      return { time, direction: "sent", kind: "message", text: ev.json, size: ev.size, meta };
    }
    case "sent":
      return { time, direction: "sent", kind: "message", badge: `#${ev.index}`, text: ev.json, size: ev.size };
    case "half-close":
      return {
        time,
        direction: "system",
        kind: "half-close",
        text: t("commands.grpc.requestStreamEnded", { count: ev.index }),
        size: 0,
        meta: { sent: ev.index },
      };
    case "headers":
      return { time, direction: "system", kind: "headers", text: metadataText(ev.metadata ?? []) };
    case "message":
      if (ev.decodeError !== "") {
        return { time, direction: "received", kind: "message", badge: `#${ev.index}`, data: decodeBase64(ev.raw), size: ev.size, meta: { error: ev.decodeError } };
      }
      return { time, direction: "received", kind: "message", badge: `#${ev.index}`, text: ev.json, size: ev.size };
    default: {
      const status = ev.status;
      const trailers = ev.metadata ?? [];
      const text = status === null ? t("commands.grpc.ended") : statusText(status);
      const meta: Record<string, string | number> = { messages: ev.messages, sent: ev.sent, elapsed: formatUnit("ms", ev.elapsedMs) };
      if (status !== null) meta.code = status.code;
      const details = status?.rich?.details ?? [];
      if (details.length > 0) meta["error details"] = details.map((d) => d.type || d.typeUrl).join(", ");
      else if (status?.rich?.error) meta["error details"] = status.rich.error;
      for (const e of trailers) meta[`trailer ${e.key}`] = e.binary ? `${e.value} (Base64)` : e.value;
      return { time, direction: "system", kind: "status", badge: status?.name, text, meta };
    }
  }
}

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** One grpc request's latest call and its log. */
export class GrpcCall {
  readonly requestId: string;
  readonly log: MessageSource;
  private state: GrpcCallState = INITIAL;
  private readonly listeners = new Set<() => void>();
  private handle: OperationHandle | null = null;
  /** Counts calls, so a send answered after its call ended leaves the next one alone. */
  private generation = 0;

  constructor(requestId: string, options: { log?: MessageSource } = {}) {
    this.requestId = requestId;
    this.log = options.log ?? new MessageSource();
  }

  getState = (): GrpcCallState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<GrpcCallState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  /** Whether a call is starting or running. */
  get running(): boolean {
    return this.state.status === "starting" || this.state.status === "running";
  }

  /**
   * Starts a call, unless one is running. The log is cleared first. The
   * outcome arrives as state changes; input Go refuses ends the call with
   * that error. `kind` is the method's call type, which the response view
   * uses to show a unary result as one message.
   */
  invoke(input: InvokeInputDTO, options: { kind?: string; now?: number } = {}): void {
    if (this.running) return;
    this.log.clear();
    this.generation++;
    const kind = options.kind ?? "";
    this.set({
      ...INITIAL,
      status: "starting",
      method: `${input.service}/${input.method}`,
      kind,
      clientStreaming: streamsFromClient(kind),
      startedAt: options.now ?? Date.now(),
    });
    let current: OperationHandle | null = null;
    const mine = () => this.handle === current;
    streamStarted(this.requestId, "grpc");
    current = startOperation<CallEventDTO>(GRPC_DOMAIN, () => invokeGrpc(input), {
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
        if (result.opId === null && result.error !== null) {
          this.log.append({ direction: "system", kind: "error", text: errorText(result.error) });
        }
        this.set({
          status: result.outcome,
          error: result.error,
          elapsedMs: this.state.elapsedMs ?? (result.opId !== null ? result.elapsedMs : null),
        });
      },
    });
    this.handle = current;
  }

  /** Applies call events of the running call. */
  handleItems(items: readonly CallEventDTO[]): void {
    const patch: { -readonly [K in keyof GrpcCallState]?: GrpcCallState[K] } = {};
    let messages = this.state.messages;
    let received = this.state.receivedBytes;
    let sent = this.state.sent;
    let sentBytes = this.state.sentBytes;
    for (const ev of items) {
      if (ev.type === "request") {
        if (ev.clientStreaming) patch.clientStreaming = true;
      } else if (ev.type === "sent") {
        sent++;
        sentBytes += ev.size;
      } else if (ev.type === "half-close") patch.halfClosed = true;
      else if (ev.type === "headers") patch.headers = ev.metadata ?? [];
      else if (ev.type === "message") {
        if (messages === 0) patch.firstMessage = { json: ev.json, decodeError: ev.decodeError, raw: ev.raw, size: ev.size };
        messages++;
        received += ev.size;
      } else if (ev.type === "end") {
        patch.trailers = ev.metadata ?? [];
        patch.grpcStatus = ev.status;
        patch.elapsedMs = ev.elapsedMs;
        streamEnded(this.requestId, ev.tests);
      }
    }
    patch.messages = messages;
    patch.receivedBytes = received;
    patch.sent = sent;
    patch.sentBytes = sentBytes;
    this.log.append(items.map(callLogMessage));
    this.set(patch);
  }

  /** Whether messages can be sent now: a running call that streams from the client, with its stream not ended. */
  get canSend(): boolean {
    return this.state.status === "running" && this.state.clientStreaming && !this.state.halfClosed && this.handle?.opId() != null;
  }

  /**
   * Sends one message (protobuf JSON) on the running call's request stream
   * (GRPC-13, GRPC-14). Its `sent` item arrives with the call's data, in
   * order with the replies. Resolves true once Go has sent it; a refusal
   * is kept as sendError and resolves false.
   */
  async send(message: string): Promise<boolean> {
    const opId = this.handle?.opId() ?? null;
    if (!this.canSend || opId === null) return false;
    return this.request(() => sendGrpcMessage({ opId, message }));
  }

  /** Ends the running call's request stream (half-close); the server then answers or ends the call. */
  async closeSend(): Promise<boolean> {
    const opId = this.handle?.opId() ?? null;
    if (!this.canSend || opId === null) return false;
    return this.request(() => closeGrpcSend({ opId }));
  }

  private async request(fn: () => Promise<unknown>): Promise<boolean> {
    const generation = this.generation;
    const mine = () => generation === this.generation;
    this.set({ sending: this.state.sending + 1, sendError: null });
    try {
      await fn();
      if (mine()) this.set({ sending: this.state.sending - 1 });
      return true;
    } catch (reason) {
      if (mine()) {
        // After the call ended the refusal says nothing new: the status does.
        const ended = !this.running;
        this.set({ sending: this.state.sending - 1, sendError: ended ? null : toAppError(reason) });
      }
      return false;
    }
  }

  /** Cancels the running call, if any. The state follows when Go has stopped it. */
  cancel(): void {
    this.handle?.cancel();
  }

  /** Cancels a running call and releases the log. */
  dispose(): void {
    const handle = this.handle;
    this.handle = null;
    handle?.cancel();
    handle?.dispose();
    this.listeners.clear();
    this.log.dispose();
  }
}

/** The call slots of every open grpc request. */
export class GrpcCalls {
  private readonly calls = new Map<string, GrpcCall>();
  private readonly unsubscribe: () => void;

  constructor() {
    // Closing a request's tab cancels its call and drops its log.
    this.unsubscribe = onTabClosed((ids) => this.drop(ids));
  }

  /** The call slot of request `requestId`, created on first use. */
  get(requestId: string): GrpcCall {
    let call = this.calls.get(requestId);
    if (call === undefined) {
      call = new GrpcCall(requestId);
      this.calls.set(requestId, call);
    }
    return call;
  }

  /** The call slot of `requestId` if it has one. */
  peek(requestId: string): GrpcCall | undefined {
    return this.calls.get(requestId);
  }

  /** Cancels and forgets the calls of these requests. */
  drop(requestIds: readonly string[]): void {
    for (const id of requestIds) {
      const call = this.calls.get(id);
      if (call === undefined) continue;
      this.calls.delete(id);
      call.dispose();
    }
  }

  /** Cancels every call and stops following tab closes. */
  dispose(): void {
    this.drop([...this.calls.keys()]);
    this.unsubscribe();
  }
}

let registry: GrpcCalls | null = null;

/** The app's gRPC call slots, created on first use. */
export function grpcCalls(): GrpcCalls {
  registry ??= new GrpcCalls();
  return registry;
}

/** Drops the registry, for tests. */
export function resetGrpcCalls(): void {
  registry?.dispose();
  registry = null;
}
