// A request's earlier responses (HTTP-39) and its response examples
// (HTTP-38). One slot per open request, outside React like the send slots
// (httpSends.ts): the request's latest responses from the history (Go's
// NAV-02 store, kept up to date by history:changed) and what the response
// pane shows: the latest send, an earlier response or a saved example.
// Picking one loads its view from Go (HTTPService.StoredResponse); a new
// send shows itself again. Examples live in the request's draft
// (`responseExamples`), so saving, renaming and deleting one is an
// auto-saved, undoable edit. Closing the tab drops the slot. See
// docs/history.md#response-examples.

import { t } from "../i18n";
import { toAppError, type AppError } from "../lib/errors";
import type { RequestDTO } from "../lib/collection";
import type { ResponseDTO } from "../lib/http";
import {
  entryAsStored,
  exampleAsStored,
  exampleFromHistory,
  historyEntry,
  listRequestResponses,
  onHistoryChanged,
  RESPONSE_HISTORY_LIMIT,
  storedResponseView,
  uniqueName,
  type HistoryChangedDTO,
  type HistorySummaryDTO,
  type ResponseExampleDTO,
} from "../lib/responseHistory";
import type { RequestId, Stores } from "../stores";
import { editRequest } from "./autosave";
import { onTabClosed } from "./tabs";
import { showToast } from "./toasts";

/** The request editor's sub-tab with the examples. */
export const EXAMPLES_SUB_TAB = "examples";

/** What the response pane shows instead of the latest send, or the latest send. */
export type ShownResponse =
  | { readonly kind: "latest" }
  /** An earlier response: history entry `id`, sent at `time` (RFC 3339). */
  | { readonly kind: "history"; readonly id: string; readonly time: string }
  /** Response example `index` of the request, named `name` when it was picked. */
  | { readonly kind: "example"; readonly index: number; readonly name: string };

export interface ResponseHistoryState {
  /** The request's latest responses in the history, newest first. */
  readonly entries: readonly HistorySummaryDTO[];
  /** Set once the list was asked for (whether or not Go had one). */
  readonly loaded: boolean;
  readonly shown: ShownResponse;
  /**
   * The startedAt of the latest send when `shown` was picked (null before
   * any send): once another send starts, the pane shows it again.
   */
  readonly since: number | null;
  /** The view of `shown` when it is not the latest: loading, loaded or failed. */
  readonly status: "idle" | "loading" | "ok" | "error";
  readonly response: ResponseDTO | null;
  readonly error: AppError | null;
  /** Set while a response is being saved as an example. */
  readonly saving: boolean;
}

const LATEST: ShownResponse = { kind: "latest" };

const INITIAL: ResponseHistoryState = {
  entries: [],
  loaded: false,
  shown: LATEST,
  since: null,
  status: "idle",
  response: null,
  error: null,
  saving: false,
};

/** The earlier responses and the shown response of one request. */
export class ResponseHistory {
  readonly requestId: string;
  private state: ResponseHistoryState = INITIAL;
  private readonly listeners = new Set<() => void>();
  /** Bumped by every pick, so a late view does nothing. */
  private generation = 0;
  private loading = false;

  constructor(requestId: string) {
    this.requestId = requestId;
  }

  getState = (): ResponseHistoryState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    if (!this.state.loaded) void this.refresh();
    return () => {
      this.listeners.delete(listener);
    };
  };

  private set(patch: Partial<ResponseHistoryState>): void {
    this.state = { ...this.state, ...patch };
    for (const listener of [...this.listeners]) listener();
  }

  /** Asks Go for the request's latest responses. Without a history the list stays empty. */
  async refresh(): Promise<void> {
    if (this.loading) return;
    this.loading = true;
    try {
      const entries = await listRequestResponses(this.requestId);
      this.set({ entries, loaded: true });
    } catch {
      // No project or no history: nothing to offer.
      this.set({ loaded: true });
    } finally {
      this.loading = false;
    }
  }

  /** Follows Go's history:changed. */
  changed(ev: HistoryChangedDTO): void {
    const { entries } = this.state;
    switch (ev.action) {
      case "added": {
        const e = ev.entry;
        if (e === null || e.requestId !== this.requestId || e.outcome !== "ok" || entries.some((x) => x.id === e.id)) return;
        this.set({ entries: [e, ...entries].slice(0, RESPONSE_HISTORY_LIMIT) });
        return;
      }
      case "deleted":
        if (entries.some((x) => x.id === ev.id)) this.set({ entries: entries.filter((x) => x.id !== ev.id) });
        return;
      case "cleared":
        // The whole history, or one request's (the history panel, NAV-02).
        if ((ev.requestId ?? "") !== "" && ev.requestId !== this.requestId) return;
        if (entries.length > 0) this.set({ entries: [] });
        return;
    }
  }

  /** Shows the latest send again. */
  showLatest(): void {
    this.generation++;
    this.set({ shown: LATEST, status: "idle", response: null, error: null });
  }

  /**
   * Shows `shown` (an earlier response or an example) until the next send
   * (`since` is the latest send's startedAt), with the view `load` makes.
   */
  show(shown: ShownResponse, since: number | null, load: () => Promise<ResponseDTO>): void {
    const generation = ++this.generation;
    this.set({ shown, since, status: "loading", response: null, error: null });
    void (async () => {
      try {
        const response = await load();
        if (generation === this.generation) this.set({ status: "ok", response });
      } catch (err: unknown) {
        if (generation === this.generation) this.set({ status: "error", error: toAppError(err) });
      }
    })();
  }

  setSaving(saving: boolean): void {
    if (this.state.saving !== saving) this.set({ saving });
  }

  dispose(): void {
    this.generation++;
    this.listeners.clear();
  }
}

