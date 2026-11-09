// Jumping from a GraphQL error to the query (GQL-05). The response pane and
// the Query editor are in different panes and the editor may not even be
// mounted (another sub-tab is open), so they meet here: the response view
// asks for a jump with jumpToQueryLocation, which keeps it as the request's
// pending jump and opens the Query sub-tab; the Query editor takes pending
// jumps with useQueryJump as soon as it has a view, now or when it mounts.
import { useEffect } from "react";
import type { EditorView } from "@codemirror/view";
import { queryLocationOffset, type QueryLocation } from "../lib/graphqlResponse";
import type { RequestId, Stores } from "../stores";

const pending = new Map<RequestId, QueryLocation>();
const listeners = new Map<RequestId, Set<() => void>>();

/** Asks the request's Query editor to put the cursor on `location` (1-based line and code-point column). */
export function jumpToQueryLocation(stores: Stores, id: RequestId, location: QueryLocation): void {
  pending.set(id, location);
  stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: "query" });
  for (const listener of [...(listeners.get(id) ?? [])]) listener();
}

/** Takes the request's pending jump, if any. */
export function takeQueryJump(id: RequestId): QueryLocation | null {
  const location = pending.get(id) ?? null;
  pending.delete(id);
  return location;
}

/** Calls `listener` on every jump asked for the request. */
export function subscribeQueryJump(id: RequestId, listener: () => void): () => void {
  let set = listeners.get(id);
  if (set === undefined) {
    set = new Set();
    listeners.set(id, set);
  }
  set.add(listener);
  return () => {
    set.delete(listener);
    if (set.size === 0) listeners.delete(id);
  };
}

/** Moves the cursor of `view` to a query location, scrolls to it and focuses the editor. */
export function applyQueryJump(view: EditorView, location: QueryLocation): void {
  const pos = queryLocationOffset(view.state.doc, location);
  view.dispatch({ selection: { anchor: pos }, scrollIntoView: true });
  view.focus();
}

/** The Query editor's side: applies the request's pending and future jumps to `view`. */
export function useQueryJump(id: RequestId, view: EditorView | null): void {
  useEffect(() => {
    if (view === null) return;
    const run = () => {
      const location = takeQueryJump(id);
      if (location !== null) applyQueryJump(view, location);
    };
    run();
    return subscribeQueryJump(id, run);
  }, [id, view]);
}

/** Drops every pending jump (tests). */
export function resetQueryJumps(): void {
  pending.clear();
  listeners.clear();
}
