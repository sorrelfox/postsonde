// Jumping from a test result to its assertion (TEST-06). The response
// pane's Tests tab and the request editor's Tests sub-tab are in different
// panes, and the sub-tab may not be mounted, so they meet here as
// graphqlJump.ts does for GraphQL errors: jumpToAssertion keeps the row as
// the request's pending jump and opens the Tests sub-tab; the assertions
// editor takes pending jumps with useAssertionJump, now or when it mounts.
import { useEffect, useRef } from "react";
import type { RequestId, Stores } from "../stores";

/** The request editor's sub-tab with the assertions (HTTP and GraphQL editors). */
export const TESTS_SUB_TAB = "tests";

const pending = new Map<RequestId, number>();
const listeners = new Map<RequestId, Set<() => void>>();

/** Opens the request's Tests sub-tab and asks it to focus assertion `index` (its position in the request's list). */
export function jumpToAssertion(stores: Stores, id: RequestId, index: number): void {
  pending.set(id, index);
  stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: TESTS_SUB_TAB });
  for (const listener of [...(listeners.get(id) ?? [])]) listener();
}

/** Takes the request's pending jump, if any. */
export function takeAssertionJump(id: RequestId): number | null {
  const index = pending.get(id);
  pending.delete(id);
  return index ?? null;
}

function subscribe(id: RequestId, listener: () => void): () => void {
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

/** The assertions editor's side: calls `focus` with the row of each pending and future jump. */
export function useAssertionJump(id: RequestId, focus: (index: number) => void): void {
  const focusRef = useRef(focus);
  focusRef.current = focus;
  useEffect(() => {
    const run = () => {
      const index = takeAssertionJump(id);
      if (index !== null) focusRef.current(index);
    };
    run();
    return subscribe(id, run);
  }, [id]);
}

/** Drops every pending jump (tests). */
export function resetAssertionJumps(): void {
  pending.clear();
  listeners.clear();
}
