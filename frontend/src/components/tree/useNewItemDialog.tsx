import { useCallback, useEffect, useState, type ReactNode } from "react";
import { onImportCurlDialog } from "../../commands/curlImport";
import { onImportDroppedFiles } from "../../commands/fileDrop";
import { useT } from "../../i18n/react";
import { useCollection, useStores, type NodeId } from "../../stores";
import { useShortcut } from "../shell/useShortcut";
import { ImportCurlDialog, type ImportCurlRequest } from "./ImportCurlDialog";
import { ImportDialog, type ImportRequest } from "./ImportDialog";
import { NewItemDialog, type NewItemRequest } from "./NewItemDialog";

/** What `ask` opens: the New folder, New request, Import cURL or Import dialog. */
export type NewItemType = "folder" | "request" | "curl" | "import";

export interface NewItemDialogControl {
  /** Whether items can be created now: a tree is loaded and the project is writable. */
  readonly canCreate: boolean;
  /** Why not, for a tooltip, or "". */
  readonly disabledReason: string;
  /**
   * Opens the dialog. `from` is the node it was asked for (its folder is
   * the default target); undefined uses the tree's selection.
   */
  readonly ask: (type: NewItemType, from?: NodeId | null) => void;
  /** The dialog elements; render them anywhere (they are portals). */
  readonly dialog: ReactNode;
}

/**
 * The state of the New folder and New request dialogs (COL-04), the
 * Import cURL dialog (IMP-01) and the Import dialog (IMP-10) for the sidebar: its header buttons, the
 * tree's context menu and the New request shortcut (request.new, ⌘N) all
 * open them through `ask`; openImportCurlDialog (commands/curlImport.ts,
 * the URL bar's paste toast) opens Import cURL with a command filled in.
 * Files dropped on the window (IMP-11, commands/fileDrop.ts) open the
 * Import dialog once per file, one after another.
 */
export function useNewItemDialog(): NewItemDialogControl {
  const t = useT();
  const stores = useStores();
  const status = useCollection((s) => s.status);
  const readOnly = useCollection((s) => s.readOnly);
  const [request, setRequest] = useState<NewItemRequest | null>(null);
  const [curl, setCurl] = useState<ImportCurlRequest | null>(null);
  // The Import dialog shows the first; files dropped on the window (IMP-11) queue up behind it.
  const [imports, setImports] = useState<readonly ImportRequest[]>([]);
  // Keys the Import dialog, so the next queued file gets a fresh one.
  const [importKey, setImportKey] = useState(0);

  const canCreate = status === "ready" && readOnly === "";
  const disabledReason = status !== "ready" ? "" : readOnly !== "" ? t("tree.readOnlyProject") : "";

  const ask = useCallback(
    (type: NewItemType, from?: NodeId | null) => {
      const state = stores.collection.getState();
      if (state.status !== "ready" || state.readOnly !== "") return;
      const node = from === undefined ? state.selectedId : from;
      if (type === "curl") setCurl({ from: node, text: "" });
      else if (type === "import") setImports([{ from: node }]);
      else setRequest({ type, from: node });
    },
    [stores],
  );

  useEffect(
    () =>
      onImportCurlDialog((text) => {
        const state = stores.collection.getState();
        if (state.status !== "ready" || state.readOnly !== "") return;
        setCurl({ from: state.selectedId, text });
      }),
    [stores],
  );

  // Files dropped on the window: one Import dialog per file, in turn, each
  // preselected with its file and detected format (commands/fileDrop.ts).
  useEffect(
    () =>
      onImportDroppedFiles((files) => {
        const state = stores.collection.getState();
        if (state.status !== "ready" || state.readOnly !== "") return;
        const total = files.length;
        const next = files.map(
          (f, i): ImportRequest => ({
            from: state.selectedId,
            path: f.path,
            detectedFormat: f.format,
            position: { index: i + 1, total },
          }),
        );
        setImports((queue) => [...queue, ...next]);
      }),
    [stores],
  );

  useShortcut("request.new", () => {
    if (!canCreate) return false;
    ask("request");
  });

  const close = useCallback(() => setRequest(null), []);
  const closeCurl = useCallback(() => setCurl(null), []);
  const closeImport = useCallback(() => {
    setImports((queue) => queue.slice(1));
    setImportKey((k) => k + 1);
  }, []);

  return {
    canCreate,
    disabledReason,
    ask,
    dialog: (
      <>
        <NewItemDialog request={request} onClose={close} />
        <ImportCurlDialog request={curl} onClose={closeCurl} />
        <ImportDialog key={importKey} request={imports[0] ?? null} onClose={closeImport} />
      </>
    ),
  };
}
