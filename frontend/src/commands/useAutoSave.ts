import { useCallback } from "react";
import {
  selectIsDirty,
  selectRequestSaveStatus,
  useAutosave,
  useDrafts,
  useStores,
  type RequestData,
  type RequestId,
  type RequestSaveStatus,
  type SaveFailure,
} from "../stores";
import { editRequest, flushRequests, type EditOptions } from "./autosave";

export interface AutoSave {
  /** "saving" from the first unsaved edit until Go wrote it; "error" until a later save succeeds. */
  status: RequestSaveStatus;
  /** The draft has edits Go has not confirmed on disk. */
  dirty: boolean;
  /** The last failed save, or null. */
  failure: SaveFailure | null;
  /** Applies a patch to the draft, auto-saves it and records it for undo (NAV-07). */
  edit: (patch: RequestData, options?: EditOptions) => void;
  /** Writes pending edits now. Never rejects. */
  flush: () => Promise<void>;
}

/**
 * Auto-save for the editor of request `id`. The request editor
 * loads the draft with loadRequestDraft, calls `edit` from its change
 * handlers and shows `status`; closing the tab flushes (closeTabs, through onTabClosed).
 */
export function useAutoSave(id: RequestId): AutoSave {
  const stores = useStores();
  const status = useAutosave((s) => selectRequestSaveStatus(s, id));
  const failure = useAutosave((s) => s.failed[id] ?? null);
  const dirty = useDrafts((s) => selectIsDirty(s, id));
  const edit = useCallback(
    (patch: RequestData, options?: EditOptions) => editRequest(stores, id, patch, options),
    [stores, id],
  );
  const flush = useCallback(() => flushRequests(stores, [id]), [stores, id]);
  return { status, dirty, failure, edit, flush };
}
