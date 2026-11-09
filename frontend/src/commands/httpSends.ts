// Sending HTTP requests (HTTP-20, HTTP-21): one send slot per open request,
// holding the state of its latest send and the response. Sends run as http
// operations (docs/events.md). The slots live outside React, so switching
// tabs keeps a running send and its response; closing the tab cancels the
// send and drops the response. A slot also runs repeat and interval sends
// (HTTP-27): one http.repeat operation whose items are the results of its
// sends. See docs/http-client.md.
import { t, type MessageKey } from "../i18n";
import { toAppError, type AppError } from "../lib/errors";
import {
  HTTP_DOMAIN,
  httpStageText,
  PROMPT_CODE,
  REPEAT_DOMAIN,
  UNRESOLVED_CODE,
  promptsOf,
  repeatHttpRequest,
  sendHttpRequest,
  type RepeatResultDTO,
  type ResponseDTO,
  type SendAnswers,
} from "../lib/http";
import { startOperation, type OperationHandle, type StartedDTO } from "../lib/operations";
import type { RequestId, Stores } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { confirm } from "./dialogs";
import { askPrompts, type PromptPurpose } from "./prompts";
import { onTabClosed } from "./tabs";

const UNRESOLVED_TEXT = {
  send: { title: "commands.unresolved.send.title", message: "commands.unresolved.send.message", confirm: "commands.unresolved.send.confirm" },
  connect: {
    title: "commands.unresolved.connect.title",
    message: "commands.unresolved.connect.message",
    confirm: "commands.unresolved.connect.confirm",
  },
  subscribe: {
    title: "commands.unresolved.subscribe.title",
    message: "commands.unresolved.subscribe.message",
    confirm: "commands.unresolved.subscribe.confirm",
  },
} as const satisfies Record<string, Record<string, MessageKey>>;

/**
 * Asks whether to send a request (or, with `action` "connect", open a
 * connection, WS-11; with "subscribe", start a GraphQL subscription,
 * GQL-34) whose {{variables}} `names` are not defined (ENV-11). Nothing is
 * remembered: the next send asks again.
 */
export function confirmUnresolved(names: readonly string[], action: "send" | "connect" | "subscribe" = "send"): Promise<boolean> {
  const list = names.map((n) => `{{${n}}}`).join(", ");
  const count = names.length;
  const text = UNRESOLVED_TEXT[action];
  return confirm({
    title: t(text.title, { count }),
    message: t(text.message, { count, list }),
    confirmLabel: t(text.confirm),
    cancelLabel: t("common.cancel"),
  });
}

/** "idle" before the first send; "starting" until Go has accepted it; then how it ended. */
export type HttpSendStatus = "idle" | "starting" | "running" | "ok" | "error" | "cancelled";

export interface HttpSendState {
  readonly status: HttpSendStatus;
  /**
   * What the running send is doing ("Waiting for the response"), from Go's
   * progress, in the language in use; during a run, Go's stage as it came.
   * "" otherwise.
   */
  readonly stage: string;
  /**
   * While the body arrives (HTTP-37): the bytes received so far, and the
   * size the server announced (0 when it did not), from Go's progress.
   */
  readonly received: number;
  readonly total: number;
  /** The response of the latest send, once received. */
  readonly response: ResponseDTO | null;
  /** Why the latest send failed or could not start; null otherwise. */
  readonly error: AppError | null;
  /** When the latest send started (Date.now()), for a running timer. */
  readonly startedAt: number | null;
  /**
   * The latest send was a run (HTTP-27): its options and results. The
   * status is then the run's (a stopped run is "cancelled"), `error` why
   * it could not go on, and `response` the latest response it carried.
   */
  readonly run: HttpRunState | null;
}

/** How a run sends (HTTP-27): `count` sends, 0 until stopped; `intervalMs` from the start of one to the next, 0 back to back. */
export interface HttpRunOptions {
  /** Which form of the Repeat dialog set them: N times, or at an interval. Go only gets count and interval. */
  readonly mode?: "times" | "interval";
  readonly count: number;
  readonly intervalMs: number;
}

/** The results of a run. */
export interface HttpRunState {
  readonly options: HttpRunOptions;
  /** The latest MAX_RUN_RESULTS results, oldest first. */
  readonly results: readonly RepeatResultDTO[];
  /** Sends listed so far; those with a response, and those that failed without one. */
  readonly sent: number;
  readonly responses: number;
  readonly failed: number;
  /** Responses whose status is 400 or more, or with a failed test. */
  readonly problems: number;
  /** Total, shortest and longest time of the sends with a response, in ms. */
  readonly totalMs: number;
  readonly minMs: number;
  readonly maxMs: number;
  /** The index of the send whose response `response` is, 0 before one arrived. */
  readonly responseIndex: number;
}

