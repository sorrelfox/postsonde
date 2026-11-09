// Calls into Go for a request's earlier responses (HTTP-39) and its saved
// response examples (HTTP-38): the request history (HistoryService, the
// entries of one request, NAV-02), a history entry made into a response
// example (HistoryService.Example, secrets masked in Go) and the view of a
// stored response (HTTPService.StoredResponse), so an earlier response or
// an example shows in the response view like the latest one. See
// docs/history.md and docs/file-format.md#response-examples.
import { Events } from "@wailsio/runtime";
import {
  HistoryService,
  type ChangedEventDTO as HistoryChangedDTO,
  type EntryDTO as HistoryEntryDTO,
  type SummaryDTO as HistorySummaryDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history";
import {
  HTTPService,
  type ResponseDTO,
  type StoredResponseInputDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import type {
  ExampleRequestDTO,
  ResponseExampleDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";

export type { ExampleRequestDTO, HistoryChangedDTO, HistoryEntryDTO, HistorySummaryDTO, ResponseExampleDTO, StoredResponseInputDTO };

/** Go's history:changed event: an entry added or deleted, or the history cleared. */
export const HISTORY_CHANGED_EVENT = "history:changed";

/** How many earlier responses of a request are offered (HTTP-39). */
export const RESPONSE_HISTORY_LIMIT = 20;

/** The body encoding of a response example kept as Base64 (docs/file-format.md#response-examples). */
export const EXAMPLE_BASE64 = "base64";

/**
 * The newest history entries of request `requestId` that got a response,
 * at most `limit` of them (failed and cancelled sends are left out).
 * Rejects when there is no history (no project open).
 */
export async function listRequestResponses(requestId: string, limit = RESPONSE_HISTORY_LIMIT): Promise<HistorySummaryDTO[]> {
  const page = await HistoryService.List({ text: "", method: "", status: "", requestId, offset: 0, limit });
  return (page.entries ?? []).filter((e) => e.outcome === "ok");
}

/** History entry `id`, with the request as sent and the response kept. */
export async function historyEntry(id: string): Promise<HistoryEntryDTO> {
  return HistoryService.Get(id);
}

/**
 * History entry `id` as a response example named `name` ("" names it by its
 * status), secrets masked by Go. Nothing is saved: the caller adds it to the
 * request.
 */
export async function exampleFromHistory(id: string, name = ""): Promise<ResponseExampleDTO> {
  return HistoryService.Example({ id, name });
}

/** The response view (ResponseDTO) of a stored response. */
export async function storedResponseView(input: StoredResponseInputDTO): Promise<ResponseDTO> {
  return HTTPService.StoredResponse(input);
}

/** Calls `callback` on every history:changed event; returns the unsubscribe function. */
export function onHistoryChanged(callback: (event: HistoryChangedDTO) => void): () => void {
  return Events.On(HISTORY_CHANGED_EVENT, (ev) => callback(ev.data as HistoryChangedDTO));
}

/** What the view needs of a history entry. Null when it has no response. */
export function entryAsStored(entry: HistoryEntryDTO): StoredResponseInputDTO | null {
  const r = entry.response;
  if (r === null) return null;
  return {
    method: entry.request.method,
    url: entry.request.url,
    requestHeaders: entry.request.headers ?? [],
    requestBody: entry.request.body.text,
    requestBodyBase64: entry.request.body.base64,
    proto: r.proto,
    finalUrl: r.finalUrl,
    redirects: r.redirects,
    statusCode: r.statusCode,
    status: r.status,
    headers: r.headers ?? [],
    body: r.body.text,
    bodyBase64: r.body.base64,
    bodySize: r.body.size,
    bodyClipped: r.body.clipped || r.truncated,
    durationMs: entry.durationMs,
  };
}

/** What the view needs of a response example. */
export function exampleAsStored(example: ResponseExampleDTO): StoredResponseInputDTO {
  const q = example.request;
  const requestBody = q !== null && q.body.mode === "raw" ? q.body.text : "";
  return {
    method: q?.method ?? "",
    url: q?.url ?? "",
    requestHeaders: (q?.headers ?? []).map((h) => ({ name: h.name, value: h.value })),
    requestBody,
    requestBodyBase64: false,
    proto: "",
    finalUrl: "",
    redirects: 0,
    statusCode: example.statusCode,
    status: example.statusText,
    headers: (example.headers ?? []).map((h) => ({ name: h.name, value: h.value })),
    body: example.body,
    bodyBase64: example.bodyEncoding === EXAMPLE_BASE64,
    bodySize: example.bodyClipped ? example.bodySize : 0,
    bodyClipped: example.bodyClipped,
    durationMs: 0,
  };
}

/** `name`, or `name (2)`, `name (3)`…: the first not in `taken`. */
export function uniqueName(name: string, taken: readonly string[]): string {
  const used = new Set(taken);
  if (!used.has(name)) return name;
  for (let n = 2; ; n++) {
    const next = `${name} (${n})`;
    if (!used.has(next)) return next;
  }
}
