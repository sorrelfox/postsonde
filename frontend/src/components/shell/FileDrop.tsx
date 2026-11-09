import { useEffect, useState, type RefObject } from "react";
import { importDroppedFiles } from "../../commands/fileDrop";
import { onFilesDropped } from "../../lib/collection";
import { useCollection, useStores } from "../../stores";
import { IconFileImport } from "../common/icons";
import { useT } from "../../i18n/react";
import styles from "./FileDrop.module.css";

// Files dropped on the window (IMP-11, docs/frontend.md "Dropped files").
//
// The app shell is the window's file drop target (data-file-drop-target,
// commands/fileDrop.ts). Wails' runtime marks the drop target under a file
// drag with DROP_ACTIVE_CLASS: natively on macOS and Linux, from the DOM's
// drag events on Windows, and only for drags that carry files, never for
// text or the tree's own drags. The overlay follows that class, so it shows
// exactly when a drop would reach the shell; over an inner drop target
// (none yet) the class moves there and the overlay hides.

/** The class Wails' runtime puts on the drop target under a file drag. */
export const DROP_ACTIVE_CLASS = "file-drop-target-active";

/**
 * Follows file drags over the element in `ref`, a file drop target, and
 * imports what is dropped on it (commands/fileDrop.ts). Returns whether a
 * file drag is over it now.
 */
export function useFileDrop(ref: RefObject<HTMLElement | null>): boolean {
  const stores = useStores();
  const [active, setActive] = useState(false);

  useEffect(
    () =>
      onFilesDropped((ev) => {
        setActive(false);
        void importDroppedFiles(stores, ev.paths ?? [], ev.target);
      }),
    [stores],
  );

  useEffect(() => {
    const el = ref.current;
    if (el === null) return;
    const sync = () => setActive(el.classList.contains(DROP_ACTIVE_CLASS));
    sync();
    const observer = new MutationObserver(sync);
    observer.observe(el, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, [ref]);

  // A drag that leaves the window may not tell the runtime (a WebView2
  // dragleave without a related target is ignored). No pointer or key
  // events arrive during a drag, so any of them means it is over.
  useEffect(() => {
    if (!active) return;
    const end = () => {
      ref.current?.classList.remove(DROP_ACTIVE_CLASS);
      setActive(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") end();
    };
    window.addEventListener("pointermove", end);
    window.addEventListener("pointerdown", end);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("pointermove", end);
      window.removeEventListener("pointerdown", end);
      window.removeEventListener("keydown", onKey);
    };
  }, [active, ref]);

  return active;
}

/**
 * The "Drop to import" overlay over the whole window while files are
 * dragged over it. It takes no pointer events, so the element under the
 * cursor stays the one the runtime and the drop see.
 */
export function FileDropOverlay({ active }: { active: boolean }) {
  const t = useT();
  const status = useCollection((s) => s.status);
  const readOnly = useCollection((s) => s.readOnly);
  if (!active) return null;
  const blocked =
    status !== "ready" ? t("shell.fileDrop.noProject") : readOnly !== "" ? t("shell.fileDrop.readOnly") : null;
  return (
    <div className={styles.overlay} role="status" data-blocked={blocked !== null ? "" : undefined}>
      <div className={styles.card}>
        <IconFileImport size={28} />
        <strong className={styles.title}>{blocked ?? t("shell.fileDrop.title")}</strong>
        {blocked === null && (
          <span className={styles.hint}>{t("shell.fileDrop.hint")}</span>
        )}
      </div>
    </div>
  );
}
