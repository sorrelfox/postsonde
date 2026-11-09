// Named body examples (HTTP-17): several bodies per request, one active.
// The active example's body is the request's `body`, the one the send path
// uses; the others keep theirs in `bodyExamples`, where the active item has
// an empty body (docs/file-format.md#body-examples). Switching moves the
// bodies between the two, so what is sent always follows the active
// example. Every change is an auto-saved edit (editRequest).

import { t } from "../i18n";
import type { BodyDTO, BodyExampleDTO, RequestDTO } from "../lib/collection";
import type { RequestId, Stores } from "../stores";
import { editRequest } from "./autosave";

/** The body of the active example inside `bodyExamples`: empty, mode "". */
export const ACTIVE_EXAMPLE_BODY: BodyDTO = {
  mode: "",
  type: "",
  text: "",
  fields: null,
  parts: null,
  path: "",
  contentType: "",
  extra: "",
};

/** The name the request's body gets when its first example is added, in the language in use. */
export function firstExampleName(): string {
  return t("commands.bodyExamples.first");
}

function draftRequest(stores: Stores, id: RequestId): RequestDTO | undefined {
  // Drafts hold RequestDTOs (loadRequestDraft).
  return stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined;
}

/** The examples of `request`, never null. */
export function bodyExamples(request: Pick<RequestDTO, "bodyExamples">): readonly BodyExampleDTO[] {
  return request.bodyExamples ?? [];
}

/** The index of the active example, or -1 when there are none. */
export function activeExampleIndex(examples: readonly BodyExampleDTO[]): number {
  return examples.findIndex((e) => e.active);
}

/** "Example N" for the lowest N from the list's length on that no example is named. */
export function newExampleName(examples: readonly BodyExampleDTO[]): string {
  const taken = new Set(examples.map((e) => e.name));
  for (let n = Math.max(2, examples.length + 1); ; n++) {
    const name = t("commands.bodyExamples.numbered", { n });
    if (!taken.has(name)) return name;
  }
}

/**
 * The examples with `index` active: the example that was active keeps
 * `body` (the request's body until now), the new one hands its body over.
 * Returns the next list and the body to send, or null when `index` is
 * already active or out of range.
 */
export function switchExamples(
  examples: readonly BodyExampleDTO[],
  body: BodyDTO,
  index: number,
): { examples: BodyExampleDTO[]; body: BodyDTO } | null {
  const from = activeExampleIndex(examples);
  const target = examples[index];
  if (target === undefined || index === from) return null;
  const next = examples.map((e, i) => {
    if (i === from) return { ...e, active: false, body };
    if (i === index) return { ...e, active: true, body: ACTIVE_EXAMPLE_BODY };
    return e;
  });
  return { examples: next, body: target.body };
}

/** Makes example `index` of request `id` active: its body is what is sent from now on. */
export function selectBodyExample(stores: Stores, id: RequestId, index: number): void {
  const request = draftRequest(stores, id);
  if (request === undefined) return;
  const next = switchExamples(bodyExamples(request), request.body, index);
  if (next === null) return;
  // The mode stash belongs to the body being left.
  stores.drafts.dispatch({ type: "drafts/clearBodyStash", id });
  editRequest(stores, id, { body: next.body, bodyExamples: next.examples }, { history: "separate" });
}

/**
 * Adds an example to request `id`, a copy of the current body, and makes it
 * active. The first time, the current body becomes the example "Default".
 */
export function addBodyExample(stores: Stores, id: RequestId): void {
  const request = draftRequest(stores, id);
  if (request === undefined) return;
  let examples: BodyExampleDTO[] = [...bodyExamples(request)];
  if (examples.length === 0) {
    examples = [{ name: firstExampleName(), active: true, body: ACTIVE_EXAMPLE_BODY, extra: "" }];
  }
  const added: BodyExampleDTO = { name: newExampleName(examples), active: false, body: structuredClone(request.body), extra: "" };
  const next = switchExamples([...examples, added], request.body, examples.length);
  if (next === null) return;
  stores.drafts.dispatch({ type: "drafts/clearBodyStash", id });
  editRequest(stores, id, { body: next.body, bodyExamples: next.examples }, { history: "separate" });
}

/** Renames example `index` of request `id`. */
export function renameBodyExample(stores: Stores, id: RequestId, index: number, name: string): void {
  const request = draftRequest(stores, id);
  const examples = request === undefined ? [] : bodyExamples(request);
  const target = examples[index];
  if (target === undefined || target.name === name) return;
  editRequest(stores, id, { bodyExamples: examples.map((e, i) => (i === index ? { ...e, name } : e)) });
}

/**
 * Deletes example `index` of request `id`. Deleting the active example
 * makes its neighbour (the one before it, else the one after) active.
 * Deleting the only example leaves its body as the request's one unnamed
 * body.
 */
export function deleteBodyExample(stores: Stores, id: RequestId, index: number): void {
  const request = draftRequest(stores, id);
  if (request === undefined) return;
  const examples = bodyExamples(request);
  if (examples[index] === undefined) return;
  if (examples.length === 1) {
    editRequest(stores, id, { bodyExamples: [] }, { history: "separate" });
    return;
  }
  let list = examples;
  let body = request.body;
  if (examples[index].active) {
    const switched = switchExamples(examples, request.body, index > 0 ? index - 1 : index + 1);
    if (switched === null) return;
    list = switched.examples;
    body = switched.body;
    stores.drafts.dispatch({ type: "drafts/clearBodyStash", id });
  }
  editRequest(stores, id, { body, bodyExamples: list.filter((_, i) => i !== index) }, { history: "separate" });
}
