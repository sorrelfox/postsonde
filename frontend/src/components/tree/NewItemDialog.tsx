import { useId, useMemo, useRef, useState } from "react";
import { createItem, saveScratchTo } from "../../commands/collection";
import type { NewRequestKind } from "../../lib/collection";
import { useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { useCollection, useStores, useWorkspace, type NodeId } from "../../stores";
import { Dialog } from "../common/Dialog";
import { FolderSelect } from "./FolderSelect";
import { defaultTarget, folderOptions, previewPath, REQUEST_KINDS, TOP_LEVEL, validateName } from "./newItem";
import styles from "./NewItemDialog.module.css";

/** Which dialog to show, and the node it was asked for (its folder is the default target). */
export interface NewItemRequest {
  /** "saveScratch": the Save to… dialog of a scratch request (COL-06). */
  readonly type: "folder" | "request" | "saveScratch";
  /** A context menu's row or the tree's selection; null for the top level. */
  readonly from: NodeId | null;
  /** The scratch request to save, for "saveScratch". */
  readonly scratchId?: NodeId;
}

export interface NewItemDialogProps {
  /** null: closed. A new request object opens a fresh dialog. */
  request: NewItemRequest | null;
  onClose: () => void;
}

/**
 * The New folder and New request dialogs (COL-04): a request type picker
 * (every kind the app can create), the name, and the folder it goes into
 * with a preview of the file path. On confirm the item is created through
 * createItem and opens in a tab; a failure is shown in the dialog.
 *
 * The same form, without the type picker and with the name filled in, is
 * the Save to… dialog of a scratch request (COL-06): on confirm
 * saveScratchTo moves it into the chosen folder as a normal request.
 */
export function NewItemDialog({ request, onClose }: NewItemDialogProps) {
  if (request === null) return null;
  return <NewItemForm request={request} onClose={onClose} />;
}

function NewItemForm({ request, onClose }: { request: NewItemRequest; onClose: () => void }) {
  const t = useT();
  const stores = useStores();
  const isFolder = request.type === "folder";
  const isSave = request.type === "saveScratch";
  // The whole tree state: the dialog re-renders on any tree change, which is rare while it is open.
  const state = useCollection((s) => s);
  const projectName = useWorkspace((s) => s.current?.name) ?? t("tree.project");

  const [kind, setKind] = useState<NewRequestKind>("http");
  const [name, setName] = useState(() => (isSave ? (state.nodes[request.scratchId ?? ""]?.name ?? "") : ""));
  const [parentId, setParentId] = useState<NodeId>(() => defaultTarget(state, request.from));
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  const baseId = useId();
  // The name field gets focus, not the type picker above it.
  const nameRef = useRef<HTMLInputElement>(null);
  const nameErrorId = `${baseId}-name-error`;
  const pathId = `${baseId}-path`;
  const kindHelpId = `${baseId}-kind-help`;

  const options = useMemo(() => folderOptions(state, projectName), [state, projectName]);
  // The chosen folder was removed meanwhile (a reload): fall back to the top level.
  const target = options.some((o) => o.id === parentId && !o.disabled) ? parentId : TOP_LEVEL;
  const nameError = validateName(state, target, name);
  const path = previewPath(state, target, name.trim(), isFolder);
  const kindInfo = REQUEST_KINDS.find((k) => k.kind === kind) ?? REQUEST_KINDS[0];

  const confirm = async () => {
    if (nameError !== null || busy) return;
    setBusy(true);
    setFailure(null);
    try {
      const parentId = target === TOP_LEVEL ? null : target;
      if (isSave) await saveScratchTo(stores, request.scratchId ?? "", parentId, name.trim());
      else await createItem(stores, parentId, name.trim(), isFolder ? { type: "folder" } : { type: "request", kind });
      onClose();
    } catch (err: unknown) {
      const appErr = toAppError(err);
      setFailure(appErr.hint === "" ? appErr.message : `${appErr.message} ${appErr.hint}`);
      setBusy(false);
    }
  };

  const shownError = nameError !== null && nameError !== "" ? nameError : failure;

  return (
    <Dialog
      open
      title={isSave ? t("tree.newItem.saveTitle") : isFolder ? t("tree.newItem.folderTitle") : t("tree.newItem.requestTitle")}
      confirmLabel={isSave ? t("tree.newItem.saveConfirm") : isFolder ? t("tree.newItem.folderConfirm") : t("tree.newItem.requestConfirm")}
      onCancel={onClose}
      onConfirm={() => void confirm()}
      confirmDisabled={nameError !== null}
      busy={busy}
      initialFocus={nameRef}
    >
      {!isFolder && !isSave && (
        <div className={styles.field}>
          <span className={styles.label} id={`${baseId}-kind`}>
            {t("tree.newItem.type")}
          </span>
          <div className={styles.kinds} role="radiogroup" aria-labelledby={`${baseId}-kind`} aria-describedby={kindHelpId}>
            {REQUEST_KINDS.map((k) => (
              <label key={k.kind} className={styles.kind}>
                <input
                  type="radio"
                  name={`${baseId}-kind`}
                  value={k.kind}
                  checked={k.kind === kind}
                  disabled={busy}
                  onChange={() => setKind(k.kind)}
                />
                {k.label}
              </label>
            ))}
          </div>
          <span className={styles.help} id={kindHelpId}>
            {t(kindInfo.descriptionKey)}
          </span>
        </div>
      )}

      <div className={styles.field}>
        <label className={styles.label} htmlFor={`${baseId}-name`}>
          {t("tree.name")}
        </label>
        <input
          id={`${baseId}-name`}
          className={styles.input}
          value={name}
          placeholder={isFolder ? t("tree.folderName") : t("tree.requestName")}
          maxLength={1000}
          spellCheck={false}
          autoComplete="off"
          ref={nameRef}
          readOnly={busy}
          aria-invalid={shownError !== null || undefined}
          aria-describedby={shownError !== null ? nameErrorId : undefined}
          onChange={(e) => {
            setName(e.target.value);
            setFailure(null);
          }}
        />
        {shownError !== null && (
          <span className={styles.error} id={nameErrorId} role="alert">
            {shownError}
          </span>
        )}
      </div>

      <div className={styles.field}>
        <label className={styles.label} htmlFor={`${baseId}-folder`}>
          {t("tree.folder")}
        </label>
        <FolderSelect
          id={`${baseId}-folder`}
          options={options}
          value={target}
          disabled={busy}
          describedBy={pathId}
          onChange={(id) => {
            setParentId(id);
            setFailure(null);
          }}
        />
        <span className={styles.path} id={pathId}>
          {path}
        </span>
      </div>
    </Dialog>
  );
}
