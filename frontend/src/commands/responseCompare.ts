// Comparing two responses of a request (HTTP-40, docs/response-diff.md):
// any two of the latest send and the earlier responses (by their history
// entries, HTTP-39) and the request's saved examples (HTTP-38). Each is
// read as a stored response, as the response view reads it
// (commands/responseHistory.ts), and Go compares them
// (HTTPService.CompareResponses).

import { t } from "../i18n";
import { compareResponses, type ResponseDiffDTO } from "../lib/responseDiff";
import { entryAsStored, exampleAsStored, historyEntry, type StoredResponseInputDTO } from "../lib/responseHistory";
import type { RequestDTO } from "../lib/collection";
import type { RequestId, Stores } from "../stores";

/** A response to compare: a history entry (the latest send or an earlier one) or a saved example, by index. */
export type CompareSource = { readonly kind: "history"; readonly id: string } | { readonly kind: "example"; readonly index: number };

/** A source as a select value: "h:<entry id>" or "e:<index>". */
export function sourceKey(source: CompareSource): string {
  return source.kind === "history" ? `h:${source.id}` : `e:${source.index}`;
}

/** The source of a select value, or null. */
export function parseSourceKey(key: string): CompareSource | null {
  if (key.startsWith("h:") && key.length > 2) return { kind: "history", id: key.slice(2) };
  if (key.startsWith("e:")) {
    const index = Number(key.slice(2));
    if (Number.isInteger(index) && index >= 0) return { kind: "example", index };
  }
  return null;
}

/** Response `source` of request `id` as a stored response. */
async function storedOf(stores: Stores, id: RequestId, source: CompareSource): Promise<StoredResponseInputDTO> {
  if (source.kind === "history") {
    const stored = entryAsStored(await historyEntry(source.id));
    if (stored === null) throw new Error(t("commands.responseExamples.noResponse"));
    return stored;
  }
  // Drafts hold RequestDTOs (loadRequestDraft).
  const request = stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined;
  const example = request?.responseExamples?.[source.index];
  if (example === undefined) throw new Error(t("commands.compare.noExample"));
  return exampleAsStored(example);
}

/** The differences between responses `left` and `right` of request `id`. */
export async function compareSources(stores: Stores, id: RequestId, left: CompareSource, right: CompareSource): Promise<ResponseDiffDTO> {
  const [a, b] = await Promise.all([storedOf(stores, id, left), storedOf(stores, id, right)]);
  return compareResponses(a, b);
}
