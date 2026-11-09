import { useMemo, useState } from "react";
import {
  closeDeletedRequest,
  keepMine,
  loadConflictVersions,
  restoreDeletedRequest,
  takeTheirs,
  type ConflictVersions,
} from "../../commands/externalChanges";
import { showToast } from "../../commands/toasts";
import { useT } from "../../i18n/react";
import { formatError } from "../../lib/errors";
import { diffLines } from "../../lib/lineDiff";
import { DELETED_READ_ONLY, selectConflictSource, useCollection, useDrafts, useStores, type RequestId } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconWarning } from "../common/icons";
import styles from "./ExternalChangeBanner.module.css";

/**
 * The notice above an editor whose file changed outside PostSonde (STO-05):
 *
 * - the draft had unsaved edits when its file changed (drafts.conflicts),
 *   or when another window changed the item (drafts.otherWindow, APP-06):
 *   Keep mine writes the draft over the file, Take theirs loads the file's
 *   (or the other window's) version, Show changes compares both;
 * - the request's file was removed (collection.deletedIds): the editor is
 *   read-only; Restore file writes it back, Close drops it.
 *
 * Renders nothing otherwise.
 */
export function ExternalChangeBanner({ id }: { id: RequestId }) {
  const conflict = useDrafts((s) => s.conflicts[id] === true);
  const deleted = useCollection((s) => s.nodes[id]?.readOnly === DELETED_READ_ONLY);
  if (deleted) return <DeletedNotice id={id} />;
  if (conflict) return <ConflictNotice id={id} />;
  return null;
}

function useBusy(): [boolean, (run: () => Promise<void>) => void] {
  const [busy, setBusy] = useState(false);
  const run = (fn: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    fn()
      .catch((err: unknown) => showToast({ message: formatError(err), icon: <IconWarning size={14} strokeWidth={1.9} /> }))
      .finally(() => setBusy(false));
  };
  return [busy, run];
}

function ConflictNotice({ id }: { id: RequestId }) {
  const t = useT();
  const stores = useStores();
  const folder = useCollection((s) => s.nodes[id]?.kind === "folder");
  const otherWindow = useDrafts((s) => selectConflictSource(s, id) === "window");
  const [busy, run] = useBusy();
  const [versions, setVersions] = useState<ConflictVersions | null>(null);
  let text: string;
  if (otherWindow) text = folder ? t("tabs.banner.conflict.folderWindow") : t("tabs.banner.conflict.requestWindow");
  else text = folder ? t("tabs.banner.conflict.folder") : t("tabs.banner.conflict.request");

  return (
    <div className={styles.banner} role="alert" data-external-change="conflict" data-conflict-source={otherWindow ? "window" : "disk"}>
      <IconWarning size={14} />
      <p className={styles.text}>{text}</p>
      <div className={styles.actions}>
        <button type="button" className={styles.button} disabled={busy} onClick={() => run(async () => setVersions(await loadConflictVersions(stores, id)))}>
          {t("tabs.banner.showChanges")}
        </button>
        <button type="button" className={styles.button} disabled={busy} onClick={() => run(() => takeTheirs(stores, id))}>
          {t("tabs.banner.takeTheirs")}
        </button>
        <button type="button" className={styles.primary} disabled={busy} onClick={() => run(() => keepMine(stores, id))}>
          {t("tabs.banner.keepMine")}
        </button>
      </div>
      {versions !== null && <DiffDialog versions={versions} otherWindow={otherWindow} onClose={() => setVersions(null)} />}
    </div>
  );
}

function DeletedNotice({ id }: { id: RequestId }) {
  const t = useT();
  const stores = useStores();
  const hasDraft = useDrafts((s) => s.byId[id] !== undefined);
  const [busy, run] = useBusy();
  return (
    <div className={styles.banner} role="alert" data-external-change="deleted">
      <IconWarning size={14} />
      <p className={styles.text}>{t("tabs.banner.deleted")}</p>
      <div className={styles.actions}>
        <button type="button" className={styles.button} disabled={busy} onClick={() => closeDeletedRequest(stores, id)}>
          {t("tabs.banner.close")}
        </button>
        {hasDraft && (
          <button type="button" className={styles.primary} disabled={busy} onClick={() => run(() => restoreDeletedRequest(stores, id))}>
            {t("tabs.banner.restore")}
          </button>
        )}
      </div>
    </div>
  );
}

/**
 * The two versions, line by line: yours (removed lines) against the file on
 * disk, or the other window's version (added lines).
 */
function DiffDialog({ versions, otherWindow, onClose }: { versions: ConflictVersions; otherWindow: boolean; onClose: () => void }) {
  const t = useT();
  const lines = useMemo(() => diffLines(versions.mine, versions.theirs), [versions]);
  const changed = lines.some((l) => l.kind !== "same");
  return (
    <Dialog open title={otherWindow ? t("tabs.banner.diff.titleWindow") : t("tabs.banner.diff.title")} onCancel={onClose} width={720}>
      <p className={styles.legend}>
        <span data-kind="removed">{t("tabs.banner.diff.onlyMine")}</span>{" "}
        <span data-kind="added">{otherWindow ? t("tabs.banner.diff.onlyTheirsWindow") : t("tabs.banner.diff.onlyTheirs")}</span>
      </p>
      {changed ? (
        <pre className={styles.diff} aria-label={t("tabs.banner.diff.label")}>
          {lines.map((line, i) => (
            <span key={i} className={styles.line} data-kind={line.kind}>
              {line.kind === "removed" ? "− " : line.kind === "added" ? "+ " : "  "}
              {line.text}
              {"\n"}
            </span>
          ))}
        </pre>
      ) : (
        <p className={styles.legend}>{t("tabs.banner.diff.same")}</p>
      )}
    </Dialog>
  );
}
