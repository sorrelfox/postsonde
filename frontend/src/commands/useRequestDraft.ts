import { useCallback, useEffect, useState } from "react";
import type { AppError } from "../lib/errors";
import type { FolderDTO, RequestDTO } from "../lib/collection";
import { useDrafts, useStores, type RequestId, type Stores } from "../stores";
import { loadFolderDraft, loadRequestDraft } from "./autosave";

export type DraftState<T> =
  | { status: "loading" }
  | { status: "error"; error: AppError; retry: () => void }
  | { status: "ready"; request: T };

export type RequestDraftState = DraftState<RequestDTO>;

/** Loads the draft of `id` with `load` the first time it is shown; see useRequestDraft. */
function useDraft<T>(id: RequestId, load: (stores: Stores, id: RequestId) => Promise<void>): DraftState<T> {
  const stores = useStores();
  // Drafts hold what Go returned plus patches of its fields (commands/autosave.ts).
  const request = useDrafts((s) => s.byId[id]?.request) as unknown as T | undefined;
  const [error, setError] = useState<AppError | null>(null);
  const [attempt, setAttempt] = useState(0);

  const hasDraft = request !== undefined;
  useEffect(() => {
    if (hasDraft) return;
    let live = true;
    setError(null);
    load(stores, id).catch((err: AppError) => {
      if (live) setError(err);
    });
    return () => {
      live = false;
    };
  }, [stores, id, hasDraft, attempt, load]);

  const retry = useCallback(() => setAttempt((n) => n + 1), []);

  if (request !== undefined) return { status: "ready", request };
  if (error !== null) return { status: "error", error, retry };
  return { status: "loading" };
}

/**
 * The draft of request `id` for its editor. Loads it from Go
 * (CollectionService.Request, with pending edits) the first time the
 * editor shows it; while the tab stays open the draft stays in the drafts
 * store, so switching tabs back does not load it again.
 */
export function useRequestDraft(id: RequestId): RequestDraftState {
  return useDraft<RequestDTO>(id, loadRequestDraft);
}

/**
 * The draft of folder `id`'s settings for the folder editor (COL-03),
 * like useRequestDraft (CollectionService.Folder). Edits go through the
 * same auto-save and undo history as a request's.
 */
export function useFolderDraft(id: RequestId): DraftState<FolderDTO> {
  return useDraft<FolderDTO>(id, loadFolderDraft);
}