/** How many results a run keeps for the list; the counts cover all of them. */
export const MAX_RUN_RESULTS = 1000;

const INITIAL: HttpSendState = {
  status: "idle",
  stage: "",
  received: 0,
  total: 0,
  response: null,
  error: null,
  startedAt: null,
  run: null,
};

function newRun(options: HttpRunOptions): HttpRunState {
  return { options, results: [], sent: 0, responses: 0, failed: 0, problems: 0, totalMs: 0, minMs: 0, maxMs: 0, responseIndex: 0 };
}

/** `run` with `items` added. */
export function addRunResults(run: HttpRunState, items: readonly RepeatResultDTO[]): HttpRunState {
  let { sent, responses, failed, problems, totalMs, minMs, maxMs, responseIndex } = run;
  for (const r of items) {
    sent++;
    if (r.error !== null) {
      failed++;
    } else {
      minMs = responses === 0 ? r.durationMs : Math.min(minMs, r.durationMs);
      maxMs = Math.max(maxMs, r.durationMs);
      responses++;
      totalMs += r.durationMs;
      if (r.statusCode >= 400 || r.testsFailed > 0) problems++;
    }
    if (r.response !== null) responseIndex = r.index;
  }
  const all = run.results.concat(items);
  const results = all.length > MAX_RUN_RESULTS ? all.slice(all.length - MAX_RUN_RESULTS) : all;
  return { ...run, results, sent, responses, failed, problems, totalMs, minMs, maxMs, responseIndex };
}

/** Waits until the editor's edits have reached Go, so the send sends what is on screen. */
export type PrepareSend = () => Promise<void>;

/** One HTTP request's latest send. */
export class HttpSend {
  readonly requestId: string;
  private state: HttpSendState = INITIAL;
  private readonly listeners = new Set<() => void>();
  private handle: OperationHandle | null = null;
  /** Bumped by every send and cancel, so a stale callback does nothing. */
  private generation = 0;

  constructor(requestId: string) {
    this.requestId = requestId;
  }

  getState = (): HttpSendState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<HttpSendState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  /** Whether a send is starting or running. */
  get running(): boolean {
    return this.state.status === "starting" || this.state.status === "running";
  }

  /** The options of the latest run, offered for the next one (HTTP-27). */
  runOptions: HttpRunOptions = { count: 10, intervalMs: 0 };

  /**
   * Sends the request, unless a send is running. `prepare` runs first (it
   * flushes the editor's queued edits to Go). The outcome arrives as state
   * changes; a request Go cannot build ends the send with that error.
   */
  send(prepare?: PrepareSend): void {
    this.begin<ResponseDTO>(HTTP_DOMAIN, (answers) => sendHttpRequest(this.requestId, answers), null, prepare, (items) => {
      const response = items[items.length - 1];
      if (response !== undefined) this.set({ response });
    });
  }

  /**
   * Starts a run (HTTP-27): the request sent `options.count` times (0:
   * until stopped) with `options.intervalMs` between the starts, unless a
   * send or run is running. Each result is added to `state.run` as it
   * arrives; cancel() stops the run.
   */
  repeat(options: HttpRunOptions, prepare?: PrepareSend): void {
    if (this.running) return;
    this.runOptions = options;
    this.begin<RepeatResultDTO>(
      REPEAT_DOMAIN,
      (answers) => repeatHttpRequest(this.requestId, options.count, options.intervalMs, answers),
      newRun(options),
      prepare,
      (items) => {
        const run = this.state.run;
        if (run === null) return;
        let response: ResponseDTO | null = null;
        for (const r of items) if (r.response !== null) response = r.response;
        const next = addRunResults(run, items);
        this.set(response === null ? { run: next } : { run: next, response });
      },
    );
  }

