// Keeps the CodeMirror editors of an open request across tab switches
// (NAV-07). EditHistoryScope (rendered by TabPanel around every item's
// editor) gives the editors inside it the request's editor-state scope
// (commands/editHistory.ts); each CodeEditor in it stores its EditorState
// there when it unmounts and takes it back when it mounts again, so the
// text, selection and undo history come back with the tab. Any editor
// rendered in the tab panel gets this without doing anything.

import { createContext, Fragment, useCallback, useContext, useMemo, useSyncExternalStore, type ReactNode } from "react";
import {
  editHistoryGeneration,
  editorStateScope,
  subscribeEditHistory,
  type EditorStateScope,
} from "../../commands/editHistory";
import { useStores, type RequestId } from "../../stores";

const EditorStateContext = createContext<EditorStateScope | null>(null);

/** The editor-state scope of the request around this component, or null outside one. */
export function useEditorStateScope(): EditorStateScope | null {
  return useContext(EditorStateContext);
}

export interface EditHistoryScopeProps {
  /** The open item whose editors are inside. */
  id: RequestId;
  children: ReactNode;
}

/**
 * Keeps the editor states of item `id`'s editors. When the item's history
 * is cleared while it shows (a reload from disk), the editors inside mount
 * again with fresh states.
 */
export function EditHistoryScope({ id, children }: EditHistoryScopeProps) {
  const stores = useStores();
  const subscribe = useCallback((listener: () => void) => subscribeEditHistory(stores, listener), [stores]);
  const getGeneration = useCallback(() => editHistoryGeneration(stores, id), [stores, id]);
  const generation = useSyncExternalStore(subscribe, getGeneration, getGeneration);
  // A new scope per generation: one made before a clear stores nothing.
  const scope = useMemo(() => editorStateScope(stores, id), [stores, id, generation]);
  return (
    <EditorStateContext.Provider value={scope}>
      <Fragment key={generation}>{children}</Fragment>
    </EditorStateContext.Provider>
  );
}