/** The slots of every open request. */
export class ResponseHistories {
  private readonly slots = new Map<string, ResponseHistory>();
  private readonly unsubscribe: () => void;
  private stopEvents: (() => void) | null = null;

  constructor() {
    this.unsubscribe = onTabClosed((ids) => this.drop(ids));
  }

  /** The slot of request `requestId`, created on first use. */
  get(requestId: string): ResponseHistory {
    let slot = this.slots.get(requestId);
    if (slot === undefined) {
      slot = new ResponseHistory(requestId);
      this.slots.set(requestId, slot);
      this.follow();
    }
    return slot;
  }

  /** Subscribes to history:changed once there is a slot. */
  private follow(): void {
    if (this.stopEvents !== null) return;
    try {
      this.stopEvents = onHistoryChanged((ev) => {
        for (const slot of this.slots.values()) slot.changed(ev);
      });
    } catch {
      // No event bus (tests without the runtime): the lists are not followed.
      this.stopEvents = () => {};
    }
  }

  drop(requestIds: readonly string[]): void {
    for (const id of requestIds) {
      this.slots.get(id)?.dispose();
      this.slots.delete(id);
    }
  }

  dispose(): void {
    this.drop([...this.slots.keys()]);
    this.stopEvents?.();
    this.unsubscribe();
  }
}

let registry: ResponseHistories | null = null;

/** The app's response history slots, created on first use. */
export function responseHistories(): ResponseHistories {
  registry ??= new ResponseHistories();
  return registry;
}

/** Drops the registry, for tests. */
export function resetResponseHistories(): void {
  registry?.dispose();
  registry = null;
}

function draftRequest(stores: Stores, id: RequestId): RequestDTO | undefined {
  // Drafts hold RequestDTOs (loadRequestDraft).
  return stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined;
}

/** The response examples of `request`, never null. */
export function responseExamples(request: Pick<RequestDTO, "responseExamples"> | undefined): readonly ResponseExampleDTO[] {
  return request?.responseExamples ?? [];
}

/** Shows the latest send of request `id` in its response pane. */
export function showLatestResponse(id: RequestId): void {
  responseHistories().get(id).showLatest();
}

/** Shows history entry `entryId` (sent at `time`) in request `id`'s response pane until the next send. */
export function showEarlierResponse(id: RequestId, entryId: string, time: string, since: number | null): void {
  responseHistories()
    .get(id)
    .show({ kind: "history", id: entryId, time }, since, async () => {
      const stored = entryAsStored(await historyEntry(entryId));
      if (stored === null) throw new Error(t("commands.responseExamples.noResponse"));
      return storedResponseView(stored);
    });
}

/** Shows response example `index` of request `id` in its response pane until the next send. */
export function showResponseExample(stores: Stores, id: RequestId, index: number, since: number | null): void {
  const example = responseExamples(draftRequest(stores, id))[index];
  if (example === undefined) return;
  responseHistories()
    .get(id)
    .show({ kind: "example", index, name: example.name }, since, () => storedResponseView(exampleAsStored(example)));
}

/**
 * Saves history entry `entryId` as a response example of request `id`
 * (HTTP-38): Go makes it from the history, secrets masked; it is added to
 * the draft (named by its status, made unique) as one undoable edit. A
 * toast says so and offers to open the Examples sub-tab.
 */
export async function saveResponseAsExample(stores: Stores, id: RequestId, entryId: string): Promise<void> {
  const slot = responseHistories().get(id);
  if (slot.getState().saving) return;
  slot.setSaving(true);
  try {
    const example = await exampleFromHistory(entryId);
    const request = draftRequest(stores, id);
    if (request === undefined) return;
    const list = responseExamples(request);
    const name = uniqueName(example.name, list.map((e) => e.name));
    editRequest(stores, id, { responseExamples: [...list, { ...example, name }] }, { history: "separate" });
    showToast({
      message: t("commands.responseExamples.saved", { name }),
      action: {
        label: t("commands.responseExamples.show"),
        run: () => stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: EXAMPLES_SUB_TAB }),
      },
    });
  } catch (err: unknown) {
    showToast({ message: t("commands.responseExamples.saveFailed", { error: toAppError(err).message }) });
  } finally {
    slot.setSaving(false);
  }
}

/** Renames response example `index` of request `id`. */
export function renameResponseExample(stores: Stores, id: RequestId, index: number, name: string): void {
  const list = responseExamples(draftRequest(stores, id));
  const target = list[index];
  if (target === undefined || target.name === name) return;
  editRequest(stores, id, { responseExamples: list.map((e, i) => (i === index ? { ...e, name } : e)) });
}

/** Deletes response example `index` of request `id`; the pane stops showing it. */
export function deleteResponseExample(stores: Stores, id: RequestId, index: number): void {
  const list = responseExamples(draftRequest(stores, id));
  if (list[index] === undefined) return;
  editRequest(stores, id, { responseExamples: list.filter((_, i) => i !== index) }, { history: "separate" });
  const slot = responseHistories().get(id);
  const shown = slot.getState().shown;
  if (shown.kind === "example") slot.showLatest();
}