  /**
   * Starts an operation of `domain` with `call`, asking first for the
   * {{$prompt}} values (DYN-07), then about undefined variables (ENV-11).
   */
  private begin<T>(
    domain: string,
    call: (answers: SendAnswers) => Promise<StartedDTO>,
    run: HttpRunState | null,
    prepare: PrepareSend | undefined,
    onItems: (items: T[]) => void,
  ): void {
    if (this.running) return;
    const generation = ++this.generation;
    const before = this.state;
    this.set({ ...INITIAL, status: "starting", startedAt: Date.now(), run });
    const mine = () => this.generation === generation;
    // Set when the user cancelled the prompt or declined to send with
    // undefined variables: the state goes back to what it was before.
    let declined = false;
    const purpose: PromptPurpose = run === null ? "send" : "run";
    // Go refuses a request with {{$prompt}} values it has no answer for
    // (DYN-07): ask for them all in one dialog and send again with them.
    // It refuses undefined {{variables}} (ENV-11): ask, and on "Send
    // anyway" send again with the references as written. Each is asked at
    // most once per send; the answers live in this closure only.
    const start = async (): Promise<StartedDTO> => {
      let answers: SendAnswers = {};
      for (;;) {
        try {
          return await call(answers);
        } catch (err: unknown) {
          const e = toAppError(err);
          if (!mine()) throw err;
          if (e.code === PROMPT_CODE && answers.prompts === undefined) {
            const values = await askPrompts(promptsOf(e.details), purpose);
            if (values === null || !mine()) {
              declined = true;
              throw err;
            }
            answers = { ...answers, prompts: values };
            continue;
          }
          if (e.code === UNRESOLVED_CODE && !answers.allowUnresolved) {
            const names = (e.details.names ?? "").split(",").filter((n) => n !== "");
            if (!(await confirmUnresolved(names)) || !mine()) {
              declined = true;
              throw err;
            }
            answers = { ...answers, allowUnresolved: true };
            continue;
          }
          throw err;
        }
      }
    };
    void (async () => {
      try {
        await prepare?.();
      } catch (err: unknown) {
        console.warn("http: the edits were not flushed before sending", toAppError(err));
      }
      if (!mine()) return;
      const handle = startOperation<T>(domain, start, {
        onStarted: () => {
          if (mine()) this.set({ status: "running" });
        },
        onProgress: (ev) => {
          if (!mine() || !this.running) return;
          // A run's progress counts sends, not bytes.
          // A single send's stage is shown as it is, so in the language in use; a run's is Go's,
          // compared with REPEAT_WAIT_STAGE (RunResults words it itself).
          if (run === null) this.set({ stage: httpStageText(ev.message), received: ev.done, total: ev.total });
          else this.set({ stage: ev.message });
        },
        onData: (items) => {
          if (mine()) onItems(items);
        },
        onDone: (result) => {
          if (!mine()) return;
          this.handle = null;
          if (declined) {
            this.set(before);
            return;
          }
          this.set({ status: result.outcome, stage: "", error: result.error });
        },
      });
      if (mine()) this.handle = handle;
      else {
        handle.cancel();
        handle.dispose();
      }
    })();
  }

  /** Cancels the running send, if any. The state follows when Go has stopped it. */
  cancel(): void {
    if (!this.running) return;
    if (this.handle !== null) {
      this.handle.cancel();
      return;
    }
    // Still flushing the edits: nothing was sent yet.
    this.generation++;
    this.set({ status: "cancelled", stage: "" });
  }

  /** Cancels a running send and forgets the listeners. */
  dispose(): void {
    this.generation++;
    const handle = this.handle;
    this.handle = null;
    handle?.cancel();
    handle?.dispose();
    this.listeners.clear();
  }
}

/** The send slots of every open HTTP request. */
export class HttpSends {
  private readonly sends = new Map<string, HttpSend>();
  private readonly unsubscribe: () => void;

  constructor() {
    // Closing a request's tab cancels its send and drops its response.
    this.unsubscribe = onTabClosed((ids) => this.drop(ids));
  }

  /** The send slot of request `requestId`, created on first use. */
  get(requestId: string): HttpSend {
    let send = this.sends.get(requestId);
    if (send === undefined) {
      send = new HttpSend(requestId);
      this.sends.set(requestId, send);
    }
    return send;
  }

  /** Cancels and forgets the sends of these requests. */
  drop(requestIds: readonly string[]): void {
    for (const id of requestIds) {
      const send = this.sends.get(id);
      if (send === undefined) continue;
      this.sends.delete(id);
      send.dispose();
    }
  }

  /** Cancels every send and stops following tab closes. */
  dispose(): void {
    this.drop([...this.sends.keys()]);
    this.unsubscribe();
  }
}

let registry: HttpSends | null = null;

/** The app's HTTP send slots, created on first use. */
export function httpSends(): HttpSends {
  registry ??= new HttpSends();
  return registry;
}

/** Drops the registry, for tests. */
export function resetHttpSends(): void {
  registry?.dispose();
  registry = null;
}

/**
 * Sends HTTP request `id` (the Send button and the request.send shortcut):
 * its queued edits reach Go first, so what is sent is what the editor
 * shows. Does nothing while a send of it is running.
 */
export function sendRequest(stores: Stores, id: RequestId): void {
  httpSends()
    .get(id)
    .send(() => waitForQueuedEdits(stores, id));
}

/**
 * Starts a run of HTTP request `id` (HTTP-27, the Repeat dialog of the
 * Send button): its queued edits reach Go first. Does nothing while a send
 * or run of it is running.
 */
export function repeatRequest(stores: Stores, id: RequestId, options: HttpRunOptions): void {
  httpSends()
    .get(id)
    .repeat(options, () => waitForQueuedEdits(stores, id));
}

/** Cancels the running send (or stops the run) of request `id`, if any. */
export function cancelRequest(id: RequestId): void {
  httpSends().get(id).cancel();
}
