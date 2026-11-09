// Jumping from an extraction result to its rule (TEST-05), as
// assertionJump.ts does for assertions: the response pane's Variables tab
// and the request editor's Extract sub-tab are in different panes, and the
// sub-tab may not be mounted, so jumpToExtraction keeps the row as the
// request's pending jump and opens the Extract sub-tab; the extractions
// editor takes pending jumps with useExtractionJump, now or when it mounts.
import { useEffect, useRef } from "react";
import type { RequestId, Stores } from "../stores";

/** The request editor's sub-tab with the extraction rules (HTTP and GraphQL editors). */
export const EXTRACT_SUB_TAB = "extract";

const pending = new Map<RequestId, number>();
const listeners = new Map<RequestId, Set<() => void>>();

/** Opens the request's Extract sub-tab and asks it to focus rule `index` (its position in the request's list); null just opens it. */
export function jumpToExtraction(stores: Stores, id: RequestId, index: number | null): void {
  if (index !== null) pending.set(id, index);
  stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: EXTRACT_SUB_TAB });
  for (const listener of [...(listeners.get(id) ?? [])]) listener();
}

function take(id: RequestId): number | null {
  const index = pending.get(id);
  pending.delete(id);
  return index ?? null;
}

/** The extractions editor's side: calls `focus` with the row of each pending and future jump. */
export function useExtractionJump(id: RequestId, focus: (index: number) => void): void {
  const focusRef = useRef(focus);
  focusRef.current = focus;
  useEffect(() => {
    const run = () => {
      const index = take(id);
      if (index !== null) focusRef.current(index);
    };
    run();
    let set = listeners.get(id);
    if (set === undefined) {
      set = new Set();
      listeners.set(id, set);
    }
    set.add(run);
    return () => {
      set.delete(run);
      if (set.size === 0) listeners.delete(id);
    };
  }, [id]);
}

/** Drops every pending jump (tests). */
export function resetExtractionJumps(): void {
  pending.clear();
  listeners.clear();
}
